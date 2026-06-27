// Package managedmetrics runs a Rigger-managed VictoriaMetrics container as an
// internal infra sidecar on the Rigger host's daemon (Part C). It is the durable,
// long-retention store for Rigger's OWN container metrics (CPU/mem/disk/net): when
// enabled, internal/metrics dual-writes every snapshot here in addition to SQLite.
//
// Like internal/managedregistry, Rigger drives it through the Docker socket (no change
// to Rigger's own compose). Unlike the registry it is INTERNAL-ONLY: it attaches to
// the shared traefik_net (which Rigger is on) and is reached by container name at
// WriteURL — no published host port, no Traefik route, no auth (VictoriaMetrics
// single-node has none). A later iteration can front VMUI/Grafana behind basic-auth.
package managedmetrics

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/mansoor/rigger/ui/internal/databases"
	"github.com/mansoor/rigger/ui/internal/executor"
)

const (
	// Container is the fixed name of the managed VictoriaMetrics container.
	Container = "rigger-victoriametrics"
	// DataVolume backs the time-series store (survives Down).
	DataVolume = "rigger-victoriametrics-data"
	// Network is the shared network Rigger is also on, so Rigger reaches the
	// container by name (no published port needed).
	Network = "traefik_net"
	// Port is VictoriaMetrics' single HTTP listen port (in-network only).
	Port = "8428"
	// WriteURL is the in-network base URL Rigger writes to / queries (by container name).
	WriteURL = "http://" + Container + ":" + Port
	// ImportPath is VictoriaMetrics' plain-text Prometheus-exposition ingest endpoint.
	ImportPath = "/api/v1/import/prometheus"
	// Retention is how long VictoriaMetrics keeps data (months) — long by design, since
	// this is the durable store complementing SQLite's short window.
	Retention = "12"
	// SettingKey is the app_settings flag that turns dual-write on.
	SettingKey = "metrics_tsdb_enabled"

	dataPath      = "/victoria-metrics-data"
	defaultHelper = "busybox:1.36"
)

// Image returns the VictoriaMetrics image reference, pinned to the managed-DB
// catalog's default version so the sidecar and the managed engine stay in lock-step.
func Image() string {
	eng, ok := databases.Get("victoriametrics")
	ver := "latest"
	if ok {
		ver = eng.DefaultVersion
	}
	img := "victoriametrics/victoria-metrics"
	if ok && eng.Image != "" {
		img = eng.Image
	}
	return img + ":" + ver
}

// Manager drives the managed VictoriaMetrics container on the Rigger host.
type Manager struct {
	Exec   executor.Executor // runs docker on the rigger host (local daemon)
	Helper string            // tiny image used to measure the data volume
}

// New builds a Manager bound to exec (Local when nil).
func New(exec executor.Executor) *Manager {
	return &Manager{Exec: executor.Default(exec), Helper: defaultHelper}
}

func (m *Manager) helper() string {
	if m.Helper != "" {
		return m.Helper
	}
	return defaultHelper
}

// runArgs builds the `docker run` argument list (pure, so it is unit-tested).
// Internal-only: on traefik_net, no published port, no Traefik labels, no auth.
func (m *Manager) runArgs() []string {
	return []string{
		"run", "-d",
		"--name", Container,
		"--restart", "unless-stopped",
		"--network", Network,
		"-v", DataVolume + ":" + dataPath,
		Image(),
		// VictoriaMetrics flags (after the image = the container's command):
		"-storageDataPath=" + dataPath,
		"-retentionPeriod=" + Retention,
	}
}

// Up (re)creates the container, keeping the data volume across restarts.
func (m *Manager) Up() error {
	_ = m.Exec.Docker(executor.Spec{Args: []string{"rm", "-f", Container}}) //nolint:errcheck — idempotent
	var buf bytes.Buffer
	if err := m.Exec.Docker(executor.Spec{Args: m.runArgs(), Stdout: &buf, Stderr: &buf}); err != nil {
		return fmt.Errorf("run victoriametrics: %s", strings.TrimSpace(buf.String()))
	}
	return nil
}

// Down stops and removes the container but KEEPS the data volume, so a later Up
// restores the same history.
func (m *Manager) Down() error {
	var buf bytes.Buffer
	if err := m.Exec.Docker(executor.Spec{Args: []string{"rm", "-f", Container}, Stdout: &buf, Stderr: &buf}); err != nil {
		return fmt.Errorf("stop victoriametrics: %s", strings.TrimSpace(buf.String()))
	}
	return nil
}

// Running reports whether the container is up.
func (m *Manager) Running() bool {
	out, err := m.Exec.DockerOutput(executor.Spec{
		Args: []string{"inspect", "-f", "{{.State.Running}}", Container},
	})
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// Exists reports whether the container exists at all (running or stopped).
func (m *Manager) Exists() bool {
	_, err := m.Exec.DockerOutput(executor.Spec{
		Args: []string{"inspect", "-f", "{{.Name}}", Container},
	})
	return err == nil
}

// DiskUsage returns a human-readable size of the data volume (e.g. "42M"), or "".
func (m *Manager) DiskUsage() string {
	out, err := m.Exec.DockerOutput(executor.Spec{
		Args: []string{"run", "--rm", "-v", DataVolume + ":/v:ro", m.helper(), "du", "-sh", "/v"},
	})
	if err != nil {
		return ""
	}
	if f := strings.Fields(string(out)); len(f) > 0 {
		return f[0]
	}
	return ""
}
