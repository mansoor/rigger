package detect

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectSeedsLaravelAppKey(t *testing.T) {
	dir := t.TempDir()
	// A minimal Laravel repo: artisan console + composer requiring laravel, with a
	// blank APP_KEY in .env.example (Laravel's default).
	os.WriteFile(filepath.Join(dir, "artisan"), []byte("#!/usr/bin/env php\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"require":{"laravel/framework":"^11.0"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, ".env.example"), []byte("APP_NAME=Test\nAPP_KEY=\n"), 0o644)

	d := Detect(dir, nil)
	key := d.EnvVars["APP_KEY"]
	if !strings.HasPrefix(key, "base64:") {
		t.Fatalf("expected a generated base64 APP_KEY, got %q", key)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(key, "base64:"))
	if err != nil || len(raw) != 32 {
		t.Fatalf("APP_KEY must decode to 32 bytes: err=%v len=%d", err, len(raw))
	}
}

func TestDetectKeepsExistingAppKey(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "artisan"), []byte("#!/usr/bin/env php\n"), 0o644)
	real := "base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	// parseDotenv reads .env.example/.env.sample — a real key there must be kept.
	os.WriteFile(filepath.Join(dir, ".env.example"), []byte("APP_KEY="+real+"\n"), 0o644)

	d := Detect(dir, nil)
	if d.EnvVars["APP_KEY"] != real {
		t.Fatalf("existing APP_KEY should be preserved, got %q", d.EnvVars["APP_KEY"])
	}
}

func TestDetectSkipsAppKeyForNonLaravel(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o644)
	d := Detect(dir, nil)
	if _, ok := d.EnvVars["APP_KEY"]; ok {
		t.Fatalf("non-Laravel repo should not get an APP_KEY, got %q", d.EnvVars["APP_KEY"])
	}
}
