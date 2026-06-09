package db

import "testing"

func TestDropUsernameUnique(t *testing.T) {
	dir := t.TempDir()
	// Simulate a legacy DB: create it, then re-create the users table WITH the old
	// UNIQUE constraint to mimic an already-deployed schema, then re-run migrate.
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Force the legacy shape back on, as if this DB predated the fix.
	d.Exec(`DROP TABLE users`)
	d.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE, password TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'admin', created_at DATETIME DEFAULT CURRENT_TIMESTAMP, last_login_at DATETIME, email TEXT NOT NULL DEFAULT '', phone TEXT NOT NULL DEFAULT '', email_verified INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'active', appearance_prefs TEXT NOT NULL DEFAULT '')`)
	d.Exec(`INSERT INTO users (username, password, email) VALUES ('Mansoor','x','a@x.com')`)
	d.Close()

	// Re-open: migrate() must rebuild and drop the UNIQUE.
	d2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen/migrate: %v", err)
	}
	defer d2.Close()

	// Same display name, different email — must now be allowed.
	if _, err := d2.Exec(`INSERT INTO users (username, password, email) VALUES ('Mansoor','y','b@x.com')`); err != nil {
		t.Fatalf("duplicate username still rejected: %v", err)
	}
	// Duplicate email must still be rejected (idx_users_email survives the rebuild).
	if _, err := d2.Exec(`INSERT INTO users (username, password, email) VALUES ('Other','z','a@x.com')`); err == nil {
		t.Fatal("duplicate email was wrongly allowed")
	}
	// Original row preserved with its id.
	var n int
	d2.QueryRow(`SELECT COUNT(*) FROM users WHERE email='a@x.com'`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected original row preserved, got %d", n)
	}
}
