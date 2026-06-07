// Package alerts implements the Phase 6 observability layer: alert rules
// (6a), the background evaluator that turns rule conditions into alert events,
// and the alert history/inbox store (6c). It is deliberately self-contained —
// it talks to Docker and the DB directly so it can be driven by a background
// goroutine without depending on the api package (which would create a cycle).
package alerts

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
)

// ── Condition types & severities ────────────────────────────────────────────────

const (
	CondContainerDown   = "container_down"
	CondRestartCount    = "restart_count"
	CondDiskAbovePct    = "disk_above_pct"
	CondBackupFailed    = "backup_failed"
	CondBackupStale     = "backup_stale"
	CondImageUpdate     = "image_update_available"
	CondImageVersionAvailable = "image_version_available"
	CondCPUAbovePct     = "cpu_above_pct"
	CondMemoryAbovePct  = "memory_above_pct"

	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// numericConditions need a threshold; the rest are boolean.
var numericConditions = map[string]bool{
	CondRestartCount:   true,
	CondDiskAbovePct:   true,
	CondCPUAbovePct:    true,
	CondMemoryAbovePct: true,
	CondBackupStale:    true,
}

var validConditions = map[string]bool{
	CondContainerDown: true, CondRestartCount: true, CondDiskAbovePct: true,
	CondBackupFailed: true, CondBackupStale: true, CondImageUpdate: true, CondImageVersionAvailable: true,
	CondCPUAbovePct: true, CondMemoryAbovePct: true,
}

var validSeverities = map[string]bool{
	SeverityInfo: true, SeverityWarning: true, SeverityCritical: true,
}

// IsNumeric reports whether a condition type compares a metric to a threshold.
func IsNumeric(condition string) bool { return numericConditions[condition] }

// ── Types ───────────────────────────────────────────────────────────────────────

// Rule is one alert rule. Targeting: workspace+env (specific stack), workspace
// only (all its envs), or both empty (all workspaces).
type Rule struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	ConditionType   string    `json:"condition_type"`
	Threshold       float64   `json:"threshold"`
	Workspace       string    `json:"workspace"`
	Env             string    `json:"env"`
	Severity        string    `json:"severity"`
	CooldownMinutes int       `json:"cooldown_minutes"`
	Enabled         bool      `json:"enabled"`
	NotifyChannelIDs []int64  `json:"notify_channel_ids"` // channels to notify on fire/resolve
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Event is a fired alert. Rule fields are denormalised so the event survives
// deletion of its rule.
type Event struct {
	ID            int64      `json:"id"`
	RuleID        *int64     `json:"rule_id"`
	RuleName      string     `json:"rule_name"`
	ConditionType string     `json:"condition_type"`
	Workspace     string     `json:"workspace"`
	Env           string     `json:"env"`
	Message       string     `json:"message"`
	Severity      string     `json:"severity"`
	Value         float64    `json:"value"`
	FiredAt       time.Time  `json:"fired_at"`
	ResolvedAt    *time.Time `json:"resolved_at"`
	Dismissed     bool       `json:"dismissed"`
}

// Validate checks a rule's fields before persisting.
func (r *Rule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if !validConditions[r.ConditionType] {
		return fmt.Errorf("invalid condition_type %q", r.ConditionType)
	}
	if r.Severity == "" {
		r.Severity = SeverityWarning
	}
	if !validSeverities[r.Severity] {
		return fmt.Errorf("invalid severity %q", r.Severity)
	}
	if IsNumeric(r.ConditionType) && r.Threshold <= 0 {
		return fmt.Errorf("condition %q requires a threshold greater than 0", r.ConditionType)
	}
	if r.CooldownMinutes < 0 {
		return fmt.Errorf("cooldown_minutes cannot be negative")
	}
	if r.CooldownMinutes == 0 {
		r.CooldownMinutes = 15
	}
	// An env target without a workspace is meaningless.
	if r.Env != "" && r.Workspace == "" {
		return fmt.Errorf("env target requires a workspace")
	}
	return nil
}

// ── Rule CRUD ────────────────────────────────────────────────────────────────────

// ruleColumns is the shared SELECT column list (must match scanRule order).
const ruleColumns = `id, name, condition_type, threshold, project, env, severity,
	cooldown_minutes, enabled, notify_channel_ids, created_at, updated_at`

func ListRules(d *db.DB) ([]Rule, error) {
	rows, err := d.Query(`SELECT ` + ruleColumns + ` FROM alert_rules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListEnabledRules returns only enabled rules — the evaluator's working set.
func ListEnabledRules(d *db.DB) ([]Rule, error) {
	rows, err := d.Query(`SELECT ` + ruleColumns + ` FROM alert_rules WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func GetRule(d *db.DB, id int64) (*Rule, error) {
	row := d.QueryRow(`SELECT `+ruleColumns+` FROM alert_rules WHERE id = ?`, id)
	r, err := scanRule(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func CreateRule(d *db.DB, r Rule) (*Rule, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	res, err := d.Exec(`
		INSERT INTO alert_rules (name, condition_type, threshold, project, env,
		                         severity, cooldown_minutes, enabled, notify_channel_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.ConditionType, r.Threshold, r.Workspace, r.Env,
		r.Severity, r.CooldownMinutes, boolToInt(r.Enabled), marshalIDs(r.NotifyChannelIDs))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetRule(d, id)
}

func UpdateRule(d *db.DB, id int64, r Rule) (*Rule, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	_, err := d.Exec(`
		UPDATE alert_rules
		SET name=?, condition_type=?, threshold=?, project=?, env=?,
		    severity=?, cooldown_minutes=?, enabled=?, notify_channel_ids=?,
		    updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		r.Name, r.ConditionType, r.Threshold, r.Workspace, r.Env,
		r.Severity, r.CooldownMinutes, boolToInt(r.Enabled), marshalIDs(r.NotifyChannelIDs), id)
	if err != nil {
		return nil, err
	}
	return GetRule(d, id)
}

func DeleteRule(d *db.DB, id int64) error {
	_, err := d.Exec(`DELETE FROM alert_rules WHERE id=?`, id)
	return err
}

// ── Event store (6c) ─────────────────────────────────────────────────────────────

// ListEventsOptions filters the inbox/history query.
type ListEventsOptions struct {
	IncludeResolved  bool
	IncludeDismissed bool
	Limit            int
}

func ListEvents(d *db.DB, opt ListEventsOptions) ([]Event, error) {
	var where []string
	if !opt.IncludeResolved {
		where = append(where, "resolved_at IS NULL")
	}
	if !opt.IncludeDismissed {
		where = append(where, "dismissed = 0")
	}
	q := `SELECT id, rule_id, rule_name, condition_type, project, env, message,
	             severity, value, fired_at, resolved_at, dismissed
	      FROM alert_events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY fired_at DESC"
	if opt.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", opt.Limit)
	}

	rows, err := d.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// OpenEventFor returns the active (unresolved) event for a rule+target, if any.
func OpenEventFor(d *db.DB, ruleID int64, ws, env string) (*Event, error) {
	row := d.QueryRow(`
		SELECT id, rule_id, rule_name, condition_type, project, env, message,
		       severity, value, fired_at, resolved_at, dismissed
		FROM alert_events
		WHERE rule_id = ? AND project = ? AND env = ? AND resolved_at IS NULL
		ORDER BY fired_at DESC LIMIT 1`, ruleID, ws, env)
	e, err := scanEvent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// LastEventFor returns the most recent event (any state) for a rule+target —
// used to enforce the cooldown window before re-firing.
func LastEventFor(d *db.DB, ruleID int64, ws, env string) (*Event, error) {
	row := d.QueryRow(`
		SELECT id, rule_id, rule_name, condition_type, project, env, message,
		       severity, value, fired_at, resolved_at, dismissed
		FROM alert_events
		WHERE rule_id = ? AND project = ? AND env = ?
		ORDER BY fired_at DESC LIMIT 1`, ruleID, ws, env)
	e, err := scanEvent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func CreateEvent(d *db.DB, e Event) (*Event, error) {
	res, err := d.Exec(`
		INSERT INTO alert_events (rule_id, rule_name, condition_type, project,
		                          env, message, severity, value)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.RuleID, e.RuleName, e.ConditionType, e.Workspace, e.Env,
		e.Message, e.Severity, e.Value)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetEvent(d, id)
}

func GetEvent(d *db.DB, id int64) (*Event, error) {
	row := d.QueryRow(`
		SELECT id, rule_id, rule_name, condition_type, project, env, message,
		       severity, value, fired_at, resolved_at, dismissed
		FROM alert_events WHERE id = ?`, id)
	e, err := scanEvent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// ResolveEvent marks an event resolved (auto-clear when the condition lifts).
func ResolveEvent(d *db.DB, id int64) error {
	_, err := d.Exec(`UPDATE alert_events SET resolved_at = CURRENT_TIMESTAMP WHERE id = ? AND resolved_at IS NULL`, id)
	return err
}

func DismissEvent(d *db.DB, id int64) error {
	_, err := d.Exec(`UPDATE alert_events SET dismissed = 1 WHERE id = ?`, id)
	return err
}

// DismissAll acknowledges every non-dismissed event.
func DismissAll(d *db.DB) (int64, error) {
	res, err := d.Exec(`UPDATE alert_events SET dismissed = 1 WHERE dismissed = 0`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UnreadCount is the nav-bell badge: non-dismissed events.
func UnreadCount(d *db.DB) (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM alert_events WHERE dismissed = 0`).Scan(&n)
	return n, err
}

// WorkspaceAlertCount is a per-workspace active-alert breakdown.
type WorkspaceAlertCount struct {
	Total    int `json:"total"`
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// SummaryResult aggregates currently-active (unresolved, not dismissed) alerts
// for the Phase 6e dashboard: totals by severity plus a per-workspace breakdown.
type SummaryResult struct {
	Total       int                            `json:"total"`
	Critical    int                            `json:"critical"`
	Warning     int                            `json:"warning"`
	Info        int                            `json:"info"`
	ByWorkspace map[string]WorkspaceAlertCount `json:"by_workspace"`
}

// Summary returns the active-alert aggregation for the dashboard.
func Summary(d *db.DB) (SummaryResult, error) {
	res := SummaryResult{ByWorkspace: map[string]WorkspaceAlertCount{}}
	rows, err := d.Query(`
		SELECT project, severity, COUNT(*)
		FROM alert_events
		WHERE resolved_at IS NULL AND dismissed = 0
		GROUP BY project, severity`)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	for rows.Next() {
		var ws, sev string
		var n int
		if err := rows.Scan(&ws, &sev, &n); err != nil {
			return res, err
		}
		wc := res.ByWorkspace[ws]
		wc.Total += n
		switch sev {
		case SeverityCritical:
			wc.Critical += n
			res.Critical += n
		case SeverityInfo:
			wc.Info += n
			res.Info += n
		default:
			wc.Warning += n
			res.Warning += n
		}
		res.Total += n
		res.ByWorkspace[ws] = wc
	}
	return res, rows.Err()
}

// ── backup_log (source for the backup_failed condition + Phase 11) ───────────────

// LogBackup records the outcome of a per-env backup run.
func LogBackup(d *db.DB, ws, env, status, message string, sizeBytes int64) error {
	if status != "ok" && status != "error" {
		status = "error"
	}
	_, err := d.Exec(`
		INSERT INTO backup_log (project, env, status, message, size_bytes)
		VALUES (?, ?, ?, ?, ?)`, ws, env, status, message, sizeBytes)
	return err
}

// LatestBackupStatus returns the status ("ok"/"error") of the most recent backup
// for a target, or ok=false if no backup has ever run.
func LatestBackupStatus(d *db.DB, ws, env string) (status string, at time.Time, ok bool, err error) {
	row := d.QueryRow(`
		SELECT status, created_at FROM backup_log
		WHERE project = ? AND env = ? ORDER BY created_at DESC LIMIT 1`, ws, env)
	e := row.Scan(&status, &at)
	if e == sql.ErrNoRows {
		return "", time.Time{}, false, nil
	}
	if e != nil {
		return "", time.Time{}, false, e
	}
	return status, at, true, nil
}

// ── scan helpers ─────────────────────────────────────────────────────────────────

type scanner interface{ Scan(dest ...any) error }

func scanRule(s scanner) (Rule, error) {
	var r Rule
	var enabled int
	var channelIDs string
	err := s.Scan(&r.ID, &r.Name, &r.ConditionType, &r.Threshold, &r.Workspace,
		&r.Env, &r.Severity, &r.CooldownMinutes, &enabled, &channelIDs, &r.CreatedAt, &r.UpdatedAt)
	r.Enabled = enabled != 0
	r.NotifyChannelIDs = unmarshalIDs(channelIDs)
	return r, err
}

// marshalIDs serialises channel IDs to a JSON array for the notify_channel_ids
// column; nil/empty becomes "[]".
func marshalIDs(ids []int64) string {
	if len(ids) == 0 {
		return "[]"
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func unmarshalIDs(s string) []int64 {
	if s == "" {
		return nil
	}
	var ids []int64
	if json.Unmarshal([]byte(s), &ids) != nil {
		return nil
	}
	return ids
}

func scanEvent(s scanner) (Event, error) {
	var e Event
	var dismissed int
	var ruleID sql.NullInt64
	var resolvedAt sql.NullTime
	err := s.Scan(&e.ID, &ruleID, &e.RuleName, &e.ConditionType, &e.Workspace,
		&e.Env, &e.Message, &e.Severity, &e.Value, &e.FiredAt, &resolvedAt, &dismissed)
	if ruleID.Valid {
		e.RuleID = &ruleID.Int64
	}
	if resolvedAt.Valid {
		e.ResolvedAt = &resolvedAt.Time
	}
	e.Dismissed = dismissed != 0
	return e, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
