package settings

import (
	"strconv"
	"testing"

	"github.com/mansoor/rigger/ui/internal/db"
)

// TestBuildHostResolution covers the Phase-4 build-host resolver: project binding
// wins, else the workspace default, else nil (inherit the env's deploy host).
func TestBuildHostResolution(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	h1, err := CreateHost(d, "builder-a", "10.0.0.1", 22, "root", "enc", "", WorkspaceOwnerScope("ws"))
	if err != nil {
		t.Fatalf("create host a: %v", err)
	}
	h2, err := CreateHost(d, "builder-b", "10.0.0.2", 22, "root", "enc", "", WorkspaceOwnerScope("ws"))
	if err != nil {
		t.Fatalf("create host b: %v", err)
	}

	// No binding, no ws default → nil (inherit deploy host).
	if got, err := BuildHostFor(d, "ws", "ws_proj"); err != nil || got != nil {
		t.Fatalf("expected nil build host, got %v (err %v)", got, err)
	}

	// Workspace default applies when no project binding.
	if err := SetWorkspaceSetting(d, "ws", "default_build_host_id", strconv.FormatInt(h2.ID, 10)); err != nil {
		t.Fatal(err)
	}
	if got, _ := BuildHostFor(d, "ws", "ws_proj"); got == nil || got.ID != h2.ID {
		t.Fatalf("expected ws-default host %d, got %v", h2.ID, got)
	}

	// Project binding overrides the workspace default.
	if err := SetProjectBuildHost(d, "ws_proj", h1.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := BuildHostFor(d, "ws", "ws_proj"); got == nil || got.ID != h1.ID {
		t.Fatalf("expected project host %d, got %v", h1.ID, got)
	}

	// Clearing the project binding reverts to the workspace default.
	if err := SetProjectBuildHost(d, "ws_proj", 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := BuildHostFor(d, "ws", "ws_proj"); got == nil || got.ID != h2.ID {
		t.Fatalf("expected revert to ws-default %d, got %v", h2.ID, got)
	}
}
