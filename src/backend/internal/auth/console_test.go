package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
)

// These tests prove recovery through the paths a person actually uses: Login2
// for signing in, ResetPassword for what the browser's /reset-password page
// calls. A column check would pass even if a hash were written in a form the
// login code could not read.

func newTestService(t *testing.T) (*Service, *db.DB) {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return NewService(d, "test-secret-not-real", 15), d
}

func setupAdmin(t *testing.T, s *Service) int64 {
	t.Helper()
	id, err := s.SetupAdmin("admin@example.com", "Admin", "Original-Passw0rd!")
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}
	return id
}

func setPolicy(t *testing.T, d *db.DB, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if _, err := d.Exec(`INSERT OR REPLACE INTO app_settings (key, value) VALUES (?, ?)`, k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
}

func TestFindUserByEmailOrID(t *testing.T) {
	s, _ := newTestService(t)
	id := setupAdmin(t, s)

	for _, ref := range []string{"admin@example.com", "  ADMIN@Example.com ", "1"} {
		u, err := s.FindUser(ref)
		if err != nil || u.ID != id {
			t.Errorf("FindUser(%q) = %v, %v; want id %d", ref, u, err, id)
		}
	}
	for _, ref := range []string{"", "nobody@example.com", "999"} {
		if _, err := s.FindUser(ref); !errors.Is(err, ErrUserNotFound) {
			t.Errorf("FindUser(%q) error = %v, want ErrUserNotFound", ref, err)
		}
	}
}

// The point of the feature: a locked-out administrator gets back in.
func TestConsolePasswordSignsIn(t *testing.T) {
	s, _ := newTestService(t)
	id := setupAdmin(t, s)

	if _, err := s.SetPasswordFromConsole(id, "Brand-New-Passw0rd!"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if _, _, err := s.Login2("admin@example.com", "Brand-New-Passw0rd!", ""); err != nil {
		t.Errorf("signing in with the new password failed: %v", err)
	}
	if _, _, err := s.Login2("admin@example.com", "Original-Passw0rd!", ""); err == nil {
		t.Error("the old password still works")
	}
}

// A console reset is not a way around the password policy.
func TestConsolePasswordHonoursThePolicy(t *testing.T) {
	s, d := newTestService(t)
	id := setupAdmin(t, s)
	setPolicy(t, d, map[string]string{"pw_min_length": "16", "pw_require_symbol": "1"})

	if _, err := s.SetPasswordFromConsole(id, "short"); err == nil {
		t.Error("a password below the minimum length was accepted")
	}
	if _, err := s.SetPasswordFromConsole(id, "LongEnoughButNoSymbol1"); err == nil {
		t.Error("a password missing a required symbol was accepted")
	}
	// The rejected attempts must leave the old password working.
	if _, _, err := s.Login2("admin@example.com", "Original-Passw0rd!", ""); err != nil {
		t.Errorf("a rejected reset changed the password: %v", err)
	}
}

// Setting the first password for an invited account activates it — and kills
// its invitation link, which would otherwise let whoever holds that email set a
// different password over this one.
func TestConsolePasswordActivatesInvitedAccount(t *testing.T) {
	s, _ := newTestService(t)
	setupAdmin(t, s)
	u, invite, err := s.InviteUser("new@example.com", RoleUser, "New")
	if err != nil {
		t.Fatalf("invite: %v", err)
	}

	activated, err := s.SetPasswordFromConsole(u.ID, "Invited-Passw0rd!")
	if err != nil {
		t.Fatalf("set password: %v", err)
	}
	if !activated {
		t.Error("an invited account should report that it was activated")
	}
	if _, _, err := s.Login2("new@example.com", "Invited-Passw0rd!", ""); err != nil {
		t.Errorf("the activated account cannot sign in: %v", err)
	}
	if _, err := s.peekToken(invite, KindInvite); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("the invitation link still works after console activation (err=%v)", err)
	}

	again, _ := s.SetPasswordFromConsole(u.ID, "Another-Passw0rd!")
	if again {
		t.Error("an already-active account must not report activation")
	}
}

func TestGeneratedPasswordSatisfiesTheStrictestPolicy(t *testing.T) {
	s, d := newTestService(t)
	id := setupAdmin(t, s)
	setPolicy(t, d, map[string]string{
		"pw_min_length": "32", "pw_require_upper": "1", "pw_require_lower": "1",
		"pw_require_number": "1", "pw_require_symbol": "1",
	})

	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		pw, err := s.GeneratePassword()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(pw) < 32 {
			t.Fatalf("generated %d characters under a 32-character minimum", len(pw))
		}
		if strings.ContainsAny(pw, " '\"\\`") {
			t.Fatalf("generated password %q contains a character that is awkward to paste", pw)
		}
		if seen[pw] {
			t.Fatal("generated the same password twice")
		}
		seen[pw] = true
	}
	// And one of them actually works for signing in.
	pw, _ := s.GeneratePassword()
	if _, err := s.SetPasswordFromConsole(id, pw); err != nil {
		t.Fatalf("set generated password: %v", err)
	}
	if _, _, err := s.Login2("admin@example.com", pw, ""); err != nil {
		t.Errorf("signing in with a generated password failed: %v", err)
	}
}

func TestGeneratedPasswordDefaultsToTwentyCharacters(t *testing.T) {
	s, _ := newTestService(t)
	pw, err := s.GeneratePassword()
	if err != nil || len(pw) != 20 {
		t.Errorf("GeneratePassword() = %d characters, %v; want 20", len(pw), err)
	}
}

// Lost authenticator: after the console turns 2FA off, the password alone signs
// in, and the old recovery codes are gone.
func TestResetTOTP(t *testing.T) {
	s, d := newTestService(t)
	id := setupAdmin(t, s)
	d.Exec(`UPDATE users SET totp_secret='JBSWY3DPEHPK3PXP', totp_enabled=1 WHERE id=?`, id) //nolint:errcheck
	d.Exec(`INSERT INTO user_recovery_codes (user_id, code_hash) VALUES (?, 'x'), (?, 'y')`, id, id) //nolint:errcheck

	if _, _, err := s.Login2("admin@example.com", "Original-Passw0rd!", ""); err == nil {
		t.Fatal("setup: 2FA should be demanding a code")
	}
	was, err := s.ResetTOTP(id)
	if err != nil || !was {
		t.Fatalf("ResetTOTP = %v, %v; want true, nil", was, err)
	}
	if _, _, err := s.Login2("admin@example.com", "Original-Passw0rd!", ""); err != nil {
		t.Errorf("signing in without a code failed after the reset: %v", err)
	}
	var codes int
	d.QueryRow(`SELECT COUNT(*) FROM user_recovery_codes WHERE user_id=?`, id).Scan(&codes) //nolint:errcheck
	if codes != 0 {
		t.Errorf("%d recovery codes survived", codes)
	}
	if was, _ := s.ResetTOTP(id); was {
		t.Error("a second reset should report 2FA as already off")
	}
}

// The browser half: the console's link token is consumed by the same
// ResetPassword the /reset-password page posts to, exactly once.
func TestResetLinkWorksInTheBrowserFlowOnce(t *testing.T) {
	s, _ := newTestService(t)
	id := setupAdmin(t, s)

	raw, err := s.CreateResetLinkToken(id, 24*time.Hour)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if err := s.ResetPassword(raw, "Chosen-In-Browser1!"); err != nil {
		t.Fatalf("the reset page's call failed: %v", err)
	}
	if _, _, err := s.Login2("admin@example.com", "Chosen-In-Browser1!", ""); err != nil {
		t.Errorf("signing in with the password chosen in the browser failed: %v", err)
	}
	if err := s.ResetPassword(raw, "Second-Attempt1!"); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("the link worked twice (err=%v)", err)
	}
}

func TestResetLinkExpires(t *testing.T) {
	s, d := newTestService(t)
	id := setupAdmin(t, s)

	raw, err := s.CreateResetLinkToken(id, MinResetLinkTTL)
	if err != nil {
		t.Fatal(err)
	}
	d.Exec(`UPDATE user_tokens SET expires_at=? WHERE token_hash=?`, time.Now().Add(-time.Minute).UTC(), hashToken(raw)) //nolint:errcheck
	if err := s.ResetPassword(raw, "Too-Late-Passw0rd!"); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("an expired link still worked (err=%v)", err)
	}
}

func TestNewResetLinkCancelsThePrevious(t *testing.T) {
	s, _ := newTestService(t)
	id := setupAdmin(t, s)
	first, _ := s.CreateResetLinkToken(id, time.Hour)
	second, _ := s.CreateResetLinkToken(id, time.Hour)

	if err := s.ResetPassword(first, "From-First-Link1!"); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("the superseded link still worked (err=%v)", err)
	}
	if err := s.ResetPassword(second, "From-Second-Link1!"); err != nil {
		t.Errorf("the newest link failed: %v", err)
	}
}

func TestResetLinkRules(t *testing.T) {
	s, _ := newTestService(t)
	id := setupAdmin(t, s)
	u, _, _ := s.InviteUser("pending@example.com", RoleUser, "Pending")

	// The reset page does not activate accounts, so a link for an invited account
	// would set a password nobody could sign in with.
	if _, err := s.CreateResetLinkToken(u.ID, time.Hour); !errors.Is(err, ErrAccountInvited) {
		t.Errorf("invited account: err=%v, want ErrAccountInvited", err)
	}
	for _, ttl := range []time.Duration{time.Minute, 73 * time.Hour} {
		if _, err := s.CreateResetLinkToken(id, ttl); err == nil {
			t.Errorf("a %s link was accepted; bounds are %s to %s", ttl, MinResetLinkTTL, MaxResetLinkTTL)
		}
	}
	if _, err := s.CreateResetLinkToken(999, time.Hour); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("unknown account: err=%v", err)
	}
}
