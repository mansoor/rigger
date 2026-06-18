package apikey

import (
	"encoding/json"

	"github.com/mansoor/rigger/ui/internal/db"
)

// Create inserts a new key. k.Token/KeyPrefix/Scopes/etc. must already be set by the
// caller (which generated the raw key). Returns the new row id. The (workspace,project)
// grants in k.Projects are written only when ProjectAccess=="specific".
func Create(d *db.DB, hash string, k Key) (int64, error) {
	scopes, _ := json.Marshal(k.Scopes)
	res, err := d.Exec(
		`INSERT INTO api_keys (name, workspace, key_hash, key_prefix, scopes, project_access, rate_limit, enabled, created_by, created_at, expires_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		k.Name, k.Workspace, hash, k.KeyPrefix, string(scopes), k.ProjectAccess, k.RateLimit,
		boolToInt(k.Enabled), k.CreatedBy, k.CreatedAt, k.ExpiresAt,
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if k.ProjectAccess == "specific" {
		for _, p := range k.Projects {
			d.Exec(`INSERT OR IGNORE INTO api_key_projects (key_id, workspace, project) VALUES (?,?,?)`, id, p.Workspace, p.Project) //nolint:errcheck
		}
	}
	return id, nil
}

// List returns ALL keys (global + every workspace's) newest first — the admin view.
func List(d *db.DB) ([]Key, error) { return query(d, "") }

// ListForWorkspace returns only the keys confined to one workspace — the workspace-admin
// view (never the global admin keys).
func ListForWorkspace(d *db.DB, ws string) ([]Key, error) { return query(d, ws) }

// query lists keys, optionally filtered to a workspace ("" = all). Never includes the
// raw token (it isn't stored).
func query(d *db.DB, ws string) ([]Key, error) {
	q := `SELECT id, name, workspace, key_prefix, scopes, project_access, rate_limit, enabled, created_by, created_at, last_used_at, expires_at
		 FROM api_keys`
	args := []any{}
	if ws != "" {
		q += ` WHERE workspace = ?`
		args = append(args, ws)
	}
	q += ` ORDER BY id DESC`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []Key
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, *k)
	}
	for i := range keys {
		if keys[i].ProjectAccess == "specific" {
			keys[i].Projects, _ = listProjects(d, keys[i].ID)
		}
	}
	if keys == nil {
		keys = []Key{}
	}
	return keys, nil
}

// Get loads one key by id (for ownership checks). Returns ErrNotFound if absent.
func Get(d *db.DB, id int64) (*Key, error) {
	row := d.QueryRow(
		`SELECT id, name, workspace, key_prefix, scopes, project_access, rate_limit, enabled, created_by, created_at, last_used_at, expires_at
		 FROM api_keys WHERE id = ?`, id,
	)
	k, err := scanKey(row)
	if err != nil {
		return nil, ErrNotFound
	}
	return k, nil
}

// GetByHash resolves a presented (hashed) key for the auth middleware. Returns
// ErrNotFound if no row matches. Loads specific-project grants so Authorize can run.
func GetByHash(d *db.DB, hash string) (*Key, error) {
	row := d.QueryRow(
		`SELECT id, name, workspace, key_prefix, scopes, project_access, rate_limit, enabled, created_by, created_at, last_used_at, expires_at
		 FROM api_keys WHERE key_hash = ?`, hash,
	)
	k, err := scanKey(row)
	if err != nil {
		return nil, ErrNotFound
	}
	if k.ProjectAccess == "specific" {
		k.Projects, _ = listProjects(d, k.ID)
	}
	return k, nil
}

// SetEnabled flips a key's enabled flag (revoke/restore without deleting history).
func SetEnabled(d *db.DB, id int64, enabled bool) error {
	_, err := d.Exec(`UPDATE api_keys SET enabled = ? WHERE id = ?`, boolToInt(enabled), id)
	return err
}

// Delete removes a key and its project grants.
func Delete(d *db.DB, id int64) error {
	d.Exec(`DELETE FROM api_key_projects WHERE key_id = ?`, id) //nolint:errcheck
	_, err := d.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	return err
}

// TouchLastUsed records the most recent successful auth (best-effort, epoch seconds).
func TouchLastUsed(d *db.DB, id, nowUnix int64) {
	d.Exec(`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, nowUnix, id) //nolint:errcheck
}

func listProjects(d *db.DB, keyID int64) ([]ProjectRef, error) {
	rows, err := d.Query(`SELECT workspace, project FROM api_key_projects WHERE key_id = ? ORDER BY workspace, project`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectRef
	for rows.Next() {
		var p ProjectRef
		if err := rows.Scan(&p.Workspace, &p.Project); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// scanner abstracts *sql.Row and *sql.Rows so scanKey serves both Get and List.
type scanner interface {
	Scan(dest ...any) error
}

func scanKey(s scanner) (*Key, error) {
	var k Key
	var scopes string
	var enabled int
	if err := s.Scan(&k.ID, &k.Name, &k.Workspace, &k.KeyPrefix, &scopes, &k.ProjectAccess, &k.RateLimit,
		&enabled, &k.CreatedBy, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt); err != nil {
		return nil, err
	}
	k.Enabled = enabled != 0
	if scopes != "" {
		json.Unmarshal([]byte(scopes), &k.Scopes) //nolint:errcheck
	}
	if k.Scopes == nil {
		k.Scopes = []string{}
	}
	return &k, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
