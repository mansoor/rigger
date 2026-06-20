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
	jwt.RegisteredClaims
}

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
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE users SET password = ? WHERE id = ?", string(newHash), userID)
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
	claims := Claims{
		UserID:        id,
		Username:      username,
		Role:          role,
		Email:         email,
		EmailVerified: emailVerified,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(s.jwtExpiry) * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

// issueRefreshToken creates a long-lived JWT (7 days) stored in the httpOnly cookie.
func (s *Service) issueRefreshToken(id int64, username, role, email string, emailVerified bool) (string, error) {
	claims := Claims{
		UserID:        id,
		Username:      username,
		Role:          role,
		Email:         email,
		EmailVerified: emailVerified,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   "refresh",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

// IssueSession returns a fresh (access, refresh) pair for a user id — used after
// completing registration so the new user is logged straight in.
func (s *Service) IssueSession(userID int64) (accessToken, refreshToken string, err error) {
	u, err := s.getUserByID(userID)
	if err != nil {
		return "", "", err
	}
	s.touchLastLogin(userID)
	name := displayName(u.Username, u.Email)
	if accessToken, err = s.issueToken(userID, name, u.Role, u.Email, u.EmailVerified); err != nil {
		return "", "", err
	}
	refreshToken, err = s.issueRefreshToken(userID, name, u.Role, u.Email, u.EmailVerified)
	return
}

// RefreshAccessToken validates a refresh token cookie and issues a new short-lived access token.
// Accepts any JWT signed with our secret — the cookie's MaxAge governs session lifetime so we
// skip expiry validation here. No Subject check so old-build cookies still work after upgrades.
func (s *Service) RefreshAccessToken(refreshToken string) (accessToken, newRefresh string, err error) {
	var claims Claims

	// Parse the token; ignore expiry errors — we trust the httpOnly cookie's MaxAge instead.
	_, parseErr := jwt.ParseWithClaims(
		refreshToken, &claims,
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return s.jwtSecret, nil
		},
		jwt.WithoutClaimsValidation(), // skip exp/nbf/iat checks — cookie MaxAge is the guard
	)
	if parseErr != nil {
		// Only fail for signature errors; ignore validation errors (exp, etc.)
		if !errors.Is(parseErr, jwt.ErrTokenExpired) &&
			!errors.Is(parseErr, jwt.ErrTokenNotValidYet) {
			return "", "", fmt.Errorf("invalid refresh token")
		}
	}

	if claims.UserID == 0 {
		return "", "", fmt.Errorf("invalid token claims")
	}

	// Re-fetch user from DB to get current role/email and verify account still exists
	var username, role, email string
	var ev int
	if dbErr := s.db.QueryRow(
		"SELECT username, role, email, email_verified FROM users WHERE id = ?", claims.UserID,
	).Scan(&username, &role, &email, &ev); dbErr != nil {
		return "", "", fmt.Errorf("user not found")
	}
	name := displayName(username, email)
	accessToken, err = s.issueToken(claims.UserID, name, role, email, ev != 0)
	if err != nil {
		return "", "", err
	}
	// Rolling: issue a fresh 7-day refresh token so the session stays alive with activity
	newRefresh, err = s.issueRefreshToken(claims.UserID, name, role, email, ev != 0)
	return accessToken, newRefresh, err
}

// Login2 authenticates by email (or legacy username) and returns access + refresh tokens.
func (s *Service) Login2(identifier, password string) (accessToken, refreshToken string, err error) {
	row, err := s.authenticate(identifier, password)
	if err != nil {
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

// ValidateToken parses and validates a JWT, returning the claims.
func (s *Service) ValidateToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
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
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Store claims in context
		r = r.WithContext(WithClaims(r.Context(), claims))
		next.ServeHTTP(w, r)
	})
}
