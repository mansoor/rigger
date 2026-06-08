package auth

import (
	"database/sql"
	"errors"
)

// Workspace membership + per-project overrides (Phase 5.2). Membership-gated:
// a non-admin can access a workspace only if listed in workspace_members.
// Effective role = global admin (super) ?? project override ?? workspace role.
// A project override of RoleNone revokes access to that one project.

const RoleNone = "none" // project-level override meaning "no access to this project"

var ErrInvalidMemberRole = errors.New("invalid role")

func validMemberRole(r string) bool { return ValidRole(r) }            // viewer/developer/operator/admin
func validOverrideRole(r string) bool { return ValidRole(r) || r == RoleNone }

// ProjectOverride is one per-project role override for a member.
type ProjectOverride struct {
	ProjKey string `json:"proj_key"`
	Role    string `json:"role"`
}

// Member is a workspace member with their workspace role and any project overrides.
type Member struct {
	UserID    int64             `json:"user_id"`
	Email     string            `json:"email"`
	Username  string            `json:"username"`
	Role      string            `json:"role"` // workspace-tier role
	Overrides []ProjectOverride `json:"overrides"`
}

// ListWorkspaceMembers returns the members of a workspace with their project overrides.
func (s *Service) ListWorkspaceMembers(wsKey string) ([]Member, error) {
	rows, err := s.db.Query(
		`SELECT m.user_id, u.email, u.username, m.role
		   FROM workspace_members m JOIN users u ON u.id = m.user_id
		  WHERE m.ws_key = ? ORDER BY u.email, u.username`, wsKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	byID := map[int64]int{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Email, &m.Username, &m.Role); err != nil {
			return nil, err
		}
		m.Overrides = []ProjectOverride{}
		byID[m.UserID] = len(out)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	orows, err := s.db.Query(`SELECT user_id, proj_key, role FROM project_acl WHERE ws_key = ?`, wsKey)
	if err != nil {
		return nil, err
	}
	defer orows.Close()
	for orows.Next() {
		var uid int64
		var po ProjectOverride
		if err := orows.Scan(&uid, &po.ProjKey, &po.Role); err != nil {
			return nil, err
		}
		if i, ok := byID[uid]; ok {
			out[i].Overrides = append(out[i].Overrides, po)
		}
	}
	if out == nil {
		out = []Member{}
	}
	return out, orows.Err()
}

// MemberCandidates lists active users who can be added to a workspace: not global
// super-admins (they already have access) and not already members. Lets a
// workspace admin pick members without needing the global user list.
func (s *Service) MemberCandidates(wsKey string) ([]UserInfo, error) {
	rows, err := s.db.Query(`SELECT `+userCols+` FROM users
		WHERE status='active' AND role <> ?
		  AND id NOT IN (SELECT user_id FROM workspace_members WHERE ws_key=?)
		ORDER BY email, username`, RoleSuperadmin, wsKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserInfo{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// SetWorkspaceMember adds or updates a member's workspace-tier role.
func (s *Service) SetWorkspaceMember(wsKey string, userID int64, role string) error {
	if !validMemberRole(role) {
		return ErrInvalidMemberRole
	}
	_, err := s.db.Exec(
		`INSERT INTO workspace_members (user_id, ws_key, role) VALUES (?, ?, ?)
		 ON CONFLICT(user_id, ws_key) DO UPDATE SET role=excluded.role`,
		userID, wsKey, role)
	return err
}

// RemoveWorkspaceMember removes a member and all their project overrides in the workspace.
func (s *Service) RemoveWorkspaceMember(wsKey string, userID int64) error {
	if _, err := s.db.Exec(`DELETE FROM project_acl WHERE ws_key=? AND user_id=?`, wsKey, userID); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM workspace_members WHERE ws_key=? AND user_id=?`, wsKey, userID)
	return err
}

// SetProjectOverride sets a per-project role override for a member (RoleNone revokes).
func (s *Service) SetProjectOverride(wsKey, projKey string, userID int64, role string) error {
	if !validOverrideRole(role) {
		return ErrInvalidMemberRole
	}
	_, err := s.db.Exec(
		`INSERT INTO project_acl (user_id, ws_key, proj_key, role) VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id, ws_key, proj_key) DO UPDATE SET role=excluded.role`,
		userID, wsKey, projKey, role)
	return err
}

// RemoveProjectOverride clears a project override (member falls back to workspace role).
func (s *Service) RemoveProjectOverride(wsKey, projKey string, userID int64) error {
	_, err := s.db.Exec(`DELETE FROM project_acl WHERE user_id=? AND ws_key=? AND proj_key=?`, userID, wsKey, projKey)
	return err
}

// EffectiveRole resolves a user's role for a workspace (and optional project).
// Returns "" when the user has no access. Global admins are super-admins everywhere.
func (s *Service) EffectiveRole(userID int64, globalRole, wsKey, projKey string) string {
	if IsSuperadmin(globalRole) {
		return RoleAdmin
	}
	if projKey != "" {
		var r string
		err := s.db.QueryRow(`SELECT role FROM project_acl WHERE user_id=? AND ws_key=? AND proj_key=?`, userID, wsKey, projKey).Scan(&r)
		if err == nil {
			if r == RoleNone {
				return ""
			}
			return r
		}
	}
	var r string
	err := s.db.QueryRow(`SELECT role FROM workspace_members WHERE user_id=? AND ws_key=?`, userID, wsKey).Scan(&r)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return ""
	}
	return r
}

// WorkspaceVisible reports whether a user can see a workspace at all — either a
// super-admin, a workspace member, or someone with at least one per-project grant
// (override role other than 'none'). This lets a project-only grant surface the
// parent workspace (with just that project visible) without making the user a
// member of the whole workspace.
func (s *Service) WorkspaceVisible(userID int64, globalRole, wsKey string) bool {
	if IsSuperadmin(globalRole) {
		return true
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(1) FROM workspace_members WHERE user_id=? AND ws_key=?`, userID, wsKey).Scan(&n) //nolint:errcheck
	if n > 0 {
		return true
	}
	s.db.QueryRow(`SELECT COUNT(1) FROM project_acl WHERE user_id=? AND ws_key=? AND role<>?`, userID, wsKey, RoleNone).Scan(&n) //nolint:errcheck
	return n > 0
}

// VisibleWorkspaceKeys returns the workspace keys a user can see — membership
// keys plus any keys where they hold a per-project grant.
func (s *Service) VisibleWorkspaceKeys(userID int64) (map[string]bool, error) {
	out, err := s.MemberWorkspaceKeys(userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT DISTINCT ws_key FROM project_acl WHERE user_id=? AND role<>?`, userID, RoleNone)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err == nil {
			out[k] = true
		}
	}
	return out, rows.Err()
}

// MemberWorkspaceKeys returns the workspace keys a user is a member of (for the
// membership-gated workspace list). Global admins are handled by the caller.
func (s *Service) MemberWorkspaceKeys(userID int64) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT ws_key FROM workspace_members WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// DeleteWorkspaceACL removes all membership + overrides for a workspace (on ws delete).
func (s *Service) DeleteWorkspaceACL(wsKey string) {
	s.db.Exec(`DELETE FROM project_acl WHERE ws_key=?`, wsKey)        //nolint:errcheck
	s.db.Exec(`DELETE FROM workspace_members WHERE ws_key=?`, wsKey)  //nolint:errcheck
}
