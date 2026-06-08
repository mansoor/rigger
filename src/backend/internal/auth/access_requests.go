package auth

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Access requests (Phase 5 RBAC). A user asks for access to a workspace (and
// optionally a single project) at a workspace-tier role; a workspace admin or a
// super-admin approves (granting membership / a project override) or rejects.

var ErrRequestNotFound = errors.New("access request not found")

// AccessRequest is one request row joined with the requester's identity.
type AccessRequest struct {
	ID        int64      `json:"id"`
	UserID    int64      `json:"user_id"`
	Email     string     `json:"email"`
	Username  string     `json:"username"`
	WsKey     string     `json:"ws_key"`
	ProjKey   string     `json:"proj_key,omitempty"`
	Role      string     `json:"role"`
	Message   string     `json:"message,omitempty"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}

const accessReqCols = `r.id, r.user_id, u.email, u.username, r.ws_key, r.proj_key, r.role, r.message, r.status, r.created_at, r.decided_at`

func scanAccessRequest(s scanner) (*AccessRequest, error) {
	var a AccessRequest
	var decided sql.NullTime
	if err := s.Scan(&a.ID, &a.UserID, &a.Email, &a.Username, &a.WsKey, &a.ProjKey, &a.Role, &a.Message, &a.Status, &a.CreatedAt, &decided); err != nil {
		return nil, err
	}
	if decided.Valid {
		a.DecidedAt = &decided.Time
	}
	return &a, nil
}

// CreateAccessRequest records a pending request, replacing any existing pending
// request for the same (user, workspace, project) target.
func (s *Service) CreateAccessRequest(userID int64, wsKey, projKey, role, message string) (int64, error) {
	wsKey = strings.TrimSpace(wsKey)
	if wsKey == "" {
		return 0, errors.New("a workspace is required")
	}
	if !validMemberRole(role) {
		return 0, ErrInvalidMemberRole
	}
	// Drop a prior pending request for the same target so a re-request just refreshes.
	s.db.Exec(`DELETE FROM access_requests WHERE user_id=? AND ws_key=? AND proj_key=? AND status='pending'`, userID, wsKey, projKey) //nolint:errcheck
	res, err := s.db.Exec(
		`INSERT INTO access_requests (user_id, ws_key, proj_key, role, message, status) VALUES (?,?,?,?,?,'pending')`,
		userID, wsKey, projKey, role, strings.TrimSpace(message))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// ListMyRequests returns a user's own requests, newest first.
func (s *Service) ListMyRequests(userID int64) ([]AccessRequest, error) {
	rows, err := s.db.Query(`SELECT `+accessReqCols+` FROM access_requests r JOIN users u ON u.id=r.user_id
		WHERE r.user_id=? ORDER BY r.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectAccessRequests(rows)
}

// ListPendingRequests returns pending requests the approver may act on: all of
// them for a super-admin, or those for workspaces where the approver is a
// workspace admin.
func (s *Service) ListPendingRequests(approverID int64, globalRole string) ([]AccessRequest, error) {
	rows, err := s.db.Query(`SELECT ` + accessReqCols + ` FROM access_requests r JOIN users u ON u.id=r.user_id
		WHERE r.status='pending' ORDER BY r.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all, err := collectAccessRequests(rows)
	if err != nil {
		return nil, err
	}
	if IsSuperadmin(globalRole) {
		return all, nil
	}
	out := []AccessRequest{}
	for _, a := range all {
		if s.EffectiveRole(approverID, globalRole, a.WsKey, "") == RoleAdmin {
			out = append(out, a)
		}
	}
	return out, nil
}

// GetAccessRequest returns one request by id.
func (s *Service) GetAccessRequest(id int64) (*AccessRequest, error) {
	a, err := scanAccessRequest(s.db.QueryRow(`SELECT `+accessReqCols+` FROM access_requests r JOIN users u ON u.id=r.user_id WHERE r.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRequestNotFound
	}
	return a, err
}

// DecideAccessRequest marks a pending request approved/rejected by deciderID.
func (s *Service) DecideAccessRequest(id, deciderID int64, status string) error {
	res, err := s.db.Exec(`UPDATE access_requests SET status=?, decided_at=CURRENT_TIMESTAMP, decided_by=? WHERE id=? AND status='pending'`,
		status, deciderID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRequestNotFound
	}
	return nil
}

// PendingRequestCount returns how many pending requests the approver can act on
// (for a nav badge). Super-admins get the global count.
func (s *Service) PendingRequestCount(approverID int64, globalRole string) int {
	reqs, err := s.ListPendingRequests(approverID, globalRole)
	if err != nil {
		return 0
	}
	return len(reqs)
}

func collectAccessRequests(rows *sql.Rows) ([]AccessRequest, error) {
	out := []AccessRequest{}
	for rows.Next() {
		a, err := scanAccessRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}
