package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/envorder"
	"github.com/mansoor/rigger/ui/internal/notify"
	"github.com/mansoor/rigger/ui/internal/pipelines"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// POST /api/workspaces/{workspace}/projects/{name}/pipelines/suggest
// Returns a DRAFT pipeline (not persisted) generated from the project's ordered
// environments. The UI loads it into the editor; saving goes through Create.
func (h *Handler) SuggestPipeline(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var body struct {
		Template   string `json:"template"`
		BumpPart   string `json:"bump_part"`
		Gate       bool   `json:"gate"`
		HotfixFrom string `json:"hotfix_from"`
		HotfixTo   string `json:"hotfix_to"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	if len(cfg.BuildServices()) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no build services — release pipelines build & promote images; this project pulls prebuilt images"})
		return
	}
	envs := envorder.Resolve(cfg.EnvNames(), cfg.Project.EnvOrder, h.tierNames(ws))
	if len(envs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project has no environments"})
		return
	}
	if body.Template == "hotfix" && len(envs) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hotfix needs at least two environments"})
		return
	}
	gname, stages := pipelines.Generate(pipelines.GenerateOptions{
		Template: body.Template, Envs: envs, BumpPart: body.BumpPart, Gate: body.Gate,
		HotfixFrom: body.HotfixFrom, HotfixTo: body.HotfixTo,
	})
	writeJSON(w, http.StatusOK, pipelines.Pipeline{
		Workspace: ws, Project: name, Name: gname, Stages: stages, Enabled: true,
	})
}

// Phase 9 — Deployment Pipelines. Pipelines are project-scoped (workspace +
// project keys) and surfaced in the Edit Project "Pipelines" tab. Viewing needs
// viewer+, managing/running needs operator+ (pipelines edit config and trigger
// deploys). RBAC is enforced per-project via EffectiveRole, mirroring RunAction.

// pipelineRole resolves the caller's effective workspace-tier role for a project.
func (h *Handler) pipelineRole(r *http.Request, ws, project string) string {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		return ""
	}
	return h.auth.EffectiveRole(claims.UserID, claims.Role, ws, project)
}

// GET /api/workspaces/{workspace}/projects/{name}/pipelines
func (h *Handler) ListPipelines(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	list, err := pipelines.List(h.db, ws, name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// POST /api/workspaces/{workspace}/projects/{name}/pipelines
func (h *Handler) CreatePipeline(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var body pipelines.Pipeline
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	body.Workspace, body.Project = ws, name
	created, err := pipelines.Create(h.db, body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// PUT /api/workspaces/{workspace}/projects/{name}/pipelines/{id}
func (h *Handler) UpdatePipeline(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	id, ok := h.ownedPipeline(w, r, ws, name)
	if !ok {
		return
	}
	var body pipelines.Pipeline
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	body.Workspace, body.Project = ws, name
	updated, err := pipelines.Update(h.db, id, body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DELETE /api/workspaces/{workspace}/projects/{name}/pipelines/{id}
func (h *Handler) DeletePipeline(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	id, ok := h.ownedPipeline(w, r, ws, name)
	if !ok {
		return
	}
	if err := pipelines.Delete(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/workspaces/{workspace}/projects/{name}/pipelines/{id}/runs
func (h *Handler) ListPipelineRuns(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	id, ok := h.ownedPipeline(w, r, ws, name)
	if !ok {
		return
	}
	limit := 30
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	runs, err := pipelines.ListRuns(h.db, id, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// GET /api/workspaces/{workspace}/projects/{name}/pipelines/{id}/runs/{runId}
func (h *Handler) GetPipelineRun(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if _, ok := h.ownedPipeline(w, r, ws, name); !ok {
		return
	}
	runID, _ := strconv.ParseInt(r.PathValue("runId"), 10, 64)
	run, err := pipelines.GetRun(h.db, runID)
	if err != nil || run == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// ownedPipeline parses {id} and confirms the pipeline belongs to (ws, project).
// On any failure it writes the response and returns ok=false.
func (h *Handler) ownedPipeline(w http.ResponseWriter, r *http.Request, ws, project string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return 0, false
	}
	p, err := pipelines.Get(h.db, id)
	if err != nil || p == nil || p.Workspace != ws || p.Project != project {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not found"})
		return 0, false
	}
	return id, true
}

// StartPipelineRun triggers a run in the BACKGROUND and returns its id immediately,
// so the caller can open (and later reopen) a polling log/status view bound to the
// run without holding a socket open. This is the uniform path used by the project
// page, the Edit-Project pipelines tab, and webhooks — closing the log window never
// affects the run. POST /api/workspaces/{workspace}/projects/{name}/pipelines/{id}/run.
func (h *Handler) StartPipelineRun(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	id, ok := h.ownedPipeline(w, r, ws, name)
	if !ok {
		return
	}
	p, _ := pipelines.Get(h.db, id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not found"})
		return
	}
	username := "system"
	var uid int64
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		username, uid = claims.Username, claims.UserID
	}
	runID, _ := pipelines.CreateRun(h.db, pipelines.Run{
		PipelineID: p.ID, Workspace: ws, Project: name, Trigger: "manual",
		Username: username, Status: "running", StartedAt: time.Now().UnixMilli(),
	})
	h.db.Exec( //nolint:errcheck
		"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
		uid, username, h.resourcePrefix(ws, name), "pipeline:"+p.Name, "",
	)
	go h.continueRun(runID, p, nil, 0, io.Discard)
	writeJSON(w, http.StatusAccepted, map[string]int64{"run_id": runID})
}

// ── Webhooks (9a) ─────────────────────────────────────────────────────────────

// GET /api/workspaces/{workspace}/projects/{name}/pipelines/{id}/webhooks
func (h *Handler) ListPipelineWebhooks(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	id, ok := h.ownedPipeline(w, r, ws, name)
	if !ok {
		return
	}
	list, err := pipelines.ListWebhooks(h.db, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// POST /api/workspaces/{workspace}/projects/{name}/pipelines/{id}/webhooks
// Returns the webhook with its raw token (shown once).
func (h *Handler) CreatePipelineWebhook(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	id, ok := h.ownedPipeline(w, r, ws, name)
	if !ok {
		return
	}
	var body struct {
		Secret string `json:"secret"`
	}
	readJSON(r, &body) //nolint:errcheck — secret is optional
	created, err := pipelines.CreateWebhook(h.db, pipelines.Webhook{
		PipelineID: id, Workspace: ws, Project: name, Secret: body.Secret,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// DELETE /api/workspaces/{workspace}/projects/{name}/pipelines/{id}/webhooks/{whId}
func (h *Handler) DeletePipelineWebhook(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	if _, ok := h.ownedPipeline(w, r, ws, name); !ok {
		return
	}
	whID, _ := strconv.ParseInt(r.PathValue("whId"), 10, 64)
	if err := pipelines.DeleteWebhook(h.db, whID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// InboundWebhook is the PUBLIC trigger endpoint (no JWT). It resolves the webhook
// by URL token, optionally verifies an HMAC-SHA256 signature, then runs the bound
// pipeline in the background (trigger=webhook). POST /api/pipelines/hooks/{token}.
func (h *Handler) InboundWebhook(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	wh, err := pipelines.GetWebhookByToken(h.db, token)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "webhook not found"})
		return
	}

	// Read the body (bounded) for optional signature verification.
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if wh.Secret != "" {
		sig := r.Header.Get("X-Hub-Signature-256") // GitHub/Gitea style: "sha256=<hex>"
		mac := hmac.New(sha256.New, []byte(wh.Secret))
		mac.Write(body)
		expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if sig == "" || !hmac.Equal([]byte(sig), []byte(expected)) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid signature"})
			return
		}
	}

	p, err := pipelines.Get(h.db, wh.PipelineID)
	if err != nil || p == nil || !p.Enabled {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not found or disabled"})
		return
	}

	pipelines.TouchWebhook(h.db, wh.ID)
	h.db.Exec( //nolint:errcheck
		"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
		0, "webhook", h.resourcePrefix(wh.Workspace, wh.Project), "pipeline:"+p.Name, "",
	)
	// Run in the background — the caller (CI) just gets an accepted ack. Per-stage
	// output is still captured into the run record for the history view.
	go h.executePipelineRun(p, "webhook", "webhook", io.Discard)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "triggered", "pipeline": p.Name})
}

// executePipelineRun creates a run record, executes the pipeline from the start
// (streaming live output to out), finalizes the record, and invalidates image
// caches for any update/deploy stage. Shared by the interactive WS run and
// webhook-triggered background runs. Returns the outcome (ok|fail|awaiting).
func (h *Handler) executePipelineRun(p *pipelines.Pipeline, trigger, username string, out io.Writer) string {
	runID, _ := pipelines.CreateRun(h.db, pipelines.Run{
		PipelineID: p.ID, Workspace: p.Workspace, Project: p.Project, Trigger: trigger,
		Username: username, Status: "running", StartedAt: time.Now().UnixMilli(),
	})
	return h.continueRun(runID, p, nil, 0, out)
}

// ── Live-run cancel registry ──────────────────────────────────────────────────

// registerRun creates a cancellable context for a run and stores its cancel func
// so CancelPipelineRun can stop it. The caller must defer unregisterRun(id).
func (h *Handler) registerRun(id int64) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	h.runMu.Lock()
	h.runCancels[id] = cancel
	h.runMu.Unlock()
	return ctx
}

func (h *Handler) unregisterRun(id int64) {
	h.runMu.Lock()
	delete(h.runCancels, id)
	h.runMu.Unlock()
}

// cancelRun signals a live run to stop (killing its in-flight docker process).
// Returns false if the run isn't currently executing in this process.
func (h *Handler) cancelRun(id int64) bool {
	h.runMu.Lock()
	cancel, ok := h.runCancels[id]
	h.runMu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// continueRun executes stages from startIdx, persisting per-stage progress LIVE
// (running → ok/fail, with throttled in-flight output) so a polling client or a
// reopened log window can track the run independent of any socket, then finalizes.
// prior is the already-recorded stages (gate resume); nil for a fresh run. The run
// is registered for cancellation for the lifetime of this call.
func (h *Handler) continueRun(runID int64, p *pipelines.Pipeline, prior []pipelines.StageResult, startIdx int, out io.Writer) string {
	ctx := h.registerRun(runID)
	defer h.unregisterRun(runID)
	// "Started" fires once, at the genuine start of a run (startIdx 0) — not when
	// resuming after a gate approval (startIdx > 0).
	if startIdx == 0 {
		h.notifyPipelineRun(p, "started", nil)
	}
	progress := func(seg []pipelines.StageResult) {
		all := append(append([]pipelines.StageResult{}, prior...), seg...)
		pipelines.UpdateRunProgress(h.db, runID, all) //nolint:errcheck
	}
	results, outcome := pipelines.Execute(ctx, h.bridge, *p, out, startIdx, progress)
	all := append(append([]pipelines.StageResult{}, prior...), results...)
	h.finalizeRun(runID, p, all, outcome)
	return outcome
}

// notifyPipelineRun fans out a run-event notification ("started"|"succeeded"|
// "failed") to the pipeline's configured channels, when that event is enabled.
// The message carries the workspace/project display names, environment, pipeline
// name, and — on failure — the stage that failed plus a short error excerpt.
// No-op when no dispatcher, no channels, or the event is off.
func (h *Handler) notifyPipelineRun(p *pipelines.Pipeline, event string, stages []pipelines.StageResult) {
	if h.notifier == nil || len(p.NotifyChannelIDs) == 0 {
		return
	}
	ev := p.NotifyEvents
	on := (event == "started" && ev.Started) || (event == "succeeded" && ev.Succeeded) || (event == "failed" && ev.Failed)
	if !on {
		return
	}

	wsName := workspace.WorkspaceDisplayName(h.workspacesDir, p.Workspace)
	projName := p.Project
	if cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, p.Workspace, p.Project)); err == nil && cfg.Project.Name != "" {
		projName = cfg.Project.Name
	}

	// Common context line shared by every event.
	ctxLine := fmt.Sprintf("Workspace: %s · Project: %s · Pipeline: %s", wsName, projName, p.Name)

	var n notify.Notification
	switch event {
	case "started":
		n = notify.Notification{
			Title: fmt.Sprintf("Pipeline started: %s", p.Name),
			Body:  "▶ Pipeline run started.\n" + ctxLine,
			Level: notify.LevelInfo,
		}
	case "succeeded":
		n = notify.Notification{
			Title: fmt.Sprintf("Pipeline succeeded: %s", p.Name),
			Body:  "✓ Pipeline completed successfully.\n" + ctxLine,
			Level: notify.LevelSuccess,
		}
	case "failed":
		// Identify the failing stage + a short tail of its output.
		stageLabel, env, errMsg := "", "", ""
		for i := range stages {
			if stages[i].Status == pipelines.OutcomeFail {
				stageLabel, env, errMsg = stages[i].Label, stages[i].Env, shortErr(stages[i].Output)
				break
			}
		}
		body := "❌ Pipeline failed."
		if stageLabel != "" {
			body += fmt.Sprintf("\nFailed stage: %s", stageLabel)
		}
		if env != "" {
			body += fmt.Sprintf("\nEnvironment: %s", env)
		}
		body += "\n" + ctxLine
		if errMsg != "" {
			body += "\nError: " + errMsg
		}
		n = notify.Notification{
			Title: fmt.Sprintf("Pipeline failed: %s", p.Name),
			Body:  body,
			Level: notify.LevelFailure,
		}
	default:
		return
	}
	h.notifier.DispatchToChannels(p.NotifyChannelIDs, n)
}

// shortErr returns a compact single-paragraph excerpt of a failing stage's
// output: the last non-empty lines, trimmed to a sane length for a notification.
func shortErr(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	lines := strings.Split(out, "\n")
	// Keep the last few non-empty lines (the error is usually at the tail).
	var tail []string
	for i := len(lines) - 1; i >= 0 && len(tail) < 4; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			tail = append([]string{s}, tail...)
		}
	}
	msg := strings.Join(tail, " ")
	const max = 400
	if len(msg) > max {
		msg = msg[:max] + "…"
	}
	return msg
}

// finalizeRun persists a run's outcome (finish time left NULL while awaiting) and
// invalidates image caches for deploy/update stages.
func (h *Handler) finalizeRun(runID int64, p *pipelines.Pipeline, stages []pipelines.StageResult, outcome string) {
	finished := time.Now().UnixMilli()
	if outcome == pipelines.OutcomeAwaiting {
		finished = 0
	}
	pipelines.UpdateRun(h.db, pipelines.Run{ //nolint:errcheck
		ID: runID, PipelineID: p.ID, Status: outcome, Stages: stages, FinishedAt: finished,
	})
	for _, s := range stages {
		if (s.Type == "update" || s.Type == "deploy") && s.Status == "ok" {
			h.imgCache.Invalidate(p.Workspace, p.Project, s.Env)
			h.recordDeploy(p.Workspace, p.Project, s.Env, "pipeline")
		}
	}
	// Run-event alerting (only on a terminal outcome — a gate pause is not a finish).
	switch outcome {
	case pipelines.OutcomeOK:
		h.notifyPipelineRun(p, "succeeded", stages)
	case pipelines.OutcomeFail:
		h.notifyPipelineRun(p, "failed", stages)
	}
}

// resumePipelineRun continues an awaiting run after gate approval: it flips the
// trailing awaiting gate to ok and executes the remaining stages in the
// background. Runs to completion (or the next gate).
func (h *Handler) resumePipelineRun(run *pipelines.Run, p *pipelines.Pipeline) {
	if n := len(run.Stages); n > 0 && run.Stages[n-1].Status == pipelines.OutcomeAwaiting {
		run.Stages[n-1].Status = "ok"
	}
	h.continueRun(run.ID, p, run.Stages, len(run.Stages), io.Discard)
}

// POST /api/workspaces/{workspace}/projects/{name}/pipelines/{id}/runs/{runId}/approve
func (h *Handler) ApprovePipelineRun(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	p, run, ok := h.awaitingRun(w, r, ws, name)
	if !ok {
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, h.resourcePrefix(ws, name), "pipeline-approve:"+p.Name, "",
		)
	}
	go h.resumePipelineRun(run, p)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "resumed"})
}

// POST /api/workspaces/{workspace}/projects/{name}/pipelines/{id}/runs/{runId}/reject
func (h *Handler) RejectPipelineRun(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	p, run, ok := h.awaitingRun(w, r, ws, name)
	if !ok {
		return
	}
	// Flip the gate to rejected and mark the remaining stages skipped.
	if n := len(run.Stages); n > 0 && run.Stages[n-1].Status == pipelines.OutcomeAwaiting {
		run.Stages[n-1].Status = "rejected"
	}
	for i := len(run.Stages); i < len(p.Stages); i++ {
		s := p.Stages[i]
		run.Stages = append(run.Stages, pipelines.StageResult{Type: s.Type, Env: s.Env, Status: "skipped"})
	}
	pipelines.UpdateRun(h.db, pipelines.Run{ //nolint:errcheck
		ID: run.ID, PipelineID: p.ID, Status: "cancelled", Stages: run.Stages, FinishedAt: time.Now().UnixMilli(),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// awaitingRun loads the pipeline + run for an approve/reject, validating ownership
// and that the run is actually awaiting approval.
func (h *Handler) awaitingRun(w http.ResponseWriter, r *http.Request, ws, name string) (*pipelines.Pipeline, *pipelines.Run, bool) {
	id, ok := h.ownedPipeline(w, r, ws, name)
	if !ok {
		return nil, nil, false
	}
	p, _ := pipelines.Get(h.db, id)
	runID, _ := strconv.ParseInt(r.PathValue("runId"), 10, 64)
	run, err := pipelines.GetRun(h.db, runID)
	if err != nil || run == nil || run.PipelineID != id {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
		return nil, nil, false
	}
	if run.Status != pipelines.OutcomeAwaiting {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "run is not awaiting approval"})
		return nil, nil, false
	}
	return p, run, true
}

// CancelPipelineRun force-stops a running pipeline: it signals the run's context
// (which kills the in-flight stage's docker process), and the run's goroutine then
// finalizes it as "cancelled". If the run isn't executing in this process (e.g. it
// was already orphaned by a restart), the record is marked cancelled directly so
// the UI never shows a stranded "running". Operator+.
// POST /api/workspaces/{workspace}/projects/{name}/pipelines/{id}/runs/{runId}/cancel
func (h *Handler) CancelPipelineRun(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	id, ok := h.ownedPipeline(w, r, ws, name)
	if !ok {
		return
	}
	runID, _ := strconv.ParseInt(r.PathValue("runId"), 10, 64)
	run, err := pipelines.GetRun(h.db, runID)
	if err != nil || run == nil || run.PipelineID != id {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	if run.Status != "running" && run.Status != pipelines.OutcomeAwaiting {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "run is not active"})
		return
	}
	pname := ""
	if p, _ := pipelines.Get(h.db, id); p != nil {
		pname = p.Name
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, h.resourcePrefix(ws, name), "pipeline-cancel:"+pname, "",
		)
	}
	// Live run: signal it and let its goroutine finalize the record (kills the
	// in-flight docker process via the threaded context).
	if h.cancelRun(runID) {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
		return
	}
	// Not live here (orphaned, or awaiting with no goroutine) — mark it directly so
	// the UI clears, flipping any non-terminal stage to cancelled/skipped.
	pipelines.MarkRunCancelled(h.db, run, time.Now().UnixMilli()) //nolint:errcheck
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}
