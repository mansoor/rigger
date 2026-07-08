package api

import (
	"testing"

	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/settings"
)

func seedHost(t *testing.T, d *db.DB, name string) int64 {
	t.Helper()
	h, err := settings.CreateHost(d, name, "1.2.3.4", 22, "root", "enc", "", "global")
	if err != nil {
		t.Fatalf("seed host %s: %v", name, err)
	}
	return h.ID
}

func countRows(t *testing.T, d *db.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

// seedProject fills the source project alpha/web with one of each portable row.
func seedProject(t *testing.T, d *db.DB, hostID int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO pipelines (workspace, project, name, stages, enabled) VALUES ('alpha','web','deploy','[{"type":"deploy"}]',1)`); err != nil {
		t.Fatal(err)
	}
	var pid int64
	d.QueryRow(`SELECT id FROM pipelines WHERE workspace='alpha' AND project='web' AND name='deploy'`).Scan(&pid)
	d.Exec(`INSERT INTO pipeline_webhooks (pipeline_id, workspace, project, token_hash, secret, enabled) VALUES (?,?,?,?,?,?)`, pid, "alpha", "web", "hash123", "sek", 1)
	d.Exec(`INSERT INTO alert_rules (name, condition_type, threshold, ws_key, project, env, severity, cooldown_minutes, enabled, notify_channel_ids) VALUES ('cpu','cpu_pct',90,'alpha','web','prod','warning',15,1,'[]')`)
	d.Exec(`INSERT INTO custom_domains (workspace, project, env, domain, token, verified) VALUES ('alpha','web','prod','ex.com','tok',1)`)
	d.Exec(`INSERT INTO env_maintenance (workspace, project, env, enabled, title) VALUES ('alpha','web','prod',1,'down')`)
	d.Exec(`INSERT INTO workspace_host_envs (project, env, host_id) VALUES ('alpha_web','prod',?)`, hostID)
	d.Exec(`INSERT INTO project_build_hosts (project, host_id) VALUES ('alpha_web',?)`, hostID)
}

func TestProjectDBRoundTrip(t *testing.T) {
	dir1 := t.TempDir()
	d1, err := db.Open(dir1)
	if err != nil {
		t.Fatal(err)
	}
	h1 := &Handler{db: d1, workspacesDir: dir1}
	srcHost := seedHost(t, d1, "builder")
	seedProject(t, d1, srcHost)

	data, err := h1.exportProjectDB("alpha", "web")
	if err != nil {
		t.Fatal(err)
	}

	// Target server: fresh DB where "builder" has a DIFFERENT id (seed a filler first).
	dir2 := t.TempDir()
	d2, err := db.Open(dir2)
	if err != nil {
		t.Fatal(err)
	}
	h2 := &Handler{db: d2, workspacesDir: dir2}
	seedHost(t, d2, "filler")
	tgtHost := seedHost(t, d2, "builder")

	h2.importProjectDB("alpha", "web", data)

	// Pipeline restored + webhook re-linked to the NEW pipeline id.
	var npid int64
	if d2.QueryRow(`SELECT id FROM pipelines WHERE workspace='alpha' AND project='web' AND name='deploy'`).Scan(&npid); npid == 0 {
		t.Fatal("pipeline not restored")
	}
	if got := countRows(t, d2, `SELECT COUNT(*) FROM pipeline_webhooks WHERE pipeline_id=?`, npid); got != 1 {
		t.Fatalf("webhook not re-linked to new pipeline id: %d", got)
	}

	// Flat config tables restored.
	if got := countRows(t, d2, `SELECT COUNT(*) FROM alert_rules WHERE ws_key='alpha' AND project='web'`); got != 1 {
		t.Fatalf("alert_rules: %d", got)
	}
	if got := countRows(t, d2, `SELECT COUNT(*) FROM custom_domains WHERE workspace='alpha' AND project='web'`); got != 1 {
		t.Fatalf("custom_domains: %d", got)
	}
	if got := countRows(t, d2, `SELECT COUNT(*) FROM env_maintenance WHERE workspace='alpha' AND project='web'`); got != 1 {
		t.Fatalf("env_maintenance: %d", got)
	}

	// Host bindings remapped by NAME to the target's (different) builder id.
	var boundID, buildID int64
	d2.QueryRow(`SELECT host_id FROM workspace_host_envs WHERE project='alpha_web' AND env='prod'`).Scan(&boundID)
	d2.QueryRow(`SELECT host_id FROM project_build_hosts WHERE project='alpha_web'`).Scan(&buildID)
	if boundID != tgtHost {
		t.Fatalf("env host binding not remapped by name: got %d want %d", boundID, tgtHost)
	}
	if buildID != tgtHost {
		t.Fatalf("build host not remapped by name: got %d want %d", buildID, tgtHost)
	}
}

func TestProjectDBImportDropsUnknownHost(t *testing.T) {
	dir1 := t.TempDir()
	d1, _ := db.Open(dir1)
	h1 := &Handler{db: d1, workspacesDir: dir1}
	seedProject(t, d1, seedHost(t, d1, "builder"))
	data, _ := h1.exportProjectDB("alpha", "web")

	// Target has no host named "builder" — bindings drop, other rows still import.
	dir2 := t.TempDir()
	d2, _ := db.Open(dir2)
	h2 := &Handler{db: d2, workspacesDir: dir2}
	h2.importProjectDB("alpha", "web", data)

	if got := countRows(t, d2, `SELECT COUNT(*) FROM workspace_host_envs WHERE project='alpha_web'`); got != 0 {
		t.Fatalf("expected host binding dropped, got %d", got)
	}
	if got := countRows(t, d2, `SELECT COUNT(*) FROM project_build_hosts WHERE project='alpha_web'`); got != 0 {
		t.Fatalf("expected build host dropped, got %d", got)
	}
	if got := countRows(t, d2, `SELECT COUNT(*) FROM alert_rules WHERE ws_key='alpha' AND project='web'`); got != 1 {
		t.Fatalf("alert_rules should still import: %d", got)
	}
}
