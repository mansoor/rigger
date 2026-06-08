// Package notify implements Phase 6b notification channels: persistence for
// channels, and dispatch of alert notifications. Email is delivered directly
// over SMTP; every other channel type is delivered through the Apprise API
// sidecar, so Slack/Discord/Telegram/webhook/etc. require no per-service code.
package notify

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
)

const (
	TypeEmail   = "email"   // delivered directly via SMTP
	TypeApprise = "apprise" // delivered via the Apprise API sidecar
)

// Channel is a configured notification destination.
type Channel struct {
	ID         int64           `json:"id"`
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Config     json.RawMessage `json:"config"`
	Enabled    bool            `json:"enabled"`
	OwnerScope string          `json:"owner_scope"`      // 'global' or 'ws:{key}'
	Grants     []string        `json:"grants,omitempty"` // for global channels: workspaces offered to ('*' = all)
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// WorkspaceScope returns the workspace key a channel is private to, or "" if global.
func (c Channel) WorkspaceScope() string {
	if strings.HasPrefix(c.OwnerScope, "ws:") {
		return c.OwnerScope[len("ws:"):]
	}
	return ""
}

func wsScope(wsKey string) string { return "ws:" + wsKey }

// EmailConfig is the SMTP configuration for a TypeEmail channel.
type EmailConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	To       string `json:"to"`      // comma- or newline-separated recipients
	UseTLS   bool   `json:"use_tls"` // STARTTLS (port 587/25); port 465 implies implicit TLS
}

// AppriseConfig is the configuration for a TypeApprise channel: one or more
// Apprise URLs (e.g. slack://…, discord://…), one per line or comma-separated.
type AppriseConfig struct {
	URLs string `json:"urls"`
}

// Validate checks a channel before persisting and normalises defaults.
func (c *Channel) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name is required")
	}
	switch c.Type {
	case TypeEmail:
		var e EmailConfig
		if err := json.Unmarshal(nonNil(c.Config), &e); err != nil {
			return fmt.Errorf("invalid email config: %w", err)
		}
		if e.Host == "" {
			return fmt.Errorf("SMTP host is required")
		}
		if e.Port == 0 {
			return fmt.Errorf("SMTP port is required")
		}
		if strings.TrimSpace(e.From) == "" {
			return fmt.Errorf("from address is required")
		}
		if strings.TrimSpace(e.To) == "" {
			return fmt.Errorf("at least one recipient is required")
		}
	case TypeApprise:
		var a AppriseConfig
		if err := json.Unmarshal(nonNil(c.Config), &a); err != nil {
			return fmt.Errorf("invalid apprise config: %w", err)
		}
		if strings.TrimSpace(a.URLs) == "" {
			return fmt.Errorf("at least one Apprise URL is required")
		}
	default:
		return fmt.Errorf("invalid channel type %q (must be %q or %q)", c.Type, TypeEmail, TypeApprise)
	}
	return nil
}

// ── CRUD ─────────────────────────────────────────────────────────────────────

func ListChannels(d *db.DB) ([]Channel, error) {
	rows, err := d.Query(`SELECT id, name, type, config, enabled, owner_scope, created_at, updated_at
		FROM notification_channels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListChannelsForWorkspace returns the channel pool visible to one workspace: its
// own (owner_scope='ws:{key}') plus any global channel granted to it (or '*').
func ListChannelsForWorkspace(d *db.DB, wsKey string) ([]Channel, error) {
	rows, err := d.Query(`
		SELECT id, name, type, config, enabled, owner_scope, created_at, updated_at
		FROM notification_channels c
		WHERE c.owner_scope = ?
		   OR (c.owner_scope = 'global' AND EXISTS(
		         SELECT 1 FROM global_notification_channel_grants g
		         WHERE g.channel_id = c.id AND g.workspace IN (?, '*')))
		ORDER BY name`, wsScope(wsKey), wsKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChannelInWorkspacePool reports whether a channel is usable by a workspace.
func ChannelInWorkspacePool(d *db.DB, wsKey string, id int64) (bool, error) {
	var n int
	err := d.QueryRow(`
		SELECT COUNT(1) FROM notification_channels c
		WHERE c.id = ?
		  AND (c.owner_scope = ?
		    OR (c.owner_scope = 'global' AND EXISTS(
		          SELECT 1 FROM global_notification_channel_grants g
		          WHERE g.channel_id = c.id AND g.workspace IN (?, '*'))))`,
		id, wsScope(wsKey), wsKey).Scan(&n)
	return n > 0, err
}

// ChannelGrants returns the workspace allowlist for a global channel ('*' = all).
func ChannelGrants(d *db.DB, id int64) ([]string, error) {
	rows, err := d.Query(`SELECT workspace FROM global_notification_channel_grants WHERE channel_id=? ORDER BY workspace`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ws string
		if err := rows.Scan(&ws); err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

// SetChannelGrants replaces a global channel's workspace allowlist.
func SetChannelGrants(d *db.DB, id int64, workspaces []string) error {
	if _, err := d.Exec(`DELETE FROM global_notification_channel_grants WHERE channel_id=?`, id); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, ws := range workspaces {
		if ws == "" || seen[ws] {
			continue
		}
		seen[ws] = true
		if _, err := d.Exec(`INSERT OR IGNORE INTO global_notification_channel_grants (channel_id, workspace) VALUES (?, ?)`, id, ws); err != nil {
			return err
		}
	}
	return nil
}

// WorkspaceOwnedChannelIDs returns the ids of channels private to a workspace.
func WorkspaceOwnedChannelIDs(d *db.DB, wsKey string) ([]int64, error) {
	rows, err := d.Query(`SELECT id FROM notification_channels WHERE owner_scope=?`, wsScope(wsKey))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func GetChannel(d *db.DB, id int64) (*Channel, error) {
	row := d.QueryRow(`SELECT id, name, type, config, enabled, owner_scope, created_at, updated_at
		FROM notification_channels WHERE id = ?`, id)
	c, err := scanChannel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func CreateChannel(d *db.DB, c Channel) (*Channel, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.OwnerScope == "" {
		c.OwnerScope = "global"
	}
	res, err := d.Exec(`INSERT INTO notification_channels (name, type, config, enabled, owner_scope)
		VALUES (?, ?, ?, ?, ?)`, c.Name, c.Type, string(nonNil(c.Config)), boolToInt(c.Enabled), c.OwnerScope)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetChannel(d, id)
}

// WorkspaceOwnerScope formats the owner_scope value for a workspace-owned channel.
func WorkspaceOwnerScope(wsKey string) string { return wsScope(wsKey) }

func UpdateChannel(d *db.DB, id int64, c Channel) (*Channel, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	_, err := d.Exec(`UPDATE notification_channels
		SET name=?, type=?, config=?, enabled=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		c.Name, c.Type, string(nonNil(c.Config)), boolToInt(c.Enabled), id)
	if err != nil {
		return nil, err
	}
	return GetChannel(d, id)
}

func DeleteChannel(d *db.DB, id int64) error {
	_, err := d.Exec(`DELETE FROM notification_channels WHERE id=?`, id)
	return err
}

// ── helpers ──────────────────────────────────────────────────────────────────

type scanner interface{ Scan(dest ...any) error }

func scanChannel(s scanner) (Channel, error) {
	var c Channel
	var cfg string
	var enabled int
	err := s.Scan(&c.ID, &c.Name, &c.Type, &cfg, &enabled, &c.OwnerScope, &c.CreatedAt, &c.UpdatedAt)
	c.Config = json.RawMessage(cfg)
	c.Enabled = enabled != 0
	return c, err
}

func nonNil(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage(`{}`)
	}
	return r
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// splitRecipients splits a comma/newline/semicolon-separated address list.
func splitRecipients(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';' || r == ' '
	})
	out := make([]string, 0, len(f))
	for _, a := range f {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}
