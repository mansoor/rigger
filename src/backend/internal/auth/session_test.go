package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/mansoor/rigger/ui/internal/db"
)

// These tests drive sessions through the calls the server makes: ValidateToken
// for every request and socket, RefreshAccessToken for /api/auth/refresh, and
// SessionActive for streams that stay open.

func login(t *testing.T, s *Service, email, password string) (access, refresh string) {
	t.Helper()
	a, r, err := s.Login2(email, password, "")
	if err != nil {
		t.Fatalf("login %s: %v", email, err)
	}
	return a, r
}

// addActiveUser invites an account and completes its registration, as a person
// accepting an invitation does.
func addActiveUser(t *testing.T, s *Service, email, role string) int64 {
	t.Helper()
	u, raw, err := s.InviteUser(email, role, "")
	if err != nil {
		t.Fatalf("invite %s: %v", email, err)
	}
	if _, err := s.CompleteRegistration(raw, "Member-Passw0rd!", "", ""); err != nil {
		t.Fatalf("register %s: %v", email, err)
	}
	return u.ID
}

// passwordSetAnHourAgo backdates an account's last password change, so a test
// can hold a session older than a change without sleeping across a second.
func passwordSetAnHourAgo(t *testing.T, d *db.DB, id int64) {
	t.Helper()
	if _, err := d.Exec(`UPDATE users SET password_changed_at=datetime('now', '-1 hour') WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
}

// tokenIssuedAt signs a token of the given kind as if issued at iat.
func tokenIssuedAt(t *testing.T, s *Service, id int64, use string, iat time.Time, ttl time.Duration) string {
	t.Helper()
	c := Claims{UserID: id, Username: "someone", Role: RoleUser, Use: use,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(iat),
			ExpiresAt: jwt.NewNumericDate(iat.Add(ttl)),
		}}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestEachTokenKindHasOneUse(t *testing.T) {
	s, _ := newTestService(t)
	setupAdmin(t, s)
	access, refresh := login(t, s, "admin@example.com", "Original-Passw0rd!")

	if _, err := s.ValidateToken(access); err != nil {
		t.Fatalf("access token refused: %v", err)
	}
	if _, err := s.ValidateToken(refresh); err == nil {
		t.Error("a refresh token authenticated a request")
	}
	if _, _, err := s.RefreshAccessToken(access); err == nil {
		t.Error("an access token was exchanged for a new session")
	}

	newAccess, newRefresh, err := s.RefreshAccessToken(refresh)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := s.ValidateToken(newAccess); err != nil {
		t.Errorf("the refreshed access token was refused: %v", err)
	}
	if _, _, err := s.RefreshAccessToken(newRefresh); err != nil {
		t.Errorf("the rolled refresh token was refused: %v", err)
	}
}

func TestExpiredRefreshTokenIsRefused(t *testing.T) {
	s, d := newTestService(t)
	id := setupAdmin(t, s)
	passwordSetAnHourAgo(t, d, id)
	// Issued 30 minutes ago with a 20-minute life.
	old := tokenIssuedAt(t, s, id, tokenRefresh, time.Now().Add(-30*time.Minute), 20*time.Minute)
	if _, _, err := s.RefreshAccessToken(old); err == nil {
		t.Error("an expired refresh token started a new session")
	}
	// The same token, still in date, works — expiry is what refused it.
	fresh := tokenIssuedAt(t, s, id, tokenRefresh, time.Now().Add(-30*time.Minute), time.Hour)
	if _, _, err := s.RefreshAccessToken(fresh); err != nil {
		t.Errorf("an in-date refresh token was refused: %v", err)
	}
}

// Tokens from before token kinds existed — such as those sitting in reverse-proxy
// access logs — must not work for anything, expired or not.
func TestTokensWithoutAKindAreRefused(t *testing.T) {
	s, _ := newTestService(t)
	id := setupAdmin(t, s)
	legacy := func(subject string, exp time.Time) string {
		c := Claims{UserID: id, Username: "admin", Role: RoleSuperadmin,
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   subject, // shadowed by Username, as it always was
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				ExpiresAt: jwt.NewNumericDate(exp),
			}}
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.jwtSecret)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	other := NewService(nil, "a-different-secret", 15)
	forged, err := other.sign(Claims{UserID: id, Role: RoleSuperadmin, Use: tokenAccess}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{
		"old access token":         legacy("", time.Now().Add(15*time.Minute)),
		"old expired access token": legacy("", time.Now().Add(-time.Hour)),
		"old refresh token":        legacy("refresh", time.Now().Add(7*24*time.Hour)),
		"token from another key":   forged,
	} {
		if _, err := s.ValidateToken(tok); err == nil {
			t.Errorf("%s authenticated a request", name)
		}
		if _, _, err := s.RefreshAccessToken(tok); err == nil {
			t.Errorf("%s started a new session", name)
		}
	}
}

// Every way a password changes ends the sessions issued before it, and the
// session handed back right after the change — in the same second — stands.
func TestPasswordChangeEndsEarlierSessions(t *testing.T) {
	const newPassword = "Changed-Passw0rd-1!"
	for _, tc := range []struct {
		name   string
		change func(s *Service, id int64) error
	}{
		{"in the app", func(s *Service, id int64) error {
			return s.ChangePassword(id, "Original-Passw0rd!", newPassword)
		}},
		{"from the console", func(s *Service, id int64) error {
			_, err := s.SetPasswordFromConsole(id, newPassword)
			return err
		}},
		{"by an administrator", func(s *Service, id int64) error {
			_, err := s.UpdateUser(id, RoleSuperadmin, newPassword)
			return err
		}},
		{"through a reset link", func(s *Service, id int64) error {
			raw, err := s.CreateResetLinkToken(id, time.Hour)
			if err != nil {
				return err
			}
			return s.ResetPassword(raw, newPassword)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, d := newTestService(t)
			id := setupAdmin(t, s)
			passwordSetAnHourAgo(t, d, id)
			issued := time.Now().Add(-10 * time.Second)
			access := tokenIssuedAt(t, s, id, tokenAccess, issued, 15*time.Minute)
			refresh := tokenIssuedAt(t, s, id, tokenRefresh, issued, refreshTTL)
			claims, err := s.ValidateToken(access)
			if err != nil {
				t.Fatalf("setup: the session should stand before the change: %v", err)
			}

			if err := tc.change(s, id); err != nil {
				t.Fatalf("change: %v", err)
			}

			if _, err := s.ValidateToken(access); !errors.Is(err, ErrSessionEnded) {
				t.Errorf("access token after the change: err = %v, want ErrSessionEnded", err)
			}
			if _, _, err := s.RefreshAccessToken(refresh); !errors.Is(err, ErrSessionEnded) {
				t.Errorf("refresh after the change: err = %v, want ErrSessionEnded", err)
			}
			if s.SessionActive(claims) {
				t.Error("an open stream's session still counts as active")
			}

			a, _, err := s.RenewSession(id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ValidateToken(a); err != nil {
				t.Errorf("the session issued after the change was refused: %v", err)
			}
		})
	}
}

func TestDisableEndsSessionsForGood(t *testing.T) {
	s, d := newTestService(t)
	setupAdmin(t, s)
	id := addActiveUser(t, s, "dev@example.com", RoleUser)
	passwordSetAnHourAgo(t, d, id)
	issued := time.Now().Add(-10 * time.Second)
	access := tokenIssuedAt(t, s, id, tokenAccess, issued, 15*time.Minute)
	refresh := tokenIssuedAt(t, s, id, tokenRefresh, issued, refreshTTL)
	claims, err := s.ValidateToken(access)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	if changed, err := s.DisableUser(id); err != nil || !changed {
		t.Fatalf("disable: changed=%v err=%v", changed, err)
	}
	if _, err := s.ValidateToken(access); !errors.Is(err, ErrSessionEnded) {
		t.Errorf("access token of a disabled account: err = %v, want ErrSessionEnded", err)
	}
	if _, _, err := s.RefreshAccessToken(refresh); err == nil {
		t.Error("a disabled account refreshed its session")
	}
	if s.SessionActive(claims) {
		t.Error("a disabled account's open stream still counts as active")
	}
	if _, _, err := s.Login2("dev@example.com", "Member-Passw0rd!", ""); err == nil {
		t.Error("a disabled account signed in")
	}
	if changed, err := s.DisableUser(id); err != nil || changed {
		t.Errorf("disabling again: changed=%v err=%v, want no change", changed, err)
	}

	if changed, err := s.EnableUser(id); err != nil || !changed {
		t.Fatalf("enable: changed=%v err=%v", changed, err)
	}
	if _, err := s.ValidateToken(access); err == nil {
		t.Error("enabling the account revived a session from before it was disabled")
	}
	if _, _, err := s.RefreshAccessToken(refresh); err == nil {
		t.Error("enabling the account revived a refresh token from before it was disabled")
	}
	newAccess, _ := login(t, s, "dev@example.com", "Member-Passw0rd!")
	if _, err := s.ValidateToken(newAccess); err != nil {
		t.Errorf("a new session after enabling was refused: %v", err)
	}
	if changed, err := s.EnableUser(id); err != nil || changed {
		t.Errorf("enabling again: changed=%v err=%v, want no change", changed, err)
	}
}

func TestDisableKeepsOneSuperadmin(t *testing.T) {
	s, _ := newTestService(t)
	admin := setupAdmin(t, s)
	if _, err := s.DisableUser(admin); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disabling the only super-admin: err = %v, want ErrLastAdmin", err)
	}
	second := addActiveUser(t, s, "second@example.com", RoleSuperadmin)
	if _, err := s.DisableUser(admin); err != nil {
		t.Fatalf("disabling one of two super-admins: %v", err)
	}
	// A disabled super-admin doesn't count, so the second is now the last.
	if _, err := s.DisableUser(second); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("disabling the last active super-admin: err = %v, want ErrLastAdmin", err)
	}
}

func TestDisableAndEnableRefuseInvitedAccounts(t *testing.T) {
	s, _ := newTestService(t)
	setupAdmin(t, s)
	u, _, err := s.InviteUser("new@example.com", RoleUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DisableUser(u.ID); !errors.Is(err, ErrAccountInvited) {
		t.Errorf("disable an invited account: err = %v, want ErrAccountInvited", err)
	}
	if _, err := s.EnableUser(u.ID); !errors.Is(err, ErrAccountInvited) {
		t.Errorf("enable an invited account: err = %v, want ErrAccountInvited", err)
	}
}

// Nothing but `enable` lets a disabled account back in: not a reset link issued
// before it was disabled, not a new one, not a console password reset.
func TestDisabledAccountStaysLockedOut(t *testing.T) {
	s, _ := newTestService(t)
	setupAdmin(t, s)
	id := addActiveUser(t, s, "dev@example.com", RoleUser)
	raw, err := s.CreateResetLinkToken(id, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DisableUser(id); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(raw, "Another-Passw0rd-1!"); err == nil {
		t.Error("a reset link from before the account was disabled still worked")
	}
	if _, err := s.CreateResetLinkToken(id, time.Hour); !errors.Is(err, ErrAccountDisabled) {
		t.Errorf("reset link for a disabled account: err = %v, want ErrAccountDisabled", err)
	}
	activated, err := s.SetPasswordFromConsole(id, "Console-Passw0rd-1!")
	if err != nil || activated {
		t.Fatalf("console reset: activated=%v err=%v", activated, err)
	}
	if _, _, err := s.Login2("dev@example.com", "Console-Passw0rd-1!", ""); err == nil {
		t.Error("a console password reset let a disabled account sign in")
	}
}

func TestRoleChangeAppliesToTheNextRequest(t *testing.T) {
	s, _ := newTestService(t)
	setupAdmin(t, s)
	id := addActiveUser(t, s, "second@example.com", RoleSuperadmin)
	access, _ := login(t, s, "second@example.com", "Member-Passw0rd!")
	if _, err := s.UpdateUser(id, RoleUser, ""); err != nil {
		t.Fatal(err)
	}
	c, err := s.ValidateToken(access)
	if err != nil {
		t.Fatal(err)
	}
	if c.Role != RoleUser {
		t.Errorf("role after demotion = %q, want %q", c.Role, RoleUser)
	}
}

func TestDeletedAccountSessionEnds(t *testing.T) {
	s, _ := newTestService(t)
	setupAdmin(t, s)
	id := addActiveUser(t, s, "dev@example.com", RoleUser)
	access, refresh := login(t, s, "dev@example.com", "Member-Passw0rd!")
	if err := s.DeleteUser(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateToken(access); !errors.Is(err, ErrSessionEnded) {
		t.Errorf("access token of a deleted account: err = %v, want ErrSessionEnded", err)
	}
	if _, _, err := s.RefreshAccessToken(refresh); err == nil {
		t.Error("a deleted account refreshed its session")
	}
}

// A failed lookup is not a verdict on the session: requests get 503 rather than
// the 401 that signs the browser out, and open streams keep running.
func TestSessionLookupFailureIsNotASignOut(t *testing.T) {
	s, d := newTestService(t)
	setupAdmin(t, s)
	access, refresh := login(t, s, "admin@example.com", "Original-Passw0rd!")
	claims, err := s.ValidateToken(access)
	if err != nil {
		t.Fatal(err)
	}
	next := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	status := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/anything", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := status(access); got != http.StatusNoContent {
		t.Fatalf("valid access token: status %d", got)
	}
	if got := status(refresh); got != http.StatusUnauthorized {
		t.Errorf("refresh token as a bearer: status %d, want 401", got)
	}

	d.Close()
	if _, err := s.ValidateToken(access); !errors.Is(err, ErrSessionLookup) {
		t.Errorf("err = %v, want ErrSessionLookup", err)
	}
	if got := status(access); got != http.StatusServiceUnavailable {
		t.Errorf("database down: status %d, want 503", got)
	}
	if !s.SessionActive(claims) {
		t.Error("a database failure ended an open stream")
	}
}
