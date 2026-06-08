package api

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Workspace-scoped Docker Registries (Phase 3). A workspace sees its own
// registries (owner_scope='ws:{key}') plus any global registry granted to it. It
// may create/edit/delete only its own; globals are read-only here.

func wsRegistryID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("regid"), 10, 64)
}

// ownsRegistry loads a registry and reports whether it is private to the workspace.
func (h *Handler) ownsRegistry(wsKey string, id int64) (*settings.DockerRegistry, bool) {
	reg, err := settings.GetRegistry(h.db, id)
	if err != nil || reg == nil {
		return nil, false
	}
	return reg, reg.WorkspaceScope() == wsKey
}

// GET /api/workspaces/{ws}/registries — the workspace's registry pool.
func (h *Handler) ListWorkspaceRegistries(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	regs, err := settings.ListRegistriesForWorkspace(h.db, ws)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, regs)
}

// POST /api/workspaces/{ws}/registries — create a registry private to this workspace.
func (h *Handler) CreateWorkspaceRegistry(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if _, err := os.Stat(wspath.WorkspaceMeta(h.workspacesDir, ws)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	var body registryBody
	if err := readJSON(r, &body); err != nil || body.Name == "" || body.URL == "" || body.Username == "" || body.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, url, username, and password are required"})
		return
	}
	reg, err := settings.CreateRegistry(h.db, body.Name, body.URL, body.Username, body.Password, settings.WorkspaceOwnerScope(ws))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a registry with that name already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = dockerLogin(body.URL, body.Username, body.Password)
	writeJSON(w, http.StatusCreated, reg)
}

// PUT /api/workspaces/{ws}/registries/{id} — edit a registry owned by this workspace.
func (h *Handler) UpdateWorkspaceRegistry(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsRegistryID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	reg, owned := h.ownsRegistry(ws, id)
	if reg == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !owned {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this is a shared global registry — manage it from Settings"})
		return
	}
	var body registryBody
	if err := readJSON(r, &body); err != nil || body.Name == "" || body.URL == "" || body.Username == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, url, and username are required"})
		return
	}
	updated, err := settings.UpdateRegistry(h.db, id, body.Name, body.URL, body.Username, body.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if body.Password != "" {
		_ = dockerLogin(body.URL, body.Username, body.Password)
	}
	writeJSON(w, http.StatusOK, updated)
}

// DELETE /api/workspaces/{ws}/registries/{id} — delete a registry owned by this workspace.
func (h *Handler) DeleteWorkspaceRegistry(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsRegistryID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	reg, owned := h.ownsRegistry(ws, id)
	if reg == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !owned {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this is a shared global registry — manage it from Settings"})
		return
	}
	if err := settings.DeleteRegistry(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/workspaces/{ws}/registries/{id}/test — docker login test, gated to the pool.
func (h *Handler) TestWorkspaceRegistry(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsRegistryID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	// Pool membership: own registry or a granted global.
	pool, _ := settings.ListRegistriesForWorkspace(h.db, ws)
	inPool := false
	for _, p := range pool {
		if p.ID == id {
			inPool = true
			break
		}
	}
	if !inPool {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	reg, err := settings.GetRegistry(h.db, id)
	if err != nil || reg == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "registry not found"})
		return
	}
	if loginErr := dockerLogin(reg.URL, reg.Username, reg.Password); loginErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "docker login failed: " + loginErr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Login succeeded"})
}
