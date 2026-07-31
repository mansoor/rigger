package alerts

// Disk usage across the fleet.
//
// `disk_above_pct` used to read the control plane's disk and nothing else. That
// made the one alert an operator most needs — "a machine is filling up" — blind
// to every machine actually running workloads. Housekeeping can now clean any
// registered host; without this it stays purely reactive, and you learn a host
// is full when a deploy fails.
//
// The sampling itself lives outside this package. Reaching a remote host means
// SSH, which means settings + remotehost, which import back towards here — so
// the evaluator takes a sampler function instead and the api layer supplies one.

import (
	"time"

	"github.com/mansoor/rigger/ui/internal/stats"
)

// hostLabel names a machine in an alert message. The empty host is the control
// plane — stored empty so every event predating multi-host evaluation keeps its
// meaning without a backfill.
func hostLabel(host string) string {
	if host == "" {
		return "Control plane"
	}
	return host
}

// localDiskUsedPct is the control plane's own disk usage.
func localDiskUsedPct() float64 { return stats.Host().DiskUsedPct }

// HostDisk is one machine's disk usage at a point in time.
type HostDisk struct {
	// Host is the display label. Empty means the control plane, matching the
	// stored event's host column for everything recorded before multi-host
	// evaluation existed.
	Host string
	// UsedPct is meaningful only when Reachable — a zero on an unreachable host
	// would read as a healthy empty disk, which is the opposite of the truth.
	UsedPct   float64
	Reachable bool
}

// HostDiskSampler returns disk usage for every machine Rigger knows about,
// including the control plane. Implementations should cache: the evaluator runs
// every 60s and SSH-ing a fleet that often is a poor trade for a number that
// moves slowly.
type HostDiskSampler func() []HostDisk

// SetHostDiskSampler installs the fleet sampler. Without one the evaluator falls
// back to the control plane alone, which is the pre-multi-host behaviour.
func (e *Evaluator) SetHostDiskSampler(fn HostDiskSampler) { e.hostDisk = fn }

// diskTargets expands a disk rule into one target per machine.
//
// Unreachable hosts are OMITTED rather than reported at 0%. A skipped target is
// neither fired nor resolved, which is the point: a host whose disk was full and
// has now gone unreachable must keep its open alert. Auto-resolving it would
// turn "I can't see the machine" into "the machine is fine", at the moment that
// is least likely to be true.
func (e *Evaluator) diskTargets(rule Rule) []target {
	samples := e.sampleHostDisk()
	out := make([]target, 0, len(samples))
	for _, s := range samples {
		if !s.Reachable {
			continue
		}
		out = append(out, target{
			ws: rule.Workspace, env: rule.Env,
			host: s.Host, diskPct: s.UsedPct,
		})
	}
	return out
}

// hostDiskTTL is how long a fleet sample is reused. The evaluator ticks every
// 60s; a disk that crosses a threshold within five minutes of doing so is still
// caught long before it matters, and the fleet is spared an SSH round trip per
// host per minute.
const hostDiskTTL = 5 * time.Minute

// sampleHostDisk returns the current fleet sample, refreshing it when stale.
// Called only when an enabled disk rule exists, so an install with no such rule
// never dials anything.
func (e *Evaluator) sampleHostDisk() []HostDisk {
	if e.hostDisk == nil {
		// No sampler wired: the control plane is all we can see.
		return []HostDisk{{Host: "", UsedPct: localDiskUsedPct(), Reachable: true}}
	}
	e.diskMu.Lock()
	defer e.diskMu.Unlock()
	if e.diskCache != nil && e.diskClock().Sub(e.diskAt) < hostDiskTTL {
		return e.diskCache
	}
	e.diskCache = e.hostDisk()
	e.diskAt = e.diskClock()
	return e.diskCache
}

// diskClock is time.Now, indirected so the cache expiry is testable without
// sleeping for five minutes.
func (e *Evaluator) diskClock() time.Time {
	if e.nowFn != nil {
		return e.nowFn()
	}
	return time.Now()
}
