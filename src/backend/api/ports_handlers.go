package api

// Host-aware port-conflict checking (tiers C+D). Given proposed host-port
// mappings, report which are already taken on their target host — either
// published by a running container (live, D) or declared by another workspace's
// config bound to that host (static, C). Non-blocking: the UI only warns.

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// publishedPortRe captures the host port from a `docker ps` Ports field entry,
// e.g. "0.0.0.0:8080->80/tcp" → 8080. Exposed-only ports ("3306/tcp") have no
// "->" and are ignored.
var publishedPortRe = regexp.MustCompile(`(\d+)->`)

type portCheckItem struct {
	HostID  int64  `json:"host_id"`
	Port    int    `json:"port"`
	Service string `json:"service"`
}

type portCheckReq struct {
	HostPorts        []portCheckItem `json:"host_ports"`
	ExcludeWorkspace string          `json:"exclude_workspace"`
}

type portConflict struct {
	HostID  int64  `json:"host_id"`
	Port    int    `json:"port"`
	Service string `json:"service"`
	UsedBy  string `json:"used_by"`
}

// PortCheck — POST /api/port-check. Returns the subset of the requested
// (host, port, service) mappings whose host port is already in use.
func (h *Handler) PortCheck(w http.ResponseWriter, r *http.Request) {
	var req portCheckReq
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	// Inspect each distinct target host once.
	hosts := map[int64]bool{}
	for _, it := range req.HostPorts {
		if it.Port > 0 {
			hosts[it.HostID] = true
		}
	}
	inUse := map[int64]map[int]string{}
	for hid := range hosts {
		inUse[hid] = h.portsInUseForHost(hid, req.ExcludeWorkspace)
	}

	conflicts := []portConflict{}
	for _, it := range req.HostPorts {
		if it.Port <= 0 {
			continue
		}
		if usedBy, ok := inUse[it.HostID][it.Port]; ok {
			conflicts = append(conflicts, portConflict{HostID: it.HostID, Port: it.Port, Service: it.Service, UsedBy: usedBy})
		}
	}
	writeJSON(w, http.StatusOK, conflicts)
}

// portsInUseForHost returns published host port → description for one host:
// live running containers first (D), then ports declared by other workspaces'
// configs that target this host (C). excludeWs is skipped so an edited
// workspace never flags itself.
func (h *Handler) portsInUseForHost(hostID int64, excludeWs string) map[int]string {
	inUse := map[int]string{}

	// D — live published ports on the host's daemon (local or remote over SSH).
	if ex, err := h.bridge.HostExecutor(hostID); err == nil {
		if out, derr := ex.DockerOutput(executor.Spec{Args: []string{"ps", "--format", "{{.Names}}|{{.Ports}}"}}); derr == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if line == "" {
					continue
				}
				parts := strings.SplitN(line, "|", 2)
				name := parts[0]
				if excludeWs != "" && strings.HasPrefix(name, excludeWs+"_") {
					continue // an edited workspace's own containers don't count
				}
				if len(parts) < 2 {
					continue
				}
				for _, m := range publishedPortRe.FindAllStringSubmatch(parts[1], -1) {
					if p, e := strconv.Atoi(m[1]); e == nil {
						inUse[p] = name + " (running)"
					}
				}
			}
		}
	}

	// C — host ports declared by other projects' configs targeting this host.
	// Iterate the nested Workspace→Project layout; key host bindings by the
	// project's resource prefix ({workspace}_{project}), which excludeWs matches.
	if wsEntries, err := os.ReadDir(h.workspacesDir); err == nil {
		for _, we := range wsEntries {
			if !we.IsDir() {
				continue
			}
			wsName := we.Name()
			projEntries, perr := os.ReadDir(wspath.ProjectsDir(h.workspacesDir, wsName))
			if perr != nil {
				continue
			}
			for _, pe := range projEntries {
				if !pe.IsDir() {
					continue
				}
				pkey := wsName + "_" + pe.Name()
				if pkey == excludeWs {
					continue // the edited project's own ports don't count
				}
				cfg, err := readWsPortConfig(wspath.ConfigPath(h.workspacesDir, wsName, pe.Name()))
				if err != nil || !cfg.targetsHost(h.db, pkey, hostID) {
					continue
				}
				for _, p := range cfg.hostPorts() {
					if _, ok := inUse[p]; !ok {
						inUse[p] = "configured in " + pkey
					}
				}
			}
		}
	}
	return inUse
}

// wsPortConfig is the minimal slice of config.json needed for port checks.
type wsPortConfig struct {
	Environments map[string]json.RawMessage `json:"environments"`
	Images       []struct {
		HostPort   string   `json:"host_port"`
		ExtraPorts []string `json:"extra_ports"`
	} `json:"images"`
}

func readWsPortConfig(path string) (*wsPortConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c wsPortConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// hostPorts returns every declared host port (host_port + extra_ports' host side).
func (c *wsPortConfig) hostPorts() []int {
	out := []int{}
	for _, img := range c.Images {
		if p, e := strconv.Atoi(strings.TrimSpace(img.HostPort)); e == nil {
			out = append(out, p)
		}
		for _, ep := range img.ExtraPorts {
			if p, e := strconv.Atoi(strings.TrimSpace(strings.SplitN(ep, ":", 2)[0])); e == nil {
				out = append(out, p)
			}
		}
	}
	return out
}

// targetsHost reports whether any of the workspace's environments run on hostID.
// Env→host resolution mirrors settings.HostForEnv: explicit env row wins, env=''
// is the workspace default, absent ⇒ local (0).
func (c *wsPortConfig) targetsHost(d *db.DB, ws string, hostID int64) bool {
	bindings, _ := settings.EnvHosts(d, ws)
	boundEnvs := map[string]int64{}
	for _, b := range bindings {
		boundEnvs[b.Env] = b.HostID
	}
	defaultHost := boundEnvs[""] // 0 when no default row (local)
	if len(c.Environments) == 0 {
		return hostID == defaultHost
	}
	for env := range c.Environments {
		hid := defaultHost
		if h, ok := boundEnvs[env]; ok {
			hid = h
		}
		if hid == hostID {
			return true
		}
	}
	return false
}
