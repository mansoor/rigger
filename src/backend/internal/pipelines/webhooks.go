package pipelines

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
)

// Webhook is an inbound trigger bound to one pipeline. The URL token is never
// stored — only its sha256 hash; the raw token is shown once at creation time.
type Webhook struct {
	ID              int64      `json:"id"`
	PipelineID      int64      `json:"pipeline_id"`
	Workspace       string     `json:"workspace"`
	Project         string     `json:"project"`
	Secret          string     `json:"secret"`  // optional HMAC signing key
	Enabled         bool       `json:"enabled"`
	CreatedAt       time.Time  `json:"created_at"`
	LastTriggeredAt *time.Time `json:"last_triggered_at"`
	// Token is only populated on create (the one time the raw value is returned).
	Token string `json:"token,omitempty"`
}

// hashToken returns the hex sha256 of a raw webhook token.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CreateWebhook generates a token, stores its hash, and returns the webhook with
// the raw token populated (shown to the user once).
func CreateWebhook(d *db.DB, w Webhook) (*Webhook, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	raw := hex.EncodeToString(buf)
	res, err := d.Exec(
		`INSERT INTO pipeline_webhooks (pipeline_id, workspace, project, token_hash, secret, enabled)
		 VALUES (?,?,?,?,?,1)`,
		w.PipelineID, w.Workspace, w.Project, hashToken(raw), w.Secret,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	out, err := getWebhook(d, id)
	if err != nil {
		return nil, err
	}
	out.Token = raw
	return out, nil
}

// ListWebhooks returns the webhooks bound to a pipeline.
func ListWebhooks(d *db.DB, pipelineID int64) ([]Webhook, error) {
	rows, err := d.Query(
		`SELECT id, pipeline_id, workspace, project, secret, enabled, created_at, last_triggered_at
		   FROM pipeline_webhooks WHERE pipeline_id=? ORDER BY id DESC`, pipelineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Webhook{}
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// GetWebhookByToken resolves an enabled webhook from a raw URL token.
func GetWebhookByToken(d *db.DB, rawToken string) (*Webhook, error) {
	w, err := scanWebhook(d.QueryRow(
		`SELECT id, pipeline_id, workspace, project, secret, enabled, created_at, last_triggered_at
		   FROM pipeline_webhooks WHERE token_hash=? AND enabled=1`, hashToken(rawToken)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("webhook not found")
	}
	return w, err
}

// DeleteWebhook removes a webhook.
func DeleteWebhook(d *db.DB, id int64) error {
	_, err := d.Exec(`DELETE FROM pipeline_webhooks WHERE id=?`, id)
	return err
}

// TouchWebhook records the last-triggered time.
func TouchWebhook(d *db.DB, id int64) {
	d.Exec(`UPDATE pipeline_webhooks SET last_triggered_at=CURRENT_TIMESTAMP WHERE id=?`, id) //nolint:errcheck
}

func getWebhook(d *db.DB, id int64) (*Webhook, error) {
	return scanWebhook(d.QueryRow(
		`SELECT id, pipeline_id, workspace, project, secret, enabled, created_at, last_triggered_at
		   FROM pipeline_webhooks WHERE id=?`, id))
}

func scanWebhook(s scanner) (*Webhook, error) {
	var w Webhook
	var enabled int
	var last sql.NullTime
	if err := s.Scan(&w.ID, &w.PipelineID, &w.Workspace, &w.Project, &w.Secret, &enabled, &w.CreatedAt, &last); err != nil {
		return nil, err
	}
	w.Enabled = enabled != 0
	if last.Valid {
		w.LastTriggeredAt = &last.Time
	}
	return &w, nil
}
