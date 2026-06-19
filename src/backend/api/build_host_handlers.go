package api

import (
	"net/http"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/settings"
)

// Per-project build host (image-distribution Phase 4). A project can pin its image
// builds to a specific host (default: build on the env's own deploy host). The pool
// of selectable hosts is the workspace's host pool (GET /api/workspaces/{ws}/hosts).

type buildHostResponse struct {
	HostID      int64 `json:"host_id"`       // explicit per-project binding (0 = inherit)
	WSDefaultID int64 `json:"ws_default_id"` // workspace default build host (0 = none)
}

// GET /api/workspaces/{workspace}/projects/{name}/build-host
func (h *Handler) GetProjectBuildHost(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	id, err := settings.ProjectBuildHostID(h.db, h.resourcePrefix(ws, name))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, buildHostResponse{
		HostID:      id,
		WSDefaultID: settings.WorkspaceDefaultBuildHostID(h.db, ws),
	})
}

// PUT /api/workspaces/{workspace}/projects/{name}/build-host   body: {"host_id": 3}
// host_id 0 clears the binding (revert to the workspace default / deploy host).
func (h *Handler) SetProjectBuildHost(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var body struct {
		HostID int64 `json:"host_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_id is required"})
		return
	}
	// A build host must be one this workspace can use (its own host or a granted global).
	if body.HostID != 0 {
		if inPool, _ := settings.HostInWorkspacePool(h.db, ws, body.HostID); !inPool {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "that host is not available to this workspace"})
			return
		}
	}
	if err := settings.SetProjectBuildHost(h.db, h.resourcePrefix(ws, name), body.HostID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
