package dockerops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMissingBindFiles(t *testing.T) {
	dir := t.TempDir()
	// A bind config file that DOES exist (must not be flagged).
	if err := os.WriteFile(filepath.Join(dir, "present.conf"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	compose := `services:
  prometheus:
    image: prom/prometheus
    volumes:
      - ${RIGGER_BIND_ROOT:-.}/volumes/prometheus_data:/prometheus
      - ${RIGGER_BIND_ROOT:-.}/prometheus.yml:/etc/prometheus/prometheus.yml:ro
  app:
    image: app
    volumes:
      - ${RIGGER_BIND_ROOT:-.}/present.conf:/app/present.conf:ro
      - ./config/app.yaml:/app/config.yaml
      - app_data:/data
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	got := missingBindFiles(dir)
	// Expect exactly the two MISSING file-like binds: prometheus.yml and config/app.yaml.
	// NOT: volumes/prometheus_data (dir, no extension), present.conf (exists), app_data (named volume).
	if len(got) != 2 {
		t.Fatalf("want 2 missing bind files, got %d: %+v", len(got), got)
	}
	if got[0].Source != "config/app.yaml" || got[0].Target != "/app/config.yaml" {
		t.Errorf("unexpected first entry: %+v", got[0])
	}
	if got[1].Source != "prometheus.yml" || got[1].Target != "/etc/prometheus/prometheus.yml" {
		t.Errorf("unexpected second entry: %+v", got[1])
	}
}

// All bind sources present (or dirs/named volumes only) ⇒ nothing flagged.
func TestMissingBindFilesAllPresent(t *testing.T) {
	dir := t.TempDir()
	compose := `services:
  db:
    image: postgres
    volumes:
      - ${RIGGER_BIND_ROOT:-.}/volumes/db_data:/var/lib/postgresql/data
      - pgconf:/etc/pg
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := missingBindFiles(dir); len(got) != 0 {
		t.Errorf("want no missing bind files, got %+v", got)
	}
}

// No compose file ⇒ no-op (don't block a deploy on the guard's own failure).
func TestMissingBindFilesNoCompose(t *testing.T) {
	if got := missingBindFiles(t.TempDir()); got != nil {
		t.Errorf("want nil for missing compose, got %+v", got)
	}
}
