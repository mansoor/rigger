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

// retentionDays is how long snapshots are kept before pruning.
const retentionDays = 90

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
		interval = 1 * time.Minute
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
		base := w.Config.Project.Name
		if base == "" {
			base = w.Name
		}
		for _, env := range w.Envs {
			ps := projStats[base+"_"+env]
			memBytes := int64(ps.MemMB * 1024 * 1024)
			diskBytes := dirSizeBytes(filepath.Join(c.workspacesDir, w.Name, "envs", env))
			if _, err := c.db.Exec(
				`INSERT INTO metrics_snapshots (workspace, env, cpu_pct, memory_bytes, disk_bytes, net_rx_bytes, net_tx_bytes)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				w.Name, env, ps.CPUPct, memBytes, diskBytes, int64(ps.NetRxBytes), int64(ps.NetTxBytes),
			); err != nil {
				log.Printf("metrics: insert %s/%s: %v", w.Name, env, err)
			}
		}
	}
	c.prune()
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
