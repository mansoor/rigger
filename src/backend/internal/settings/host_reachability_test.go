package settings

import (
	"testing"

	"github.com/mansoor/rigger/ui/internal/db"
)

// TestHostReadPathsScan exercises every host read path against a real row so a
// SELECT/Scan column mismatch (e.g. adding a column to the struct + scan but not to
// one query) fails loudly here instead of at runtime. Reachability was one such
// near-miss: ListHostsForWorkspace scanned it before its SELECT selected it.
func TestHostReadPathsScan(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := CreateHost(d, "host-01", "10.0.0.9", 22, "root", "enc", "", WorkspaceOwnerScope("ws"))
	if err != nil {
		t.Fatal(err)
	}
	if err := SetHostReachability(d, h.ID, "private"); err != nil {
		t.Fatal(err)
	}

	got, err := GetHost(d, h.ID)
	if err != nil || got == nil {
		t.Fatalf("GetHost: %v", err)
	}
	if !got.IsPrivate() {
		t.Errorf("GetHost reachability = %q, want private", got.Reachability)
	}

	all, err := ListHosts(d)
	if err != nil || len(all) != 1 {
		t.Fatalf("ListHosts: %v (n=%d)", err, len(all))
	}
	pool, err := ListHostsForWorkspace(d, "ws")
	if err != nil || len(pool) != 1 {
		t.Fatalf("ListHostsForWorkspace: %v (n=%d)", err, len(pool))
	}
	if pool[0].Reachability != "private" {
		t.Errorf("pool reachability = %q, want private", pool[0].Reachability)
	}

	// New hosts default to public.
	h2, _ := CreateHost(d, "host-02", "10.0.0.10", 22, "root", "enc", "", WorkspaceOwnerScope("ws"))
	if g2, _ := GetHost(d, h2.ID); g2 == nil || g2.IsPrivate() {
		t.Error("new host should default to public reachability")
	}
}
