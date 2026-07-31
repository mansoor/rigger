package api

// Running host-OS housekeeping (package cache, journal, kernels, /tmp) on a
// machine that isn't the control plane.
//
// Phase 1 made the DOCKER operations host-aware by routing them through
// internal/executor. The host-OS ones could not follow, because they don't talk
// to a daemon — they run commands on the operating system itself, and the way
// Rigger reaches that OS differs per target:
//
//	control plane  nsenter into PID 1's namespaces from inside Rigger's own
//	               container (needs privileged: true and pid: host)
//	remote host    the SSH session already lands on the OS — no nsenter, but
//	               possibly not as root
//
// So the transport is chosen per target, and what a target can actually do is
// probed rather than assumed. Assuming is how you get `apt-get: not found` on a
// Rocky box, or a command that hangs forever on a sudo password prompt against a
// session with no TTY.
//
// Privilege: never plain `sudo`, always `sudo -n`. Without -n, sudo on a
// non-TTY session waits on a password that can never arrive, and the request
// hangs until something times out. With -n it fails immediately and we can say
// why.

import (
	"bytes"
	"context"
	"os"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/executor"
)

// hostCaps is what a target can do at the OS level. Every field is probed; none
// is inferred from the fact that a host is registered or that Docker answers.
type hostCaps struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"` // why not, when Available is false
	Sudo      bool   `json:"sudo"`             // commands are prefixed with `sudo -n`
	Root      bool   `json:"root"`
	User      string `json:"user,omitempty"`
	PkgMgr    string `json:"pkg_manager,omitempty"` // apt-get | dnf | yum
	Journal   bool   `json:"journal"`               // journalctl present
	OS        string `json:"os,omitempty"`          // PRETTY_NAME from /etc/os-release
}

// hostProbeScript reports the target's capabilities as key=value lines. One
// script rather than five commands because each remote command is a fresh SSH
// session, and the answers are useless unless they all describe the same
// machine at the same moment.
//
// `exit 0` at the end: several branches are conditionals whose failure is a
// normal answer ("no sudo"), and without it the last false test would make the
// whole probe look like a transport failure.
const hostProbeScript = `uid=$(id -u 2>/dev/null || echo -1)
echo "uid=$uid"
echo "user=$(id -un 2>/dev/null)"
for p in apt-get dnf yum; do
  if command -v $p >/dev/null 2>&1; then echo "pkg=$p"; break; fi
done
if command -v journalctl >/dev/null 2>&1; then echo "journal=1"; fi
if [ "$uid" != "0" ] && sudo -n true >/dev/null 2>&1; then echo "sudo=1"; fi
if [ -r /etc/os-release ]; then . /etc/os-release; echo "os=$PRETTY_NAME"; fi
exit 0`

// parseHostProbe reads the probe output. Unknown keys are ignored so a future
// probe line doesn't break an older reader, and a truncated response degrades to
// "not available" rather than to a confidently wrong capability set.
func parseHostProbe(out string) hostCaps {
	var c hostCaps
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "uid":
			c.Root = v == "0"
		case "user":
			c.User = v
		case "pkg":
			c.PkgMgr = v
		case "journal":
			c.Journal = v == "1"
		case "sudo":
			c.Sudo = v == "1"
		case "os":
			c.OS = v
		}
	}
	return c
}

// hostOSTimeout bounds a host-OS command. Generous because `apt-get autoremove`
// on a neglected box is genuinely slow, but bounded because an SSH session that
// stalls would otherwise hold the request open indefinitely.
const hostOSTimeout = 10 * time.Minute

// hostProbeTimeout is short: the probe runs a handful of builtins, so anything
// slower is a sick host, and the status endpoint should say so quickly rather
// than making the whole page wait.
const hostProbeTimeout = 20 * time.Second

// hostRun runs a command against the target's operating system.
//
// Locally that means nsenter into PID 1's namespaces — Rigger is in a container,
// and /tmp or apt inside it are not the host's. Remotely the SSH session is
// already on the host, so the command runs directly, with `sudo -n` in front
// when the SSH user isn't root.
func (t *hkTarget) hostRun(d time.Duration, args ...string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	bin, full := t.hostCmd(args)

	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()

	var b bytes.Buffer
	// Context rather than Spec.Timeout: the SSH executor honours cancellation
	// (it closes the session, killing the remote command) but ignores Timeout.
	err := executor.Default(t.ex).Docker(executor.Spec{
		Bin: bin, Args: full, Stdout: &b, Stderr: &b, Context: ctx,
	})
	return strings.TrimSpace(b.String()), err
}

// hostCmd renders the transport prefix for a host-OS command.
func (t *hkTarget) hostCmd(args []string) (bin string, full []string) {
	if !t.Remote {
		// -t 1 -m -u -i -n: mount, UTS, IPC and network namespaces of PID 1.
		return "nsenter", append([]string{"-t", "1", "-m", "-u", "-i", "-n", "--"}, args...)
	}
	if t.caps().Sudo {
		return "sudo", append([]string{"-n"}, args...)
	}
	return args[0], args[1:]
}

// caps probes the target once and memoises the answer. Memoised because a single
// request may ask several capability questions, and each probe on a remote host
// is a round trip.
func (t *hkTarget) caps() hostCaps {
	if t.capsOnce {
		return t.capsCache
	}
	t.capsOnce = true
	t.capsCache = t.probeHost()
	return t.capsCache
}

// ── Is nsenter actually reaching the host? ────────────────────────────────────
//
// `nsenter -t 1` enters the namespaces of PID 1 *as this process sees it*. With
// `pid: host` that is the host's init, which is the point. Without it, PID 1 is
// Rigger's own entrypoint — so nsenter "succeeds" and enters the namespaces it
// was already in, and every host-OS task then runs INSIDE Rigger's container.
//
// That failure is silent and it is the bad kind: `apt-get clean` reports success
// having cleaned the container, `find /tmp -delete` deletes Rigger's temp files,
// and the host it was aimed at is untouched. A missing `privileged: true` at
// least fails loudly with EPERM.
//
// The tell is that PID 1 shares our mount namespace. Mount specifically because
// it is the one that decides whether /tmp and the package database are the
// host's or the container's.

// containerFiles are the markers a container runtime leaves behind. Checked
// because the namespace test alone would misfire when Rigger runs directly on a
// Linux host: there PID 1 legitimately shares our namespaces, nsenter is a
// harmless no-op, and host-OS tasks work.
var containerFiles = []string{"/.dockerenv", "/run/.containerenv"}

func inContainer() bool {
	for _, f := range containerFiles {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

// readMountNS returns this process's and PID 1's mount-namespace identities.
// Either being unreadable yields "", which pidHostMissing treats as "can't tell"
// rather than as a problem — a wrong accusation here would block a working
// install.
func readMountNS() (self, init string) {
	self, _ = os.Readlink("/proc/self/ns/mnt")
	init, _ = os.Readlink("/proc/1/ns/mnt")
	return self, init
}

// pidHostMissing reports the privileged-but-no-pid:host case.
func pidHostMissing(selfNS, initNS string, containerized bool) bool {
	return containerized && selfNS != "" && selfNS == initNS
}

const pidHostMissingReason = "Rigger can only see its own container: PID 1 shares this container's " +
	"namespaces, which means `pid: host` is missing from the rigger service. Host-OS tasks would run " +
	"inside Rigger's container instead of on the host — cleaning the wrong machine and reporting " +
	"success. Add BOTH `privileged: true` and `pid: host` to the rigger service in docker-compose.yml."

// probeHost runs hostProbeScript through the target's transport and turns the
// result into a capability set, including the reason when there isn't one.
func (t *hkTarget) probeHost() hostCaps {
	// Checked before running anything: this is the case where the commands would
	// have worked, on the wrong machine.
	if !t.Remote {
		self, init := readMountNS()
		if pidHostMissing(self, init, inContainer()) {
			return hostCaps{Reason: pidHostMissingReason}
		}
	}

	// Deliberately not via hostRun: hostRun consults caps() to decide on sudo,
	// and the probe is what determines sudo. It runs unprivileged — everything
	// in it works as any user, and needing root to ask "am I root?" would be a
	// poor test.
	bin, full := "sh", []string{"-c", hostProbeScript}
	if !t.Remote {
		bin, full = "nsenter", append([]string{"-t", "1", "-m", "-u", "-i", "-n", "--", "sh", "-c"}, hostProbeScript)
	}

	ctx, cancel := context.WithTimeout(context.Background(), hostProbeTimeout)
	defer cancel()

	var b bytes.Buffer
	err := executor.Default(t.ex).Docker(executor.Spec{
		Bin: bin, Args: full, Stdout: &b, Stderr: &b, Context: ctx,
	})
	if err != nil {
		return hostCaps{Reason: t.probeFailureReason(err)}
	}

	c := parseHostProbe(b.String())
	switch {
	case !c.Root && !c.Sudo:
		// The SSH user can read the machine but not change it. Attempting anyway
		// would produce a permission error per task; saying it once is kinder.
		c.Reason = "SSH user " + orUnknown(c.User) + " is not root and has no passwordless sudo. " +
			"Grant NOPASSWD sudo for this user, or connect as root."
	default:
		c.Available = true
	}
	return c
}

// probeFailureReason explains a probe that couldn't run at all, which means
// something different on each transport.
func (t *hkTarget) probeFailureReason(err error) string {
	if t.Remote {
		return "Couldn't run commands over SSH on this host: " + err.Error()
	}
	return "Rigger's container can't reach the host's namespaces. Add `privileged: true` " +
		"and `pid: host` to the rigger service in docker-compose.yml."
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// ── Capability gates ──────────────────────────────────────────────────────────

// requireHostOS reports whether host-OS commands can run at all, returning the
// operator-facing reason when they can't.
func (t *hkTarget) requireHostOS() (hostCaps, string) {
	c := t.caps()
	if !c.Available {
		return c, c.Reason
	}
	return c, ""
}

// pkgCleanCmds is the package-cache cleanup for a package manager. Returns nil
// for one we don't handle, which the caller reports rather than guessing at.
func pkgCleanCmds(pkg string) [][]string {
	switch pkg {
	case "apt-get":
		return [][]string{{"apt-get", "autoremove", "-y"}, {"apt-get", "clean"}}
	case "dnf":
		return [][]string{{"dnf", "autoremove", "-y"}, {"dnf", "clean", "all"}}
	case "yum":
		return [][]string{{"yum", "autoremove", "-y"}, {"yum", "clean", "all"}}
	}
	return nil
}

// unsupportedPkgMgr phrases the two ways package cleanup can be unavailable on a
// host that is otherwise reachable.
func unsupportedPkgMgr(pkg string) string {
	if pkg == "" {
		return "No supported package manager found on this host (looked for apt-get, dnf, yum)."
	}
	return "Package cleanup isn't implemented for " + pkg + " yet."
}
