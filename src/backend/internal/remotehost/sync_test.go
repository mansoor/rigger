package remotehost

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/mansoor/rigger/ui/internal/executor"
)

func TestRemoteCommand(t *testing.T) {
	r := NewRemote(nil, "/toolkit/workspaces", "/srv/rigger/workspaces")

	got := r.command(executor.Spec{
		Args: []string{"compose", "-p", "app_dev", "-f", "docker-compose.yml", "up", "-d"},
		Dir:  "/toolkit/workspaces/app/envs/dev",
	})
	want := "cd '/srv/rigger/workspaces/app/envs/dev' && docker 'compose' '-p' 'app_dev' '-f' 'docker-compose.yml' 'up' '-d'"
	if got != want {
		t.Errorf("command:\n got %q\nwant %q", got, want)
	}

	// No Dir → no cd prefix.
	if got := r.command(executor.Spec{Args: []string{"version"}}); got != "docker 'version'" {
		t.Errorf("no-dir command = %q", got)
	}

	// Path outside the local base passes through untranslated.
	if d := r.translate("/etc/passwd"); d != "/etc/passwd" {
		t.Errorf("translate passthrough = %q", d)
	}
	// An arg containing a single quote is escaped safely.
	if got := r.command(executor.Spec{Args: []string{"logs", "a'b"}}); got != `docker 'logs' 'a'\''b'` {
		t.Errorf("quote-escaped command = %q", got)
	}
}

func TestTarGzRoundTrip(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "docker-compose.yml"), "services: {}\n")
	mustWrite(t, filepath.Join(src, ".env"), "SECRET=keepme\n")
	mustWrite(t, filepath.Join(src, "backend", "Dockerfile"), "FROM scratch\n")

	// Push everything except .env (host-authoritative).
	var buf bytes.Buffer
	if err := writeTarGz(&buf, src, []string{".env"}); err != nil {
		t.Fatalf("writeTarGz: %v", err)
	}

	dest := t.TempDir()
	if err := extractTarGz(&buf, dest); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}

	if got := readFile(t, filepath.Join(dest, "docker-compose.yml")); got != "services: {}\n" {
		t.Errorf("compose content = %q", got)
	}
	if got := readFile(t, filepath.Join(dest, "backend", "Dockerfile")); got != "FROM scratch\n" {
		t.Errorf("nested file content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dest, ".env")); !os.IsNotExist(err) {
		t.Errorf(".env should have been skipped, stat err = %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
