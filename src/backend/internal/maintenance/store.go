// Package maintenance implements per-environment maintenance mode: an "under
// maintenance" page served to an env's public host(s) via a Traefik file-provider
// fragment. Because the fragment lives in Traefik's dynamic config (not the env's
// docker labels), it overrides the app's routers when running AND keeps serving the
// page while the env's stack is stopped. State is persisted in env_maintenance.
package maintenance

import (
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
)

// Record is one env's maintenance state. Project is the project KEY (the path
// segment), so the scheduler can read config.json + custom domains directly.
type Record struct {
	Workspace   string `json:"workspace"`
	Project     string `json:"project"`
	Env         string `json:"env"`
	Enabled     bool   `json:"enabled"`      // ad-hoc "maintenance now"
	WindowStart int64  `json:"window_start"` // unix seconds; 0 = no window
	WindowEnd   int64  `json:"window_end"`
	Title       string `json:"title"`
	Message     string `json:"message"`
	RetryAfter  int64  `json:"retry_after"` // seconds for the Retry-After header (0 = default)
	UpdatedAt   int64  `json:"updated_at"`
	UpdatedBy   string `json:"updated_by"`
}

// ActiveAt reports whether maintenance is effectively ON at time t: the ad-hoc
// toggle, or inside the scheduled window.
func (r Record) ActiveAt(t time.Time) bool {
	if r.Enabled {
		return true
	}
	if r.WindowStart > 0 && r.WindowEnd > 0 {
		now := t.Unix()
		return now >= r.WindowStart && now < r.WindowEnd
	}
	return false
}

// Scheduled reports whether a (future or current) window is set.
func (r Record) Scheduled() bool { return r.WindowStart > 0 && r.WindowEnd > 0 }

// Store persists per-env maintenance state in SQLite.
type Store struct{ DB *db.DB }

func NewStore(d *db.DB) *Store { return &Store{DB: d} }

const cols = `workspace, project, env, enabled, window_start, window_end, title, message, retry_after, updated_at, updated_by`

func scan(s interface{ Scan(...any) error }) (Record, error) {
	var r Record
	var enabled int
	err := s.Scan(&r.Workspace, &r.Project, &r.Env, &enabled, &r.WindowStart, &r.WindowEnd,
		&r.Title, &r.Message, &r.RetryAfter, &r.UpdatedAt, &r.UpdatedBy)
	r.Enabled = enabled != 0
	return r, err
}

// Get returns the record for an env (found=false when absent).
func (s *Store) Get(ws, project, env string) (Record, bool, error) {
	row := s.DB.QueryRow(`SELECT `+cols+` FROM env_maintenance WHERE workspace=? AND project=? AND env=?`, ws, project, env)
	r, err := scan(row)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return Record{Workspace: ws, Project: project, Env: env}, false, nil
		}
		return Record{}, false, err
	}
	return r, true, nil
}

// Upsert writes the full record.
func (s *Store) Upsert(r Record) error {
	en := 0
	if r.Enabled {
		en = 1
	}
	_, err := s.DB.Exec(`
		INSERT INTO env_maintenance (`+cols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(workspace, project, env) DO UPDATE SET
			enabled=excluded.enabled, window_start=excluded.window_start, window_end=excluded.window_end,
			title=excluded.title, message=excluded.message, retry_after=excluded.retry_after,
			updated_at=excluded.updated_at, updated_by=excluded.updated_by`,
		r.Workspace, r.Project, r.Env, en, r.WindowStart, r.WindowEnd,
		r.Title, r.Message, r.RetryAfter, r.UpdatedAt, r.UpdatedBy)
	return err
}

// List returns every maintenance row (the scheduler walks all of them).
func (s *Store) List() ([]Record, error) {
	rows, err := s.DB.Query(`SELECT ` + cols + ` FROM env_maintenance ORDER BY workspace, project, env`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete drops an env's row (e.g. when the env/project is removed).
func (s *Store) Delete(ws, project, env string) error {
	_, err := s.DB.Exec(`DELETE FROM env_maintenance WHERE workspace=? AND project=? AND env=?`, ws, project, env)
	return err
}
