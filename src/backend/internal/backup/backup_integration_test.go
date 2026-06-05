//go:build integration

package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mansoor/rigger/ui/internal/composegen"
)

// TestIntegrationDBRoundtrip proves the SQL backup→restore round-trip against a
// live postgres stack: create a row, back up (Go), drop it, restore (Go), and
// confirm the row is back. Run with:
//   go test -tags integration ./internal/backup/...
func TestIntegrationDBRoundtrip(t *testing.T) {
	base := t.TempDir()
	const ws, env = "bkit", "dev"
	prefix := "bkit_dev"
	envDir := filepath.Join(base, ws, "envs", env)
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := `{
		"project": {"name":"bkit","type":"image","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"images": [{"name":"db","image":"postgres","tag":"16-alpine","port":5432,
			"volumes":["pgdata:/var/lib/postgresql/data"]}],
		"environments": {"dev": {"deployment":"compose"}}
	}`
	mustWrite(t, filepath.Join(base, ws, "config.json"), cfg)
	content, err := composegen.Generate([]byte(cfg), env)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(envDir, "docker-compose.yml"), string(content))
	mustWrite(t, filepath.Join(envDir, ".env"), "POSTGRES_USER=app\nPOSTGRES_PASSWORD=pw\nPOSTGRES_DB=appdb\n")

	composeFile := filepath.Join(envDir, "docker-compose.yml")
	compose := func(args ...string) *exec.Cmd {
		return exec.Command("docker", append([]string{"compose", "-p", prefix, "-f", composeFile}, args...)...)
	}
	t.Cleanup(func() { _ = compose("down", "-v").Run() })

	if out, err := compose("up", "-d").CombinedOutput(); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}
	waitPgReady(t, compose)

	psql := func(sql string) (string, error) {
		out, err := compose("exec", "-T", prefix+"_db", "psql", "-U", "app", "-d", "appdb", "-tAc", sql).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if out, err := psql("CREATE TABLE t(id int); INSERT INTO t VALUES (42);"); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	opts := func(cmd string, extra ...string) Options {
		return Options{
			WorkspacesDir: base, Workspace: ws, Command: cmd, Env: env, Extra: extra,
			EnvVars: os.Environ(), Stdout: os.Stdout, Stderr: os.Stderr,
			Timestamp: "2026-01-02_03-04-05",
		}
	}

	// Back up the database only (SQL dump).
	if handled, err := Run(opts("backup", "db")); !handled || err != nil {
		t.Fatalf("backup: handled=%v err=%v", handled, err)
	}
	dump := filepath.Join(base, ws, "backups", env, "2026-01-02_03-04-05", "bkit_dev_db_2026-01-02_03-04-05.sql.gz")
	if fi, err := os.Stat(dump); err != nil || fi.Size() == 0 {
		t.Fatalf("expected non-empty dump at %s (err=%v)", dump, err)
	}

	// Destroy the data, then restore.
	if out, err := psql("DROP TABLE t;"); err != nil {
		t.Fatalf("drop: %v\n%s", err, out)
	}
	if handled, err := Run(opts("restore", "2026-01-02_03-04-05")); !handled || err != nil {
		t.Fatalf("restore: handled=%v err=%v", handled, err)
	}

	got, err := psql("SELECT count(*) FROM t WHERE id=42;")
	if err != nil {
		t.Fatalf("verify query: %v (%s)", err, got)
	}
	if got != "1" {
		t.Fatalf("row not restored: want count 1, got %q", got)
	}
}

// TestIntegrationVolumeArchive verifies a named-volume backup produces a tar.gz
// that actually contains the volume's data.
func TestIntegrationVolumeArchive(t *testing.T) {
	base := t.TempDir()
	const ws, env = "vkit", "dev"
	prefix := "vkit_dev"
	envDir := filepath.Join(base, ws, "envs", env)
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{
		"project": {"name":"vkit","type":"image","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"images": [{"name":"store","image":"alpine","tag":"3","port":1,"command":"sleep 600",
			"volumes":["data:/data"]}],
		"environments": {"dev": {"deployment":"compose"}}
	}`
	mustWrite(t, filepath.Join(base, ws, "config.json"), cfg)
	content, err := composegen.Generate([]byte(cfg), env)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(envDir, "docker-compose.yml"), string(content))
	mustWrite(t, filepath.Join(envDir, ".env"), "")

	composeFile := filepath.Join(envDir, "docker-compose.yml")
	compose := func(args ...string) *exec.Cmd {
		return exec.Command("docker", append([]string{"compose", "-p", prefix, "-f", composeFile}, args...)...)
	}
	t.Cleanup(func() { _ = compose("down", "-v").Run() })

	if out, err := compose("up", "-d").CombinedOutput(); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}
	if out, err := compose("exec", "-T", prefix+"_store", "sh", "-c", "echo hello-backup > /data/marker.txt").CombinedOutput(); err != nil {
		t.Fatalf("seed file: %v\n%s", err, out)
	}

	opts := Options{
		WorkspacesDir: base, Workspace: ws, Command: "backup", Env: env, Extra: []string{"files"},
		EnvVars: os.Environ(), Stdout: os.Stdout, Stderr: os.Stderr, Timestamp: "2026-01-02_03-04-05",
	}
	if handled, err := Run(opts); !handled || err != nil {
		t.Fatalf("backup files: handled=%v err=%v", handled, err)
	}

	archive := filepath.Join(base, ws, "backups", env, "2026-01-02_03-04-05", "vkit_dev_store_data_2026-01-02_03-04-05.tar.gz")
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("expected volume archive at %s: %v", archive, err)
	}
	// The archive must contain marker.txt with our content. Stream the archive
	// into an alpine container and extract just that file to stdout.
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command("docker", "run", "--rm", "-i", "alpine:3", "sh", "-c", "tar xzO ./marker.txt")
	cmd.Stdin = f
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "hello-backup") {
		t.Fatalf("archive does not contain expected marker content (err=%v out=%q)", err, out)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────────

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitPgReady(t *testing.T, compose func(args ...string) *exec.Cmd) {
	t.Helper()
	for i := 0; i < 60; i++ {
		if compose("exec", "-T", "bkit_dev_db", "pg_isready", "-U", "app", "-d", "appdb").Run() == nil {
			time.Sleep(1 * time.Second) // a beat past ready
			return
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatal("postgres did not become ready in time")
}
