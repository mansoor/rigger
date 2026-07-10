package detect

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectUnrecognizedFallsBackToNixpacks: a repo with no compose, no Dockerfile,
// and no manifest Rigger templates → a single web-routed "app" build service that
// builds via Nixpacks (auto-detect), rather than the old no-services dead end.
func TestDetectUnrecognizedFallsBackToNixpacks(t *testing.T) {
	dir := t.TempDir()
	// Something Rigger doesn't template (e.g. a Rust/Elixir/etc. project shape) — here
	// just a README, so identify() can't match a supported framework.
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# my app\n"), 0o644) //nolint:errcheck

	// DetectRepo is the scan entrypoint; the fallback is a last resort after nested
	// discovery, so exercise it (not bare Detect).
	d := DetectRepo(dir, "", nil)
	var app *Service
	for i := range d.Services {
		if d.Services[i].Build != nil {
			app = &d.Services[i]
			break
		}
	}
	if app == nil {
		t.Fatalf("expected a build service, got services=%+v", d.Services)
	}
	if app.Build.Method != "nixpacks" {
		t.Errorf("build method = %q, want nixpacks", app.Build.Method)
	}
	if app.Build.Template != "" {
		t.Errorf("nixpacks service should have no Dockerfile template, got %q", app.Build.Template)
	}
	if !app.WebRouted {
		t.Errorf("fallback app should be web-routed")
	}
}

func TestDetectRecognizedIsNotNixpacks(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.23\n"), 0o644) //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main(){}\n"), 0o644) //nolint:errcheck

	d := Detect(dir, nil)
	for _, s := range d.Services {
		if s.Build != nil && s.Build.Method == "nixpacks" {
			t.Fatalf("recognized Go repo should scaffold a Dockerfile, not default to nixpacks: %+v", s.Build)
		}
	}
}
