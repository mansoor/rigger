// Package metrics implements Phase 6d: a background collector that records
// per-workspace/env resource snapshots (CPU, memory, disk) on an interval, with
// retention pruning. Env cards render sparklines from this history.
package metrics

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/stats"
	"github.com/mansoor/rigger/ui/internal/workspace"
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
}

// NewCollector builds a collector. interval <= 0 defaults to 1 minute. A nil
// provider falls back to local-only stats.
func NewCollector(d *db.DB, workspacesDir string, interval time.Duration, provider StatsProvider) *Collector {
	if interval <= 0 {
		interval = DefaultIntervalSeconds * time.Second
	}
	return &Collector{db: d, workspacesDir: workspacesDir, interval: interval, provider: provider}
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

// collect writes one snapshot per workspace/env, then prunes old rows.
func (c *Collector) collect() {
	var projStats map[string]stats.ProjectStats
	if c.provider != nil {
		projStats = c.provider.ProjectStatsAllHosts()
	} else {
		projStats = stats.ContainerStatsByProject()
	}

	wss, err := workspace.List(c.workspacesDir)
	if err != nil {
		log.Printf("metrics: list workspaces: %v", err)
		return
	}

	for _, w := range wss {
		base := w.Config.Project.Prefix()
		if base == "" {
			base = w.Name
		}
		for _, env := range w.Envs {
			ps := projStats[base+"_"+env]
			memBytes := int64(ps.MemMB * 1024 * 1024)
			diskBytes := dirSizeBytes(filepath.Join(c.workspacesDir, w.Name, "envs", env))
			if _, err := c.db.Exec(
				`INSERT INTO metrics_snapshots (project, env, cpu_pct, memory_bytes, disk_bytes, net_rx_bytes, net_tx_bytes)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				w.Name, env, ps.CPUPct, memBytes, diskBytes, int64(ps.NetRxBytes), int64(ps.NetTxBytes),
			); err != nil {
				log.Printf("metrics: insert %s/%s: %v", w.Name, env, err)
			}
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
func dirSizeBytes(path string) int64 {
	if _, err := os.Stat(path); err != nil {
		return 0
	}
	out, err := exec.Command("du", "-sk", path).Output()
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0
	}
	kb, _ := strconv.ParseInt(fields[0], 10, 64)
	return kb * 1024
}
