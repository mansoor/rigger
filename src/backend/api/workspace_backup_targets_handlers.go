package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Workspace-scoped Backup Targets (Phase 3). A workspace sees its own targets
// (owner_scope='ws:{key}') plus any global target granted to it. It may
// create/edit/delete only its own; globals are read-only here.

func wsTargetID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("targetid"), 10, 64)
}

func (h *Handler) ownsTarget(wsKey string, id int64) (*settings.BackupTarget, bool) {
	t, err := settings.GetBackupTarget(h.db, id)
	if err != nil || t == nil {
		return nil, false
	}
	return t, t.WorkspaceScope() == wsKey
}

// GET /api/workspaces/{ws}/backup-targets — the workspace's target pool.
func (h *Handler) ListWorkspaceBackupTargets(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	targets, err := settings.ListBackupTargetsForWorkspace(h.db, ws)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, targets)
}

// POST /api/workspaces/{ws}/backup-targets — create a target private to this workspace.
func (h *Handler) CreateWorkspaceBackupTarget(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if _, err := os.Stat(wspath.WorkspaceMeta(h.workspacesDir, ws)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	var body backupTargetBody
	if err := readJSON(r, &body); err != nil || body.Name == "" || body.Type == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, type, and config are required"})
		return
	}
	if body.Config == nil {
		body.Config = json.RawMessage(`{}`)
	}
	t, err := settings.CreateBackupTarget(h.db, body.Name, body.Type, body.Config, settings.WorkspaceOwnerScope(ws))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a backup target with that name already exists"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// PUT /api/workspaces/{ws}/backup-targets/{id} — edit a target owned by this workspace.
func (h *Handler) UpdateWorkspaceBackupTarget(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsTargetID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	t, owned := h.ownsTarget(ws, id)
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !owned {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this is a shared global backup target — manage it from Settings"})
		return
	}
	var body backupTargetBody
	if err := readJSON(r, &body); err != nil || body.Name == "" || body.Type == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, type, and config are required"})
		return
	}
	if body.Config == nil {
		body.Config = json.RawMessage(`{}`)
	}
	updated, err := settings.UpdateBackupTarget(h.db, id, body.Name, body.Type, body.Config)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DELETE /api/workspaces/{ws}/backup-targets/{id} — delete a target owned by this workspace.
func (h *Handler) DeleteWorkspaceBackupTarget(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsTargetID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	t, owned := h.ownsTarget(ws, id)
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !owned {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this is a shared global backup target — manage it from Settings"})
		return
	}
	if err := settings.DeleteBackupTarget(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/workspaces/{ws}/backup-targets/{id}/test — connectivity test, gated to the pool.
func (h *Handler) TestWorkspaceBackupTarget(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsTargetID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if inPool, _ := settings.TargetInWorkspacePool(h.db, ws, id); !inPool {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	h.testBackupTargetByID(w, r, id)
}
