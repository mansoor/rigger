package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/executor"
)

const cfgBody = `{
  "project": { "name": "app", "registry": "reg",
    "version": { "major": 1, "minor": 2, "patch": 3, "build": 4 } },
  "services": [ { "name": "backend", "build": { "template": "laravel", "context": "backend" } } ],
  "environments": {
    "stage": { "domain": "stage.app" },
    "prod":  { "domain": "app.com" }
  }
}`

// setup writes a workspace with config.json and a backend build context.
func setup(t *testing.T) string {
	t.Helper()
	wsDir := t.TempDir()
	wsRoot := filepath.Join(wsDir, "ws", "projects", "app")
	beCtx := filepath.Join(wsRoot, "envs", "prod", "backend")
	if err := os.MkdirAll(beCtx, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wsRoot, "config.json"), []byte(cfgBody), 0o644) //nolint:errcheck
	os.WriteFile(filepath.Join(beCtx, "Dockerfile"), []byte("FROM scratch"), 0o644) //nolint:errcheck
	return wsDir
}

// recorder is a fake executor.Executor that captures docker argv and the working
// dir of each call.
type recorder struct {
	calls [][]string
	dirs  []string
}

func (r *recorder) Docker(s executor.Spec) error {
	r.calls = append(r.calls, s.Args)
	r.dirs = append(r.dirs, s.Dir)
	return nil
}

func (r *recorder) DockerOutput(s executor.Spec) ([]byte, error) {
	r.calls = append(r.calls, s.Args)
	r.dirs = append(r.dirs, s.Dir)
	return nil, nil
}

func joined(call []string) string { return strings.Join(call, " ") }

func TestBuildBackendWithPushAndBump(t *testing.T) {
	wsDir := setup(t)
	rec := &recorder{}
	var out strings.Builder

	o := Options{
		WorkspacesDir: wsDir, Workspace: "ws", Project: "app", Command: "build", Env: "prod",
		Extra: []string{"backend", "--push", "--bump", "minor"},
		Stdout: &out, Exec: rec,
	}
	if handled, err := o.Run(); !handled || err != nil {
		t.Fatalf("Run = (%v, %v)", handled, err)
	}

	// Version bumped 1.2.3-build.4 → minor → 1.3.0-build.0 on disk.
	data, _ := os.ReadFile(filepath.Join(wsDir, "ws", "projects", "app", "config.json"))
	if !strings.Contains(string(data), `"minor": 3`) || !strings.Contains(string(data), `"build": 0`) {
		t.Errorf("version not bumped to 1.3.0-build.0:\n%s", data)
	}

	if len(rec.calls) != 2 {
		t.Fatalf("expected build + push (2 calls), got %d: %v", len(rec.calls), rec.calls)
	}
	build := joined(rec.calls[0])
	wantTag := "reg/app-backend:1.3.0-build.0-prod"
	if !strings.HasPrefix(build, "build ") {
		t.Errorf("first call not a build: %s", build)
	}
	if !strings.Contains(build, "-t "+wantTag) {
		t.Errorf("build missing tag %q: %s", wantTag, build)
	}
	if !strings.Contains(build, "--build-arg VERSION=1.3.0-build.0") {
		t.Errorf("build missing VERSION build-arg: %s", build)
	}
	if !strings.Contains(build, "--label service=backend") {
		t.Errorf("build missing service label: %s", build)
	}
	if push := joined(rec.calls[1]); push != "push "+wantTag {
		t.Errorf("push call = %q, want push %s", push, wantTag)
	}
}

// TestBuildRunsInContextDir locks in the contract the remote executor relies on:
// the build runs with the working dir set to the context dir and uses relative
// paths (`-f Dockerfile .`), so the remote executor can translate the dir to the
// host and build against the pushed context.
func TestBuildRunsInContextDir(t *testing.T) {
	wsDir := setup(t)
	rec := &recorder{}
	o := Options{
		WorkspacesDir: wsDir, Workspace: "ws", Project: "app", Command: "build", Env: "prod",
		Extra: []string{"backend"}, Stdout: &strings.Builder{}, Exec: rec,
	}
	if _, err := o.Run(); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 build call, got %d: %v", len(rec.calls), rec.calls)
	}
	build := joined(rec.calls[0])
	if !strings.Contains(build, "-f Dockerfile") || !strings.HasSuffix(build, " .") {
		t.Errorf("build should use relative -f Dockerfile and context '.': %s", build)
	}
	wantDir := filepath.Join(wsDir, "ws", "projects", "app", "envs", "prod", "backend")
	if rec.dirs[0] != wantDir {
		t.Errorf("build Dir = %q, want %q", rec.dirs[0], wantDir)
	}
}

func TestBuildAllSkipsDisabledFrontend(t *testing.T) {
	wsDir := setup(t)
	rec := &recorder{}
	o := Options{
		WorkspacesDir: wsDir, Workspace: "ws", Project: "app", Command: "build", Env: "prod",
		Extra: []string{"all"}, Stdout: &strings.Builder{}, Exec: rec,
	}
	if _, err := o.Run(); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("expected only backend build, got %d calls: %v", len(rec.calls), rec.calls)
	}
	if !strings.Contains(joined(rec.calls[0]), "service=backend") {
		t.Errorf("expected backend build, got: %v", rec.calls[0])
	}
}

func TestBuildFrontendDisabledErrors(t *testing.T) {
	wsDir := setup(t)
	o := Options{
		WorkspacesDir: wsDir, Workspace: "ws", Project: "app", Command: "build", Env: "prod",
		Extra: []string{"frontend"}, Stdout: &strings.Builder{}, Exec: &recorder{},
	}
	if _, err := o.Run(); err == nil {
		t.Error("expected error building disabled frontend")
	}
}

func TestPromoteDryRun(t *testing.T) {
	wsDir := setup(t)
	rec := &recorder{}
	var out strings.Builder
	o := Options{
		WorkspacesDir: wsDir, Workspace: "ws", Project: "app", Command: "promote", Env: "stage",
		Extra: []string{"prod", "--dry-run"}, Stdout: &out, Exec: rec,
		deploy: func(string) error { t.Fatal("deploy must not run in dry-run"); return nil },
	}
	if _, err := o.Run(); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("dry-run should not exec docker, got: %v", rec.calls)
	}
	s := out.String()
	for _, want := range []string{
		"[dry-run] docker pull reg/app-backend:1.2.3-build.4-stage",
		"[dry-run] docker tag reg/app-backend:1.2.3-build.4-stage reg/app-backend:1.2.3-build.4-prod",
		"[dry-run] docker push reg/app-backend:1.2.3-build.4-prod",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("dry-run output missing %q\n--- got ---\n%s", want, s)
		}
	}
}

func TestPromoteRealRetagsAndDeploys(t *testing.T) {
	wsDir := setup(t)
	rec := &recorder{}
	deployed := ""
	o := Options{
		WorkspacesDir: wsDir, Workspace: "ws", Project: "app", Command: "promote", Env: "stage",
		Extra: []string{"prod"}, Stdout: &strings.Builder{}, Exec: rec,
		deploy: func(env string) error { deployed = env; return nil },
	}
	if _, err := o.Run(); err != nil {
		t.Fatal(err)
	}
	// backend only (frontend disabled on prod): pull, tag, push.
	if len(rec.calls) != 3 {
		t.Fatalf("expected pull+tag+push (3), got %d: %v", len(rec.calls), rec.calls)
	}
	if got := joined(rec.calls[0]); got != "pull reg/app-backend:1.2.3-build.4-stage" {
		t.Errorf("pull = %q", got)
	}
	if got := joined(rec.calls[1]); got != "tag reg/app-backend:1.2.3-build.4-stage reg/app-backend:1.2.3-build.4-prod" {
		t.Errorf("tag = %q", got)
	}
	if got := joined(rec.calls[2]); got != "push reg/app-backend:1.2.3-build.4-prod" {
		t.Errorf("push = %q", got)
	}
	if deployed != "prod" {
		t.Errorf("deploy env = %q, want prod", deployed)
	}
}

func TestPromoteSameEnvErrors(t *testing.T) {
	wsDir := setup(t)
	o := Options{
		WorkspacesDir: wsDir, Workspace: "ws", Project: "app", Command: "promote", Env: "prod",
		Extra: []string{"prod"}, Stdout: &strings.Builder{},
		Exec: &recorder{}, deploy: func(string) error { return nil },
	}
	if _, err := o.Run(); err == nil {
		t.Error("expected error promoting env to itself")
	}
}
