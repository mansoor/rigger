package auth

import (
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// User management (Phase 5 RBAC + 5.1b identity). Email is the login identity and
// is required for active accounts; phone is optional. New users are invite-based.

const (
	StatusActive  = "active"
	StatusInvited = "invited"

	inviteTTL = 7 * 24 * time.Hour
	verifyTTL = 24 * time.Hour
	resetTTL  = 1 * time.Hour
)

var (
	ErrInvalidRole  = errors.New("invalid role")
	ErrLastAdmin    = errors.New("cannot remove the last super-admin")
	ErrUserNotFound = errors.New("user not found")
	ErrInvalidEmail = errors.New("a valid email address is required")
	ErrEmailTaken   = errors.New("a user with that email already exists")
)

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func validEmail(s string) bool { return emailRe.MatchString(strings.TrimSpace(s)) }

// UserInfo is a user record for the admin Users list (never includes the hash).
type UserInfo struct {
	ID            int64      `json:"id"`
	Username      string     `json:"username"`
	Email         string     `json:"email"`
	Phone         string     `json:"phone"`
	Role          string     `json:"role"`
	EmailVerified bool       `json:"email_verified"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	LastLoginAt   *time.Time `json:"last_login_at"`
}

const userCols = `id, username, email, phone, role, email_verified, status, created_at, last_login_at`

func scanUser(s scanner) (*UserInfo, error) {
	var u UserInfo
	var ev int
	var last sql.NullTime
	if err := s.Scan(&u.ID, &u.Username, &u.Email, &u.Phone, &u.Role, &ev, &u.Status, &u.CreatedAt, &last); err != nil {
		return nil, err
	}
	u.EmailVerified = ev != 0
	if last.Valid {
		u.LastLoginAt = &last.Time
	}
	return &u, nil
}

type scanner interface{ Scan(dest ...any) error }

// ListUsers returns all users ordered by email/username.
func (s *Service) ListUsers() ([]UserInfo, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY email, username`)
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

func (s *Service) getUserByID(id int64) (*UserInfo, error) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return u, err
}

// GetUser returns a single user by id (admin/profile views).
func (s *Service) GetUser(id int64) (*UserInfo, error) { return s.getUserByID(id) }

// SetupAdmin creates the first super-admin during first-run setup. The account is
// active immediately (pragmatic gate); email starts unverified.
func (s *Service) SetupAdmin(email, username, password string) (int64, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if !validEmail(email) {
		return 0, ErrInvalidEmail
	}
	if err := s.ValidatePassword(password); err != nil {
		return 0, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	if username == "" {
		username = strings.SplitN(email, "@", 2)[0]
	}
	res, err := s.db.Exec(
		`INSERT INTO users (username, email, password, role, status, email_verified) VALUES (?, ?, ?, ?, 'active', 0)`,
		username, email, string(hash), RoleSuperadmin,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrEmailTaken
		}
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// InviteUser creates an invited (password-less) account and returns the raw invite
// token for the registration link. role is the GLOBAL role (superadmin|user);
// empty defaults to a plain user whose access comes from workspace membership.
func (s *Service) InviteUser(email, role, username string) (*UserInfo, string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if !validEmail(email) {
		return nil, "", ErrInvalidEmail
	}
	if role == "" {
		role = RoleUser
	}
	if !ValidGlobalRole(role) {
		return nil, "", ErrInvalidRole
	}
	if username == "" {
		username = strings.SplitN(email, "@", 2)[0]
	}
	res, err := s.db.Exec(
		`INSERT INTO users (username, email, password, role, status, email_verified) VALUES (?, ?, '', ?, 'invited', 0)`,
		username, email, role,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, "", ErrEmailTaken
		}
		return nil, "", err
	}
	id, _ := res.LastInsertId()
	raw, err := s.createToken(id, KindInvite, inviteTTL)
	if err != nil {
		return nil, "", err
	}
	u, err := s.getUserByID(id)
	return u, raw, err
}

// ResendInvite issues a fresh invite token for an account still pending registration.
func (s *Service) ResendInvite(id int64) (string, error) {
	u, err := s.getUserByID(id)
	if err != nil {
		return "", err
	}
	if u.Status != StatusInvited {
		return "", errors.New("user has already completed registration")
	}
	return s.createToken(id, KindInvite, inviteTTL)
}

// RegisterInfo validates an invite token and returns the invitee's email (for the
// registration page) without consuming the token.
func (s *Service) RegisterInfo(rawToken string) (*UserInfo, error) {
	id, err := s.peekToken(rawToken, KindInvite)
	if err != nil {
		return nil, err
	}
	return s.getUserByID(id)
}

// CompleteRegistration consumes an invite token, sets the password (and optional
// phone/username), and activates the account with a verified email (the user
// proved ownership by following the emailed link).
func (s *Service) CompleteRegistration(rawToken, password, phone, username string) (int64, error) {
	if err := s.ValidatePassword(password); err != nil {
		return 0, err
	}
	id, err := s.consumeToken(rawToken, KindInvite)
	if err != nil {
		return 0, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(username) != "" {
		if _, err := s.db.Exec(`UPDATE users SET username=? WHERE id=?`, strings.TrimSpace(username), id); err != nil {
			return 0, err
		}
	}
	_, err = s.db.Exec(
		`UPDATE users SET password=?, phone=?, status='active', email_verified=1 WHERE id=?`,
		string(hash), strings.TrimSpace(phone), id,
	)
	return id, err
}

// CreateVerifyToken issues an email-verification token for a user (setup, resend,
// or after an email change).
func (s *Service) CreateVerifyToken(id int64) (string, error) {
	return s.createToken(id, KindVerify, verifyTTL)
}

// VerifyEmail consumes a verification token and marks the email verified.
func (s *Service) VerifyEmail(rawToken string) (int64, error) {
	id, err := s.consumeToken(rawToken, KindVerify)
	if err != nil {
		return 0, err
	}
	_, err = s.db.Exec(`UPDATE users SET email_verified=1 WHERE id=?`, id)
	return id, err
}

// IsVerified reports whether a user's email is verified.
func (s *Service) IsVerified(id int64) bool {
	u, err := s.getUserByID(id)
	return err == nil && u.EmailVerified
}

// CreateResetToken mints a password-reset token for the ACTIVE account with the
// given email. ok is false (with no error) when no eligible account exists — the
// caller MUST respond identically either way so the endpoint never reveals which
// emails are registered. Invited (not-yet-registered) accounts reset via their
// invite link, not here.
func (s *Service) CreateResetToken(email string) (rawToken, username string, ok bool, err error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if !validEmail(email) {
		return "", "", false, nil
	}
	var id int64
	var uname, status string
	e := s.db.QueryRow(`SELECT id, username, status FROM users WHERE email=? LIMIT 1`, email).Scan(&id, &uname, &status)
	if errors.Is(e, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if e != nil {
		return "", "", false, e
	}
	if status != StatusActive {
		return "", "", false, nil
	}
	raw, e := s.createToken(id, KindReset, resetTTL)
	if e != nil {
		return "", "", false, e
	}
	return raw, uname, true, nil
}

// ResetPassword validates the new password against the policy, consumes a
// password-reset token, and sets the new password. The email is marked verified
// since following the emailed link proves ownership.
func (s *Service) ResetPassword(rawToken, newPassword string) error {
	if err := s.ValidatePassword(newPassword); err != nil {
		return err
	}
	id, err := s.peekToken(rawToken, KindReset)
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE users SET password=?, email_verified=1 WHERE id=?`, string(hash), id); err != nil {
		return err
	}
	_, err = s.consumeToken(rawToken, KindReset)
	return err
}

// UpdateProfile lets a user set their own phone/username and change email. A changed
// email resets verification; emailChanged signals the caller to send a new link.
func (s *Service) UpdateProfile(id int64, email, phone, username string) (u *UserInfo, emailChanged bool, err error) {
	cur, err := s.getUserByID(id)
	if err != nil {
		return nil, false, err
	}
	email = strings.TrimSpace(strings.ToLower(email))
	if email != "" && email != cur.Email {
		if !validEmail(email) {
			return nil, false, ErrInvalidEmail
		}
		if _, e := s.db.Exec(`UPDATE users SET email=?, email_verified=0 WHERE id=?`, email, id); e != nil {
			if strings.Contains(e.Error(), "UNIQUE") {
				return nil, false, ErrEmailTaken
			}
			return nil, false, e
		}
		emailChanged = true
	}
	if _, e := s.db.Exec(`UPDATE users SET phone=?, username=? WHERE id=?`,
		strings.TrimSpace(phone), strings.TrimSpace(username), id); e != nil {
		return nil, false, e
	}
	u, err = s.getUserByID(id)
	return u, emailChanged, err
}

// UpdateUser changes a user's role and, if newPassword is non-empty, resets their
// password (admin action). Demoting the last admin is rejected.
func (s *Service) UpdateUser(id int64, role, newPassword string) (*UserInfo, error) {
	if !ValidGlobalRole(role) {
		return nil, ErrInvalidRole
	}
	cur, err := s.getUserByID(id)
	if err != nil {
		return nil, err
	}
	if cur.Role == RoleSuperadmin && role != RoleSuperadmin {
		if n, _ := s.countSuperadmins(); n <= 1 {
			return nil, ErrLastAdmin
		}
	}
	if _, err := s.db.Exec(`UPDATE users SET role=? WHERE id=?`, role, id); err != nil {
		return nil, err
	}
	if newPassword != "" {
		if verr := s.ValidatePassword(newPassword); verr != nil {
			return nil, verr
		}
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
	if cur.Role == RoleSuperadmin {
		if n, _ := s.countSuperadmins(); n <= 1 {
			return ErrLastAdmin
		}
	}
	// FK cascade isn't enforced (foreign_keys off), so sweep membership rows.
	s.db.Exec(`DELETE FROM project_acl WHERE user_id=?`, id)        //nolint:errcheck
	s.db.Exec(`DELETE FROM workspace_members WHERE user_id=?`, id)  //nolint:errcheck
	s.db.Exec(`DELETE FROM user_tokens WHERE user_id=?`, id)        //nolint:errcheck
	_, err = s.db.Exec(`DELETE FROM users WHERE id=?`, id)
	return err
}

func (s *Service) countSuperadmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM users WHERE role=? AND status='active'`, RoleSuperadmin).Scan(&n)
	return n, err
}

// GetAppearance returns a user's own appearance prefs JSON ("" = not set / inherit).
func (s *Service) GetAppearance(id int64) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT appearance_prefs FROM users WHERE id=?`, id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUserNotFound
	}
	return v, err
}

// SetAppearance stores a user's own appearance prefs ("" clears it, so they fall
// back to the workspace/global default).
func (s *Service) SetAppearance(id int64, prefs string) error {
	_, err := s.db.Exec(`UPDATE users SET appearance_prefs=? WHERE id=?`, prefs, id)
	return err
}

func (s *Service) touchLastLogin(id int64) {
	_, _ = s.db.Exec(`UPDATE users SET last_login_at=CURRENT_TIMESTAMP WHERE id=?`, id) //nolint:errcheck
}
