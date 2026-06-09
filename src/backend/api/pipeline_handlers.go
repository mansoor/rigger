package api

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/pipelines"
)

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

// RunPipeline executes a pipeline over a WebSocket, streaming stage output live and
// recording the run. Mirrors RunAction: auth via the first WS message token, then
// operator+ RBAC. GET .../pipelines/{id}/run.
func (h *Handler) RunPipeline(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	pkey := h.resourcePrefix(ws, name)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var req struct {
		Token string `json:"token"`
	}
	if err := conn.ReadJSON(&req); err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("error: invalid request\n")) //nolint:errcheck
		return
	}
	claims, err := h.auth.ValidateToken(req.Token)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("error: unauthorized\n")) //nolint:errcheck
		return
	}
	eff := h.auth.EffectiveRole(claims.UserID, claims.Role, ws, name)
	if !auth.AtLeast(eff, auth.RoleOperator) {
		conn.WriteMessage(websocket.TextMessage, []byte("\033[31m✗ Error: operator role required to run pipelines\033[0m\n")) //nolint:errcheck
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("error: invalid id\n")) //nolint:errcheck
		return
	}
	p, err := pipelines.Get(h.db, id)
	if err != nil || p == nil || p.Workspace != ws || p.Project != name {
		conn.WriteMessage(websocket.TextMessage, []byte("error: pipeline not found\n")) //nolint:errcheck
		return
	}

	// Audit + create the run record (status running).
	h.db.Exec( //nolint:errcheck
		"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
		claims.UserID, claims.Username, pkey, "pipeline:"+p.Name, "",
	)
	startedAt := time.Now()
	runID, _ := pipelines.CreateRun(h.db, pipelines.Run{
		PipelineID: p.ID, Workspace: ws, Project: name, Trigger: "manual",
		Username: claims.Username, Status: "running", StartedAt: startedAt.UnixMilli(),
	})

	conn.WriteMessage(websocket.TextMessage, []byte("\033[1mRunning pipeline \""+p.Name+"\"…\033[0m\n")) //nolint:errcheck

	// Stream stage output to the socket via a pipe (same pattern as RunAction).
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, readErr := pr.Read(buf)
			if n > 0 {
				conn.WriteMessage(websocket.TextMessage, buf[:n]) //nolint:errcheck
			}
			if readErr != nil {
				break
			}
		}
	}()

	results, ok := pipelines.Execute(h.bridge, *p, pw)
	pw.Close()
	<-done

	status := "ok"
	if !ok {
		status = "fail"
	}
	var marker bytes.Buffer
	if ok {
		marker.WriteString("\n\033[32m✓ Pipeline \"" + p.Name + "\" completed successfully.\033[0m\n")
	} else {
		marker.WriteString("\n\033[31m✗ Pipeline \"" + p.Name + "\" failed.\033[0m\n")
	}
	conn.WriteMessage(websocket.TextMessage, marker.Bytes()) //nolint:errcheck

	pipelines.UpdateRun(h.db, pipelines.Run{ //nolint:errcheck
		ID: runID, PipelineID: p.ID, Status: status, Stages: results,
		FinishedAt: time.Now().UnixMilli(),
	})

	// A deploy stage pulls fresh images; invalidate the image-check cache for each
	// deployed env so the next poll re-checks (matches RunAction's update path).
	for _, s := range p.Stages {
		if s.Type == "deploy" {
			h.imgCache.Invalidate(ws, name, s.Env)
		}
	}
}
