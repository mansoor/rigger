package api

import (
	"net/http"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
)

// Phase 5.2b enforcement. A single gate guards every /api/workspaces/{ws}/...
// request by the caller's effective role for that workspace (and project, when the
// path is project-scoped). Global admins bypass; non-members are denied.

// visibleWorkspaceSet returns the workspace keys a caller may see. all=true means
// no filtering (global admin). For non-admins it's their membership set.
func (h *Handler) visibleWorkspaceSet(r *http.Request) (set map[string]bool, all bool) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		return map[string]bool{}, false
	}
	if claims.Role == auth.RoleAdmin {
		return nil, true
	}
	set, _ = h.auth.MemberWorkspaceKeys(claims.UserID)
	if set == nil {
		set = map[string]bool{}
	}
	return set, false
}

func seg(path string, i int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

// wsMinRole maps a workspace-scoped request to the minimum role required.
//   viewer   — reads
//   operator — environment actions (deploy/restart/stop/backup/vars/host/...)
//   admin    — workspace settings, resources, members, project config + delete,
//              rename/delete the workspace
func wsMinRole(method, seg3, seg4, seg5 string) string {
	isGET := method == http.MethodGet
	switch seg3 {
	case "": // /api/workspaces/{ws}  — PUT rename / DELETE tier
		return auth.RoleAdmin
	case "members", "settings":
		return auth.RoleAdmin
	case "hosts", "registries", "backup-targets", "notification-channels", "alerts":
		if isGET {
			return auth.RoleViewer // members may view the pool (e.g. to pick on deploy)
		}
		return auth.RoleAdmin
	case "projects":
		if seg4 == "" || isGET {
			return auth.RoleViewer
		}
		if seg5 == "config" {
			return auth.RoleAdmin // editing project config
		}
		if seg5 == "" && method == http.MethodDelete {
			return auth.RoleAdmin // deleting a project
		}
		return auth.RoleOperator // env actions, vars, host, rotate, backup-sync, containers…
	default:
		return auth.RoleViewer
	}
}

// GateWorkspace authorizes a /api/workspaces/{ws}/... request. Returns false (and
// writes the response) when denied.
func (h *Handler) GateWorkspace(w http.ResponseWriter, r *http.Request) bool {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	if claims.Role == auth.RoleAdmin {
		return true // super-admin
	}
	ws := seg(r.URL.Path, 2)
	seg3, seg4, seg5 := seg(r.URL.Path, 3), seg(r.URL.Path, 4), seg(r.URL.Path, 5)

	// Transfer moves projects between tiers — super-admin only.
	if seg3 == "transfer" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a super-admin can transfer a workspace"})
		return false
	}

	projKey := ""
	if seg3 == "projects" {
		projKey = seg4
	}
	min := wsMinRole(r.Method, seg3, seg4, seg5)
	eff := h.auth.EffectiveRole(claims.UserID, claims.Role, ws, projKey)
	if eff == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you don't have access to this workspace"})
		return false
	}
	if !auth.AtLeast(eff, min) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this action requires the " + min + " role"})
		return false
	}
	return true
}
