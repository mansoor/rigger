// Package pipelines persists deployment pipelines (Phase 9) and their run history,
// and executes a pipeline as an ordered sequence of shell-bridge commands. A
// pipeline is project-scoped (by the workspace + project keys) and made of stages;
// each stage maps to an existing bridge command (deploy/build/push/restart/backup)
// or a sandboxed `test` that runs a command inside a service container.
package pipelines

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
)

const (
	maxOutputBytes = 64 * 1024 // cap stored per-stage output; keep the tail
	keepPerPipe    = 50        // recent runs retained per pipeline
)

// stageTypes are the allowed pipeline stage types. Each maps to a bridge command
// (or, for `test`, a sandboxed compose-exec) in executor.go. `push`/promote is a
// two-environment operation and is deferred to a later iteration.
var stageTypes = map[string]bool{
	"deploy": true, "update": true, "build": true,
	"refresh": true, // regenerate docker-compose.yml from config/.env, then up -d (apply config changes)
	"restart": true, "backup": true, "test": true,
	"push":    true, // promote src env → dst env (pull/tag/push via registry)
	"gate":    true, // manual approval pause (9d) — no env
	"version": true, // semver bump (major|minor|patch|build) — no env
	"script":  true, // run a one-off tool container (Trivy/Cypress/Sonar/custom)
}

// Stage is one step of a pipeline definition.
type Stage struct {
	Type      string `json:"type"`              // deploy|update|build|restart|backup|test|push|gate|version|script
	Env       string `json:"env"`               // target env (source env for push; context env for script)
	ToEnv     string `json:"to_env,omitempty"`  // push only — destination env
	Service   string `json:"service,omitempty"` // test (required) / backup (optional)
	Command   string `json:"command,omitempty"` // test/script command / gate note
	Image     string `json:"image,omitempty"`   // script only — the tool container image
	Network   bool   `json:"network,omitempty"` // script only — attach to the env's compose network
	Part      string `json:"part,omitempty"`    // version only — major|minor|patch|build
	Push      bool   `json:"push,omitempty"`    // build only — also push images to the registry
	When      string `json:"when,omitempty"`    // build only — always(default) | if-changed | force(no-cache)
	OnFailure string `json:"on_failure"`        // stop | continue (default stop)
}

// NotifyEvents selects which pipeline-run events fire a notification. Stage-level
// detail (which stage failed + a short error) rides on the Failed alert, so there
// is no per-stage toggle — the run-level outcome covers every case.
type NotifyEvents struct {
	Started   bool `json:"started"`
	Succeeded bool `json:"succeeded"`
	Failed    bool `json:"failed"`
}

// Any reports whether at least one event is enabled (so alerting is configured).
func (n NotifyEvents) Any() bool { return n.Started || n.Succeeded || n.Failed }

// Pipeline is a named, ordered list of stages attached to a project.
type Pipeline struct {
	ID        int64     `json:"id"`
	Workspace string    `json:"workspace"`
	Project   string    `json:"project"`
	Name      string    `json:"name"`
	Stages    []Stage   `json:"stages"`
	Enabled   bool      `json:"enabled"`
	// NotifyChannelIDs are the notification channels alerted on run events, and
	// NotifyEvents selects which events fire (mirrors alert-rule fan-out).
	NotifyChannelIDs []int64      `json:"notify_channel_ids"`
	NotifyEvents     NotifyEvents `json:"notify_events"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

// StageResult is the recorded outcome of one stage within a run.
type StageResult struct {
	Type   string `json:"type"`
	Env    string `json:"env"`
	Label  string `json:"label"`
	Status string `json:"status"` // ok | fail | skipped
	Output string `json:"output"`
	// Warnings are the ⚠ lines a SUCCEEDING stage emitted. A build that produces a
	// working image but flags a problem the app will hit at runtime is still "ok",
	// and its warning was previously buried under a hundred lines of BuildKit
	// output beneath a green tick — read by nobody. Lifting them here lets the run
	// summarise them at the end and the UI mark the stage.
	Warnings   []string `json:"warnings,omitempty"`
	MS         int64    `json:"ms"`
	StartedAt  int64    `json:"started_at,omitempty"`  // epoch ms (0 = not started / skipped)
	FinishedAt int64    `json:"finished_at,omitempty"` // epoch ms (0 while running / skipped)
}

// Run is one execution of a pipeline.
type Run struct {
	ID         int64         `json:"id"`
	PipelineID int64         `json:"pipeline_id"`
	Workspace  string        `json:"workspace"`
	Project    string        `json:"project"`
	Trigger    string        `json:"trigger"` // manual | webhook (9a)
	Username   string        `json:"username"`
	Status     string        `json:"status"` // running | ok | fail | cancelled
	Stages     []StageResult `json:"stages"`
	StartedAt  int64         `json:"started_at"`  // epoch ms
	FinishedAt int64         `json:"finished_at"` // epoch ms (0 while running)
}

// Validate checks a pipeline before persisting it.
func (p *Pipeline) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("pipeline name is required")
	}
	if len(p.Stages) == 0 {
		return errors.New("a pipeline needs at least one stage")
	}
	for i := range p.Stages {
		s := &p.Stages[i]
		if !stageTypes[s.Type] {
			return fmt.Errorf("stage %d: unknown type %q", i+1, s.Type)
		}
		// gate (manual pause) and version (project-global bump) target no env.
		if s.Type != "gate" && s.Type != "version" && strings.TrimSpace(s.Env) == "" {
			return fmt.Errorf("stage %d (%s): an environment is required", i+1, s.Type)
		}
		if s.Type == "version" {
			switch s.Part {
			case "major", "minor", "patch", "build":
			default:
				return fmt.Errorf("stage %d (version): part must be major|minor|patch|build", i+1)
			}
		}
		if s.Type == "script" {
			if strings.TrimSpace(s.Image) == "" || strings.TrimSpace(s.Command) == "" {
				return fmt.Errorf("stage %d (script): an image and command are required", i+1)
			}
		}
		if s.Type == "test" {
			if strings.TrimSpace(s.Service) == "" || strings.TrimSpace(s.Command) == "" {
				return fmt.Errorf("stage %d (test): a service and command are required", i+1)
			}
		}
		if s.Type == "push" {
			if strings.TrimSpace(s.ToEnv) == "" {
				return fmt.Errorf("stage %d (push): a destination environment is required", i+1)
			}
			if s.ToEnv == s.Env {
				return fmt.Errorf("stage %d (push): source and destination must differ", i+1)
			}
		}
		if s.OnFailure != "continue" {
			s.OnFailure = "stop"
		}
	}
	return nil
}

// ── Pipeline CRUD ─────────────────────────────────────────────────────────────

// List returns the pipelines for one project, newest first.
func List(d *db.DB, workspace, project string) ([]Pipeline, error) {
	rows, err := d.Query(
		`SELECT id, workspace, project, name, stages, enabled, notify_channel_ids, notify_events, created_at, updated_at
		   FROM pipelines WHERE workspace=? AND project=? ORDER BY id DESC`,
		workspace, project,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pipeline{}
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// Get returns one pipeline by id.
func Get(d *db.DB, id int64) (*Pipeline, error) {
	return scanPipeline(d.QueryRow(
		`SELECT id, workspace, project, name, stages, enabled, notify_channel_ids, notify_events, created_at, updated_at
		   FROM pipelines WHERE id=?`, id))
}

// Create inserts a pipeline and returns it with its assigned id.
func Create(d *db.DB, p Pipeline) (*Pipeline, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	stages, _ := json.Marshal(p.Stages)
	chans, _ := json.Marshal(nonNilIDs(p.NotifyChannelIDs))
	events, _ := json.Marshal(p.NotifyEvents)
	res, err := d.Exec(
		`INSERT INTO pipelines (workspace, project, name, stages, enabled, notify_channel_ids, notify_events) VALUES (?,?,?,?,?,?,?)`,
		p.Workspace, p.Project, p.Name, string(stages), boolToInt(p.Enabled), string(chans), string(events),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errors.New("a pipeline with that name already exists in this project")
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return Get(d, id)
}

// Update replaces a pipeline's name/stages/enabled.
func Update(d *db.DB, id int64, p Pipeline) (*Pipeline, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	stages, _ := json.Marshal(p.Stages)
	chans, _ := json.Marshal(nonNilIDs(p.NotifyChannelIDs))
	events, _ := json.Marshal(p.NotifyEvents)
	if _, err := d.Exec(
		`UPDATE pipelines SET name=?, stages=?, enabled=?, notify_channel_ids=?, notify_events=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		p.Name, string(stages), boolToInt(p.Enabled), string(chans), string(events), id,
	); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errors.New("a pipeline with that name already exists in this project")
		}
		return nil, err
	}
	return Get(d, id)
}

// Delete removes a pipeline and its run history.
func Delete(d *db.DB, id int64) error {
	d.Exec(`DELETE FROM pipeline_runs WHERE pipeline_id=?`, id) //nolint:errcheck
	_, err := d.Exec(`DELETE FROM pipelines WHERE id=?`, id)
	return err
}

// ── Run history ───────────────────────────────────────────────────────────────

// CreateRun inserts a run row (typically status='running') and returns its id.
func CreateRun(d *db.DB, r Run) (int64, error) {
	stages, _ := json.Marshal(r.Stages)
	if r.Trigger == "" {
		r.Trigger = "manual"
	}
	res, err := d.Exec(
		`INSERT INTO pipeline_runs (pipeline_id, workspace, project, trigger, username, status, stages, started_at, finished_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		r.PipelineID, r.Workspace, r.Project, r.Trigger, r.Username, r.Status, string(stages), r.StartedAt, nullable(r.FinishedAt),
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// UpdateRun persists the final status, per-stage results and finish time, then
// prunes the pipeline's history to keepPerPipe.
func UpdateRun(d *db.DB, r Run) error {
	stages, _ := json.Marshal(r.Stages)
	if _, err := d.Exec(
		`UPDATE pipeline_runs SET status=?, stages=?, finished_at=? WHERE id=?`,
		r.Status, string(stages), nullable(r.FinishedAt), r.ID,
	); err != nil {
		return err
	}
	d.Exec( //nolint:errcheck
		`DELETE FROM pipeline_runs WHERE pipeline_id=? AND id NOT IN
		   (SELECT id FROM pipeline_runs WHERE pipeline_id=? ORDER BY id DESC LIMIT ?)`,
		r.PipelineID, r.PipelineID, keepPerPipe,
	)
	return nil
}

// UpdateRunProgress persists in-flight stage results for a still-running run
// (status forced to 'running', finish time left NULL) WITHOUT pruning history.
// Called repeatedly by the executor's progress callback, so it stays lightweight.
func UpdateRunProgress(d *db.DB, id int64, stages []StageResult) error {
	b, _ := json.Marshal(stages)
	_, err := d.Exec(
		`UPDATE pipeline_runs SET status='running', stages=?, finished_at=NULL WHERE id=?`,
		string(b), id,
	)
	return err
}

// ListRuns returns up to limit most-recent runs for a pipeline, newest first.
func ListRuns(d *db.DB, pipelineID int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	rows, err := d.Query(
		`SELECT id, pipeline_id, workspace, project, trigger, username, status, stages, started_at, finished_at
		   FROM pipeline_runs WHERE pipeline_id=? ORDER BY id DESC LIMIT ?`,
		pipelineID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetRun returns one run by id.
func GetRun(d *db.DB, id int64) (*Run, error) {
	return scanRun(d.QueryRow(
		`SELECT id, pipeline_id, workspace, project, trigger, username, status, stages, started_at, finished_at
		   FROM pipeline_runs WHERE id=?`, id))
}

// cancelStages flips any still-active stage (running or awaiting) to "cancelled",
// leaving completed stages untouched. Used when a run is force-stopped or swept.
func cancelStages(stages []StageResult) []StageResult {
	for i := range stages {
		if stages[i].Status == "running" || stages[i].Status == OutcomeAwaiting {
			stages[i].Status = OutcomeCancelled
		}
	}
	return stages
}

// MarkRunCancelled finalizes a run as cancelled directly in the DB (no live
// goroutine to signal): it flips any active stage to cancelled and stamps the
// finish time. Used for orphaned/awaiting runs that aren't executing in-process.
func MarkRunCancelled(d *db.DB, run *Run, finishedMs int64) error {
	return UpdateRun(d, Run{
		ID:         run.ID,
		PipelineID: run.PipelineID,
		Status:     OutcomeCancelled,
		Stages:     cancelStages(run.Stages),
		FinishedAt: finishedMs,
	})
}

// ReconcileRunning sweeps runs left at "running" (and stages mid-flight) — e.g.
// because the server restarted while a run's goroutine was executing — marking
// them cancelled with the given finish time so the UI never shows a stranded
// in-flight run. Returns the number of runs reconciled. Call once at startup.
func ReconcileRunning(d *db.DB, finishedMs int64) (int, error) {
	rows, err := d.Query(`SELECT id, stages FROM pipeline_runs WHERE status='running'`)
	if err != nil {
		return 0, err
	}
	type pending struct {
		id     int64
		stages string
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.stages); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, p := range todo {
		var stages []StageResult
		if p.stages != "" {
			json.Unmarshal([]byte(p.stages), &stages) //nolint:errcheck
		}
		b, _ := json.Marshal(cancelStages(stages))
		d.Exec( //nolint:errcheck
			`UPDATE pipeline_runs SET status='cancelled', stages=?, finished_at=? WHERE id=? AND status='running'`,
			string(b), finishedMs, p.id,
		)
	}
	return len(todo), nil
}

// ── scanning helpers ──────────────────────────────────────────────────────────

type scanner interface{ Scan(dest ...any) error }

func scanPipeline(s scanner) (*Pipeline, error) {
	var p Pipeline
	var stages, chans, events string
	var enabled int
	if err := s.Scan(&p.ID, &p.Workspace, &p.Project, &p.Name, &stages, &enabled, &chans, &events, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	if stages != "" {
		json.Unmarshal([]byte(stages), &p.Stages) //nolint:errcheck
	}
	if p.Stages == nil {
		p.Stages = []Stage{}
	}
	if chans != "" {
		json.Unmarshal([]byte(chans), &p.NotifyChannelIDs) //nolint:errcheck
	}
	if events != "" {
		json.Unmarshal([]byte(events), &p.NotifyEvents) //nolint:errcheck
	}
	return &p, nil
}

// nonNilIDs returns a non-nil slice so an empty channel set persists as "[]".
func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

func scanRun(s scanner) (*Run, error) {
	var r Run
	var stages string
	var finished *int64
	if err := s.Scan(&r.ID, &r.PipelineID, &r.Workspace, &r.Project, &r.Trigger, &r.Username, &r.Status, &stages, &r.StartedAt, &finished); err != nil {
		return nil, err
	}
	if finished != nil {
		r.FinishedAt = *finished
	}
	if stages != "" {
		json.Unmarshal([]byte(stages), &r.Stages) //nolint:errcheck
	}
	if r.Stages == nil {
		r.Stages = []StageResult{}
	}
	return &r, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullable returns nil for a zero finished-at so the column stays NULL while a run
// is in flight.
func nullable(ms int64) any {
	if ms == 0 {
		return nil
	}
	return ms
}
