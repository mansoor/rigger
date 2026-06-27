// Package metrics implements Phase 6d: a background collector that records
// per-workspace/env resource snapshots (CPU, memory, disk) on an interval, with
// retention pruning. Env cards render sparklines from this history.
package metrics

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/managedmetrics"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/stats"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

const (
	// DefaultIntervalSeconds is the collection cadence when METRICS_INTERVAL_SECONDS
	// is unset. It also drives how often the UI refetches (via IntervalSeconds).
	DefaultIntervalSeconds = 30

	// retentionDays is the hard cap — snapshots older than this are deleted outright.
	retentionDays = 90

	// downsampleEvery is how often the tiered thinning job runs.
	downsampleEvery = 6 * time.Hour

	// Tiered resolution bands, by age:
	//   age ≤ fullResHours          : full resolution (one sample per collection interval)
	//   fullResHours…minuteResHours : thinned to one sample per minute
	//   age > minuteResHours        : thinned to one sample per 5 minutes
	fullResHours   = 24
	minuteResHours = 120
)

// IntervalSeconds resolves the metrics collection cadence from
// METRICS_INTERVAL_SECONDS (a positive integer of seconds), falling back to
// DefaultIntervalSeconds. Single source of truth shared by the collector and the
// /api/metrics/config endpoint the UI polls at, so both move together.
func IntervalSeconds() int {
	if v := os.Getenv("METRICS_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultIntervalSeconds
}

// StatsProvider supplies per-project resource stats merged across every host
// (Phase 7). Satisfied by *shell.Bridge; an interface here avoids importing it.
type StatsProvider interface {
	ProjectStatsAllHosts() map[string]stats.ProjectStats
}

// Collector samples resource usage for every workspace/env and persists it.
type Collector struct {
	db            *db.DB
	workspacesDir string
	interval      time.Duration
	provider      StatsProvider // multi-host stats; nil ⇒ local only

	diskMu    sync.Mutex       // guards diskCache
	diskCache map[string]int64 // last-known dir size per path (so a slow/timed-out du reuses it)

	sqlite *SQLiteSink // default, always-on store (env-card sparklines read it)
	tsdb   *TSDBSink   // managed VictoriaMetrics; written only when the toggle is on
}

// NewCollector builds a collector. interval <= 0 defaults to 1 minute. A nil
// provider falls back to local-only stats.
func NewCollector(d *db.DB, workspacesDir string, interval time.Duration, provider StatsProvider) *Collector {
	if interval <= 0 {
		interval = DefaultIntervalSeconds * time.Second
	}
	return &Collector{
		db: d, workspacesDir: workspacesDir, interval: interval, provider: provider,
		diskCache: map[string]int64{},
		sqlite:    NewSQLiteSink(d),
		tsdb:      NewTSDBSink(managedmetrics.WriteURL),
	}
}

// tsdbEnabled reports whether dual-write to the managed VictoriaMetrics is on. Read
// each cycle (cheap) so toggling it in Admin takes effect without restarting Rigger.
func (c *Collector) tsdbEnabled() bool {
	return settings.AppSetting(c.db, managedmetrics.SettingKey) == "true"
}

// Run starts the collector loop in a background goroutine.
func (c *Collector) Run() {
	go func() {
		time.Sleep(15 * time.Second) // let containers settle after startup
		c.collect()
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for range ticker.C {
			c.collect()
		}
	}()
	// Tiered downsampling + retention prune on a slow, independent cadence so a
	// short (e.g. 30s) collection interval doesn't bloat the table over time.
	go func() {
		time.Sleep(2 * time.Minute) // first pass shortly after startup
		c.downsample()
		ticker := time.NewTicker(downsampleEvery)
		defer ticker.Stop()
		for range ticker.C {
			c.downsample()
		}
	}()
}

// gatherStats samples per-project usage with a hard ceiling so a hung
// `docker stats` (e.g. an unhealthy container) can never freeze the collector.
// The underlying docker command is itself bounded (executor Spec.Timeout); this
// select is belt-and-suspenders so the ticker keeps firing regardless.
func (c *Collector) gatherStats() map[string]stats.ProjectStats {
	done := make(chan map[string]stats.ProjectStats, 1)
	go func() {
		if c.provider != nil {
			done <- c.provider.ProjectStatsAllHosts()
		} else {
			done <- stats.ContainerStatsByProject()
		}
	}()
	select {
	case s := <-done:
		return s
	case <-time.After(25 * time.Second):
		log.Printf("metrics: stats sampling timed out; recording disk-only this cycle")
		return map[string]stats.ProjectStats{}
	}
}

// collect samples every workspace/env once, then fans the batch out to the active
// sinks: SQLite always (the dashboard source), plus the managed VictoriaMetrics when
// the toggle is on. A sink error is logged but never aborts the cycle.
func (c *Collector) collect() {
	projStats := c.gatherStats()

	wss, err := workspace.List(c.workspacesDir)
	if err != nil {
		log.Printf("metrics: list workspaces: %v", err)
		return
	}

	now := time.Now()
	samples := make([]Sample, 0, 16)
	for _, w := range wss {
		base := w.Config.Project.Prefix()
		if base == "" {
			base = w.Name
		}
		for _, env := range w.Envs {
			ps := projStats[base+"_"+env]
			// Key by the resource prefix (globally unique) so same-named projects in
			// different workspaces don't collide.
			samples = append(samples, Sample{
				Project:     base,
				Env:         env,
				CPUPct:      ps.CPUPct,
				MemoryBytes: int64(ps.MemMB * 1024 * 1024),
				DiskBytes:   c.dirSizeBytes(wspath.EnvDir(c.workspacesDir, w.WorkspaceName, w.Name, env)),
				NetRxBytes:  int64(ps.NetRxBytes),
				NetTxBytes:  int64(ps.NetTxBytes),
				At:          now,
			})
		}
	}
	if len(samples) == 0 {
		return
	}
	if err := c.sqlite.Write(samples); err != nil {
		log.Printf("metrics: sqlite write: %v", err)
	}
	if c.tsdbEnabled() {
		if err := c.tsdb.Write(samples); err != nil {
			log.Printf("metrics: tsdb write: %v", err)
		}
	}
}

// downsample thins old rows into coarser resolution bands, then prunes anything
// past the retention window. Idempotent — safe to run repeatedly.
func (c *Collector) downsample() {
	c.thin(fullResHours, minuteResHours, 60) // 24h–120h → one sample per minute
	c.thinTail(minuteResHours, 300)          // >120h    → one sample per 5 minutes
	c.prune()
}

// thin keeps only the earliest row in each bucketSec-wide window per
// workspace/env within the [minHours, maxHours) age band, deleting the rest.
func (c *Collector) thin(minHours, maxHours, bucketSec int) {
	younger := fmt.Sprintf("-%d hours", minHours)
	older := fmt.Sprintf("-%d hours", maxHours)
	q := fmt.Sprintf(`
		DELETE FROM metrics_snapshots
		WHERE recorded_at < datetime('now', ?)
		  AND recorded_at >= datetime('now', ?)
		  AND id NOT IN (
		    SELECT MIN(id) FROM metrics_snapshots
		    WHERE recorded_at < datetime('now', ?)
		      AND recorded_at >= datetime('now', ?)
		    GROUP BY project, env, CAST(strftime('%%s', recorded_at) AS INTEGER) / %d
		  )`, bucketSec)
	if _, err := c.db.Exec(q, younger, older, younger, older); err != nil {
		log.Printf("metrics: thin %d-%dh: %v", minHours, maxHours, err)
	}
}

// thinTail is like thin but for everything older than minHours (no upper bound).
func (c *Collector) thinTail(minHours, bucketSec int) {
	older := fmt.Sprintf("-%d hours", minHours)
	q := fmt.Sprintf(`
		DELETE FROM metrics_snapshots
		WHERE recorded_at < datetime('now', ?)
		  AND id NOT IN (
		    SELECT MIN(id) FROM metrics_snapshots
		    WHERE recorded_at < datetime('now', ?)
		    GROUP BY project, env, CAST(strftime('%%s', recorded_at) AS INTEGER) / %d
		  )`, bucketSec)
	if _, err := c.db.Exec(q, older, older); err != nil {
		log.Printf("metrics: thinTail >%dh: %v", minHours, err)
	}
}

// prune deletes snapshots older than the retention window.
func (c *Collector) prune() {
	cutoff := fmt.Sprintf("-%d days", retentionDays)
	if _, err := c.db.Exec(
		`DELETE FROM metrics_snapshots WHERE recorded_at < datetime('now', ?)`, cutoff,
	); err != nil {
		log.Printf("metrics: prune: %v", err)
	}
}

// dirSizeBytes returns the size of a directory in bytes via `du -sk`.
// Returns 0 if the path is missing or du fails.
// dirSizeBytes returns a directory's size via `du`, bounded by a timeout so a
// slow/stalled walk over a (Windows) bind mount can never freeze the collector.
// On timeout or error it reuses the last-known size for that path (0 if none),
// so the metric doesn't flap to zero.
func (c *Collector) dirSizeBytes(path string) int64 {
	if _, err := os.Stat(path); err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "du", "-sk", path).Output()
	if err != nil {
		// Timed out or failed — fall back to the last value we computed.
		c.diskMu.Lock()
		last := c.diskCache[path]
		c.diskMu.Unlock()
		if ctx.Err() != nil {
			log.Printf("metrics: du timed out for %s; reusing last size", path)
		}
		return last
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0
	}
	kb, _ := strconv.ParseInt(fields[0], 10, 64)
	size := kb * 1024
	c.diskMu.Lock()
	c.diskCache[path] = size
	c.diskMu.Unlock()
	return size
}
