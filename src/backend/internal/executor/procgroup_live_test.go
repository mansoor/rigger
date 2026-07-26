//go:build !windows

package executor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveComposeLogsCancelled runs a REAL `docker compose … logs -f` and checks
// that cancelling it leaves nothing behind — including the compose plugin the
// docker CLI spawns as a child, which is what actually held the log stream open.
//
// Opt-in: needs RIGGER_LIVE_STACK (compose project) and RIGGER_LIVE_ENVDIR.
//
//	go test -c -o procgroup.test ./internal/executor
//	RIGGER_LIVE_STACK=… RIGGER_LIVE_ENVDIR=… ./procgroup.test -test.run TestLiveCompose -test.v
func TestLiveComposeLogsCancelled(t *testing.T) {
	stack, envDir := os.Getenv("RIGGER_LIVE_STACK"), os.Getenv("RIGGER_LIVE_ENVDIR")
	if stack == "" || envDir == "" {
		t.Skip("set RIGGER_LIVE_STACK and RIGGER_LIVE_ENVDIR to run the live docker test")
	}

	baseline := composeProcs(t)
	t.Logf("compose processes before: %d", len(baseline))

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Local{}.Docker(Spec{
			Context: ctx,
			Args:    []string{"compose", "-p", stack, "-f", "docker-compose.yml", "logs", "-f"},
			Dir:     envDir,
			Stdout:  io.Discard,
			Stderr:  io.Discard,
		})
	}()

	// Let the CLI start and exec its plugin child.
	time.Sleep(3 * time.Second)
	running := composeProcs(t)
	t.Logf("compose processes while streaming: %d", len(running))
	if len(running) <= len(baseline) {
		t.Fatalf("expected a live `compose logs -f` process tree, saw %d (baseline %d)", len(running), len(baseline))
	}

	cancel()
	select {
	case <-errc:
	case <-time.After(15 * time.Second):
		t.Fatal("Docker() did not return within 15s of cancellation")
	}

	// The whole group should be gone; poll briefly to let init reap.
	deadline := time.Now().Add(10 * time.Second)
	var left []string
	for time.Now().Before(deadline) {
		left = composeProcs(t)
		if len(left) <= len(baseline) {
			t.Logf("compose processes after cancel: %d — clean", len(left))
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("leaked %d compose process(es) after cancellation (baseline %d): %v",
		len(left)-len(baseline), len(baseline), left)
}

// composeProcs lists live processes whose command line mentions compose. Zombies
// are excluded: they hold only a pid slot and are reaped by init.
func composeProcs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skipf("no /proc on this platform: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid := e.Name()
		if pid[0] < '0' || pid[0] > '9' {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
		if err != nil || len(raw) == 0 {
			continue // gone, or a zombie (empty cmdline)
		}
		cmdline := strings.ReplaceAll(string(raw), "\x00", " ")
		if strings.Contains(cmdline, "compose") && strings.Contains(cmdline, "logs") {
			out = append(out, pid+": "+strings.TrimSpace(cmdline))
		}
	}
	return out
}
