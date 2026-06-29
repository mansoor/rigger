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
	if auth.IsSuperadmin(claims.Role) {
		return nil, true
	}
	set, _ = h.auth.VisibleWorkspaceKeys(claims.UserID)
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
//   viewer    — reads
//   developer — environment/Env-Card actions (deploy/restart/stop/start, env
//               vars, rotate, backup-sync, per-container actions)
//   operator  — developer + edit project config/compose
//   admin     — workspace settings, resources, members, create/delete projects,
//               rename/delete the workspace
func wsMinRole(method, seg3, seg4, seg5 string) string {
	isGET := method == http.MethodGet
	switch seg3 {
	case "": // /api/workspaces/{ws}  — PUT rename / DELETE tier
		return auth.RoleAdmin
	case "members", "settings":
		return auth.RoleAdmin
	case "hosts", "registries", "backup-targets", "notification-channels", "alerts", "access-lists":
		if isGET {
			return auth.RoleViewer // members may view the pool (e.g. to pick on deploy)
		}
		return auth.RoleAdmin
	case "api-keys": // listing + minting keys is sensitive — workspace admin only
		return auth.RoleAdmin
	case "api-key-scopes": // static scope catalog for the create form
		return auth.RoleViewer
	case "projects":
		if seg4 == "" || isGET {
			return auth.RoleViewer
		}
		if seg4 == "create" {
			return auth.RoleAdmin // creating a project (also gated in the create socket)
		}
		if seg5 == "config" {
			return auth.RoleOperator // editing project config/compose
		}
		if seg5 == "" && method == http.MethodDelete {
			return auth.RoleAdmin // deleting a project
		}
		return auth.RoleDeveloper // env actions, vars, rotate, backup-sync, containers…
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
	if auth.IsSuperadmin(claims.Role) {
		return true // super-admin
	}
	ws := seg(r.URL.Path, 2)
	seg3, seg4, seg5 := seg(r.URL.Path, 3), seg(r.URL.Path, 4), seg(r.URL.Path, 5)

	// Transfer moves projects to another workspace — requires admin of the source
	// workspace. The handler additionally verifies admin of the target.
	if seg3 == "transfer" {
		if h.auth.EffectiveRole(claims.UserID, claims.Role, ws, "") != auth.RoleAdmin {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "you must be an admin of this workspace to transfer its projects"})
			return false
		}
		return true
	}

	projKey := ""
	if seg3 == "projects" {
		projKey = seg4
	}
	min := wsMinRole(r.Method, seg3, seg4, seg5)
	eff := h.auth.EffectiveRole(claims.UserID, claims.Role, ws, projKey)
	// A project-only member (a per-project grant but no workspace membership) can
	// still read the workspace shell — listing projects, reading the workspace —
	// so they can reach the project they were granted. Per-project gates above
	// still apply to the project itself.
	if eff == "" && min == auth.RoleViewer && projKey == "" && h.auth.WorkspaceVisible(claims.UserID, claims.Role, ws) {
		eff = auth.RoleViewer
	}
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
