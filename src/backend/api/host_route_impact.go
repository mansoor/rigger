package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/shell"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// routeImpact is one environment whose auto-URL (magic-DNS / nip.io style) embeds a
// host's web address. When that address changes, the running containers keep the
// OLD address baked into their Traefik router labels, so the new URL matches no
// router (404) until the env is refreshed (compose regenerated → containers
// recreated with the new labels).
type routeImpact struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
	Env       string `json:"env"`
	OldURL    string `json:"old_url"`
	NewURL    string `json:"new_url"`
}

// prefixOwner recovers the on-disk (workspace, project) keys — plus the parsed
// config and env list — for a resource_prefix. Host bindings are keyed by prefix;
// this is what lets us compute URLs and dispatch a refresh for each bound env.
type prefixOwner struct {
	ws   string
	proj string
	cfg  []byte
	envs []string
}

// buildPrefixIndex walks every workspace/project on disk and indexes by
// resource_prefix. Cheap enough for the rare host-edit path; avoids guessing the
// {ws}_{proj} split (either key may contain an underscore).
func (h *Handler) buildPrefixIndex() map[string]prefixOwner {
	idx := map[string]prefixOwner{}
	wsEntries, err := os.ReadDir(h.workspacesDir)
	if err != nil {
		return idx
	}
	for _, we := range wsEntries {
		if !we.IsDir() {
			continue
		}
		ws := we.Name()
		projEntries, perr := os.ReadDir(wspath.ProjectsDir(h.workspacesDir, ws))
		if perr != nil {
			continue
		}
		for _, pe := range projEntries {
			if !pe.IsDir() {
				continue
			}
			proj := pe.Name()
			raw, rerr := os.ReadFile(wspath.ConfigPath(h.workspacesDir, ws, proj))
			if rerr != nil {
				continue
			}
			var cfg struct {
				Project struct {
					ResourcePrefix string `json:"resource_prefix"`
				} `json:"project"`
				Environments map[string]json.RawMessage `json:"environments"`
			}
			if json.Unmarshal(raw, &cfg) != nil || cfg.Project.ResourcePrefix == "" {
				continue
			}
			envs := make([]string, 0, len(cfg.Environments))
			for e := range cfg.Environments {
				envs = append(envs, e)
			}
			idx[cfg.Project.ResourcePrefix] = prefixOwner{ws: ws, proj: proj, cfg: raw, envs: envs}
		}
	}
	return idx
}

// hostRouteImpact returns the environments whose auto-URL embeds this host's web
// address and would break when it changes to newWeb. Empty when: the host is
// private (its apps route via the control-plane gateway, not the host address);
// newWeb is unchanged; or no bound env has a host-embedded URL (a base-domain
// workspace's URL uses the domain, not the host IP, so EnvRouteURL is identical for
// old and new — correctly excluded).
func (h *Handler) hostRouteImpact(host *settings.Host, newWeb string) []routeImpact {
	if host == nil || host.IsPrivate() {
		return nil
	}
	oldWeb := host.WebAddress()
	newWeb = strings.TrimSpace(newWeb)
	if newWeb == "" || oldWeb == newWeb {
		return nil
	}
	bindings, err := settings.HostEnvBindings(h.db, host.ID)
	if err != nil || len(bindings) == 0 {
		return nil
	}
	idx := h.buildPrefixIndex()
	var out []routeImpact
	for _, b := range bindings {
		owner, ok := idx[b.Project]
		if !ok {
			continue
		}
		baseDomain := settings.EffectiveBaseDomain(h.db, owner.ws)
		mode := settings.EffectiveAutoURLMode(h.db, owner.ws)
		envs := owner.envs
		if b.Env != "" {
			envs = []string{b.Env} // an explicit binding targets one env; env="" is the project default → all envs
		}
		for _, e := range envs {
			oldURL, ok1 := composegen.EnvRouteURL(owner.cfg, e, baseDomain, mode, oldWeb)
			newURL, ok2 := composegen.EnvRouteURL(owner.cfg, e, baseDomain, mode, newWeb)
			if !ok1 || !ok2 || oldURL == newURL {
				continue // no web route, or the URL doesn't embed the host address
			}
			out = append(out, routeImpact{Workspace: owner.ws, Project: owner.proj, Env: e, OldURL: oldURL, NewURL: newURL})
		}
	}
	return out
}

// autoHostRouteImpact returns the environments whose auto-URL embeds the GLOBAL
// magic-DNS host (auto_url_host) and would break when it changes oldHost→newHost.
// This is the control-plane analogue of hostRouteImpact: it covers envs that route
// via EffectiveAutoURLHost — LOCAL envs and PRIVATE-remote envs (behind the gateway)
// — and excludes: workspaces that OVERRIDE auto_url_host (unaffected by the global
// change), envs on a PUBLIC remote host (their URL uses the host address, not
// auto_url_host), and base-domain/localhost envs (URL doesn't embed the host, so
// EnvRouteURL is identical old vs new).
func (h *Handler) autoHostRouteImpact(oldHost, newHost string) []routeImpact {
	oldHost = strings.TrimSpace(oldHost)
	newHost = strings.TrimSpace(newHost)
	if newHost == "" || oldHost == newHost {
		return nil
	}
	var out []routeImpact
	for prefix, owner := range h.buildPrefixIndex() {
		// A workspace with its own auto_url_host override is unaffected by the global change.
		if wsVals, _ := settings.GetWorkspaceSettings(h.db, owner.ws); wsVals != nil && strings.TrimSpace(wsVals["auto_url_host"]) != "" {
			continue
		}
		baseDomain := settings.EffectiveBaseDomain(h.db, owner.ws)
		mode := settings.EffectiveAutoURLMode(h.db, owner.ws)
		for _, e := range owner.envs {
			// Public remote host → its apps route at the host address, not auto_url_host.
			if host, _ := settings.HostForEnv(h.db, prefix, e); host != nil && !host.IsPrivate() {
				continue
			}
			oldURL, ok1 := composegen.EnvRouteURL(owner.cfg, e, baseDomain, mode, oldHost)
			newURL, ok2 := composegen.EnvRouteURL(owner.cfg, e, baseDomain, mode, newHost)
			if !ok1 || !ok2 || oldURL == newURL {
				continue
			}
			out = append(out, routeImpact{Workspace: owner.ws, Project: owner.proj, Env: e, OldURL: oldURL, NewURL: newURL})
		}
	}
	return out
}

// globalAppHost returns the current global App host/IP — app_host, with the legacy
// auto_url_host fallback (mirrors settings.AppHost).
func (h *Handler) globalAppHost() string {
	if v := h.appSetting("app_host"); v != "" {
		return v
	}
	return h.appSetting("auto_url_host")
}

// GET /api/settings/route-impact?app_host=<new>
// Dry-run for the admin Domain & TLS editor: which envs' magic-DNS URLs would break
// if the global App host/IP is changed to the given value.
func (h *Handler) SettingsRouteImpact(w http.ResponseWriter, r *http.Request) {
	newHost := r.URL.Query().Get("app_host")
	writeJSON(w, http.StatusOK, h.autoHostRouteImpact(h.globalAppHost(), newHost))
}

// webAddrFrom computes a host's effective web address from proposed form values,
// mirroring settings.Host.WebAddress (public_address wins when set, but only for a
// public/direct host — a private host has no public address).
func webAddrFrom(address, publicAddress, reachability string) string {
	address = strings.TrimSpace(address)
	publicAddress = strings.TrimSpace(publicAddress)
	if reachability != "private" && publicAddress != "" {
		return publicAddress
	}
	return address
}

// GET /api/hosts/{id}/route-impact?address=&public_address=&reachability=
// Dry-run for the host editor: which bound environments' URLs would break if the
// host is saved with the given address. Drives the pre-save warning.
func (h *Handler) HostRouteImpact(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/route-impact")
	id, err := parseSettingsID(path, "/api/hosts/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	host, _ := settings.GetHost(h.db, id)
	if host == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	q := r.URL.Query()
	newWeb := webAddrFrom(q.Get("address"), q.Get("public_address"), q.Get("reachability"))
	writeJSON(w, http.StatusOK, h.hostRouteImpact(host, newWeb))
}

// GET /api/workspaces/{ws}/hosts/{id}/route-impact — workspace-scoped variant (same
// global host id; guarded by workspace visibility).
func (h *Handler) WorkspaceHostRouteImpact(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsHostID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	host, _ := h.ownsHost(ws, id)
	if host == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	q := r.URL.Query()
	newWeb := webAddrFrom(q.Get("address"), q.Get("public_address"), q.Get("reachability"))
	writeJSON(w, http.StatusOK, h.hostRouteImpact(host, newWeb))
}

// refreshImpactedRoutes corrects each impacted env's routing after a host/IP change,
// HONORING its running state: a RUNNING env is refreshed in place (regenerate compose
// + up → containers recreated with the new Traefik labels, stays running); a STOPPED
// env only has its compose regenerated (regen → on-disk labels fixed for its next
// deploy, never started). Best-effort and asynchronous: a slow remote deploy must not
// block the settings-save response, and one env's failure must not stop the rest.
func (h *Handler) refreshImpactedRoutes(impacts []routeImpact) {
	if len(impacts) == 0 {
		return
	}
	go func() {
		for _, im := range impacts {
			cmd := "regen"
			if h.bridge.EnvRunning(im.Workspace, im.Project, im.Env) {
				cmd = "refresh"
			}
			var buf bytes.Buffer
			_ = h.bridge.Run(shell.RunOptions{ //nolint:errcheck
				Workspace: im.Workspace,
				Project:   im.Project,
				Command:   cmd,
				Env:       im.Env,
				Stdout:    &buf,
				Stderr:    &buf,
			})
		}
	}()
}
