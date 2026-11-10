package main

// `rigger users …` — account recovery from the server console.
//
// The web recovery paths both assume something still works: forgot-password
// needs a configured mailer, and the Users page needs another administrator who
// can sign in. When the only administrator is locked out, neither does. This
// runs inside the container, so it needs only shell access to the host:
//
//	docker exec -it rigger rigger users list
//	docker exec -it rigger rigger users reset-password you@example.com
//	docker exec -it rigger rigger users reset-link you@example.com
//
// It works beside the running server (see db.OpenExisting). If the server is
// down, run it in a one-off container on the same data volume instead.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/db"
)

const usersUsage = `Usage: rigger users <command> [options]

Account recovery from the server console. Run it inside the Rigger container:

  docker exec -it rigger rigger users list

Commands:
  list                         List accounts.

  reset-password <email|id>    Set a new password. By default a random one is
                               generated and printed once.
      --prompt                 Type the new password instead (hidden, twice).
      --password-stdin         Read the new password from stdin, for scripts.
      --disable-2fa            Also turn off two-factor authentication.

  reset-link <email|id>        Print a single-use link that lets the account
                               owner choose a new password in the browser.
      --expires <duration>     How long the link works: 5m to 72h (default 1h).
      --base-url <url>         Rigger's public address, if Settings → System
                               email → Public base URL is not set.

  disable-2fa <email|id>       Turn off two-factor authentication and delete
                               the account's recovery codes.

  disable <email|id>           Switch an account off: it can't sign in, and its
                               sessions end, including open terminals. Refused
                               for the last active super-admin.

  enable <email|id>            Switch a disabled account back on. It signs in
                               with its existing password.

Resetting a password also signs the account out everywhere.

If Rigger is not running, use a one-off container on the same data volume:

  docker run --rm -it -v rigger_rigger-data:/data ghcr.io/mansoor/rigger users list
`

var errUsage = errors.New("usage")

// usersOptions holds the parsed flags. Flags may appear before or after the
// positional argument — people type `reset-password you@x --prompt`, which Go's
// flag package would not parse.
type usersOptions struct {
	prompt        bool
	passwordStdin bool
	disable2FA    bool
	expires       time.Duration
	baseURL       string
}

func parseUsersArgs(args []string) (positional []string, o usersOptions, err error) {
	o.expires = auth.DefaultResetLinkTTL
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-h" || a == "--help" || a == "help" {
			return nil, o, errUsage
		}
		if !strings.HasPrefix(a, "--") {
			positional = append(positional, a)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		next := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("--%s needs a value", name)
			}
			i++
			return args[i], nil
		}
		switch name {
		case "prompt":
			o.prompt = true
		case "password-stdin":
			o.passwordStdin = true
		case "disable-2fa":
			o.disable2FA = true
		case "expires":
			v, err := next()
			if err != nil {
				return nil, o, err
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, o, fmt.Errorf("--expires: %q is not a duration (try 30m or 24h)", v)
			}
			o.expires = d
		case "base-url":
			v, err := next()
			if err != nil {
				return nil, o, err
			}
			o.baseURL = strings.TrimRight(strings.TrimSpace(v), "/")
		default:
			return nil, o, fmt.Errorf("unknown option --%s", name)
		}
	}
	if o.prompt && o.passwordStdin {
		return nil, o, errors.New("use --prompt or --password-stdin, not both")
	}
	return positional, o, nil
}

// usersIO is the command's environment, injected so tests can drive it.
type usersIO struct {
	dataDir string
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	// readSecret reads a password without echoing it. Nil when stdin is not a
	// terminal, in which case --prompt is refused rather than echoing.
	readSecret func(prompt string) (string, error)
}

// runUsers is the entry point for `rigger users`. Returns the process exit code:
// 0 success, 1 failure, 2 usage error.
func runUsers(args []string, env usersIO) int {
	if len(args) == 0 {
		fmt.Fprint(env.stderr, usersUsage)
		return 2
	}
	cmd := args[0]
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		fmt.Fprint(env.stdout, usersUsage)
		return 0
	}
	pos, opts, err := parseUsersArgs(args[1:])
	if errors.Is(err, errUsage) {
		fmt.Fprint(env.stdout, usersUsage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(env.stderr, "rigger users %s: %v\n", cmd, err)
		return 2
	}

	wantsAccount := cmd != "list"
	switch cmd {
	case "list", "reset-password", "reset-link", "disable-2fa", "disable", "enable":
	default:
		fmt.Fprintf(env.stderr, "rigger users: unknown command %q\n\n%s", cmd, usersUsage)
		return 2
	}
	if wantsAccount && len(pos) != 1 {
		fmt.Fprintf(env.stderr, "rigger users %s: give one account, by email or id\n", cmd)
		return 2
	}
	if !wantsAccount && len(pos) != 0 {
		fmt.Fprintf(env.stderr, "rigger users list: takes no arguments\n")
		return 2
	}

	d, err := db.OpenExisting(env.dataDir)
	if err != nil {
		fmt.Fprintf(env.stderr, "rigger users: %v\n", err)
		return 1
	}
	defer d.Close()
	// The JWT secret is not needed: nothing here signs or checks a session.
	svc := auth.NewService(d, "", 0)

	if cmd == "list" {
		return usersList(svc, env)
	}

	u, err := svc.FindUser(pos[0])
	if errors.Is(err, auth.ErrUserNotFound) {
		fmt.Fprintf(env.stderr, "rigger users %s: no account %q — run `rigger users list`\n", cmd, pos[0])
		return 1
	}
	if err != nil {
		fmt.Fprintf(env.stderr, "rigger users %s: %v\n", cmd, err)
		return 1
	}

	switch cmd {
	case "reset-password":
		return usersResetPassword(svc, d, u, opts, env)
	case "reset-link":
		return usersResetLink(svc, d, u, opts, env)
	case "disable":
		return usersDisable(svc, d, u, env)
	case "enable":
		return usersEnable(svc, d, u, env)
	default: // disable-2fa
		return disable2FAAndReport(svc, d, u, env)
	}
}

func usersDisable(svc *auth.Service, d *db.DB, u *auth.UserInfo, env usersIO) int {
	who := accountLabel(u)
	changed, err := svc.DisableUser(u.ID)
	switch {
	case errors.Is(err, auth.ErrLastAdmin):
		fmt.Fprintf(env.stderr, "rigger users disable: %s is the only active super-admin; disabling it would leave nobody who can manage Rigger.\n"+
			"Make another account a super-admin first. To lock out someone who knows its password, use `rigger users reset-password` instead: it signs the account out everywhere.\n", who)
		return 1
	case errors.Is(err, auth.ErrAccountInvited):
		fmt.Fprintf(env.stderr, "rigger users disable: %s has not accepted its invitation yet. "+
			"Delete it from the Users page instead; that also stops its invitation link.\n", who)
		return 1
	case err != nil && strings.Contains(err.Error(), "no such column"):
		fmt.Fprintln(env.stderr, "rigger users disable: the database predates this version of rigger. "+
			"Start this version of Rigger once so it can update the database, then run this again.")
		return 1
	case err != nil:
		fmt.Fprintf(env.stderr, "rigger users disable: %v\n", err)
		return 1
	}
	if !changed {
		fmt.Fprintf(env.stdout, "%s is already disabled.\n", who)
		return 0
	}
	recordConsoleAction(d, "disable", u)
	fmt.Fprintf(env.stdout, "Disabled %s. It can't sign in, and its sessions have ended; "+
		"open terminals and log streams close within 30 seconds.\n", who)
	fmt.Fprintln(env.stdout, "API keys belong to Rigger, not to the account that created them: "+
		"review them under Admin → API Keys if this person made any.")
	return 0
}

func usersEnable(svc *auth.Service, d *db.DB, u *auth.UserInfo, env usersIO) int {
	who := accountLabel(u)
	changed, err := svc.EnableUser(u.ID)
	if errors.Is(err, auth.ErrAccountInvited) {
		fmt.Fprintf(env.stderr, "rigger users enable: %s has not accepted its invitation yet; "+
			"it becomes active when it does. Resend the invitation from the Users page, or set a password with `rigger users reset-password`.\n", who)
		return 1
	}
	if err != nil {
		fmt.Fprintf(env.stderr, "rigger users enable: %v\n", err)
		return 1
	}
	if !changed {
		fmt.Fprintf(env.stdout, "%s is already active.\n", who)
		return 0
	}
	recordConsoleAction(d, "enable", u)
	fmt.Fprintf(env.stdout, "Enabled %s. It can sign in again with its existing password; "+
		"sessions from before it was disabled stay signed out.\n", who)
	return 0
}

func usersList(svc *auth.Service, env usersIO) int {
	users, err := svc.ListUsers()
	if err != nil {
		fmt.Fprintf(env.stderr, "rigger users list: %v\n", err)
		return 1
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tEMAIL\tNAME\tROLE\tSTATUS\t2FA")
	for _, u := range users {
		twofa := "off"
		if svc.TOTPEnabled(u.ID) {
			twofa = "on"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", u.ID, orDash(u.Email), orDash(u.Username), u.Role, u.Status, twofa)
	}
	tw.Flush()
	return 0
}

func usersResetPassword(svc *auth.Service, d *db.DB, u *auth.UserInfo, o usersOptions, env usersIO) int {
	var (
		password  string
		generated bool
		err       error
	)
	switch {
	case o.prompt:
		password, err = promptNewPassword(env)
	case o.passwordStdin:
		password, err = readPasswordLine(env.stdin)
	default:
		password, err = svc.GeneratePassword()
		generated = true
	}
	if err != nil {
		fmt.Fprintf(env.stderr, "rigger users reset-password: %v\n", err)
		return 1
	}

	activated, err := svc.SetPasswordFromConsole(u.ID, password)
	if err != nil {
		// Policy failures read well as they are ("must be at least 12 characters").
		fmt.Fprintf(env.stderr, "rigger users reset-password: %v\n", err)
		return 1
	}
	recordConsoleAction(d, "reset-password", u)

	who := accountLabel(u)
	if generated {
		fmt.Fprintf(env.stdout, "Password reset for %s.\n\n  Temporary password: %s\n\n", who, password)
		fmt.Fprintln(env.stdout, "Sign in with it, then change it under your account's Security settings.")
	} else {
		fmt.Fprintf(env.stdout, "Password updated for %s.\n", who)
	}
	fmt.Fprintln(env.stdout, "Every session the account had is now signed out.")
	if activated {
		fmt.Fprintln(env.stdout, "The account had not accepted its invitation; it is now active, and the invitation link no longer works.")
	}
	if u.Status == auth.StatusDisabled {
		fmt.Fprintf(env.stdout, "The account is disabled, so it still can't sign in. Run `rigger users enable %s` when it should.\n", accountRef(u))
	}
	if o.disable2FA {
		return disable2FAAndReport(svc, d, u, env)
	}
	return 0
}

func usersResetLink(svc *auth.Service, d *db.DB, u *auth.UserInfo, o usersOptions, env usersIO) int {
	raw, err := svc.CreateResetLinkToken(u.ID, o.expires)
	if errors.Is(err, auth.ErrAccountDisabled) {
		fmt.Fprintf(env.stderr, "rigger users reset-link: %s is disabled, so it couldn't sign in with a new password. "+
			"Run `rigger users enable %s` first.\n", accountLabel(u), accountRef(u))
		return 1
	}
	if errors.Is(err, auth.ErrAccountInvited) {
		fmt.Fprintf(env.stderr, "rigger users reset-link: %s has not accepted its invitation yet. "+
			"Resend the invitation from the Users page, or set a password with `rigger users reset-password`.\n", accountLabel(u))
		return 1
	}
	if err != nil {
		fmt.Fprintf(env.stderr, "rigger users reset-link: %v\n", err)
		return 1
	}
	recordConsoleAction(d, "reset-link", u)

	path := "/reset-password?token=" + raw
	base := o.baseURL
	if base == "" {
		base = strings.TrimRight(appSetting(d, "app_base_url"), "/")
	}
	fmt.Fprintf(env.stdout, "Reset link for %s — works once, for %s:\n\n", accountLabel(u), humanDuration(o.expires))
	if base != "" {
		fmt.Fprintf(env.stdout, "  %s%s\n\n", base, path)
	} else {
		fmt.Fprintf(env.stdout, "  %s\n\n", path)
		fmt.Fprintln(env.stdout, "Open it on the address you use for Rigger, e.g. https://rigger.example.com"+path+".")
		fmt.Fprintln(env.stdout, "To print full links, set Settings → System email → Public base URL, or pass --base-url.")
		fmt.Fprintln(env.stdout)
	}
	fmt.Fprintln(env.stdout, "Anyone with this link can set the password, so send it only to the account owner.")
	fmt.Fprintln(env.stdout, "Creating another link cancels this one.")
	return 0
}

func disable2FAAndReport(svc *auth.Service, d *db.DB, u *auth.UserInfo, env usersIO) int {
	was, err := svc.ResetTOTP(u.ID)
	if err != nil {
		fmt.Fprintf(env.stderr, "rigger users: turning off 2FA: %v\n", err)
		return 1
	}
	if !was {
		fmt.Fprintf(env.stdout, "Two-factor authentication was already off for %s.\n", accountLabel(u))
		return 0
	}
	recordConsoleAction(d, "disable-2fa", u)
	fmt.Fprintf(env.stdout, "Two-factor authentication turned off for %s, and its recovery codes deleted.\n", accountLabel(u))
	fmt.Fprintln(env.stdout, "Set it up again under the account's Security settings after signing in.")
	return 0
}

// promptNewPassword asks twice without echoing. Refuses when there is no
// terminal: echoing a password back is worse than failing.
func promptNewPassword(env usersIO) (string, error) {
	if env.readSecret == nil {
		return "", errors.New("--prompt needs a terminal: run with `docker exec -it`, or use --password-stdin")
	}
	first, err := env.readSecret("New password: ")
	if err != nil {
		return "", err
	}
	second, err := env.readSecret("Repeat it: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("the two passwords do not match")
	}
	if first == "" {
		return "", errors.New("no password given")
	}
	return first, nil
}

// readPasswordLine reads one line from stdin, without its line ending.
func readPasswordLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("no password on stdin")
	}
	return line, nil
}

// recordConsoleAction writes an audit entry, so a reset done outside the UI still
// shows up in the activity log. Best effort: failing to audit must not undo a
// recovery that already succeeded.
func recordConsoleAction(d *db.DB, action string, u *auth.UserInfo) {
	d.Exec(`INSERT INTO audit_log (username, project, command, env) VALUES (?, '', ?, '')`, //nolint:errcheck
		"console", "users "+action+" "+accountLabel(u))
}

func appSetting(d *db.DB, key string) string {
	var v string
	d.QueryRow(`SELECT value FROM app_settings WHERE key=?`, key).Scan(&v) //nolint:errcheck
	return v
}

func accountLabel(u *auth.UserInfo) string {
	if u.Email != "" {
		return u.Email
	}
	return fmt.Sprintf("%s (id %d)", u.Username, u.ID)
}

// accountRef is how to name the account in a suggested command.
func accountRef(u *auth.UserInfo) string {
	if u.Email != "" {
		return u.Email
	}
	return fmt.Sprint(u.ID)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// humanDuration renders 1h0m0s as "1 hour", 24h as "24 hours", 30m as "30 minutes".
func humanDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		h := int(d / time.Hour)
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	case d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	default:
		return d.String()
	}
}

// consoleIO wires runUsers to the real process: stdin, stdout, and a hidden
// password reader when stdin is a terminal.
func consoleIO(dataDir string) usersIO {
	env := usersIO{dataDir: dataDir, stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		env.readSecret = func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			b, err := term.ReadPassword(fd)
			fmt.Fprintln(os.Stderr)
			return string(b), err
		}
	}
	return env
}
