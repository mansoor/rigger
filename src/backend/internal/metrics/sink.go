package metrics

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
)

// Sample is one metrics snapshot for a project/env at a point in time. The collector
// builds these once per cycle and hands them to each active Sink (SQLite + optionally
// VictoriaMetrics), so adding a backend doesn't touch the collection logic.
type Sample struct {
	Project     string // resource prefix (globally unique)
	Env         string
	CPUPct      float64
	MemoryBytes int64
	DiskBytes   int64
	NetRxBytes  int64
	NetTxBytes  int64
	At          time.Time
}

// Sink persists a batch of samples. Implementations must be best-effort: a Write
// error is logged by the caller but never aborts the collection cycle.
type Sink interface {
	Write(samples []Sample) error
}

// SQLiteSink writes snapshots to the metrics_snapshots table — the default, always-on
// store the env-card sparkline endpoint reads from.
type SQLiteSink struct{ db *db.DB }

// NewSQLiteSink builds the default SQLite sink.
func NewSQLiteSink(d *db.DB) *SQLiteSink { return &SQLiteSink{db: d} }

// Write inserts one row per sample (recorded_at defaults to CURRENT_TIMESTAMP).
func (s *SQLiteSink) Write(samples []Sample) error {
	var firstErr error
	for _, m := range samples {
		if _, err := s.db.Exec(
			`INSERT INTO metrics_snapshots (project, env, cpu_pct, memory_bytes, disk_bytes, net_rx_bytes, net_tx_bytes)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.Project, m.Env, m.CPUPct, m.MemoryBytes, m.DiskBytes, m.NetRxBytes, m.NetTxBytes,
		); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// TSDBSink remote-writes snapshots to a VictoriaMetrics instance via its plain-text
// Prometheus-exposition ingest endpoint (/api/v1/import/prometheus) — no client lib /
// protobuf needed. Used for long-retention + PromQL; complements (never replaces) the
// SQLite sink in this cut.
type TSDBSink struct {
	URL    string // base URL, e.g. http://rigger-victoriametrics:8428
	Client *http.Client
}

// NewTSDBSink builds a TSDB sink targeting baseURL (a 5s-timeout client by default).
func NewTSDBSink(baseURL string) *TSDBSink {
	return &TSDBSink{URL: strings.TrimRight(baseURL, "/"), Client: &http.Client{Timeout: 5 * time.Second}}
}

// exposition renders the samples as Prometheus text-exposition lines with a shared
// millisecond timestamp:
//
//	rigger_cpu_pct{project="…",env="…"} <v> <ms>
//	rigger_memory_bytes{…} <v> <ms>   (+ disk_bytes, net_rx_bytes, net_tx_bytes)
func exposition(samples []Sample) string {
	var b strings.Builder
	for _, m := range samples {
		ms := m.At.UnixMilli()
		if m.At.IsZero() {
			ms = time.Now().UnixMilli()
		}
		// %q yields Prometheus-compatible label quoting (escapes " \ and newline).
		labels := fmt.Sprintf(`{project=%q,env=%q}`, m.Project, m.Env)
		fmt.Fprintf(&b, "rigger_cpu_pct%s %g %d\n", labels, m.CPUPct, ms)
		fmt.Fprintf(&b, "rigger_memory_bytes%s %d %d\n", labels, m.MemoryBytes, ms)
		fmt.Fprintf(&b, "rigger_disk_bytes%s %d %d\n", labels, m.DiskBytes, ms)
		fmt.Fprintf(&b, "rigger_net_rx_bytes%s %d %d\n", labels, m.NetRxBytes, ms)
		fmt.Fprintf(&b, "rigger_net_tx_bytes%s %d %d\n", labels, m.NetTxBytes, ms)
	}
	return b.String()
}

// Write POSTs the exposition payload to VictoriaMetrics. A 2xx (VM returns 204) is
// success; anything else is an error the caller logs.
func (s *TSDBSink) Write(samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	body := exposition(samples)
	req, err := http.NewRequest(http.MethodPost, s.URL+"/api/v1/import/prometheus", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("victoriametrics import: %s: %s", resp.Status, strings.TrimSpace(buf.String()))
	}
	return nil
}
