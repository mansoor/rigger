package metrics

import (
	"fmt"
	"testing"

	"github.com/mansoor/rigger/ui/internal/db"
)

// TestDownsampleTiers verifies the tiered thinning: full resolution within 24h,
// one sample per minute for 24–120h, and one per 5 minutes beyond 120h.
func TestDownsampleTiers(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// Insert n rows in a band, staggered every spacingSec seconds back from the
	// band's start offset (e.g. base="-48 hours").
	insert := func(base string, n, spacingSec int) {
		for i := 0; i < n; i++ {
			if _, err := d.Exec(
				`INSERT INTO metrics_snapshots (workspace, env, recorded_at)
				 VALUES ('w','dev', datetime('now', ?, ?))`,
				base, fmt.Sprintf("-%d seconds", i*spacingSec),
			); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}
	}

	insert("-1 hours", 20, 30)   // band A (≤24h): 20 rows @30s → untouched
	insert("-48 hours", 40, 30)  // band B (24–120h): 40 rows @30s over 20min → ~20 (1/min)
	insert("-200 hours", 60, 30) // band C (>120h): 60 rows @30s over 30min → ~6 (1/5min)

	NewCollector(d, "", 0, nil).downsample()

	count := func(where string) int {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM metrics_snapshots WHERE ` + where).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	if got := count("recorded_at >= datetime('now','-24 hours')"); got != 20 {
		t.Errorf("band A (≤24h): want 20 untouched, got %d", got)
	}
	if got := count("recorded_at < datetime('now','-24 hours') AND recorded_at >= datetime('now','-120 hours')"); got < 18 || got > 22 {
		t.Errorf("band B (24–120h): want ~20 (one per minute), got %d", got)
	}
	if got := count("recorded_at < datetime('now','-120 hours')"); got < 5 || got > 8 {
		t.Errorf("band C (>120h): want ~6 (one per 5 minutes), got %d", got)
	}

	// Idempotent: a second pass must not remove anything more.
	before := count("1=1")
	NewCollector(d, "", 0, nil).downsample()
	if after := count("1=1"); after != before {
		t.Errorf("downsample not idempotent: %d → %d", before, after)
	}
}

// TestIntervalSeconds checks the env override + default.
func TestIntervalSeconds(t *testing.T) {
	t.Setenv("METRICS_INTERVAL_SECONDS", "")
	if got := IntervalSeconds(); got != DefaultIntervalSeconds {
		t.Errorf("default: want %d, got %d", DefaultIntervalSeconds, got)
	}
	t.Setenv("METRICS_INTERVAL_SECONDS", "45")
	if got := IntervalSeconds(); got != 45 {
		t.Errorf("override: want 45, got %d", got)
	}
	t.Setenv("METRICS_INTERVAL_SECONDS", "garbage")
	if got := IntervalSeconds(); got != DefaultIntervalSeconds {
		t.Errorf("invalid falls back: want %d, got %d", DefaultIntervalSeconds, got)
	}
}
