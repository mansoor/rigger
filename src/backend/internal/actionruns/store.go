// Package actionruns persists the history of workspace action runs (the "Action
// output" panel): one row per run with its captured output and result, kept per
// workspace and pruned to a bounded number of recent runs.
package actionruns

import (
	"github.com/mansoor/rigger/ui/internal/db"
)

const (
	maxOutputBytes   = 64 * 1024 // cap stored output; keep the tail (errors land at the end)
	keepPerWorkspace = 200       // recent runs retained per workspace
)

// Run is one recorded action execution.
type Run struct {
	ID         int64  `json:"id"`
	Workspace  string `json:"workspace"`
	Env        string `json:"env"`
	Command    string `json:"command"`
	Extra      string `json:"extra"`
	Username   string `json:"username"`
	Status     string `json:"status"` // ok | fail
	Output     string `json:"output"`
	StartedAt  int64  `json:"started_at"`  // epoch ms
	FinishedAt int64  `json:"finished_at"` // epoch ms
}

// Record inserts a run and prunes the workspace's history to keepPerWorkspace.
func Record(d *db.DB, r Run) error {
	if r.Status != "ok" && r.Status != "fail" {
		r.Status = "ok"
	}
	out := r.Output
	if len(out) > maxOutputBytes {
		out = "…(output truncated)…\n" + out[len(out)-maxOutputBytes:]
	}
	if _, err := d.Exec(
		`INSERT INTO action_runs (project, env, command, extra, username, status, output, started_at, finished_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		r.Workspace, r.Env, r.Command, r.Extra, r.Username, r.Status, out, r.StartedAt, r.FinishedAt,
	); err != nil {
		return err
	}
	// Prune: keep only the newest keepPerWorkspace rows for this workspace.
	d.Exec( //nolint:errcheck
		`DELETE FROM action_runs WHERE project=? AND id NOT IN
		   (SELECT id FROM action_runs WHERE project=? ORDER BY id DESC LIMIT ?)`,
		r.Workspace, r.Workspace, keepPerWorkspace,
	)
	return nil
}

// List returns up to limit most-recent runs for a workspace, newest first.
func List(d *db.DB, workspace string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.Query(
		`SELECT id, project, env, command, extra, username, status, output, started_at, finished_at
		   FROM action_runs WHERE project=? ORDER BY id DESC LIMIT ?`,
		workspace, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Workspace, &r.Env, &r.Command, &r.Extra, &r.Username,
			&r.Status, &r.Output, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Clear deletes all recorded runs for a workspace.
func Clear(d *db.DB, workspace string) error {
	_, err := d.Exec(`DELETE FROM action_runs WHERE project=?`, workspace)
	return err
}
