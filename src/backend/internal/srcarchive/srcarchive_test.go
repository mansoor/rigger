package srcarchive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// zipFile writes a .zip with the given path→content entries (plus optional raw
// entry names with no content, for traversal/dir cases) and returns its path.
func zipFile(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "src.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A CodeCanyon-shaped zip: everything under one top-level dir, with a committed
// .env that must be dropped and a .env.example that must be kept.
func TestExtractZipSingleRootStripAndEnv(t *testing.T) {
	zp := zipFile(t, map[string]string{
		"app/composer.json":     `{"name":"x"}`,
		"app/.env":              "SECRET=leak",
		"app/.env.example":      "APP_KEY=",
		"app/public/index.php":  "<?php",
		"__MACOSX/app/._x":      "junk",
	})
	dest := t.TempDir()
	if err := Extract(zp, dest); err != nil {
		t.Fatal(err)
	}
	// Single root "app" stripped → files at dest root.
	if !exists(filepath.Join(dest, "composer.json")) || !exists(filepath.Join(dest, "public", "index.php")) {
		t.Errorf("single-root strip failed; dest=%s", dest)
	}
	if read(t, filepath.Join(dest, ".env.example")) != "APP_KEY=" {
		t.Errorf(".env.example should be kept")
	}
	if exists(filepath.Join(dest, ".env")) {
		t.Errorf("committed .env must be dropped")
	}
	if exists(filepath.Join(dest, "__MACOSX")) {
		t.Errorf("__MACOSX resource fork must be dropped")
	}
}

// Two top-level dirs → no strip; both trees present.
func TestExtractTarGzMultiRoot(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range []struct{ name, body string }{
		{"backend/artisan", "#!/usr/bin/env php"},
		{"frontend/package.json", `{"name":"f"}`},
	} {
		_ = tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(e.body))
	}
	tw.Close()
	gz.Close()
	p := filepath.Join(t.TempDir(), "src.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := Extract(p, dest); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(dest, "backend", "artisan")) || !exists(filepath.Join(dest, "frontend", "package.json")) {
		t.Errorf("multi-root extraction failed; dest=%s", dest)
	}
}

// Zip-slip: a "../../escape" entry must be neutralised (contained inside dest),
// never written outside it.
func TestExtractZipSlipContained(t *testing.T) {
	zp := zipFile(t, map[string]string{
		"../../escape.txt": "pwned",
		"safe.txt":         "ok",
	})
	parent := t.TempDir()
	dest := filepath.Join(parent, "dst")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Extract(zp, dest); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(parent, "escape.txt")) {
		t.Errorf("zip-slip escaped the dest dir")
	}
	// Single-root strip may promote; just assert nothing escaped and content stayed in dest.
	if !exists(filepath.Join(dest, "escape.txt")) && !exists(filepath.Join(dest, "safe.txt")) {
		t.Errorf("expected entries to land inside dest")
	}
}

// Symlink entries in a tar must be skipped (escape + secret-exfil vector).
func TestExtractTarSymlinkSkipped(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "evil", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777})
	body := "real"
	_ = tw.WriteHeader(&tar.Header{Name: "real.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	tw.Close()
	p := filepath.Join(t.TempDir(), "src.tar")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := Extract(p, dest); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(dest, "evil")); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("symlink entry should not be created")
	}
	if !exists(filepath.Join(dest, "real.txt")) {
		t.Errorf("regular file alongside a symlink should still extract")
	}
}

// A decompression bomb is caught by the byte cap (counts bytes written, not declared).
func TestExtractByteCapEnforced(t *testing.T) {
	old := maxTotalBytes
	defer func() { maxTotalBytes = old }()
	maxTotalBytes = 8
	zp := zipFile(t, map[string]string{"big.txt": "this is definitely more than eight bytes"})
	if err := Extract(zp, t.TempDir()); err == nil {
		t.Errorf("expected byte-cap error, got nil")
	}
}
