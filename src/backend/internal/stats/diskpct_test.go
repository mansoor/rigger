package stats

import "testing"

func TestDiskPercentMatchesDF(t *testing.T) {
	// Real `df -kP /` row from an ext4 root, including df's own Capacity column:
	//   /dev/sda1  51474044  20589616  28851000  42% /
	// df rounds Use% up, so 41.6 presents as 42 — the point is that we now land
	// on df's number instead of the 40.0% the old used/blocks formula produced.
	got := DiskPercent(20589616, 28851000)
	if got < 41.5 || got > 41.7 {
		t.Errorf("DiskPercent = %.2f, want ~41.6 (df shows 42%%)", got)
	}

	// The reserved-block gap is the whole reason this exists. Same filesystem,
	// half genuinely used, 5% reserved for root:
	//   total 100, used 50, avail 45
	// old local  (100-45)/100 = 55%      old remote  50/100 = 50%
	// df                       50/95 ≈ 52.6%
	if got := DiskPercent(50, 45); got < 52.5 || got > 52.7 {
		t.Errorf("DiskPercent(50, 45) = %.2f, want ~52.6", got)
	}
}

func TestDiskPercentEdges(t *testing.T) {
	// A filesystem with no reserve: used/(used+avail) == used/total.
	if got := DiskPercent(5242880, 5242880); got != 50 {
		t.Errorf("DiskPercent = %.2f, want exactly 50", got)
	}
	if got := DiskPercent(0, 100); got != 0 {
		t.Errorf("empty disk = %.2f, want 0", got)
	}
	if got := DiskPercent(100, 0); got != 100 {
		t.Errorf("full disk = %.2f, want 100", got)
	}
	// Unreadable/absent numbers must be 0, not NaN — a NaN threshold comparison
	// is false in both directions and would silently disable the alert.
	if got := DiskPercent(0, 0); got != 0 {
		t.Errorf("DiskPercent(0, 0) = %v, want 0", got)
	}
}
