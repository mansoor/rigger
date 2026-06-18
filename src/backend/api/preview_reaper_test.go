package api

import (
	"testing"

	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/previews"
)

// The reaper must leave non-expired and no-TTL previews alone (only rows with
// expires_at in (0, now] are torn down). Here nothing is due, so teardown — which
// would need a bridge — is never reached, and both rows survive.
func TestReapExpiredPreviewsSkipsActive(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// No TTL → never reaped.
	if _, err := previews.Create(d, previews.PreviewEnv{
		Workspace: "mcl", Project: "web", PRNumber: 1, EnvKey: "pr1",
		Status: previews.StatusRunning, ExpiresAt: 0,
	}); err != nil {
		t.Fatal(err)
	}
	// TTL in the future → not yet due.
	if _, err := previews.Create(d, previews.PreviewEnv{
		Workspace: "mcl", Project: "web", PRNumber: 2, EnvKey: "pr2",
		Status: previews.StatusRunning, ExpiresAt: 9999999999,
	}); err != nil {
		t.Fatal(err)
	}

	h := &Handler{db: d} // no bridge — safe because nothing is due to tear down
	h.reapExpiredPreviews(1000)

	if n, _ := previews.CountActive(d, "mcl", "web"); n != 2 {
		t.Fatalf("active previews after reap = %d, want 2 (none were due)", n)
	}
}
