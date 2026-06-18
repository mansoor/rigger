package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/previews"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// previewHookPathPrefix is the public webhook path; the frontend builds the full
// URL as {origin}{prefix}{token} when a webhook is created (token shown once).
const previewHookPathPrefix = "/api/previews/hooks/"

// GetPreviewSettings returns a project's preview config, its webhooks (no raw
// token), and its active preview environments. Any project member may read.
//
// GET /api/workspaces/{workspace}/projects/{name}/preview
func (h *Handler) GetPreviewSettings(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "viewer role required"})
		return
	}
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	hooks, _ := previews.ListWebhooks(h.db, ws, name)
	envs, _ := previews.ListForProject(h.db, ws, name)
	writeJSON(w, http.StatusOK, map[string]any{
		"config":           cfg.Project.Preview, // nil when never configured
		"webhooks":         hooks,
		"previews":         envs,
		"hook_path_prefix": previewHookPathPrefix,
	})
}

// SetPreviewConfig writes a project's preview config into config.json
// (project.preview), preserving every other field via a raw-JSON edit.
//
// PUT /api/workspaces/{workspace}/projects/{name}/preview
func (h *Handler) SetPreviewConfig(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var pc wsconfig.PreviewConfig
	if err := readJSON(r, &pc); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if pc.Provider == "" {
		pc.Provider = "github"
	}
	cfgPath := wspath.ConfigPath(h.workspacesDir, ws, name)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config parse: " + err.Error()})
		return
	}
	var proj map[string]any
	if len(root["project"]) > 0 {
		json.Unmarshal(root["project"], &proj) //nolint:errcheck
	}
	if proj == nil {
		proj = map[string]any{}
	}
	proj["preview"] = pc
	pb, err := json.Marshal(proj)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "project marshal: " + err.Error()})
		return
	}
	root["project"] = pb
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config marshal: " + err.Error()})
		return
	}
	if err := os.WriteFile(cfgPath, out, 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "write config: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// CreatePreviewWebhook mints a signed webhook for the project and returns it WITH
// the raw token (shown once). The frontend builds the full URL from hook_path.
//
// POST /api/workspaces/{workspace}/projects/{name}/preview/webhooks
func (h *Handler) CreatePreviewWebhook(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var body struct {
		Secret   string `json:"secret"`
		Provider string `json:"provider"`
	}
	readJSON(r, &body) //nolint:errcheck — both optional
	created, err := previews.CreateWebhook(h.db, previews.Webhook{
		Workspace: ws, Project: name, Secret: body.Secret, Provider: body.Provider,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"webhook": created, "hook_path": previewHookPathPrefix + created.Token})
}

// DeletePreviewWebhook removes a preview webhook.
//
// DELETE /api/workspaces/{workspace}/projects/{name}/preview/webhooks/{id}
func (h *Handler) DeletePreviewWebhook(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := previews.DeleteWebhook(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RedeployPreview manually redeploys an existing preview env (build+up) in the
// background. POST /api/workspaces/{workspace}/projects/{name}/preview/envs/{pr}/redeploy
func (h *Handler) RedeployPreview(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	pr, _ := strconv.Atoi(r.PathValue("pr"))
	row, _ := previews.GetByPR(h.db, ws, name, pr)
	if row == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "preview not found"})
		return
	}
	pc := &wsconfig.PreviewConfig{}
	if cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, ws, name)); err == nil && cfg.Project.Preview != nil {
		pc = cfg.Project.Preview
	}
	go func() {
		if _, derr := h.updatePreview(ws, name, pc, row, row.Branch, row.HeadSHA); derr != nil {
			fmt.Fprintf(os.Stderr, "RedeployPreview: %s/%s/%s: %v\n", ws, name, row.EnvKey, derr)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "redeploying", "env": row.EnvKey})
}

// TeardownPreviewEnv tears down a preview env now (down -v + cleanup + delete row)
// in the background. DELETE /api/workspaces/{workspace}/projects/{name}/preview/envs/{pr}
func (h *Handler) TeardownPreviewEnv(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	pr, _ := strconv.Atoi(r.PathValue("pr"))
	row, _ := previews.GetByPR(h.db, ws, name, pr)
	if row == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "preview not found"})
		return
	}
	go func() {
		if derr := h.teardownPreview(ws, name, row); derr != nil {
			fmt.Fprintf(os.Stderr, "TeardownPreviewEnv: %s/%s/%s: %v\n", ws, name, row.EnvKey, derr)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "tearing down", "env": row.EnvKey})
}

// InboundPreviewWebhook is the PUBLIC PR-event receiver (no JWT — authed by the
// URL token + provider HMAC signature). It resolves the webhook, verifies the
// signature, parses the provider's pull_request payload into a PreviewEvent, and
// dispatches the create/update/teardown lifecycle in the background (webhooks must
// return fast, so deploys never block the HTTP response).
//
// POST /api/previews/hooks/{token}
func (h *Handler) InboundPreviewWebhook(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	wh, err := previews.GetWebhookByToken(h.db, token)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "webhook not found"})
		return
	}

	// Read the body (bounded) for signature verification + payload parsing.
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if !verifyPreviewSignature(wh, r, body) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid signature"})
		return
	}

	// The project must still have previews enabled (the webhook outlives a config
	// toggle, so re-check every delivery).
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, wh.Workspace, wh.Project))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	pc := cfg.Project.Preview
	if pc == nil || !pc.Enabled {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "preview environments are not enabled for this project"})
		return
	}

	ev, ok := providerParse(wh.Provider, r.Header, body)
	if !ok {
		// A non-PR event (ping/push) or an action we don't act on — ack without work.
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
		return
	}

	previews.TouchWebhook(h.db, wh.ID)
	h.db.Exec( //nolint:errcheck
		"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
		0, "preview-webhook", h.resourcePrefix(wh.Workspace, wh.Project),
		"preview:"+ev.Action, previewEnvKey(ev.PRNumber),
	)

	// Dispatch in the background — the caller (GitHub) just gets a 202 ack; the
	// clone/build/up (or teardown) runs to completion independently.
	go func() {
		if derr := h.DispatchPreviewEvent(wh.Workspace, wh.Project, pc, ev); derr != nil {
			fmt.Fprintf(os.Stderr, "preview webhook: dispatch %s PR#%d for %s/%s: %v\n",
				ev.Action, ev.PRNumber, wh.Workspace, wh.Project, derr)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "action": ev.Action, "pr": ev.PRNumber})
}

// verifyPreviewSignature validates the provider's signature against the webhook
// secret. No secret configured ⇒ no verification (matches the pipeline webhook
// behavior). GitHub/Gitea use an HMAC-SHA256 body signature; GitLab uses a plain
// shared token header.
func verifyPreviewSignature(wh *previews.Webhook, r *http.Request, body []byte) bool {
	if wh.Secret == "" {
		return true
	}
	switch wh.Provider {
	case "gitlab":
		return hmac.Equal([]byte(r.Header.Get("X-Gitlab-Token")), []byte(wh.Secret))
	default: // github, gitea
		sig := r.Header.Get("X-Hub-Signature-256") // "sha256=<hex>"
		mac := hmac.New(sha256.New, []byte(wh.Secret))
		mac.Write(body)
		expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		return sig != "" && hmac.Equal([]byte(sig), []byte(expected))
	}
}

// providerParse normalizes a provider's PR webhook payload into a PreviewEvent.
// The bool is false for events we don't act on (non-PR events, unknown actions,
// or an unsupported provider) so the receiver acks-and-ignores. GitHub is the v1
// adapter; Gitea ships a GitHub-compatible pull_request payload so it shares it.
func providerParse(provider string, hdr http.Header, body []byte) (PreviewEvent, bool) {
	switch provider {
	case "gitlab":
		return PreviewEvent{}, false // not supported in v1 (GitHub-first)
	default: // github, gitea
		return parseGitHubPR(hdr, body)
	}
}

// parseGitHubPR parses a GitHub/Gitea `pull_request` event. A PR is treated as a
// fork PR when its head repo differs from the base repo.
func parseGitHubPR(hdr http.Header, body []byte) (PreviewEvent, bool) {
	event := hdr.Get("X-GitHub-Event")
	if event == "" {
		event = hdr.Get("X-Gitea-Event")
	}
	if event != "pull_request" {
		return PreviewEvent{}, false
	}
	var p struct {
		Action      string `json:"action"`
		Number      int    `json:"number"`
		PullRequest struct {
			Head struct {
				Ref  string `json:"ref"`
				SHA  string `json:"sha"`
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"head"`
			Base struct {
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return PreviewEvent{}, false
	}
	switch p.Action {
	case "opened", "reopened", "synchronize", "closed":
		// handled
	default:
		return PreviewEvent{}, false
	}
	head, base := p.PullRequest.Head.Repo.FullName, p.PullRequest.Base.Repo.FullName
	isFork := head != "" && base != "" && head != base
	return PreviewEvent{
		Action:   p.Action,
		PRNumber: p.Number,
		Branch:   p.PullRequest.Head.Ref,
		SHA:      p.PullRequest.Head.SHA,
		IsFork:   isFork,
	}, true
}
