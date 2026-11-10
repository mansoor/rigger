package main

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/db"
)

const adminEmail = "admin@example.com"

// newDataDir creates a migrated database with one administrator, as a real
// install has, and closes it — runUsers opens it itself, as it would beside a
// running server.
func newDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.NewService(d, "x", 15).SetupAdmin(adminEmail, "Admin", "Original-Passw0rd!"); err != nil {
		t.Fatal(err)
	}
	d.Close()
	return dir
}

type result struct {
	code        int
	out, errOut string
}

func runCLI(t *testing.T, dir, stdin string, secret func(string) (string, error), args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runUsers(args, usersIO{
		dataDir: dir, stdin: strings.NewReader(stdin), stdout: &out, stderr: &errOut, readSecret: secret,
	})
	return result{code, out.String(), errOut.String()}
}

// canSignIn proves a password works through the real login path.
func canSignIn(t *testing.T, dir, password string) bool {
	t.Helper()
	d, err := db.OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, _, err = auth.NewService(d, "x", 15).Login2(adminEmail, password, "")
	return err == nil
}

var tempPassword = regexp.MustCompile(`Temporary password: (\S+)`)

func TestResetPasswordRandom(t *testing.T) {
	dir := newDataDir(t)
	r := runCLI(t, dir, "", nil, "reset-password", adminEmail)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	m := tempPassword.FindStringSubmatch(r.out)
	if m == nil {
		t.Fatalf("no temporary password in output:\n%s", r.out)
	}
	if !canSignIn(t, dir, m[1]) {
		t.Error("the printed temporary password does not sign in")
	}
	if canSignIn(t, dir, "Original-Passw0rd!") {
		t.Error("the old password still works")
	}
}

func TestResetPasswordFromStdin(t *testing.T) {
	dir := newDataDir(t)
	r := runCLI(t, dir, "Chosen-Passw0rd!\r\n", nil, "reset-password", adminEmail, "--password-stdin")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if strings.Contains(r.out, "Chosen-Passw0rd!") {
		t.Error("a password the operator supplied was echoed back")
	}
	if !canSignIn(t, dir, "Chosen-Passw0rd!") {
		t.Error("the password from stdin does not sign in (line ending kept?)")
	}
}

func TestResetPasswordPrompt(t *testing.T) {
	dir := newDataDir(t)
	same := func(string) (string, error) { return "Typed-Passw0rd!", nil }
	if r := runCLI(t, dir, "", same, "reset-password", adminEmail, "--prompt"); r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if !canSignIn(t, dir, "Typed-Passw0rd!") {
		t.Error("the prompted password does not sign in")
	}

	// Two different entries must not set either.
	n := 0
	differ := func(string) (string, error) { n++; return "Typed-Passw0rd-" + string(rune('A'+n)), nil }
	if r := runCLI(t, dir, "", differ, "reset-password", adminEmail, "--prompt"); r.code == 0 {
		t.Error("mismatched entries were accepted")
	}
	// Without a terminal the prompt is refused rather than echoing the password.
	if r := runCLI(t, dir, "", nil, "reset-password", adminEmail, "--prompt"); r.code == 0 ||
		!strings.Contains(r.errOut, "terminal") {
		t.Errorf("--prompt without a terminal: exit %d, %q", r.code, r.errOut)
	}
}

func TestResetPasswordHonoursPolicy(t *testing.T) {
	dir := newDataDir(t)
	r := runCLI(t, dir, "short\n", nil, "reset-password", adminEmail, "--password-stdin")
	if r.code != 1 {
		t.Errorf("a weak password: exit %d, want 1", r.code)
	}
	if !canSignIn(t, dir, "Original-Passw0rd!") {
		t.Error("a rejected reset changed the password")
	}
}

var resetURL = regexp.MustCompile(`(\S*)/reset-password\?token=(\S+)`)

// The browser half: the printed link's token completes a reset through the same
// call the /reset-password page makes.
func TestResetLinkCompletesInTheBrowserFlow(t *testing.T) {
	dir := newDataDir(t)
	r := runCLI(t, dir, "", nil, "reset-link", adminEmail, "--expires", "24h", "--base-url", "https://rigger.example.com/")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	m := resetURL.FindStringSubmatch(r.out)
	if m == nil || m[1] != "https://rigger.example.com" {
		t.Fatalf("expected a full link on the given base URL:\n%s", r.out)
	}
	if !strings.Contains(r.out, "24 hours") {
		t.Errorf("the output should state the lifetime:\n%s", r.out)
	}

	d, err := db.OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.NewService(d, "x", 15).ResetPassword(m[2], "Chosen-In-Browser1!"); err != nil {
		t.Fatalf("the reset page's call failed with the printed token: %v", err)
	}
	d.Close()
	if !canSignIn(t, dir, "Chosen-In-Browser1!") {
		t.Error("the password chosen in the browser does not sign in")
	}
}

func TestResetLinkBaseURL(t *testing.T) {
	dir := newDataDir(t)

	// Neither a flag nor the setting: print the path and say how to complete it.
	r := runCLI(t, dir, "", nil, "reset-link", adminEmail)
	if m := resetURL.FindStringSubmatch(r.out); r.code != 0 || m == nil || m[1] != "" {
		t.Fatalf("expected a bare path: exit %d\n%s", r.code, r.out)
	}
	if !strings.Contains(r.out, "Public base URL") {
		t.Errorf("the output should say where to set the base URL:\n%s", r.out)
	}

	// The System email setting the forgot-password email uses.
	d, _ := db.OpenExisting(dir)
	d.Exec(`INSERT OR REPLACE INTO app_settings (key, value) VALUES ('app_base_url', 'https://from-settings.example/')`) //nolint:errcheck
	d.Close()
	r = runCLI(t, dir, "", nil, "reset-link", adminEmail)
	if m := resetURL.FindStringSubmatch(r.out); m == nil || m[1] != "https://from-settings.example" {
		t.Errorf("expected the configured base URL:\n%s", r.out)
	}
}

func TestDisable2FA(t *testing.T) {
	dir := newDataDir(t)
	d, _ := db.OpenExisting(dir)
	d.Exec(`UPDATE users SET totp_secret='JBSWY3DPEHPK3PXP', totp_enabled=1 WHERE email=?`, adminEmail) //nolint:errcheck
	d.Close()
	if canSignIn(t, dir, "Original-Passw0rd!") {
		t.Fatal("setup: 2FA should demand a code")
	}

	if r := runCLI(t, dir, "", nil, "disable-2fa", adminEmail); r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if !canSignIn(t, dir, "Original-Passw0rd!") {
		t.Error("the password alone does not sign in after disable-2fa")
	}
	if r := runCLI(t, dir, "", nil, "disable-2fa", adminEmail); !strings.Contains(r.out, "already off") {
		t.Errorf("a second run should say 2FA is already off:\n%s", r.out)
	}
}

// Locked out completely — forgotten password and lost authenticator — in one go.
func TestResetPasswordAndDisable2FATogether(t *testing.T) {
	dir := newDataDir(t)
	d, _ := db.OpenExisting(dir)
	d.Exec(`UPDATE users SET totp_secret='JBSWY3DPEHPK3PXP', totp_enabled=1 WHERE email=?`, adminEmail) //nolint:errcheck
	d.Close()

	r := runCLI(t, dir, "", nil, "reset-password", adminEmail, "--disable-2fa")
	m := tempPassword.FindStringSubmatch(r.out)
	if r.code != 0 || m == nil {
		t.Fatalf("exit %d:\n%s%s", r.code, r.out, r.errOut)
	}
	if !canSignIn(t, dir, m[1]) {
		t.Error("cannot sign in with the new password and no code")
	}
}

func TestListShowsAccounts(t *testing.T) {
	dir := newDataDir(t)
	r := runCLI(t, dir, "", nil, "list")
	if r.code != 0 || !strings.Contains(r.out, adminEmail) || !strings.Contains(r.out, "superadmin") {
		t.Errorf("exit %d:\n%s", r.code, r.out)
	}
}

// Every console action leaves a trace in the activity log.
func TestConsoleActionsAreAudited(t *testing.T) {
	dir := newDataDir(t)
	runCLI(t, dir, "", nil, "reset-password", adminEmail)
	runCLI(t, dir, "", nil, "reset-link", adminEmail)

	d, _ := db.OpenExisting(dir)
	defer d.Close()
	rows, err := d.Query(`SELECT command FROM audit_log WHERE username='console' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var c string
		rows.Scan(&c) //nolint:errcheck
		got = append(got, c)
	}
	want := []string{"users reset-password " + adminEmail, "users reset-link " + adminEmail}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("audit = %q, want %q", got, want)
	}
}

func TestUsageAndErrors(t *testing.T) {
	dir := newDataDir(t)
	for _, c := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"frobnicate"}, 2},
		{[]string{"reset-password"}, 2},                              // no account
		{[]string{"reset-password", "a@x", "b@x"}, 2},                // two accounts
		{[]string{"reset-password", adminEmail, "--bogus"}, 2},       // unknown option
		{[]string{"reset-link", adminEmail, "--expires", "soon"}, 2}, // bad duration
		{[]string{"reset-link", adminEmail, "--expires", "1m"}, 1},   // outside 5m–72h
		{[]string{"reset-password", "nobody@example.com"}, 1},        // unknown account
		{[]string{"list", "extra"}, 2},
		{[]string{"help"}, 0},
		{[]string{"reset-password", "--help"}, 0},
	} {
		if r := runCLI(t, dir, "", nil, c.args...); r.code != c.code {
			t.Errorf("%v: exit %d, want %d (%s)", c.args, r.code, c.code, strings.TrimSpace(r.errOut))
		}
	}
	// A wrong DATA_DIR fails rather than creating a fresh, empty database.
	if r := runCLI(t, t.TempDir(), "", nil, "list"); r.code != 1 {
		t.Errorf("empty data dir: exit %d, want 1", r.code)
	}
}

func TestParseUsersArgsAcceptsFlagsAnywhere(t *testing.T) {
	pos, o, err := parseUsersArgs([]string{"--disable-2fa", "admin@example.com", "--expires=30m", "--base-url", "https://r.example/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pos) != 1 || pos[0] != "admin@example.com" || !o.disable2FA ||
		o.expires != 30*time.Minute || o.baseURL != "https://r.example" {
		t.Errorf("pos=%v opts=%+v", pos, o)
	}
	if _, _, err := parseUsersArgs([]string{"--prompt", "--password-stdin"}); err == nil {
		t.Error("--prompt with --password-stdin should be refused")
	}
	if _, _, err := parseUsersArgs([]string{"-h"}); !errors.Is(err, errUsage) {
		t.Errorf("-h: err=%v", err)
	}
}
