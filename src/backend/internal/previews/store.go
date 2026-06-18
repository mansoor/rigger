package previews

import (
	"database/sql"
	"errors"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
)

// Preview status lifecycle. A preview moves creating → running, redeploys via
// updating, and ends at torn_down (or failed on an error). The reaper and the
// concurrency cap both treat anything other than torn_down as "active".
const (
	StatusCreating = "creating"
	StatusRunning  = "running"
	StatusUpdating = "updating"
	StatusFailed   = "failed"
	StatusTornDown = "torn_down"
)

// PreviewEnv is one PR preview environment (the pr{n} env cloned for a PR).
// CreatedAt / LastDeployedAt / ExpiresAt are epoch seconds; ExpiresAt 0 means no
// TTL (lives until the PR closes). LastRunID links the deploying pipeline run
// (0 = none) for log linking.
type PreviewEnv struct {
	ID             int64  `json:"id"`
	Workspace      string `json:"workspace"`
	Project        string `json:"project"`
	PRNumber       int    `json:"pr_number"`
	Provider       string `json:"provider"`
	Branch         string `json:"branch"`
	HeadSHA        string `json:"head_sha"`
	EnvKey         string `json:"env_key"`
	URL            string `json:"url"`
	Status         string `json:"status"`
	LastRunID      int64  `json:"last_run_id"`
	CreatedAt      int64  `json:"created_at"`
	LastDeployedAt int64  `json:"last_deployed_at"`
	ExpiresAt      int64  `json:"expires_at"`
}

const previewCols = `id, workspace, project, pr_number, provider, branch, head_sha,
	env_key, url, status, last_run_id, created_at, last_deployed_at, expires_at`

// Create inserts a new preview row. CreatedAt defaults to now when zero; Status
// defaults to creating. Returns the stored row (with its assigned id).
func Create(d *db.DB, p PreviewEnv) (*PreviewEnv, error) {
	if p.CreatedAt == 0 {
		p.CreatedAt = time.Now().Unix()
	}
	if p.Status == "" {
		p.Status = StatusCreating
	}
	if p.Provider == "" {
		p.Provider = "github"
	}
	res, err := d.Exec(
		`INSERT INTO preview_environments
		   (workspace, project, pr_number, provider, branch, head_sha, env_key, url,
		    status, last_run_id, created_at, last_deployed_at, expires_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.Workspace, p.Project, p.PRNumber, p.Provider, p.Branch, p.HeadSHA, p.EnvKey,
		p.URL, p.Status, p.LastRunID, p.CreatedAt, p.LastDeployedAt, p.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return Get(d, id)
}

// Get returns a preview by id.
func Get(d *db.DB, id int64) (*PreviewEnv, error) {
	return scanPreview(d.QueryRow(`SELECT `+previewCols+` FROM preview_environments WHERE id=?`, id))
}

// GetByPR returns the preview for a (workspace, project, PR) — or (nil, nil) when
// none exists, so callers can branch create-vs-update without an error path.
func GetByPR(d *db.DB, workspace, project string, prNumber int) (*PreviewEnv, error) {
	p, err := scanPreview(d.QueryRow(
		`SELECT `+previewCols+` FROM preview_environments
		   WHERE workspace=? AND project=? AND pr_number=?`, workspace, project, prNumber))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// ListForProject returns all previews for a project, newest first.
func ListForProject(d *db.DB, workspace, project string) ([]PreviewEnv, error) {
	rows, err := d.Query(
		`SELECT `+previewCols+` FROM preview_environments
		   WHERE workspace=? AND project=? ORDER BY id DESC`, workspace, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PreviewEnv{}
	for rows.Next() {
		p, err := scanPreview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ListExpired returns active previews whose TTL has elapsed (ExpiresAt in (0, now]).
// torn_down rows are excluded — the reaper only needs to act on live ones.
func ListExpired(d *db.DB, now int64) ([]PreviewEnv, error) {
	rows, err := d.Query(
		`SELECT `+previewCols+` FROM preview_environments
		   WHERE expires_at>0 AND expires_at<=? AND status<>? ORDER BY id`, now, StatusTornDown)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PreviewEnv{}
	for rows.Next() {
		p, err := scanPreview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// CountActive returns the number of non-torn-down previews for a project, used to
// enforce Preview.MaxConcurrent.
func CountActive(d *db.DB, workspace, project string) (int, error) {
	var n int
	err := d.QueryRow(
		`SELECT COUNT(*) FROM preview_environments
		   WHERE workspace=? AND project=? AND status<>?`, workspace, project, StatusTornDown).Scan(&n)
	return n, err
}

// Update writes the mutable fields of a preview (everything except the immutable
// scope/identity columns) by id.
func Update(d *db.DB, p PreviewEnv) error {
	_, err := d.Exec(
		`UPDATE preview_environments SET
		   branch=?, head_sha=?, url=?, status=?, last_run_id=?, last_deployed_at=?, expires_at=?
		 WHERE id=?`,
		p.Branch, p.HeadSHA, p.URL, p.Status, p.LastRunID, p.LastDeployedAt, p.ExpiresAt, p.ID)
	return err
}

// Delete removes a preview row.
func Delete(d *db.DB, id int64) error {
	_, err := d.Exec(`DELETE FROM preview_environments WHERE id=?`, id)
	return err
}

func scanPreview(s scanner) (*PreviewEnv, error) {
	var p PreviewEnv
	if err := s.Scan(&p.ID, &p.Workspace, &p.Project, &p.PRNumber, &p.Provider, &p.Branch,
		&p.HeadSHA, &p.EnvKey, &p.URL, &p.Status, &p.LastRunID, &p.CreatedAt,
		&p.LastDeployedAt, &p.ExpiresAt); err != nil {
		return nil, err
	}
	return &p, nil
}
