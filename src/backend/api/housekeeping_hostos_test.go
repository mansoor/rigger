package api

import (
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/executor"
)

func TestParseHostProbe(t *testing.T) {
	// Shape emitted by hostProbeScript on a non-root Ubuntu host with sudo.
	const out = `uid=1000
user=deploy
pkg=apt-get
journal=1
sudo=1
os=Ubuntu 22.04.4 LTS`
	c := parseHostProbe(out)
	if c.Root {
		t.Error("uid 1000 is not root")
	}
	if !c.Sudo || !c.Journal || c.PkgMgr != "apt-get" || c.User != "deploy" {
		t.Errorf("unexpected caps: %+v", c)
	}
	if c.OS != "Ubuntu 22.04.4 LTS" {
		t.Errorf("os = %q — a value with spaces must survive intact", c.OS)
	}
}

func TestParseHostProbeRoot(t *testing.T) {
	c := parseHostProbe("uid=0\nuser=root\npkg=dnf\n")
	if !c.Root {
		t.Error("uid 0 is root")
	}
	// The script only emits sudo=1 for a non-root user, and root needs none.
	if c.Sudo {
		t.Error("root should not be marked as needing sudo")
	}
	if c.PkgMgr != "dnf" {
		t.Errorf("pkg = %q", c.PkgMgr)
	}
}

// A truncated or unrecognisable probe must land on "can't", never on a
// confidently wrong capability set.
func TestParseHostProbeDegrades(t *testing.T) {
	for _, in := range []string{"", "garbage", "uid=", "\n\n", "command not found"} {
		c := parseHostProbe(in)
		if c.Root || c.Sudo || c.PkgMgr != "" || c.Available {
			t.Errorf("parseHostProbe(%q) = %+v, want empty", in, c)
		}
	}
}

// hostCmd is what actually decides which machine a destructive command lands on,
// so assert the exact argv for each transport.
func TestHostCmdLocalUsesNsenter(t *testing.T) {
	tgt := &hkTarget{ex: executor.Local{}, Host: controlPlaneLabel}
	bin, args := tgt.hostCmd([]string{"apt-get", "clean"})
	if bin != "nsenter" {
		t.Fatalf("bin = %q, want nsenter — inside the container /tmp and apt are not the host's", bin)
	}
	got := strings.Join(args, " ")
	const want = "-t 1 -m -u -i -n -- apt-get clean"
	if got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
}

func TestHostCmdRemoteRoot(t *testing.T) {
	// Root over SSH: no nsenter (the session is already on the host) and no sudo.
	tgt := &hkTarget{Host: "web-1", Remote: true, capsOnce: true,
		capsCache: hostCaps{Available: true, Root: true}}
	bin, args := tgt.hostCmd([]string{"apt-get", "clean"})
	if bin != "apt-get" || strings.Join(args, " ") != "clean" {
		t.Errorf("got %q %v, want apt-get [clean]", bin, args)
	}
}

func TestHostCmdRemoteSudoIsNonInteractive(t *testing.T) {
	tgt := &hkTarget{Host: "web-1", Remote: true, capsOnce: true,
		capsCache: hostCaps{Available: true, Sudo: true, User: "deploy"}}
	bin, args := tgt.hostCmd([]string{"journalctl", "--vacuum-time=14d"})
	if bin != "sudo" {
		t.Fatalf("bin = %q, want sudo", bin)
	}
	// -n is not optional: without it sudo on a session with no TTY blocks on a
	// password prompt that can never be answered, and the request hangs.
	if args[0] != "-n" {
		t.Errorf("args = %v, want -n first", args)
	}
	if strings.Join(args, " ") != "-n journalctl --vacuum-time=14d" {
		t.Errorf("args = %v", args)
	}
}

// `privileged: true` without `pid: host` is the dangerous misconfiguration:
// nsenter -t 1 succeeds by entering the namespaces it was already in, so every
// host-OS task runs inside Rigger's container and reports success.
func TestPidHostMissing(t *testing.T) {
	const ns = "mnt:[4026532398]"
	const other = "mnt:[4026531840]"

	if !pidHostMissing(ns, ns, true) {
		t.Error("in a container, PID 1 sharing our mount namespace means pid: host is absent")
	}
	if pidHostMissing(ns, other, true) {
		t.Error("a distinct PID 1 namespace is what pid: host looks like — must not accuse")
	}
	// Running directly on a Linux host: PID 1 legitimately shares our namespaces
	// and nsenter is a harmless no-op. Accusing here would block a working setup.
	if pidHostMissing(ns, ns, false) {
		t.Error("outside a container, shared namespaces are normal")
	}
	// Unreadable /proc links must mean "can't tell", never "misconfigured".
	for _, c := range [][2]string{{"", ""}, {"", ns}, {ns, ""}} {
		if pidHostMissing(c[0], c[1], true) {
			t.Errorf("unreadable namespaces %q/%q must not be reported as a misconfiguration", c[0], c[1])
		}
	}
}

func TestPkgCleanCmds(t *testing.T) {
	if got := pkgCleanCmds("apt-get"); len(got) != 2 || got[0][0] != "apt-get" {
		t.Errorf("apt-get = %v", got)
	}
	if got := pkgCleanCmds("dnf"); len(got) != 2 || strings.Join(got[1], " ") != "dnf clean all" {
		t.Errorf("dnf = %v", got)
	}
	// An unknown manager returns nil so the caller reports it rather than
	// running an apt command on a host that has never heard of apt.
	for _, pkg := range []string{"", "apk", "pacman", "zypper"} {
		if got := pkgCleanCmds(pkg); got != nil {
			t.Errorf("pkgCleanCmds(%q) = %v, want nil", pkg, got)
		}
	}
}
