package gitsync

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPushToBareRepo verifies Push seeds an empty bare remote with the scaffold commit.
func TestPushToBareRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()

	// A bare "remote" to push into.
	bare := filepath.Join(base, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "-q", bare).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v %s", err, out)
	}

	// A generated scaffold dir with one file.
	src := filepath.Join(base, "_scaffold")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o644)

	var log bytes.Buffer
	if err := Push(src, bare, "main", nil, &log); err != nil {
		t.Fatalf("Push: %v\nlog:\n%s", err, log.String())
	}

	// Clone the pushed branch explicitly (a bare repo's HEAD may default to master,
	// which wouldn't exist here) and confirm the file landed.
	clone := filepath.Join(base, "clone")
	if out, err := exec.Command("git", "clone", "-q", "--branch", "main", bare, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(clone, "main.go")); err != nil {
		t.Fatalf("pushed file missing in clone: %v", err)
	}
}
