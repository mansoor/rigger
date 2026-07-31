package stats

// What "disk 85% full" means.
//
// The local collector (statfs) and the remote one (`df -kP /` over SSH) used to
// compute this differently, and neither matched what `df -h` prints:
//
//	local   (total - available) / total        counts reserved blocks as used
//	remote  used / total                       counts them as neither
//	df      used / (used + available)          what the operator actually reads
//
// On a stock ext4 root, ~5% of the filesystem is reserved for root. So the same
// half-full disk read 55% on the control plane and 50% on a remote host, and the
// disk_above_pct rule — one threshold, now evaluated across every machine —
// would fire on one before the other for no reason but which side of the SSH
// connection it sat on.
//
// Both now use df's definition, so a threshold means the same thing everywhere
// and matches the number an operator sees when they check by hand.

// DiskPercent returns the used percentage as `df` reports it in its Use% column.
// used and avail must share a unit; the result is unitless.
func DiskPercent(used, avail float64) float64 {
	capacity := used + avail
	if capacity <= 0 {
		return 0
	}
	return used / capacity * 100.0
}
