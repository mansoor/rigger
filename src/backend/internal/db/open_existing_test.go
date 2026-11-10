package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// A recovery command pointed at the wrong DATA_DIR must fail, not create an
// empty database and report success while changing nothing.
func TestOpenExistingNeverCreatesADatabase(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenExisting(dir); err == nil {
		t.Fatal("opening a directory with no database succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "rigger.db")); !os.IsNotExist(err) {
		t.Errorf("OpenExisting created %s", filepath.Join(dir, "rigger.db"))
	}
}

// The server owns migrations; a console command running beside it must leave the
// schema exactly as it found it.
func TestOpenExistingDoesNotMigrate(t *testing.T) {
	dir := t.TempDir()
	raw, err := sql.Open("sqlite", filepath.Join(dir, "rigger.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE marker (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	d, err := OpenExisting(dir)
	if err != nil {
		t.Fatalf("OpenExisting: %v", err)
	}
	defer d.Close()
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='users'`).Scan(&n) //nolint:errcheck
	if n != 0 {
		t.Error("OpenExisting ran migrations (a users table appeared)")
	}
	// It waits out the server's brief write locks instead of failing on them.
	var ms int
	d.QueryRow(`PRAGMA busy_timeout`).Scan(&ms) //nolint:errcheck
	if ms != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", ms)
	}
}

func TestOpenExistingRejectsADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "rigger.db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(dir); err == nil {
		t.Error("a directory named rigger.db was accepted as a database")
	}
}
