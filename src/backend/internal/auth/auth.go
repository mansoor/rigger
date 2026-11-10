package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUserExists         = errors.New("username already exists")
)

// Roles split into two independent systems (Phase 5 RBAC, reworked):
//
//   Global role (users.role, JWT claim) — superadmin | user.
//     superadmin: full control everywhere, manages users + global settings.
//     user:       a plain account whose access comes entirely from workspace
//                 membership (workspace_members / project_acl).
//
//   Workspace-tier ladder (workspace_members.role, project_acl.role,
//   EffectiveRole) — ordered by privilege:
//     viewer    — read-only
//     developer — Env Card actions (deploy/restart/stop/start, env vars, containers)
//     operator  — developer + edit project config/compose
//     admin      — operator + manage members, workspace settings, resources,
//                  create/delete projects, rename/delete the workspace
const (
	// Global roles.
	RoleSuperadmin = "superadmin"
	RoleUser       = "user"

	// Workspace-tier roles.
	RoleViewer    = "viewer"
	RoleDeveloper = "developer"
	RoleOperator  = "operator"
	RoleAdmin     = "admin"
)

// roleRank ranks ONLY the workspace-tier ladder. Global roles are not ranked
// here — superadmin is handled explicitly in AtLeast and IsSuperadmin.
var roleRank = map[string]int{RoleViewer: 1, RoleDeveloper: 2, RoleOperator: 3, RoleAdmin: 4}

// ValidRole reports whether s is a known workspace-tier role.
func ValidRole(s string) bool { _, ok := roleRank[s]; return ok }

// ValidGlobalRole reports whether s is a known global role.
func ValidGlobalRole(s string) bool { return s == RoleSuperadmin || s == RoleUser }

// IsSuperadmin reports whether a global role is the super-admin.
func IsSuperadmin(globalRole string) bool { return globalRole == RoleSuperadmin }

// RankRole returns a role's privilege level (higher = more); 0 if unknown.
func RankRole(s string) int { return roleRank[s] }

// AtLeast reports whether have is at least as privileged as want on the
// workspace ladder. A super-admin satisfies any requirement.
func AtLeast(have, want string) bool {
	if have == RoleSuperadmin {
		return true
	}
	return roleRank[have] >= roleRank[want]
}

type User struct {
	ID       int64
	Username string
	Role     string
}

type Claims struct {
	UserID        int64  `json:"uid"`
	Username      string `json:"sub"`
	Role          string `json:"role"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"ev"`
	// MustChangePassword is set when the user's password has aged past the rotation
	// policy; the frontend forces a change before granting access. Recomputed on
	// every token issue/refresh so it clears as soon as the password is updated.
	MustChangePassword bool `json:"mcp,omitempty"`
	// Use is the token's kind, tokenAccess or tokenRefresh. Each kind is accepted
	// only for its own purpose. (Refresh tokens used to be marked by Subject, but
	// Username shares the "sub" key and shadowed it, so the mark never reached the
	// token and either kind passed for the other.)
	Use string `json:"token_use,omitempty"`
	jwt.RegisteredClaims
}

// Token kinds. An access token authenticates requests and lives minutes; a
// refresh token only buys a new session and lives days. Tokens issued before
// v0.1.48 carry no kind and are refused, which signs every older session out once.
const (
	tokenAccess  = "access"
	tokenRefresh = "refresh"
)

// refreshTTL is how long a refresh token lasts. Each refresh issues a new one, so
// a session in use rolls on; one left idle this long ends.
const refreshTTL = 7 * 24 * time.Hour

// tokenLeeway absorbs small clock differences when checking a token's times.
const tokenLeeway = 30 * time.Second

var errWrongTokenKind = errors.New("token is not valid for this use")

type Service struct {
	db        *db.DB
	jwtSecret []byte
	jwtExpiry int // minutes
}

func NewService(d *db.DB, secret string, expiryMinutes int) *Service {
	return &Service{db: d, jwtSecret: []byte(secret), jwtExpiry: expiryMinutes}
}

// CreateUser hashes password and inserts a new user.
func (s *Service) CreateUser(username, password, role string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		"INSERT INTO users (username, password, role) VALUES (?, ?, ?)",
		username, string(hash), role,
	)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrUserExists
	}
	return err
}

// VerifyUserPassword reports whether password matches the stored hash for userID.
// Used to re-confirm identity in-session before an irreversible action (wipe data,
// delete project) — NOT a login (it issues no token and doesn't touch last-login).
func (s *Service) VerifyUserPassword(userID int64, password string) bool {
	if password == "" {
		return false
	}
	var hash string
	if err := s.db.QueryRow("SELECT password FROM users WHERE id = ?", userID).Scan(&hash); err != nil || hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// Login verifies credentials and returns a signed JWT.
// ChangePassword verifies the current password then updates it.
func (s *Service) ChangePassword(userID int64, currentPassword, newPassword string) error {
	var hash string
	err := s.db.QueryRow("SELECT password FROM users WHERE id = ?", userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidCredentials
	}
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(currentPassword)); err != nil {
		return ErrInvalidCredentials
	}
	if err := s.ValidatePassword(newPassword); err != nil {
		return err
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE users SET password = ?, password_changed_at=CURRENT_TIMESTAMP WHERE id = ?", string(newHash), userID)
	return err
}

// loginRow is the per-user data needed to authenticate and build claims.
type loginRow struct {
	id            int64
	hash          string
	role          string
	email         string
	username      string
	emailVerified bool
	status        string
}

// displayName is the value used for the token's username claim (JWT "sub", shown
// in the UI as the display name): the user's username, falling back to email for
// legacy rows that never set one.
func displayName(username, email string) string {
	if strings.TrimSpace(username) != "" {
		return username
	}
	return email
}

// resolveLogin looks up an account by email first, then (legacy) by username for
// rows that have no email yet — so the pre-email admin isn't locked out.
func (s *Service) resolveLogin(identifier string) (*loginRow, error) {
	var r loginRow
	var ev int
	err := s.db.QueryRow(
		`SELECT id, password, role, email, username, email_verified, status FROM users WHERE email = ? LIMIT 1`, identifier,
	).Scan(&r.id, &r.hash, &r.role, &r.email, &r.username, &ev, &r.status)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRow(
			`SELECT id, password, role, email, username, email_verified, status FROM users WHERE username = ? AND email = '' LIMIT 1`, identifier,
		).Scan(&r.id, &r.hash, &r.role, &r.email, &r.username, &ev, &r.status)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	r.emailVerified = ev != 0
	return &r, nil
}

// authenticate verifies the identifier+password and that the account can log in.
func (s *Service) authenticate(identifier, password string) (*loginRow, error) {
	row, err := s.resolveLogin(identifier)
	if err != nil {
		return nil, err
	}
	// Invited accounts (no password set yet) cannot log in until they complete
	// registration via their invite link.
	if row.status != "active" || row.hash == "" {
		return nil, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(row.hash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}
	return row, nil
}

func (s *Service) Login(identifier, password string) (string, error) {
	row, err := s.authenticate(identifier, password)
	if err != nil {
		return "", err
	}
	s.touchLastLogin(row.id)
	return s.issueToken(row.id, displayName(row.username, row.email), row.role, row.email, row.emailVerified)
}

func (s *Service) issueToken(id int64, username, role, email string, emailVerified bool) (string, error) {
	return s.sign(Claims{
		UserID:             id,
		Username:           username,
		Role:               role,
		Email:              email,
		EmailVerified:      emailVerified,
		MustChangePassword: s.passwordExpired(id),
		Use:                tokenAccess,
	}, time.Duration(s.jwtExpiry)*time.Minute)
}

// issueRefreshToken creates the long-lived token kept in the httpOnly cookie.
func (s *Service) issueRefreshToken(id int64, username, role, email string, emailVerified bool) (string, error) {
	return s.sign(Claims{
		UserID:        id,
		Username:      username,
		Role:          role,
		Email:         email,
		EmailVerified: emailVerified,
		Use:           tokenRefresh,
	}, refreshTTL)
}

func (s *Service) sign(c Claims, ttl time.Duration) (string, error) {
	now := time.Now()
	c.IssuedAt = jwt.NewNumericDate(now)
	c.ExpiresAt = jwt.NewNumericDate(now.Add(ttl))
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.jwtSecret)
}

// parseToken checks a token's signature, times and kind. Whether its session
// still stands is a separate question — see checkSession.
func (s *Service) parseToken(raw, use string) (*Claims, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(raw, &c,
		func(*jwt.Token) (any, error) { return s.jwtSecret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(tokenLeeway),
	)
	if err != nil {
		return nil, err
	}
	if c.Use != use {
		return nil, errWrongTokenKind
	}
	if c.UserID == 0 || c.IssuedAt == nil {
		return nil, errors.New("token is missing claims")
	}
	return &c, nil
}

// IssueSession returns a fresh (access, refresh) pair for a user id — used after
// completing registration so the new user is logged straight in.
func (s *Service) IssueSession(userID int64) (accessToken, refreshToken string, err error) {
	s.touchLastLogin(userID)
	return s.RenewSession(userID)
}

// RenewSession issues a fresh (access, refresh) pair from the account's current
// details. It rolls a session on refresh, and keeps a browser signed in after it
// changes its own password — a change that ends every session issued before it.
func (s *Service) RenewSession(userID int64) (accessToken, refreshToken string, err error) {
	u, err := s.getUserByID(userID)
	if err != nil {
		return "", "", err
	}
	name := displayName(u.Username, u.Email)
	if accessToken, err = s.issueToken(userID, name, u.Role, u.Email, u.EmailVerified); err != nil {
		return "", "", err
	}
	refreshToken, err = s.issueRefreshToken(userID, name, u.Role, u.Email, u.EmailVerified)
	return
}

// RefreshAccessToken exchanges a refresh token for a new access token and a new
// refresh token, so a session in use rolls on. The token must be an unexpired
// refresh token whose session still stands; an access token, however fresh, is
// refused.
func (s *Service) RefreshAccessToken(refreshToken string) (accessToken, newRefresh string, err error) {
	c, err := s.parseToken(refreshToken, tokenRefresh)
	if err != nil {
		return "", "", fmt.Errorf("invalid refresh token: %w", err)
	}
	if _, err := s.checkSession(c); err != nil {
		return "", "", err
	}
	accessToken, newRefresh, err = s.RenewSession(c.UserID)
	if err != nil && !errors.Is(err, ErrUserNotFound) {
		// The session stands; failing to read the account is a lookup failure,
		// which mustn't cost the browser its cookie.
		return "", "", fmt.Errorf("%w: %v", ErrSessionLookup, err)
	}
	return accessToken, newRefresh, err
}

// Login2 authenticates by email (or legacy username) and returns access + refresh tokens.
// When the account has 2FA enabled, a valid TOTP code is also required: an empty code
// yields ErrTOTPRequired (the caller prompts for one), a wrong code ErrTOTPInvalid.
func (s *Service) Login2(identifier, password, code string) (accessToken, refreshToken string, err error) {
	row, err := s.authenticate(identifier, password)
	if err != nil {
		return "", "", err
	}
	if err := s.checkTOTPForLogin(row.id, code); err != nil {
		return "", "", err
	}
	s.touchLastLogin(row.id)
	name := displayName(row.username, row.email)
	accessToken, err = s.issueToken(row.id, name, row.role, row.email, row.emailVerified)
	if err != nil {
		return "", "", err
	}
	refreshToken, err = s.issueRefreshToken(row.id, name, row.role, row.email, row.emailVerified)
	return
}

// ValidateToken checks an access token and that its session still stands (see
// checkSession), and returns its claims carrying the account's current role — so
// a demotion applies to the next request, not at the token's next refresh.
func (s *Service) ValidateToken(tokenStr string) (*Claims, error) {
	c, err := s.parseToken(tokenStr, tokenAccess)
	if err != nil {
		return nil, err
	}
	role, err := s.checkSession(c)
	if err != nil {
		return nil, err
	}
	c.Role = role
	return c, nil
}

// RequireSuperadmin returns middleware (to wrap inside Middleware, which
// populates the claims) that allows the request only for a global super-admin.
// Returns 403 otherwise. Used to gate global-only surfaces (users, global
// settings, hosts pool, housekeeping).
func (s *Service) RequireSuperadmin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := ClaimsFromContext(r.Context())
			if claims == nil || !IsSuperadmin(claims.Role) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Middleware extracts and validates Bearer token from Authorization header.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		claims, err := s.ValidateToken(strings.TrimPrefix(header, "Bearer "))
		if errors.Is(err, ErrSessionLookup) {
			// Not the session's fault: a 401 would sign the browser out.
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Store claims in context
		r = r.WithContext(WithClaims(r.Context(), claims))
		next.ServeHTTP(w, r)
	})
}
