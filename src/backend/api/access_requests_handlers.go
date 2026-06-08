package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/workspace"
)

// Access-request endpoints (Phase 5 RBAC, roadmap 9). Any signed-in user can
// request access to a workspace (and optionally one project); a workspace admin
// or a super-admin approves (granting access) or rejects.

// GET /api/access-requests/targets — every workspace + its projects (keys/names
// only), so a user with no access can still pick what to request.
func (h *Handler) AccessRequestTargets(w http.ResponseWriter, r *http.Request) {
	wss, err := workspace.ListWorkspaces(h.workspacesDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	type proj struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	type target struct {
		WsKey    string `json:"ws_key"`
		WsName   string `json:"ws_name"`
		Projects []proj `json:"projects"`
	}
	out := []target{}
	for _, ws := range wss {
		t := target{WsKey: ws.Key, WsName: ws.Name, Projects: []proj{}}
		if projects, err := workspace.ListProjects(h.workspacesDir, ws.Key); err == nil {
			for _, p := range projects {
				name := p.Name
				if dn := p.Config.Project.Name; dn != "" {
					name = dn
				}
				t.Projects = append(t.Projects, proj{Key: p.Name, Name: name})
			}
		}
		out = append(out, t)
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/access-requests  {ws_key, proj_key?, role, message?}
func (h *Handler) CreateAccessRequest(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		WsKey   string `json:"ws_key"`
		ProjKey string `json:"proj_key"`
		Role    string `json:"role"`
		Message string `json:"message"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	id, err := h.auth.CreateAccessRequest(claims.UserID, body.WsKey, body.ProjKey, body.Role, body.Message)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "status": "pending"})
}

// GET /api/access-requests/mine — the caller's own requests.
func (h *Handler) ListMyAccessRequests(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	reqs, err := h.auth.ListMyRequests(claims.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, reqs)
}

// GET /api/access-requests — pending requests the caller can act on (super-admin:
// all; otherwise those for workspaces they administer).
func (h *Handler) ListPendingAccessRequests(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	reqs, err := h.auth.ListPendingRequests(claims.UserID, claims.Role)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, reqs)
}

// POST /api/access-requests/{id}/approve | /reject
func (h *Handler) DecideAccessRequest(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/access-requests/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	action := parts[1] // approve | reject

	req, err := h.auth.GetAccessRequest(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "access request not found"})
		return
	}
	// Authorize: super-admin, or a workspace admin of the request's workspace.
	if !auth.IsSuperadmin(claims.Role) && h.auth.EffectiveRole(claims.UserID, claims.Role, req.WsKey, "") != auth.RoleAdmin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you can't act on this request"})
		return
	}

	switch action {
	case "approve":
		var grantErr error
		if req.ProjKey != "" {
			grantErr = h.auth.SetProjectOverride(req.WsKey, req.ProjKey, req.UserID, req.Role)
		} else {
			grantErr = h.auth.SetWorkspaceMember(req.WsKey, req.UserID, req.Role)
		}
		if grantErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": grantErr.Error()})
			return
		}
		if err := h.auth.DecideAccessRequest(id, claims.UserID, "approved"); err != nil {
			writeJSON(w, accessReqErrStatus(err), map[string]string{"error": err.Error()})
			return
		}
	case "reject":
		if err := h.auth.DecideAccessRequest(id, claims.UserID, "rejected"); err != nil {
			writeJSON(w, accessReqErrStatus(err), map[string]string{"error": err.Error()})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": action})
}

func accessReqErrStatus(err error) int {
	if errors.Is(err, auth.ErrRequestNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}
