package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/settings"
)

// Docker Engine version awareness + one-click update (utility feature).
//
// Rigger runs INSIDE a container and cannot touch the host's package manager
// directly, so a Docker-engine update is driven over SSH — the same model
// Ansible uses. The update script is launched DETACHED (setsid, redirected to a
// log, stdin from /dev/null): when the target is the machine running Rigger, the
// daemon restart kills Rigger's container and its outbound SSH connection, and a
// session-bound script would be SIGHUP'd mid-upgrade. Detaching lets it finish
// regardless; the UI reconnects and reads the log to confirm the result.
//
// "This is the Rigger host" is detected by comparing the Docker daemon ID
// (docker info .ID) of a registered host against the local daemon's — no manual
// flag needed. That flips the UI into the countdown-and-reconnect flow.

// dockerUpdateLog is the on-host path the detached updater writes to; the status
// endpoint tails it and looks for the completion sentinel.
const dockerUpdateLog = "/tmp/rigger-docker-update.log"

// dockerHostInfo is one row of the Docker-versions view: the local daemon
// (host_id 0) or a registered remote host.
type dockerHostInfo struct {
	HostID          int64  `json:"host_id"` // 0 = the local daemon Rigger talks to
	Name            string `json:"name"`
	Reachable       bool   `json:"reachable"`
	Error           string `json:"error,omitempty"`
	ServerVersion   string `json:"server_version"`
	OSType          string `json:"os_type"`          // "linux" / "windows"
	OperatingSystem string `json:"operating_system"` // e.g. "Ubuntu 22.04.3 LTS" or "Docker Desktop"
	DaemonID        string `json:"daemon_id"`
	IsRiggerHost    bool   `json:"is_rigger_host"` // same daemon as the one running Rigger
	IsDesktop       bool   `json:"is_desktop"`     // Docker Desktop — can't be apt-updated
	Linux           bool   `json:"linux"`
	SudoOK          bool   `json:"sudo_ok"`        // passwordless sudo (or root) available over SSH
	SSHAvailable    bool   `json:"ssh_available"`  // registered host with SSH creds
	UpdateAvailable bool   `json:"update_available"`
	Updatable       bool   `json:"updatable"` // linux && !desktop && ssh && sudo
}

type dockerVersionsResp struct {
	Latest      string           `json:"latest"` // newest Docker Engine release (moby), "" if unknown
	LatestError string           `json:"latest_error,omitempty"`
	Hosts       []dockerHostInfo `json:"hosts"`
}

// GET /api/docker/versions — admin. Reports the Docker Engine version of the
// local daemon and every registered host, whether each can be updated in place
// (Linux, not Docker Desktop, SSH + passwordless sudo), and the latest published
// engine release for an "update available" hint.
func (h *Handler) DockerVersions(w http.ResponseWriter, r *http.Request) {
	latest, latestErr := dockerLatest()

	// Local daemon first (host_id 0). This is "localhost" — always shown, but it
	// isn't directly updatable from inside the container: it's updated via the
	// registered host whose daemon ID matches (flagged is_rigger_host below).
	local := dockerHostInfo{HostID: 0, Name: "This machine (local daemon)"}
	if osType, opsys, ver, id, err := localDockerInfo(); err != nil {
		local.Error = "local docker not reachable: " + err.Error()
	} else {
		local.Reachable = true
		local.OSType, local.OperatingSystem, local.ServerVersion, local.DaemonID = osType, opsys, ver, id
		local.IsRiggerHost = true
		local.IsDesktop = isDockerDesktop(opsys)
		local.Linux = strings.EqualFold(osType, "linux")
		local.UpdateAvailable = engineUpdateAvailable(latest, ver)
	}
	localID := local.DaemonID

	hosts, err := settings.ListHosts(h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	rows := make([]dockerHostInfo, len(hosts))
	var wg sync.WaitGroup
	for i := range hosts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			host := hosts[i]
			row := dockerHostInfo{HostID: host.ID, Name: host.Name, SSHAvailable: true}
			rh, derr := h.dialHost(host.ID)
			if derr != nil {
				row.Error = derr.Error()
				rows[i] = row
				return
			}
			defer rh.Close()
			out, cerr := rh.RunCombined(dockerProbeCmd)
			if cerr != nil && strings.TrimSpace(out) == "" {
				row.Error = "probe failed: " + cerr.Error()
				rows[i] = row
				return
			}
			p := parseDockerProbe(out)
			row.Reachable = p.serverVersion != "" || p.daemonID != ""
			row.OSType, row.OperatingSystem = p.osType, p.operatingSystem
			row.ServerVersion, row.DaemonID = p.serverVersion, p.daemonID
			row.SudoOK = p.sudoOK
			row.IsDesktop = isDockerDesktop(p.operatingSystem)
			row.Linux = strings.EqualFold(p.osType, "linux")
			row.IsRiggerHost = localID != "" && p.daemonID == localID
			row.UpdateAvailable = engineUpdateAvailable(latest, p.serverVersion)
			row.Updatable = row.Linux && !row.IsDesktop && row.SudoOK
			rows[i] = row
		}(i)
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, dockerVersionsResp{
		Latest:      latest,
		LatestError: latestErr,
		Hosts:       append([]dockerHostInfo{local}, rows...),
	})
}

// dockerProbeCmd gathers, in one round trip, the daemon's OS/version/ID and
// whether passwordless sudo (or root) is available for a later update.
// timeout 8 guards every remote `docker` call: the daemon can be mid-restart
// (e.g. right after an in-place update) and a bare `docker info` would block the
// SSH command — and RunCombined has no deadline — wedging the status poll exactly
// when it should report completion. If `timeout` is absent it fails fast (127),
// which is harmless (empty field), never a hang.
const dockerProbeCmd = `echo "@@INFO@@"; timeout 8 docker info --format '{{.OSType}}|{{.OperatingSystem}}|{{.ServerVersion}}|{{.ID}}' 2>/dev/null; echo "@@SUDO@@"; if [ "$(id -u)" = 0 ]; then echo root; else sudo -n true 2>/dev/null && echo yes || echo no; fi; echo "@@END@@"`

type dockerProbe struct {
	osType, operatingSystem, serverVersion, daemonID string
	sudoOK                                           bool
}

// parseDockerProbe reads the marker-delimited output of dockerProbeCmd.
func parseDockerProbe(out string) dockerProbe {
	var p dockerProbe
	section := ""
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch t {
		case "@@INFO@@", "@@SUDO@@", "@@END@@":
			section = t
			continue
		case "":
			continue
		}
		switch section {
		case "@@INFO@@":
			if p.serverVersion == "" && p.osType == "" {
				p.osType, p.operatingSystem, p.serverVersion, p.daemonID = splitInfoLine(t)
			}
		case "@@SUDO@@":
			p.sudoOK = t == "root" || t == "yes"
		}
	}
	return p
}

// splitInfoLine parses "linux|Ubuntu 22.04|24.0.7|ABCD:..." into its four fields.
func splitInfoLine(s string) (osType, opsys, ver, id string) {
	parts := strings.Split(s, "|")
	get := func(i int) string {
		if i < len(parts) {
			return strings.TrimSpace(parts[i])
		}
		return ""
	}
	return get(0), get(1), get(2), get(3)
}

// localDockerInfo reads the local daemon's OS/version/ID via the local docker CLI.
func localDockerInfo() (osType, opsys, ver, id string, err error) {
	out, err := (executor.Local{}).DockerOutput(executor.Spec{
		Args:    []string{"info", "--format", "{{.OSType}}|{{.OperatingSystem}}|{{.ServerVersion}}|{{.ID}}"},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return "", "", "", "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			osType, opsys, ver, id = splitInfoLine(t)
			break
		}
	}
	return osType, opsys, ver, id, nil
}

// isDockerDesktop reports whether the daemon is Docker Desktop (Mac/Windows),
// which can't be updated with the Linux convenience script.
func isDockerDesktop(operatingSystem string) bool {
	return strings.Contains(strings.ToLower(operatingSystem), "docker desktop")
}

// engineUpdateAvailable reports whether latest is a strictly newer engine version
// than current. Unlike semverNewer it does NOT fall back to a string-differ when a
// value doesn't parse — a non-semver tag (e.g. an odd release name) must never be
// reported as an update, or equal-but-differently-written versions ("v29.6.1" vs
// "29.6.1") would show a phantom update. Empty/unparseable inputs ⇒ false.
func engineUpdateAvailable(latest, current string) bool {
	lm, ln, lp, lok := parseSemver(latest)
	cm, cn, cp, cok := parseSemver(current)
	if !lok || !cok {
		return false
	}
	if lm != cm {
		return lm > cm
	}
	if ln != cn {
		return ln > cn
	}
	return lp > cp
}

// normalizeDockerTag turns a moby release tag into a plain version. moby tags its
// GitHub releases as "docker-v29.6.1"; strip that prefix so it parses as semver and
// displays cleanly.
func normalizeDockerTag(tag string) string {
	return strings.TrimPrefix(strings.TrimSpace(tag), "docker-")
}

// --- latest-engine lookup (moby releases), cached like the Rigger update check --

var (
	dockerLatestMu  sync.Mutex
	dockerLatestVal string
	dockerLatestErr string
	dockerLatestExp time.Time
)

const dockerLatestTTL = time.Hour

// dockerLatest returns the newest published Docker Engine (moby) release tag,
// e.g. "v27.3.1". Cached for an hour; failures are soft (returned as the error
// string, HTTP stays 200) so the version view still renders.
func dockerLatest() (version, errMsg string) {
	dockerLatestMu.Lock()
	if time.Now().Before(dockerLatestExp) {
		v, e := dockerLatestVal, dockerLatestErr
		dockerLatestMu.Unlock()
		return v, e
	}
	dockerLatestMu.Unlock()

	v, e := fetchGitHubLatestTag("moby/moby")
	v = normalizeDockerTag(v)
	dockerLatestMu.Lock()
	dockerLatestVal, dockerLatestErr, dockerLatestExp = v, e, time.Now().Add(dockerLatestTTL)
	dockerLatestMu.Unlock()
	return v, e
}

// fetchGitHubLatestTag returns the tag_name of a repo's latest (non-prerelease)
// release, or a soft error string.
func fetchGitHubLatestTag(slug string) (tag, errMsg string) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+slug+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "rigger-docker-version")
	resp, err := client.Do(req)
	if err != nil {
		return "", "couldn't reach GitHub: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "GitHub returned status " + strconv.Itoa(resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "couldn't parse GitHub response"
	}
	return rel.TagName, ""
}

// POST /api/docker/update — admin. Body: {"host_id": N}. Validates the target is
// an updatable Linux host with SSH + sudo, then launches the detached Docker
// convenience-script update. Returns 202 with is_rigger_host so the UI knows to
// switch into the countdown-and-reconnect flow.
func (h *Handler) DockerUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		HostID int64 `json:"host_id"`
	}
	if err := readJSON(r, &body); err != nil || body.HostID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "select a registered host to update — the local daemon is updated via the host entry that points at this machine (SSH)"})
		return
	}

	rh, err := h.dialHost(body.HostID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "can't reach host over SSH: " + err.Error()})
		return
	}
	defer rh.Close()

	// Pre-flight so the operator gets an immediate, specific reason on failure
	// rather than a silent detached error.
	out, _ := rh.RunCombined(dockerPreflightCmd)
	pf := parseDockerPreflight(out)
	if !strings.EqualFold(pf.osType, "linux") || pf.desktop {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "in-place Docker update is supported only on Linux hosts (this host reports: " + firstNonEmpty(pf.operatingSystem, pf.osType, "unknown") + ")"})
		return
	}
	if !pf.hasDownloader {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host has neither curl nor wget — install one so the Docker convenience script can be fetched"})
		return
	}
	if !pf.sudoOK {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the SSH user needs root or passwordless sudo to update Docker on this host"})
		return
	}

	localID := ""
	if _, _, _, id, lerr := localDockerInfo(); lerr == nil {
		localID = id
	}
	isRiggerHost := localID != "" && pf.daemonID == localID

	if _, err := rh.RunCombined(dockerUpdateLaunchCmd); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to launch updater: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":         "started",
		"host_id":        body.HostID,
		"is_rigger_host": isRiggerHost,
		"log":            dockerUpdateLog,
		"message":        "Docker update started on the host. This runs detached so it survives the daemon restart.",
	})
}

// dockerPreflightCmd checks Linux/desktop, a downloader, and sudo up front.
const dockerPreflightCmd = `echo "@@INFO@@"; timeout 8 docker info --format '{{.OSType}}|{{.OperatingSystem}}|{{.ServerVersion}}|{{.ID}}' 2>/dev/null; echo "@@TOOLS@@"; command -v curl >/dev/null 2>&1 && echo curl; command -v wget >/dev/null 2>&1 && echo wget; echo "@@SUDO@@"; if [ "$(id -u)" = 0 ]; then echo root; else sudo -n true 2>/dev/null && echo yes || echo no; fi; echo "@@END@@"`

type dockerPreflight struct {
	osType, operatingSystem, daemonID string
	desktop, hasDownloader, sudoOK    bool
}

func parseDockerPreflight(out string) dockerPreflight {
	var pf dockerPreflight
	section := ""
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch t {
		case "@@INFO@@", "@@TOOLS@@", "@@SUDO@@", "@@END@@":
			section = t
			continue
		case "":
			continue
		}
		switch section {
		case "@@INFO@@":
			if pf.osType == "" {
				pf.osType, pf.operatingSystem, _, pf.daemonID = splitInfoLine(t)
				pf.desktop = isDockerDesktop(pf.operatingSystem)
			}
		case "@@TOOLS@@":
			if t == "curl" || t == "wget" {
				pf.hasDownloader = true
			}
		case "@@SUDO@@":
			pf.sudoOK = t == "root" || t == "yes"
		}
	}
	return pf
}

// dockerUpdateLaunchCmd starts the update fully detached (setsid + background +
// stdio to a log and /dev/null) so it outlives this SSH session and any restart
// of the Rigger container. It writes a RIGGER_UPDATE_DONE sentinel with the exit
// code so the status endpoint can report success/failure. The echo at the end
// makes the foreground command return immediately.
const dockerUpdateLaunchCmd = `setsid sh -c '
  echo "=== rigger docker update started $(date -u 2>/dev/null) ===";
  if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo -n"; fi;
  if command -v curl >/dev/null 2>&1; then curl -fsSL https://get.docker.com -o /tmp/rigger-get-docker.sh;
  elif command -v wget >/dev/null 2>&1; then wget -qO /tmp/rigger-get-docker.sh https://get.docker.com;
  else echo "no curl/wget"; echo "RIGGER_UPDATE_DONE rc=91"; exit 91; fi;
  $SUDO sh /tmp/rigger-get-docker.sh; rc=$?;
  echo "RIGGER_UPDATE_DONE rc=$rc";
' > ` + dockerUpdateLog + ` 2>&1 < /dev/null & echo started`

// GET /api/docker/update/status?host_id=N — admin. SSHes to the host, tails the
// updater log, and reports whether it finished (sentinel present), its exit code,
// and the daemon's current version (to confirm the new version is live).
func (h *Handler) DockerUpdateStatus(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("host_id")), 10, 64)
	if id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_id is required"})
		return
	}
	rh, err := h.dialHost(id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "can't reach host over SSH: " + err.Error()})
		return
	}
	defer rh.Close()

	// The log (with the completion sentinel) is read FIRST and can't hang; the
	// version probe is bounded so a daemon mid-restart can't block the whole poll.
	out, _ := rh.RunCombined(`echo "@@LOG@@"; cat ` + dockerUpdateLog + ` 2>/dev/null; echo "@@VER@@"; timeout 8 docker info --format '{{.ServerVersion}}' 2>/dev/null; echo "@@END@@"`)
	logText, ver := parseUpdateStatus(out)

	done, rc := false, 0
	if i := strings.LastIndex(logText, "RIGGER_UPDATE_DONE rc="); i >= 0 {
		done = true
		rest := strings.TrimSpace(logText[i+len("RIGGER_UPDATE_DONE rc="):])
		if f := strings.FieldsFunc(rest, func(r rune) bool { return r < '0' || r > '9' }); len(f) > 0 {
			rc, _ = strconv.Atoi(f[0])
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running":        !done,
		"done":           done,
		"rc":             rc,
		"server_version": ver,
		"log":            tailStr(logText, 6000),
	})
}

// parseUpdateStatus splits the marker-delimited status output into the log body
// and the current server version.
func parseUpdateStatus(out string) (logText, ver string) {
	logStart := strings.Index(out, "@@LOG@@")
	verStart := strings.Index(out, "@@VER@@")
	endStart := strings.Index(out, "@@END@@")
	if logStart < 0 || verStart < 0 {
		return strings.TrimSpace(out), ""
	}
	logText = strings.TrimSpace(out[logStart+len("@@LOG@@") : verStart])
	verEnd := endStart
	if verEnd < 0 {
		verEnd = len(out)
	}
	ver = strings.TrimSpace(out[verStart+len("@@VER@@") : verEnd])
	return logText, ver
}
