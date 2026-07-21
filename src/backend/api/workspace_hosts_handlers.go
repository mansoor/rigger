package api

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Workspace-scoped Remote Hosts (Phase 3: settings scopes). A workspace sees its
// own hosts (owner_scope='ws:{key}') plus any global host granted to it. It may
// create/edit/delete only its own; global hosts are read-only here and managed
// from the admin Settings page.

// wsHostID parses the {hostid} path value set by the router for
// /api/workspaces/{ws}/hosts/{id}/... routes.
func wsHostID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("hostid"), 10, 64)
}

// ownsHost loads a host and reports whether it is private to the given workspace.
// Returns (host, owned). host is nil when it doesn't exist.
func (h *Handler) ownsHost(wsKey string, id int64) (*settings.Host, bool) {
	host, err := settings.GetHost(h.db, id)
	if err != nil || host == nil {
		return nil, false
	}
	return host, host.WorkspaceScope() == wsKey
}

// GET /api/workspaces/{ws}/hosts — the workspace's host pool (own + granted globals).
func (h *Handler) ListWorkspaceHosts(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	hosts, err := settings.ListHostsForWorkspace(h.db, ws)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

// POST /api/workspaces/{ws}/hosts — create a host private to this workspace.
func (h *Handler) CreateWorkspaceHost(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if _, err := os.Stat(wspath.WorkspaceMeta(h.workspacesDir, ws)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	var b hostBody
	if err := readJSON(r, &b); err != nil || b.Name == "" || b.Address == "" || b.SSHUser == "" || (b.SSHKey == "" && !b.UseManagedKey) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, address, ssh_user and an SSH key (pasted or Rigger-managed) are required"})
		return
	}
	keyEnc, ok := h.hostKeyEnc(w, b)
	if !ok {
		return
	}
	host, err := settings.CreateHost(h.db, b.Name, b.Address, b.SSHPort, b.SSHUser, keyEnc, b.WorkspacesDir, settings.WorkspaceOwnerScope(ws))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a host with that name already exists"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if b.Reachability == "private" {
		_ = settings.SetHostReachability(h.db, host.ID, "private") //nolint:errcheck
		host.Reachability = "private"
	}
	if b.PublicAddress != "" {
		_ = settings.SetHostPublicAddress(h.db, host.ID, b.PublicAddress) //nolint:errcheck
		host.PublicAddress = strings.TrimSpace(b.PublicAddress)
	}
	// Provision the remote workspaces dir on registration, same as the admin path —
	// otherwise a workspace-registered host has no /…/workspaces until first deploy.
	writeJSON(w, http.StatusCreated, h.provisionHostWorkspacesDir(host))
}

// PUT /api/workspaces/{ws}/hosts/{id} — edit a host owned by this workspace.
func (h *Handler) UpdateWorkspaceHost(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsHostID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	host, owned := h.ownsHost(ws, id)
	if host == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !owned {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this is a shared global host — manage it from Settings"})
		return
	}
	var b hostBody
	if err := readJSON(r, &b); err != nil || b.Name == "" || b.Address == "" || b.SSHUser == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, address and ssh_user are required"})
		return
	}
	keyEnc, ok := h.hostKeyEnc(w, b)
	if !ok {
		return
	}
	// Guard the Deploy → Build-only transition (same rule as the admin path).
	if b.BuildOnly && !host.BuildOnly {
		if msg, gerr := buildOnlyConflict(h.db, id); gerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": gerr.Error()})
			return
		} else if msg != "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": msg})
			return
		}
	}
	updated, err := settings.UpdateHost(h.db, id, b.Name, b.Address, b.SSHPort, b.SSHUser, keyEnc, b.WorkspacesDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = settings.SetHostBuildOnly(h.db, id, b.BuildOnly) //nolint:errcheck
	updated.BuildOnly = b.BuildOnly
	_ = settings.SetHostReachability(h.db, id, b.Reachability) //nolint:errcheck
	updated.Reachability = b.Reachability
	_ = settings.SetHostPublicAddress(h.db, id, b.PublicAddress) //nolint:errcheck
	updated.PublicAddress = strings.TrimSpace(b.PublicAddress)
	h.bridge.EvictHost(id)
	// Same auto-URL refresh as the admin path: redeploy bound envs whose URL embeds
	// this host's changed address, when the user opted in. host = OLD, updated = NEW.
	if b.AutoRefreshRoutes {
		h.refreshImpactedRoutes(h.hostRouteImpact(host, updated.WebAddress()))
	}
	writeJSON(w, http.StatusOK, updated)
}

// DELETE /api/workspaces/{ws}/hosts/{id} — delete a host owned by this workspace.
func (h *Handler) DeleteWorkspaceHost(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsHostID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	host, owned := h.ownsHost(ws, id)
	if host == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !owned {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this is a shared global host — manage it from Settings"})
		return
	}
	if err := settings.DeleteHost(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.bridge.EvictHost(id)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/workspaces/{ws}/hosts/{id}/test — connectivity test, gated to the pool.
func (h *Handler) TestWorkspaceHost(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsHostID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if inPool, _ := settings.HostInWorkspacePool(h.db, ws, id); !inPool {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	h.testHostByID(w, id)
}

// POST /api/workspaces/{ws}/hosts/{id}/build-only — mark/unmark a workspace-owned
// host as a dedicated builder (excluded from deploy pickers). A shared global host's
// build-only status is managed by an admin. Body: {"build_only": bool}.
func (h *Handler) SetWorkspaceHostBuildOnly(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsHostID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	host, err := settings.GetHost(h.db, id)
	if err != nil || host == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if host.WorkspaceScope() != ws {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this is a shared global host — its build-only status is managed from Settings"})
		return
	}
	var body struct {
		BuildOnly bool `json:"build_only"`
	}
	_ = readJSON(r, &body) //nolint:errcheck
	if body.BuildOnly && !host.BuildOnly {
		if msg, gerr := buildOnlyConflict(h.db, id); gerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": gerr.Error()})
			return
		} else if msg != "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": msg})
			return
		}
	}
	if err := settings.SetHostBuildOnly(h.db, id, body.BuildOnly); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "build_only": body.BuildOnly})
}

// wsHostInPool resolves + pool-gates a workspace host route, returning the id and
// whether it's usable by the workspace (writing the error response if not).
func (h *Handler) wsHostInPool(w http.ResponseWriter, r *http.Request) (int64, bool) {
	ws := r.PathValue("workspace")
	id, err := wsHostID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return 0, false
	}
	if inPool, _ := settings.HostInWorkspacePool(h.db, ws, id); !inPool {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return 0, false
	}
	return id, true
}

// GET /api/workspaces/{ws}/hosts/{id}/components — per-host component status, pooled.
func (h *Handler) WorkspaceHostComponents(w http.ResponseWriter, r *http.Request) {
	id, ok := h.wsHostInPool(w, r)
	if !ok {
		return
	}
	c, found := h.probeHostComponents(id)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "host not found"})
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// POST /api/workspaces/{ws}/hosts/{id}/install-nixpacks — install Nixpacks, pooled.
func (h *Handler) InstallWorkspaceHostNixpacks(w http.ResponseWriter, r *http.Request) {
	if id, ok := h.wsHostInPool(w, r); ok {
		h.installNixpacksByID(w, id)
	}
}

// POST /api/workspaces/{ws}/hosts/{id}/workspaces-dir — create the remote workspaces
// directory, pooled.
func (h *Handler) CreateWorkspaceHostWorkspacesDir(w http.ResponseWriter, r *http.Request) {
	if id, ok := h.wsHostInPool(w, r); ok {
		h.createWorkspacesDirByID(w, id)
	}
}

// POST /api/workspaces/{ws}/hosts/{id}/install-edge — (re)install the Traefik edge
// on a host in the workspace's pool so web-routed workloads deployed there are
// reachable. Gated to the pool; any pool member (own or granted global) may install.
func (h *Handler) InstallWorkspaceHostEdge(w http.ResponseWriter, r *http.Request) {
	if id, ok := h.wsHostInPool(w, r); ok {
		h.installEdgeByID(w, id)
	}
}

// GET /api/workspaces/{ws}/hosts/{id}/stats — host health, gated to the pool.
func (h *Handler) WorkspaceHostStats(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsHostID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if inPool, _ := settings.HostInWorkspacePool(h.db, ws, id); !inPool {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	h.hostStatsByID(w, id)
}
