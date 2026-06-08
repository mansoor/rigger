package api

import (
	"net/http"
	"strconv"

	"github.com/mansoor/rigger/ui/internal/auth"
)

// Workspace membership management (Phase 5.2a). Managed by a workspace admin
// (effective role admin in the workspace) or a global super-admin.

// requireWorkspaceAdmin returns the caller's claims if they are an admin of the
// workspace (global admin or workspace-admin), else writes 403 and returns nil.
func (h *Handler) requireWorkspaceAdmin(w http.ResponseWriter, r *http.Request, wsKey string) *auth.Claims {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil || h.auth.EffectiveRole(claims.UserID, claims.Role, wsKey, "") != auth.RoleAdmin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "workspace admin access required"})
		return nil
	}
	return claims
}

func memberUserID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("memberid"), 10, 64)
}

// GET /api/workspaces/{ws}/members
func (h *Handler) ListWorkspaceMembers(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if h.requireWorkspaceAdmin(w, r, ws) == nil {
		return
	}
	members, err := h.auth.ListWorkspaceMembers(ws)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, members)
}

// PUT /api/workspaces/{ws}/members/{id}  {role}
func (h *Handler) SetWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if h.requireWorkspaceAdmin(w, r, ws) == nil {
		return
	}
	uid, err := memberUserID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := h.auth.SetWorkspaceMember(ws, uid, body.Role); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DELETE /api/workspaces/{ws}/members/{id}
func (h *Handler) RemoveWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if h.requireWorkspaceAdmin(w, r, ws) == nil {
		return
	}
	uid, err := memberUserID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.auth.RemoveWorkspaceMember(ws, uid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/workspaces/{ws}/members/{id}/projects/{proj}  {role}
func (h *Handler) SetProjectOverride(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if h.requireWorkspaceAdmin(w, r, ws) == nil {
		return
	}
	uid, err := memberUserID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	proj := r.PathValue("projkey")
	var body struct {
		Role string `json:"role"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := h.auth.SetProjectOverride(ws, proj, uid, body.Role); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DELETE /api/workspaces/{ws}/members/{id}/projects/{proj}
func (h *Handler) RemoveProjectOverride(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if h.requireWorkspaceAdmin(w, r, ws) == nil {
		return
	}
	uid, err := memberUserID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.auth.RemoveProjectOverride(ws, r.PathValue("projkey"), uid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
