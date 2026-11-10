package main

// One-shot commands. With no arguments the binary is the server; with a command
// it runs that and exits. Anything else is refused. An unknown word used to fall
// through and start a second server, so a typo — or a command an older image
// doesn't have yet — opened the database and died with "address already in use"
// instead of saying what was wrong.

import (
	"fmt"
	"io"

	"github.com/mansoor/rigger/ui/internal/buildinfo"
	"github.com/mansoor/rigger/ui/internal/config"
)

const commandsUsage = `Usage: rigger [command]

With no command, rigger starts the server.

Commands:
  users            Manage accounts from the console: list them, reset a
                   password, print a reset link, turn off 2FA, disable or
                   enable an account. See: rigger users --help
  version          Print this binary's version.
  init-workspace   Create a project from a config file. See: rigger init-workspace -h
  detect-host-ip   Print the host's outbound IP (Rigger runs this itself).
  help             Show this help.
`

// runCommand runs the command in args[0] and returns the process exit code:
// 0 success, 1 failure, 2 usage error.
func runCommand(args []string, stdout, stderr io.Writer) int {
	switch args[0] {
	case "users":
		return runUsers(args[1:], consoleIO(config.Load().DataDir))
	case "init-workspace":
		return runInitWorkspace(args[1:])
	case "detect-host-ip":
		// The control plane runs this in a throwaway `--network host` container,
		// so the lookup happens in the HOST network namespace (the server's own
		// only sees the 172.x bridge). A UDP "connect" selects the outbound route
		// without sending any packet; its local address is the IP the host
		// reaches the internet on.
		ip := outboundIP()
		if ip == "" {
			return 1
		}
		fmt.Fprintln(stdout, ip)
		return 0
	case "version", "--version":
		fmt.Fprintln(stdout, buildinfo.Version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, commandsUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "rigger: unknown command %q\n\n%s", args[0], commandsUsage)
		return 2
	}
}
