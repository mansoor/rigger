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

	"github.com/mansoor/rigger/ui/internal/previews"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

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
