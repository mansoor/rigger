package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envFilePath(wsDir, env string) string {
	return filepath.Join(wsDir, "ws", "projects", "app", "envs", env, ".env")
}

func seedEnv(t *testing.T, wsDir, env, body string) {
	t.Helper()
	if err := os.WriteFile(envFilePath(wsDir, env), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func envKey(t *testing.T, wsDir, env, key string) string {
	t.Helper()
	return parseDotenv(envFilePath(wsDir, env))[key]
}

// runBuild runs the build command with the fake executor, returning stdout.
func runBuild(t *testing.T, wsDir string, extra ...string) string {
	t.Helper()
	var out strings.Builder
	o := Options{
		WorkspacesDir: wsDir, Workspace: "ws", Project: "app", Command: "build", Env: "prod",
		Extra: extra, Stdout: &out, Exec: &recorder{},
	}
	if _, err := o.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}
	return out.String()
}

// Tracking pointer (== old version tag) advances to the new version on bump.
func TestAdvanceTrackingOnBump(t *testing.T) {
	wsDir := setup(t)
	seedEnv(t, wsDir, "prod", "BACKEND_IMAGE=reg/app-backend:1.2.3-build.4-prod\nOTHER=x\n")
	runBuild(t, wsDir, "backend", "--bump", "build")

	if got := envKey(t, wsDir, "prod", "BACKEND_IMAGE"); got != "reg/app-backend:1.2.3-build.5-prod" {
		t.Errorf("tracking pointer not advanced: %q", got)
	}
	if got := envKey(t, wsDir, "prod", "OTHER"); got != "x" {
		t.Errorf("unrelated key not preserved: %q", got)
	}
	compose, _ := os.ReadFile(filepath.Join(wsDir, "ws", "projects", "app", "envs", "prod", "docker-compose.yml"))
	if !strings.Contains(string(compose), "1.2.3-build.5-prod") {
		t.Errorf("compose not regenerated with new tag:\n%s", compose)
	}
}

// An absent pointer is filled with the new tag.
func TestAdvanceEmptyPointerFilled(t *testing.T) {
	wsDir := setup(t)
	seedEnv(t, wsDir, "prod", "OTHER=x\n")
	runBuild(t, wsDir, "backend", "--bump", "build")
	if got := envKey(t, wsDir, "prod", "BACKEND_IMAGE"); got != "reg/app-backend:1.2.3-build.5-prod" {
		t.Errorf("empty pointer not filled: %q", got)
	}
}

// A pinned pointer (different/older tag) is left untouched and reported.
func TestAdvancePinnedSkipped(t *testing.T) {
	wsDir := setup(t)
	seedEnv(t, wsDir, "prod", "BACKEND_IMAGE=reg/app-backend:0.9.0-build.1-prod\n")
	out := runBuild(t, wsDir, "backend", "--bump", "build")
	if got := envKey(t, wsDir, "prod", "BACKEND_IMAGE"); got != "reg/app-backend:0.9.0-build.1-prod" {
		t.Errorf("pinned pointer was changed: %q", got)
	}
	if !strings.Contains(out, "Left pinned") {
		t.Errorf("pinned skip not reported:\n%s", out)
	}
}

// A pointer whose tag's image no longer exists (a seed/stale pointer left behind
// when failed builds bumped the version past it) is re-synced to the new tag —
// NOT treated as a pin — so the next deploy doesn't chase a missing image.
func TestAdvanceStaleMissingImageResynced(t *testing.T) {
	wsDir := setup(t)
	stale := "reg/app-backend:1.2.3-build.0-prod"
	seedEnv(t, wsDir, "prod", "BACKEND_IMAGE="+stale+"\n")
	var out strings.Builder
	o := Options{
		WorkspacesDir: wsDir, Workspace: "ws", Project: "app", Command: "build", Env: "prod",
		Extra: []string{"backend", "--bump", "build"}, Stdout: &out,
		Exec: &recorder{missingImages: map[string]bool{stale: true}},
	}
	if _, err := o.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := envKey(t, wsDir, "prod", "BACKEND_IMAGE"); got != "reg/app-backend:1.2.3-build.5-prod" {
		t.Errorf("stale missing-image pointer not resynced: %q", got)
	}
}

// Build without a bump is a no-op for a tracking pointer.
func TestAdvanceNoBumpNoop(t *testing.T) {
	wsDir := setup(t)
	seedEnv(t, wsDir, "prod", "BACKEND_IMAGE=reg/app-backend:1.2.3-build.4-prod\n")
	runBuild(t, wsDir, "backend")
	if got := envKey(t, wsDir, "prod", "BACKEND_IMAGE"); got != "reg/app-backend:1.2.3-build.4-prod" {
		t.Errorf("no-bump build changed the pointer: %q", got)
	}
}
