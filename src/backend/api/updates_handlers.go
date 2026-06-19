package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mansoor/rigger/ui/internal/buildinfo"
	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/settings"
)

// imageRepo is the published image the updater pulls + recreates from.
const imageRepo = "ghcr.io/mansoor/rigger"

// prevTagKey stores the image tag that was running before the last apply, so a
// rollback can pin back to it (app_settings KV).
const prevTagKey = "self_update_prev_tag"

// Self-update Phase 2 — check for a newer Rigger release.
//
// Reads the latest GitHub Release for the Rigger repo and compares its tag to
// the running build (internal/buildinfo). No telemetry is sent; this is a plain
// read of the public releases list. Results are cached briefly so a Check button
// (and any polling) doesn't burn the unauthenticated GitHub rate limit.

// repoSlug is the GitHub owner/name whose releases drive updates. Overridable for
// forks via RIGGER_REPO_SLUG.
func repoSlug() string {
	if s := strings.TrimSpace(os.Getenv("RIGGER_REPO_SLUG")); s != "" {
		return s
	}
	return "mansoor/rigger"
}

type updateInfo struct {
	Current         string `json:"current"`          // running version (e.g. v0.1.0 or "dev")
	Latest          string `json:"latest"`           // newest release tag ("" if none/unknown)
	UpdateAvailable bool   `json:"update_available"` // latest is strictly newer than current
	Dev             bool   `json:"dev"`              // running a source/dev build (can't compare)
	Notes           string `json:"notes"`            // changelog (release body)
	PublishedAt     string `json:"published_at"`     // ISO-8601
	HTMLURL         string `json:"html_url"`         // release page
	CheckedAt       int64  `json:"checked_at"`       // epoch ms of this check
	Error           string `json:"error,omitempty"`  // soft error (e.g. GitHub unreachable)
}

var (
	updateCacheMu  sync.Mutex
	updateCache    *updateInfo
	updateCacheExp time.Time
)

const updateCacheTTL = 10 * time.Minute

// GET /api/updates/check — admin. Returns the running version + the latest
// release, whether an update is available, and its changelog. Cached for a few
// minutes; pass ?force=1 to bypass the cache.
func (h *Handler) CheckUpdates(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "1"

	updateCacheMu.Lock()
	if !force && updateCache != nil && time.Now().Before(updateCacheExp) {
		cached := *updateCache
		updateCacheMu.Unlock()
		writeJSON(w, http.StatusOK, cached)
		return
	}
	updateCacheMu.Unlock()

	info := fetchUpdateInfo()

	updateCacheMu.Lock()
	updateCache = info
	updateCacheExp = time.Now().Add(updateCacheTTL)
	updateCacheMu.Unlock()

	writeJSON(w, http.StatusOK, *info)
}

// fetchUpdateInfo reads the latest release from GitHub and builds the comparison.
// Failures are returned as a soft Error (HTTP stays 200) so the UI can show
// "couldn't check" without treating it as a hard error.
func fetchUpdateInfo() *updateInfo {
	cur := buildinfo.Version
	info := &updateInfo{
		Current:   cur,
		Dev:       cur == "" || cur == "dev",
		CheckedAt: time.Now().UnixMilli(),
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+repoSlug()+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "rigger-self-update")

	resp, err := client.Do(req)
	if err != nil {
		info.Error = "couldn't reach GitHub: " + err.Error()
		return info
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No published releases yet — nothing to update to.
		return info
	}
	if resp.StatusCode != http.StatusOK {
		info.Error = "GitHub returned status " + strconv.Itoa(resp.StatusCode)
		return info
	}

	var rel struct {
		TagName     string `json:"tag_name"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
		HTMLURL     string `json:"html_url"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		info.Error = "couldn't parse GitHub response"
		return info
	}

	info.Latest = rel.TagName
	info.Notes = rel.Body
	info.PublishedAt = rel.PublishedAt
	info.HTMLURL = rel.HTMLURL
	// A dev build never reports an update (we can't know what it's based on).
	if !info.Dev && rel.TagName != "" {
		info.UpdateAvailable = semverNewer(rel.TagName, cur)
	}
	return info
}

// currentImageTag is the image tag this instance was started with (from compose
// via RIGGER_IMAGE_TAG), defaulting to "latest".
func currentImageTag() string {
	if t := strings.TrimSpace(os.Getenv("RIGGER_IMAGE_TAG")); t != "" {
		return t
	}
	return "latest"
}

// runningPipelineCount returns how many pipeline runs are mid-flight — the apply
// guard refuses to restart Rigger while one is executing.
func (h *Handler) runningPipelineCount() int {
	var n int
	h.db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE status='running'`).Scan(&n) //nolint:errcheck
	return n
}

// POST /api/updates/apply — admin. Pulls the requested tag (default "latest")
// and recreates the Rigger container by spawning a DETACHED helper that runs
// `docker compose pull rigger && docker compose up -d rigger` against the install
// (a container can't recreate itself). Returns 202; the UI reconnects on restart.
// Body: { "tag": "0.2.0" } (optional; leading "v" tolerated).
func (h *Handler) ApplyUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tag string `json:"tag"`
	}
	_ = readJSON(r, &body) //nolint:errcheck — empty ⇒ latest

	target := strings.TrimPrefix(strings.TrimSpace(body.Tag), "v")
	if target == "" {
		target = "latest"
	}
	// Record the tag we're moving away from so rollback can return to it. Prefer a
	// concrete version (buildinfo) over a floating "latest".
	prev := strings.TrimPrefix(buildinfo.Version, "v")
	if prev == "" || prev == "dev" {
		prev = currentImageTag()
	}
	h.startUpdate(w, target, prev)
}

// POST /api/updates/rollback — admin. Re-applies the tag recorded before the last
// apply (app_settings prevTagKey).
func (h *Handler) RollbackUpdate(w http.ResponseWriter, r *http.Request) {
	prev := strings.TrimSpace(settings.AppSetting(h.db, prevTagKey))
	if prev == "" || prev == "dev" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no previous version recorded to roll back to"})
		return
	}
	h.startUpdate(w, prev, currentImageTag())
}

// startUpdate validates preconditions, records the rollback tag, and launches the
// detached recreate helper. Shared by apply + rollback.
func (h *Handler) startUpdate(w http.ResponseWriter, target, prev string) {
	hostDir := strings.TrimSpace(os.Getenv("RIGGER_HOST_DIR"))
	if hostDir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "one-click update isn't enabled for this install (RIGGER_HOST_DIR not set). Re-run install.sh, or update manually: cd <install>/src && docker compose pull && docker compose up -d"})
		return
	}
	if n := h.runningPipelineCount(); n > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a pipeline run is in progress — wait for it to finish before updating"})
		return
	}

	_ = settings.SetAppSetting(h.db, prevTagKey, prev) //nolint:errcheck

	// Helper base = the currently-running Rigger image (already local, guaranteed to
	// have docker + the compose plugin) with sh as entrypoint. It bind-mounts the
	// install dir, points .env at the target tag, then pulls + recreates only the
	// rigger service. The 2s sleep lets this HTTP 202 flush before we get killed.
	script := `sleep 2
cd /work/src || exit 1
if grep -q '^RIGGER_IMAGE_TAG=' .env 2>/dev/null; then
  sed -i "s|^RIGGER_IMAGE_TAG=.*|RIGGER_IMAGE_TAG=${RIGGER_TARGET_TAG}|" .env
else
  echo "RIGGER_IMAGE_TAG=${RIGGER_TARGET_TAG}" >> .env
fi
docker compose pull rigger && docker compose up -d rigger`

	args := []string{
		"run", "-d", "--rm",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", hostDir + ":/work",
		"-e", "RIGGER_TARGET_TAG=" + target,
		"--entrypoint", "sh",
		imageRepo + ":" + currentImageTag(),
		"-c", script,
	}
	var out bytes.Buffer
	if err := (executor.Local{}).Docker(executor.Spec{Args: args, Stdout: &out, Stderr: &out, Timeout: 30 * time.Second}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "couldn't start the update helper: " + strings.TrimSpace(out.String())})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "updating",
		"target": target,
		"message": "Rigger is updating to " + target + " and will restart in a few seconds. This page will reconnect automatically.",
	})
}

// semverNewer reports whether tag a is a strictly newer semver than b. Leading
// 'v' is tolerated. On any parse failure it falls back to "differs" (a != b) so
// a non-standard tag still surfaces as an available update rather than hiding it.
func semverNewer(a, b string) bool {
	am, an, ap, aok := parseSemver(a)
	bm, bn, bp, bok := parseSemver(b)
	if !aok || !bok {
		return strings.TrimPrefix(a, "v") != strings.TrimPrefix(b, "v")
	}
	if am != bm {
		return am > bm
	}
	if an != bn {
		return an > bn
	}
	return ap > bp
}

// parseSemver splits "v1.2.3" / "1.2.3" (ignoring any pre-release suffix) into
// its numeric major/minor/patch. ok is false if it doesn't look like semver.
func parseSemver(s string) (maj, min, pat int, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i] // drop pre-release / build metadata
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var err error
	if maj, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, 0, false
	}
	if min, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, 0, false
	}
	if pat, err = strconv.Atoi(parts[2]); err != nil {
		return 0, 0, 0, false
	}
	return maj, min, pat, true
}
