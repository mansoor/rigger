package dockerops

import (
	"os"
	"strings"
	"testing"
)

// TestLiveStaleNetworkDetection runs against a REAL docker daemon and a real
// compose stack, so it is opt-in: it needs RIGGER_LIVE_STACK (the compose
// project name) and RIGGER_LIVE_ENVDIR (the directory holding its
// docker-compose.yml). The normal `go test ./...` gate skips it.
//
//	go test -c -o stalenet.test ./internal/dockerops
//	RIGGER_LIVE_STACK=… RIGGER_LIVE_ENVDIR=… ./stalenet.test -test.run TestLive -test.v
func liveRunner(t *testing.T) *runner {
	t.Helper()
	stack, envDir := os.Getenv("RIGGER_LIVE_STACK"), os.Getenv("RIGGER_LIVE_ENVDIR")
	if stack == "" || envDir == "" {
		t.Skip("set RIGGER_LIVE_STACK and RIGGER_LIVE_ENVDIR to run the live docker test")
	}
	return &runner{
		opts:        Options{Stdout: os.Stdout, Stderr: os.Stderr},
		stack:       stack,
		envDir:      envDir,
		composePath: envDir + "/docker-compose.yml",
	}
}

// Reports which services the detector considers stale, without changing anything.
func TestLiveStaleNetworkDetection(t *testing.T) {
	r := liveRunner(t)
	stale, err := r.staleNetworkServices()
	if err != nil {
		t.Fatalf("staleNetworkServices: %v", err)
	}
	t.Logf("stale services (%d): %v", len(stale), stale)
}

// Asserts the real daemon failure is wrapped with the actionable hint. Assumes
// the stack has been put into the stale-network state first.
func TestLiveNetworkAttachHintSurfaces(t *testing.T) {
	if os.Getenv("RIGGER_LIVE_MUTATE") != "1" {
		t.Skip("set RIGGER_LIVE_MUTATE=1 to run the live failure test")
	}
	r := liveRunner(t)
	err := r.compose("up", "-d", "--remove-orphans")
	if err == nil {
		t.Fatal("expected the up to fail — is the stack actually in the stale-network state?")
	}
	t.Logf("error surfaced:\n%v", err)
	if !strings.Contains(err.Error(), "Run Refresh") {
		t.Fatalf("failure was not annotated with the hint: %v", err)
	}
}

// Removes the stale containers so a following `up` recreates them. Gated behind
// a second variable so the read-only test above can be run on its own.
func TestLiveRecreateStaleNetworkContainers(t *testing.T) {
	if os.Getenv("RIGGER_LIVE_MUTATE") != "1" {
		t.Skip("set RIGGER_LIVE_MUTATE=1 to allow this test to remove containers")
	}
	r := liveRunner(t)

	before, err := r.staleNetworkServices()
	if err != nil {
		t.Fatalf("staleNetworkServices (before): %v", err)
	}
	t.Logf("before: %d stale — %v", len(before), before)

	r.recreateStaleNetworkContainers()

	after, err := r.staleNetworkServices()
	if err != nil {
		t.Fatalf("staleNetworkServices (after): %v", err)
	}
	t.Logf("after:  %d stale — %v", len(after), after)
	if len(after) != 0 {
		t.Fatalf("still stale after recreate: %v", after)
	}
}
