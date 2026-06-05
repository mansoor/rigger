//go:build integration

package dockerops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/composegen"
)

// TestIntegrationLifecycle drives the real compose lifecycle through dockerops
// against a live Docker daemon using a throwaway nginx image-stack. Run with:
//   go test -tags integration ./internal/dockerops/...
// (requires the docker CLI + compose plugin + a reachable daemon).
func TestIntegrationLifecycle(t *testing.T) {
	base := t.TempDir()
	const ws, env, stack = "itest", "dev", "itest_dev"
	envDir := filepath.Join(base, ws, "envs", env)
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := `{
		"project": {"name":"itest","type":"image","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"images": [{"name":"web","image":"nginx","tag":"alpine","port":80}],
		"environments": {"dev": {"deployment":"compose"}}
	}`
	mustWrite(t, filepath.Join(base, ws, "config.json"), cfg)
	content, err := composegen.Generate([]byte(cfg), env)
	if err != nil {
		t.Fatalf("compose gen: %v", err)
	}
	mustWrite(t, filepath.Join(envDir, "docker-compose.yml"), string(content))
	mustWrite(t, filepath.Join(envDir, ".env"), "")

	opts := func(cmd string, extra ...string) Options {
		return Options{
			WorkspacesDir: base, Workspace: ws, Command: cmd, Env: env, Extra: extra,
			EnvVars: os.Environ(), Stdout: os.Stdout, Stderr: os.Stderr,
		}
	}
	// Always clean up the stack even if an assertion fails.
	t.Cleanup(func() { _, _ = Run(opts("down")) })

	// start → container running
	if handled, err := Run(opts("start")); !handled || err != nil {
		t.Fatalf("start: handled=%v err=%v", handled, err)
	}
	if !running(t, stack) {
		t.Fatal("expected a running container after start")
	}

	// ps → succeeds
	if handled, err := Run(opts("ps")); !handled || err != nil {
		t.Fatalf("ps: handled=%v err=%v", handled, err)
	}

	// restart → still running
	if handled, err := Run(opts("restart")); !handled || err != nil {
		t.Fatalf("restart: handled=%v err=%v", handled, err)
	}
	if !running(t, stack) {
		t.Fatal("expected running container after restart")
	}

	// stop → not running
	if handled, err := Run(opts("stop")); !handled || err != nil {
		t.Fatalf("stop: handled=%v err=%v", handled, err)
	}
	if running(t, stack) {
		t.Fatal("expected no running container after stop")
	}

	// down → container removed entirely
	if handled, err := Run(opts("down")); !handled || err != nil {
		t.Fatalf("down: handled=%v err=%v", handled, err)
	}
	if exists(t, stack) {
		t.Fatal("expected no containers after down")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func running(t *testing.T, project string) bool {
	t.Helper()
	out, _ := exec.Command("docker", "ps",
		"--filter", "label=com.docker.compose.project="+project,
		"--format", "{{.Names}}").Output()
	return strings.TrimSpace(string(out)) != ""
}

func exists(t *testing.T, project string) bool {
	t.Helper()
	out, _ := exec.Command("docker", "ps", "-a",
		"--filter", "label=com.docker.compose.project="+project,
		"--format", "{{.Names}}").Output()
	return strings.TrimSpace(string(out)) != ""
}
