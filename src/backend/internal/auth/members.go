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

func validMemberRole(r string) bool { return ValidRole(r) }            // viewer/operator/admin
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
	if globalRole == RoleAdmin {
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
