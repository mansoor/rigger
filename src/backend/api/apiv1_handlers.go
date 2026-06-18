package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/apikey"
	"github.com/mansoor/rigger/ui/internal/pipelines"
	"github.com/mansoor/rigger/ui/internal/shell"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// ── /api/v1 external REST API (API-key authenticated) ──────────────────────────────
//
// This surface is authenticated by API keys (internal/apikey), NOT the JWT/cookie auth
// the web UI uses. APIKeyMiddleware resolves the presented key and stashes it; each
// handler then enforces the specific operation scope + project access + rate limit via
// apiGate. Operations map onto the same bridge commands the UI drives, so behaviour is
// identical — only the auth and the (read/operate/pipeline) permission model differ.

type apiKeyCtxKey struct{}

// APIKeyMiddleware authenticates an /api/v1 request by its bearer API key. It only
// resolves + stashes the key (and stamps last-used); scope/project/rate checks happen
// per-handler via apiGate, since they depend on the endpoint's operation and the
// project in the path.
func (h *Handler) APIKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			apiErr(w, http.StatusUnauthorized, "missing API key (send 'Authorization: Bearer rgk_…')")
			return
		}
		key, err := apikey.GetByHash(h.db, apikey.Hash(raw))
		if err != nil {
			apiErr(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		apikey.TouchLastUsed(h.db, key.ID, time.Now().Unix())
		ctx := context.WithValue(r.Context(), apiKeyCtxKey{}, key)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken extracts a key from "Authorization: Bearer <key>" or the X-API-Key header.
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

func apiKeyFromCtx(r *http.Request) *apikey.Key {
	k, _ := r.Context().Value(apiKeyCtxKey{}).(*apikey.Key)
	return k
}

// apiGate runs the full per-request authorization for one operation against one project:
// scope + expiry/enabled + project access, then the per-key-per-project rate limit. It
// writes the appropriate error (401/403/429) and returns false when the request must
// stop. For non-project ops (e.g. listing all projects) pass ws/proj "".
func (h *Handler) apiGate(w http.ResponseWriter, r *http.Request, op, ws, proj string) (*apikey.Key, bool) {
	key := apiKeyFromCtx(r)
	if key == nil { // middleware guarantees this, but be defensive
		apiErr(w, http.StatusUnauthorized, "unauthenticated")
		return nil, false
	}
	switch err := key.Authorize(op, ws, proj, time.Now()); err {
	case nil:
	case apikey.ErrDisabled, apikey.ErrExpired:
		apiErr(w, http.StatusUnauthorized, err.Error())
		return nil, false
	default: // ErrScope, ErrProject
		apiErr(w, http.StatusForbidden, err.Error())
		return nil, false
	}
	// Rate limit: per key, per project (global bucket "*" for non-project ops).
	bucket := "*"
	if proj != "" {
		bucket = ws + "/" + proj
	}
	if !h.apiRL.Allow(key.ID, bucket, key.RateLimit, time.Now()) {
		w.Header().Set("Retry-After", "60")
		apiErr(w, http.StatusTooManyRequests, "rate limit exceeded (max "+strconv.Itoa(key.RateLimit)+" req/min for this project)")
		return nil, false
	}
	return key, true
}

func apiErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// apiGateList authorizes a LISTING request that has no single concrete project (e.g.
// list projects/pipelines in a workspace). It checks enabled/expiry + scope + (for a
// workspace-confined key) that ws matches, then rate-limits on the workspace bucket.
// The handler still filters results per-project via key.CanAccess.
func (h *Handler) apiGateList(w http.ResponseWriter, r *http.Request, op, ws string) (*apikey.Key, bool) {
	key := apiKeyFromCtx(r)
	if key == nil {
		apiErr(w, http.StatusUnauthorized, "unauthenticated")
		return nil, false
	}
	now := time.Now()
	if !key.Enabled {
		apiErr(w, http.StatusUnauthorized, apikey.ErrDisabled.Error())
		return nil, false
	}
	if key.ExpiresAt > 0 && now.Unix() >= key.ExpiresAt {
		apiErr(w, http.StatusUnauthorized, apikey.ErrExpired.Error())
		return nil, false
	}
	if !key.HasScope(op) {
		apiErr(w, http.StatusForbidden, apikey.ErrScope.Error())
		return nil, false
	}
	if key.Workspace != "" && key.Workspace != ws {
		apiErr(w, http.StatusForbidden, apikey.ErrProject.Error())
		return nil, false
	}
	if !h.apiRL.Allow(key.ID, ws, key.RateLimit, now) {
		w.Header().Set("Retry-After", "60")
		apiErr(w, http.StatusTooManyRequests, "rate limit exceeded (max "+strconv.Itoa(key.RateLimit)+" req/min)")
		return nil, false
	}
	return key, true
}

// ── Read endpoints ─────────────────────────────────────────────────────────────────

// ListProjectsV1: GET /api/v1/workspaces/{workspace}/projects — the projects in this
// workspace the key may access (filtered by its project-access list).
func (h *Handler) ListProjectsV1(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	key, ok := h.apiGateList(w, r, apikey.OpProjectsList, ws)
	if !ok {
		return
	}
	type projOut struct {
		Workspace string   `json:"workspace"`
		Project   string   `json:"project"`
		Type      string   `json:"type"`
		Envs      []string `json:"envs"`
	}
	out := []projOut{}
	projs, _ := workspace.ListProjects(h.workspacesDir, ws)
	for _, p := range projs {
		if !key.CanAccess(ws, p.Name) {
			continue
		}
		cfg := h.readProjectConfig(ws, p.Name)
		out = append(out, projOut{
			Workspace: ws, Project: p.Name,
			Type: cfg.Project.Type, Envs: cfg.envNames(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ListServicesV1: GET /api/v1/projects/{workspace}/{project}/envs/{env}/services —
// the services defined for the project (from config.json), with their source kind.
func (h *Handler) ListServicesV1(w http.ResponseWriter, r *http.Request) {
	ws, proj := r.PathValue("workspace"), r.PathValue("project")
	if _, ok := h.apiGate(w, r, apikey.OpServicesList, ws, proj); !ok {
		return
	}
	cfg := h.readProjectConfig(ws, proj)
	if cfg.Project.Name == "" && len(cfg.Services) == 0 {
		apiErr(w, http.StatusNotFound, "project not found")
		return
	}
	type svcOut struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"` // build | image
		Image     string `json:"image,omitempty"`
		WebRouted bool   `json:"web_routed"`
		Port      string `json:"port,omitempty"`
	}
	out := []svcOut{}
	for _, s := range cfg.Services {
		kind := "image"
		if s.Build != nil {
			kind = "build"
		}
		out = append(out, svcOut{Name: s.Name, Kind: kind, Image: s.Image, WebRouted: s.WebRouted, Port: s.Port})
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace": ws, "project": proj, "services": out})
}

// GetServiceLogsV1: GET .../services/{service}/logs?tail=N — a bounded, non-streaming
// log snapshot for one service (default 200 lines, capped at 2000).
func (h *Handler) GetServiceLogsV1(w http.ResponseWriter, r *http.Request) {
	ws, proj, env, svc := r.PathValue("workspace"), r.PathValue("project"), r.PathValue("env"), r.PathValue("service")
	if _, ok := h.apiGate(w, r, apikey.OpLogsRead, ws, proj); !ok {
		return
	}
	tail := 200
	if q := r.URL.Query().Get("tail"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			tail = n
		}
	}
	if tail > 2000 {
		tail = 2000
	}
	var buf bytes.Buffer
	err := h.bridge.Run(shell.RunOptions{
		Workspace: ws, Project: proj, Command: "logtail", Env: env,
		Extra: []string{svc, strconv.Itoa(tail)}, Stdout: &buf, Stderr: &buf,
	})
	if err != nil {
		apiErr(w, http.StatusBadGateway, "failed to read logs: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace": ws, "project": proj, "env": env, "service": svc,
		"tail": tail, "logs": buf.String(),
	})
}

// ── Operate endpoints ──────────────────────────────────────────────────────────────

// apiActions maps a URL action to its (bridge command, required scope). "inactivate"
// is compose `down` (stop + remove); "stop" leaves containers in place.
var apiActions = map[string]struct {
	command string
	scope   string
}{
	"start":      {"start", apikey.OpEnvStart},
	"stop":       {"stop", apikey.OpEnvStop},
	"restart":    {"restart", apikey.OpEnvRestart},
	"refresh":    {"refresh", apikey.OpEnvRefresh},
	"inactivate": {"down", apikey.OpEnvInactivate},
	"backup":     {"backup", apikey.OpEnvBackup},
}

// RunActionV1: POST .../envs/{env}/actions/{action} — runs a lifecycle action
// synchronously and returns its captured output. action ∈ apiActions.
func (h *Handler) RunActionV1(w http.ResponseWriter, r *http.Request) {
	ws, proj, env, action := r.PathValue("workspace"), r.PathValue("project"), r.PathValue("env"), r.PathValue("action")
	a, known := apiActions[action]
	if !known {
		apiErr(w, http.StatusNotFound, "unknown action "+strconv.Quote(action))
		return
	}
	key, ok := h.apiGate(w, r, a.scope, ws, proj)
	if !ok {
		return
	}
	opts := shell.RunOptions{Workspace: ws, Project: proj, Command: a.command, Env: env}
	var buf bytes.Buffer
	opts.Stdout, opts.Stderr = &buf, &buf
	if a.command == "backup" {
		opts.Extra = []string{"all"}
		opts.Trigger = "manual"
		opts.ScheduleID = "api"
		opts.ScheduleName = "API: " + key.Name
	}
	// Audit the action under the key's name (the resource prefix matches the UI path).
	h.db.Exec(`INSERT INTO audit_log (username, project, command, env) VALUES (?,?,?,?)`, //nolint:errcheck
		"apikey:"+key.Name, h.resourcePrefix(ws, proj), a.command, env)

	if err := h.bridge.Run(opts); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"status": "error", "error": err.Error(), "output": buf.String()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "action": action, "env": env, "output": buf.String()})
}

// ListPipelinesV1: GET .../projects/{project}/pipelines — the project's pipelines, so a
// client can discover the id to pass to the run endpoint.
func (h *Handler) ListPipelinesV1(w http.ResponseWriter, r *http.Request) {
	ws, proj := r.PathValue("workspace"), r.PathValue("project")
	if _, ok := h.apiGate(w, r, apikey.OpPipelineList, ws, proj); !ok {
		return
	}
	pls, err := pipelines.List(h.db, ws, proj)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type plOut struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		Stages  int    `json:"stages"`
	}
	out := []plOut{}
	for _, p := range pls {
		out = append(out, plOut{ID: p.ID, Name: p.Name, Enabled: p.Enabled, Stages: len(p.Stages)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace": ws, "project": proj, "pipelines": out})
}

// RunPipelineV1: POST .../pipelines/{id}/runs — creates a pipeline run in the background
// and returns its run id (mirrors the UI trigger; poll status via the UI for now).
func (h *Handler) RunPipelineV1(w http.ResponseWriter, r *http.Request) {
	ws, proj := r.PathValue("workspace"), r.PathValue("project")
	key, ok := h.apiGate(w, r, apikey.OpPipelineRun, ws, proj)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiErr(w, http.StatusBadRequest, "invalid pipeline id")
		return
	}
	p, _ := pipelines.Get(h.db, id)
	if p == nil || p.Workspace != ws || p.Project != proj {
		apiErr(w, http.StatusNotFound, "pipeline not found")
		return
	}
	runID, err := pipelines.CreateRun(h.db, pipelines.Run{
		PipelineID: p.ID, Workspace: ws, Project: proj, Trigger: "api",
		Username: "apikey:" + key.Name, Status: "running", StartedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.db.Exec(`INSERT INTO audit_log (username, project, command, env) VALUES (?,?,?,?)`, //nolint:errcheck
		"apikey:"+key.Name, h.resourcePrefix(ws, proj), "pipeline:"+p.Name, "")
	go h.continueRun(runID, p, nil, 0, io.Discard)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "pipeline": p.Name, "run_id": runID})
}

// CancelPipelineRunV1: POST .../pipelines/{id}/runs/{runId}/cancel — cancels a specific
// run (kills the in-flight stage's docker process if it's live here, else marks the
// record cancelled). Mirrors the internal CancelPipelineRun, gated by the API key.
func (h *Handler) CancelPipelineRunV1(w http.ResponseWriter, r *http.Request) {
	ws, proj := r.PathValue("workspace"), r.PathValue("project")
	key, ok := h.apiGate(w, r, apikey.OpPipelineCancel, ws, proj)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiErr(w, http.StatusBadRequest, "invalid pipeline id")
		return
	}
	p, _ := pipelines.Get(h.db, id)
	if p == nil || p.Workspace != ws || p.Project != proj {
		apiErr(w, http.StatusNotFound, "pipeline not found")
		return
	}
	runID, _ := strconv.ParseInt(r.PathValue("runId"), 10, 64)
	run, rerr := pipelines.GetRun(h.db, runID)
	if rerr != nil || run == nil || run.PipelineID != id {
		apiErr(w, http.StatusNotFound, "run not found")
		return
	}
	if run.Status != "running" && run.Status != pipelines.OutcomeAwaiting {
		apiErr(w, http.StatusConflict, "run is not active")
		return
	}
	h.db.Exec(`INSERT INTO audit_log (username, project, command, env) VALUES (?,?,?,?)`, //nolint:errcheck
		"apikey:"+key.Name, h.resourcePrefix(ws, proj), "pipeline-cancel:"+p.Name, "")
	// Live run in this process: signal it; its goroutine finalizes the record.
	if h.cancelRun(runID) {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "cancelling", "run_id": runID})
		return
	}
	// Orphaned/awaiting with no goroutine — mark cancelled directly.
	pipelines.MarkRunCancelled(h.db, run, time.Now().UnixMilli()) //nolint:errcheck
	writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled", "run_id": runID})
}

// ── helpers ────────────────────────────────────────────────────────────────────────

// apiConfig is a minimal view of a project's config.json for the read endpoints.
type apiConfig struct {
	Project struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"project"`
	Services []struct {
		Name      string `json:"name"`
		Image     string `json:"image"`
		Port      string `json:"port"`
		WebRouted bool   `json:"web_routed"`
		Build     *struct {
			Template string `json:"template"`
		} `json:"build"`
	} `json:"services"`
	Environments map[string]json.RawMessage `json:"environments"`
}

func (c apiConfig) envNames() []string {
	out := make([]string, 0, len(c.Environments))
	for e := range c.Environments {
		out = append(out, e)
	}
	return out
}

func (h *Handler) readProjectConfig(ws, proj string) apiConfig {
	var c apiConfig
	data, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, ws, proj))
	if err == nil {
		json.Unmarshal(data, &c) //nolint:errcheck
	}
	return c
}
