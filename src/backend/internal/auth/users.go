package auth

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// User management (Phase 5 RBAC, roadmap 10a). Admin-only at the API layer.

var (
	ErrInvalidRole  = errors.New("invalid role")
	ErrLastAdmin    = errors.New("cannot remove the last admin")
	ErrUserNotFound = errors.New("user not found")
)

// UserInfo is a user record for the admin Users list (never includes the hash).
type UserInfo struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	Role        string     `json:"role"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

// ListUsers returns all users ordered by username.
func (s *Service) ListUsers() ([]UserInfo, error) {
	rows, err := s.db.Query(`SELECT id, username, role, created_at, last_login_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserInfo{}
	for rows.Next() {
		var u UserInfo
		var last sql.NullTime
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			u.LastLoginAt = &last.Time
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AdminCreateUser creates a user with an explicit role (admin action).
func (s *Service) AdminCreateUser(username, password, role string) (*UserInfo, error) {
	if !ValidRole(role) {
		return nil, ErrInvalidRole
	}
	if strings.TrimSpace(username) == "" || password == "" {
		return nil, errors.New("username and password are required")
	}
	if err := s.CreateUser(username, password, role); err != nil {
		return nil, err
	}
	return s.getUser(username)
}

// UpdateUser changes a user's role and, if newPassword is non-empty, resets their
// password. Demoting the last admin is rejected.
func (s *Service) UpdateUser(id int64, role, newPassword string) (*UserInfo, error) {
	if !ValidRole(role) {
		return nil, ErrInvalidRole
	}
	cur, err := s.getUserByID(id)
	if err != nil {
		return nil, err
	}
	if cur.Role == RoleAdmin && role != RoleAdmin {
		if n, _ := s.countAdmins(); n <= 1 {
			return nil, ErrLastAdmin
		}
	}
	if _, err := s.db.Exec(`UPDATE users SET role=? WHERE id=?`, role, id); err != nil {
		return nil, err
	}
	if newPassword != "" {
		hash, herr := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if herr != nil {
			return nil, herr
		}
		if _, err := s.db.Exec(`UPDATE users SET password=? WHERE id=?`, string(hash), id); err != nil {
			return nil, err
		}
	}
	return s.getUserByID(id)
}

// DeleteUser removes a user, refusing to delete the last admin.
func (s *Service) DeleteUser(id int64) error {
	cur, err := s.getUserByID(id)
	if err != nil {
		return err
	}
	if cur.Role == RoleAdmin {
		if n, _ := s.countAdmins(); n <= 1 {
			return ErrLastAdmin
		}
	}
	_, err = s.db.Exec(`DELETE FROM users WHERE id=?`, id)
	return err
}

func (s *Service) countAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM users WHERE role=?`, RoleAdmin).Scan(&n)
	return n, err
}

func (s *Service) touchLastLogin(id int64) {
	_, _ = s.db.Exec(`UPDATE users SET last_login_at=CURRENT_TIMESTAMP WHERE id=?`, id) //nolint:errcheck
}

func (s *Service) getUser(username string) (*UserInfo, error) {
	var u UserInfo
	var last sql.NullTime
	err := s.db.QueryRow(`SELECT id, username, role, created_at, last_login_at FROM users WHERE username=?`, username).
		Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	if last.Valid {
		u.LastLoginAt = &last.Time
	}
	return &u, nil
}

func (s *Service) getUserByID(id int64) (*UserInfo, error) {
	var u UserInfo
	var last sql.NullTime
	err := s.db.QueryRow(`SELECT id, username, role, created_at, last_login_at FROM users WHERE id=?`, id).
		Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	if last.Valid {
		u.LastLoginAt = &last.Time
	}
	return &u, nil
}
