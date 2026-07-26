//go:build !windows

package executor

import (
	"context"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A cancelled command must take its whole process tree with it. `docker compose`
// runs the compose plugin as a child, so killing only the process we spawned
// leaves the real worker orphaned and still streaming — which is how following
// log streams leaked. Modelled here with sh spawning a long-lived grandchild.
func TestCancelKillsGrandchild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Print the grandchild's pid, then hold both processes open.
	script := `sleep 300 & echo $!; wait`
	var out strings.Builder
	cmd, cleanup := newCmd(Spec{
		Context: ctx,
		Bin:     "sh",
		Args:    []string{"-c", script},
	})
	defer cleanup()
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sh: %v", err)
	}

	// Wait for the grandchild pid to be reported.
	deadline := time.Now().Add(5 * time.Second)
	var grandchild int
	for time.Now().Before(deadline) {
		if s := strings.TrimSpace(out.String()); s != "" {
			if pid, err := strconv.Atoi(strings.Fields(s)[0]); err == nil {
				grandchild = pid
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grandchild == 0 {
		t.Skip("grandchild pid never reported")
	}
	if !processAlive(grandchild) {
		t.Fatalf("grandchild %d should be alive before cancel", grandchild)
	}

	cancel()
	_ = cmd.Wait()

	// The grandchild is in the killed process group, so it must be gone too.
	for deadline = time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if !processAlive(grandchild) {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Don't leave it behind if the assertion is about to fail.
	_ = syscallKill(grandchild)
	t.Fatalf("grandchild %d survived cancellation — the process group was not killed", grandchild)
}

// An uncancellable command must keep the previous behavior: no process group is
// set, so nothing about existing one-off calls changes.
func TestNoContextLeavesProcAttrUnset(t *testing.T) {
	cmd, cleanup := newCmd(Spec{Bin: "sh", Args: []string{"-c", "true"}})
	defer cleanup()
	if cmd.SysProcAttr != nil {
		t.Fatalf("expected no SysProcAttr for a non-cancellable command, got %+v", cmd.SysProcAttr)
	}
	if cmd.Cancel != nil {
		t.Fatal("expected no Cancel hook for a non-cancellable command")
	}
}

func TestContextSetsProcGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, cleanup := newCmd(Spec{Context: ctx, Bin: "sh", Args: []string{"-c", "true"}})
	defer cleanup()
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("cancellable command should run in its own process group")
	}
	if cmd.Cancel == nil {
		t.Fatal("cancellable command should have a group-killing Cancel hook")
	}
	if cmd.WaitDelay != waitDelay {
		t.Fatalf("WaitDelay = %v, want %v", cmd.WaitDelay, waitDelay)
	}
}

// processAlive reports whether pid exists. Signal 0 performs the permission and
// existence checks without delivering anything — os.Process.Signal(nil) is NOT
// the same thing. A reaped process yields ESRCH; a zombie still reports alive,
// which is why callers poll rather than checking once.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func syscallKill(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
