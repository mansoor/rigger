package deployhistory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

func TestRecordListPrune(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	for i := 0; i < keepPerEnv+5; i++ {
		if err := Record(d, Entry{
			Workspace: "mcl", Project: "web", Env: "dev", Ptype: "custom",
			Images: map[string]string{"backend": "img:v" + string(rune('a'+i%26))}, Version: "1.0.0",
			Username: "me", CreatedAt: int64(1000 + i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A different env must not be pruned by the first env's writes.
	Record(d, Entry{Workspace: "mcl", Project: "web", Env: "prod", Ptype: "custom", CreatedAt: 1}) //nolint:errcheck

	list, err := List(d, "mcl", "web", "dev", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != keepPerEnv {
		t.Fatalf("expected prune to %d, got %d", keepPerEnv, len(list))
	}
	if list[0].CreatedAt != int64(1000+keepPerEnv+4) {
		t.Fatalf("expected newest first, got %d", list[0].CreatedAt)
	}
	if prod, _ := List(d, "mcl", "web", "prod", 100); len(prod) != 1 {
		t.Fatalf("expected prod env untouched, got %d", len(prod))
	}
}

func writeConfig(t *testing.T, root, ws, proj, body string) {
	t.Helper()
	if err := os.MkdirAll(wspath.ProjectDir(root, ws, proj), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wspath.ConfigPath(root, ws, proj), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveCustom(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mcl", "web", `{
	  "project": {"name":"web","registry":"ghcr.io/mw","resource_prefix":"mcl_web","version":{"major":1,"minor":2,"patch":3,"build":4}},
	  "services": [{"name":"backend","build":{}},{"name":"frontend","build":{}}],
	  "environments": {"dev": {}}
	}`)
	// .env pins a custom backend image (override) but not the frontend.
	envDir := wspath.EnvDir(root, "mcl", "web", "dev")
	os.MkdirAll(envDir, 0o755) //nolint:errcheck
	os.WriteFile(filepath.Join(envDir, ".env"), []byte("# c\nBACKEND_IMAGE=ghcr.io/mw/mcl_web-backend:0.9.0-build.1-dev\nOTHER=x\n"), 0o644) //nolint:errcheck

	e := Resolve(root, "mcl", "web", "dev")
	if e.Ptype != "custom" || e.Version != "1.2.3-build.4" {
		t.Fatalf("unexpected: %+v", e)
	}
	if e.Images["backend"] != "ghcr.io/mw/mcl_web-backend:0.9.0-build.1-dev" {
		t.Fatalf("backend override not used: %q", e.Images["backend"])
	}
	if e.Images["frontend"] != "ghcr.io/mw/mcl_web-frontend:1.2.3-build.4-dev" {
		t.Fatalf("frontend should be version-derived: %q", e.Images["frontend"])
	}
}

func TestResolveImage(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mcl", "site", `{
	  "project": {"name":"site","type":"image"},
	  "environments": {"prod": {}}
	}`)
	e := Resolve(root, "mcl", "site", "prod")
	if e.Ptype != "image" {
		t.Fatalf("expected image ptype, got %q", e.Ptype)
	}
	if len(e.Images) != 0 {
		t.Fatalf("expected no image refs for image stack, got %v", e.Images)
	}
}
