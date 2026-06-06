package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/mansoor/rigger/ui/internal/metrics"
)

// GET /api/metrics/config — exposes the collection cadence (seconds) so the UI
// can poll at the same rate instead of a hardcoded interval. Driven by the same
// METRICS_INTERVAL_SECONDS the collector uses, so both stay in lock-step.
func (h *Handler) GetMetricsConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]int{
		"collect_interval_seconds": metrics.IntervalSeconds(),
	})
}

// GET /api/workspaces/{name}/envs/{env}/metrics?minutes=60
// Returns the recorded metric history for one env, oldest first, for sparklines.
// `minutes` selects the time window (default 60); `hours` is still accepted for
// backward compatibility.
func (h *Handler) GetEnvMetrics(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env := r.PathValue("env")

	const maxMinutes = 90 * 24 * 60 // matches the 90-day retention window
	minutes := 60
	if v := r.URL.Query().Get("minutes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxMinutes {
			minutes = n
		}
	} else if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n*60 <= maxMinutes {
			minutes = n * 60
		}
	}

	type point struct {
		CPUPct      float64   `json:"cpu_pct"`
		MemoryBytes int64     `json:"memory_bytes"`
		DiskBytes   int64     `json:"disk_bytes"`
		NetRxBytes  int64     `json:"net_rx_bytes"`
		NetTxBytes  int64     `json:"net_tx_bytes"`
		RecordedAt  time.Time `json:"recorded_at"`
	}

	rows, err := h.db.Query(
		`SELECT cpu_pct, memory_bytes, disk_bytes, net_rx_bytes, net_tx_bytes, recorded_at
		 FROM metrics_snapshots
		 WHERE workspace = ? AND env = ? AND recorded_at >= datetime('now', ?)
		 ORDER BY recorded_at`,
		name, env, fmt.Sprintf("-%d minutes", minutes),
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	points := []point{}
	for rows.Next() {
		var p point
		if err := rows.Scan(&p.CPUPct, &p.MemoryBytes, &p.DiskBytes, &p.NetRxBytes, &p.NetTxBytes, &p.RecordedAt); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		points = append(points, p)
	}
	writeJSON(w, http.StatusOK, points)
}
