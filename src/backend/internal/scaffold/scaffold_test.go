package scaffold

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateCopiesTree(t *testing.T) {
	templates := t.TempDir()
	// A fake bundled starter: templates/scaffold/go/{go.mod, cmd/main.go}
	src := filepath.Join(templates, "scaffold", "go")
	os.MkdirAll(filepath.Join(src, "cmd"), 0o755)
	os.WriteFile(filepath.Join(src, "go.mod"), []byte("module x\n"), 0o644)
	os.WriteFile(filepath.Join(src, "cmd", "main.go"), []byte("package main\n"), 0o644)

	dest := filepath.Join(t.TempDir(), "_scaffold")
	ok, err := Generate(templates, "go", dest)
	if err != nil || !ok {
		t.Fatalf("Generate go: ok=%v err=%v", ok, err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "go.mod")); err != nil || string(b) != "module x\n" {
		t.Fatalf("go.mod not copied: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(dest, "cmd", "main.go")); err != nil {
		t.Fatalf("nested file not copied: %v", err)
	}
}

func TestGenerateUnknownIsSkip(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "_scaffold")
	ok, err := Generate(t.TempDir(), "cobol", dest)
	if err != nil || ok {
		t.Fatalf("unknown framework should skip cleanly: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("dest should not be created for unknown framework")
	}
}

func TestInstructionsAndSupported(t *testing.T) {
	for _, id := range []string{"laravel", "nodejs", "react", "django", "go"} {
		if !Supported(id) {
			t.Errorf("%s should be supported", id)
		}
		if ins := Instructions(id); ins.Install == "" || ins.Run == "" {
			t.Errorf("%s missing install/run: %+v", id, ins)
		}
	}
	if Supported("rails") {
		t.Errorf("rails not in curated set yet")
	}
}

func TestTemplateID(t *testing.T) {
	services := []map[string]any{
		{"name": "app-nginx", "image": "nginx"},
		{"name": "app", "build": map[string]any{"template": "laravel", "context": "."}},
	}
	if got := TemplateID(services); got != "laravel" {
		t.Fatalf("TemplateID = %q, want laravel", got)
	}
	if got := TemplateID([]map[string]any{{"name": "x", "image": "nginx"}}); got != "" {
		t.Fatalf("no build service should yield empty, got %q", got)
	}
}
