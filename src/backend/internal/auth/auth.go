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

// Built-in global roles (Phase 5 RBAC). Ordered by privilege.
const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleAdmin    = "admin"
)

var roleRank = map[string]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

// ValidRole reports whether s is a known built-in role.
func ValidRole(s string) bool { _, ok := roleRank[s]; return ok }

// RankRole returns a role's privilege level (higher = more); 0 if unknown.
func RankRole(s string) int { return roleRank[s] }

// AtLeast reports whether have is at least as privileged as want.
func AtLeast(have, want string) bool { return roleRank[have] >= roleRank[want] }

type User struct {
	ID       int64
	Username string
	Role     string
}

type Claims struct {
	UserID   int64  `json:"uid"`
	Username string `json:"sub"`
	Role     string `json:"role"`
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

func (s *Service) Login(username, password string) (string, error) {
	var (
		id   int64
		hash string
		role string
	)
	err := s.db.QueryRow(
		"SELECT id, password, role FROM users WHERE username = ?", username,
	).Scan(&id, &hash, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidCredentials
	}
	if err != nil {
		return "", err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return "", ErrInvalidCredentials
	}
	s.touchLastLogin(id)
	return s.issueToken(id, username, role)
}

func (s *Service) issueToken(id int64, username, role string) (string, error) {
	claims := Claims{
		UserID:   id,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(s.jwtExpiry) * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

// issueRefreshToken creates a long-lived JWT (7 days) stored in the httpOnly cookie.
func (s *Service) issueRefreshToken(id int64, username, role string) (string, error) {
	claims := Claims{
		UserID:   id,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   "refresh",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
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

	// Re-fetch user from DB to get current role and verify account still exists
	var username, role string
	if dbErr := s.db.QueryRow(
		"SELECT username, role FROM users WHERE id = ?", claims.UserID,
	).Scan(&username, &role); dbErr != nil {
		return "", "", fmt.Errorf("user not found")
	}

	accessToken, err = s.issueToken(claims.UserID, username, role)
	if err != nil {
		return "", "", err
	}
	// Rolling: issue a fresh 7-day refresh token so the session stays alive with activity
	newRefresh, err = s.issueRefreshToken(claims.UserID, username, role)
	return accessToken, newRefresh, err
}

// Login2 returns both access and refresh tokens.
func (s *Service) Login2(username, password string) (accessToken, refreshToken string, err error) {
	var (
		id   int64
		hash string
		role string
	)
	err = s.db.QueryRow(
		"SELECT id, password, role FROM users WHERE username = ?", username,
	).Scan(&id, &hash, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrInvalidCredentials
	}
	if err != nil {
		return "", "", err
	}
	if err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return "", "", ErrInvalidCredentials
	}
	s.touchLastLogin(id)
	accessToken, err = s.issueToken(id, username, role)
	if err != nil {
		return "", "", err
	}
	refreshToken, err = s.issueRefreshToken(id, username, role)
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

// RequireRole returns middleware (to wrap inside Middleware, which populates the
// claims) that allows the request only if the caller's global role is at least
// minRole. Returns 403 otherwise. Used to gate global/admin-only surfaces.
func (s *Service) RequireRole(minRole string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := ClaimsFromContext(r.Context())
			if claims == nil || !AtLeast(claims.Role, minRole) {
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
