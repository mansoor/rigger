package api

import (
	"net/http"
	"os"

	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Per-workspace general settings (Phase 3). Scalar key/value scoped to one
// workspace, mirroring the global General tab. Currently stored values (acme_email,
// domain) — like the global ACME email, they are reference/for future automation
// until the Traefik wiring consumes them per workspace.
var workspaceSettingKeys = map[string]bool{
	"acme_email": true,
	"domain":     true,
}

// GET /api/workspaces/{ws}/settings
func (h *Handler) GetWorkspaceSettings(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	vals, err := settings.GetWorkspaceSettings(h.db, ws)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Always report every known key so the UI binds cleanly.
	out := map[string]string{}
	for k := range workspaceSettingKeys {
		out[k] = vals[k]
	}
	writeJSON(w, http.StatusOK, out)
}

// PUT /api/workspaces/{ws}/settings  Body: { acme_email, domain }
func (h *Handler) PutWorkspaceSettings(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if _, err := os.Stat(wspath.WorkspaceMeta(h.workspacesDir, ws)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	var body map[string]string
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	for k, v := range body {
		if !workspaceSettingKeys[k] {
			continue
		}
		if err := settings.SetWorkspaceSetting(h.db, ws, k, v); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	h.GetWorkspaceSettings(w, r)
}
