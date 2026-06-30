package workspace

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mansoor/rigger/ui/internal/wsconfig"
)

func TestWriteSeedFiles(t *testing.T) {
	dir := t.TempDir()
	// A file the user has already edited must be preserved (write-if-absent).
	if err := os.WriteFile(filepath.Join(dir, "prometheus.yml"), []byte("USER EDIT"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &wsconfig.Config{Project: wsconfig.Project{SeedFiles: map[string]wsconfig.MultilineString{
		"prometheus.yml":  "TEMPLATE DEFAULT", // exists → must NOT clobber
		"conf.d/app.yaml": "key: value",       // nested → dirs created
		"/etc/evil":       "x",                // absolute → rejected
		"../escape.txt":   "x",                // parent-escape → rejected
	}}}

	if err := writeSeedFiles(cfg, dir, io.Discard); err != nil {
		t.Fatal(err)
	}

	// Existing file preserved.
	if b, _ := os.ReadFile(filepath.Join(dir, "prometheus.yml")); string(b) != "USER EDIT" {
		t.Errorf("existing seed file clobbered: got %q", b)
	}
	// Nested file written with its dir created.
	if b, err := os.ReadFile(filepath.Join(dir, "conf.d", "app.yaml")); err != nil || string(b) != "key: value" {
		t.Errorf("nested seed file: got %q err=%v", b, err)
	}
	// Unsafe paths never escape the env dir.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); !os.IsNotExist(err) {
		t.Errorf("parent-escape seed file must not be written")
	}
	if _, err := os.Stat("/etc/evil"); err == nil {
		t.Errorf("absolute-path seed file must not be written")
	}
}

// LoadTemplate surfaces a template's `files` map so the create flow can persist it to
// config.json (project.seed_files) for bootstrap to materialize.
func TestLoadTemplateFiles(t *testing.T) {
	dir := t.TempDir()
	stacks := filepath.Join(dir, "stacks")
	if err := os.MkdirAll(stacks, 0o755); err != nil {
		t.Fatal(err)
	}
	// `string` body and `line-array` body (joined with \n + trailing newline) both supported.
	tmpl := `{"name":"demo","images":[{"name":"app","image":"nginx"}],
	  "default_env_vars":{"FOO":"bar"},
	  "files":{
	    "one.yml":"global:\n  scrape_interval: 15s\n",
	    "two.conf":["server {","  listen 80;","}"]
	  }}`
	if err := os.WriteFile(filepath.Join(stacks, "demo.json"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	_, envs, files, err := LoadTemplate(dir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if envs["FOO"] != "bar" {
		t.Errorf("default envs not parsed: %v", envs)
	}
	if got := files["one.yml"]; got != "global:\n  scrape_interval: 15s\n" {
		t.Errorf("string file body: %q", got)
	}
	if got := files["two.conf"]; got != "server {\n  listen 80;\n}\n" {
		t.Errorf("line-array file body not joined with newlines: %q", got)
	}
}

// A fresh env dir (no prior file) gets the template default written.
func TestWriteSeedFilesFresh(t *testing.T) {
	dir := t.TempDir()
	cfg := &wsconfig.Config{Project: wsconfig.Project{SeedFiles: map[string]wsconfig.MultilineString{
		"prometheus.yml": "global:\n  scrape_interval: 15s\n",
	}}}
	if err := writeSeedFiles(cfg, dir, io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "prometheus.yml")); err != nil || string(b) == "" {
		t.Errorf("fresh seed file not written: err=%v", err)
	}
}
