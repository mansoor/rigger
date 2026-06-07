package api

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// createArchive should bundle config + env files + only the NEWEST per-env
// backup snapshot, excluding older snapshot history (Phase 11 .rwb bloat fix).
func TestArchiveExcludesOldSnapshots(t *testing.T) {
	root := t.TempDir()
	wsDir := filepath.Join(root, "myws")

	writeFile(t, filepath.Join(wsDir, "config.json"), `{"project":{"name":"myws"}}`)
	writeFile(t, filepath.Join(wsDir, "envs", "prod", ".env"), "A=1")
	// prod has two snapshots; only the newer (by name) should be kept.
	writeFile(t, filepath.Join(wsDir, "backups", "prod", "2026-01-01_00-00-00", "old.tar.gz"), strings.Repeat("x", 4096))
	writeFile(t, filepath.Join(wsDir, "backups", "prod", "2026-02-02_00-00-00", "new.tar.gz"), "new")
	// stage has one snapshot — kept.
	writeFile(t, filepath.Join(wsDir, "backups", "stage", "2026-03-03_00-00-00", "s.tar.gz"), "s")

	dest := filepath.Join(root, "out.rwb")
	if _, err := createArchive(wsDir, "myws", dest); err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	names := tarEntryNames(t, dest)
	has := func(sub string) bool {
		for _, n := range names {
			if strings.Contains(n, sub) {
				return true
			}
		}
		return false
	}

	if has("2026-01-01_00-00-00") {
		t.Errorf("old snapshot should be excluded; got %v", names)
	}
	if !has("myws/backups/prod/2026-02-02_00-00-00/new.tar.gz") {
		t.Errorf("newest prod snapshot should be included; got %v", names)
	}
	if !has("myws/backups/stage/2026-03-03_00-00-00/s.tar.gz") {
		t.Errorf("newest stage snapshot should be included; got %v", names)
	}
	if !has("myws/config.json") || !has("myws/envs/prod/.env") {
		t.Errorf("config + env files should be included; got %v", names)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tarEntryNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var out []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		out = append(out, filepath.ToSlash(h.Name))
	}
	return out
}
