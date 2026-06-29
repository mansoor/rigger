package api

import (
	"bytes"
	"net/http"
	"os"
	"strings"

	"github.com/mansoor/rigger/ui/internal/detect"
	"github.com/mansoor/rigger/ui/internal/gitproviders"
	"github.com/mansoor/rigger/ui/internal/gitsync"
)

// ScanRepo clones a source repository to a temp dir (shallow, time-bounded),
// statically detects its stack, and returns a draft service graph for the wizard
// to pre-fill. It never executes repo code; the temp checkout is deleted after.
//
// POST /api/scan-repo  { "repo": "...", "branch": "..." }
func (h *Handler) ScanRepo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo       string `json:"repo"`
		Branch     string `json:"branch"`
		ProviderID int64  `json:"provider_id"` // optional git provider for private repos
	}
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Repo) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo is required"})
		return
	}

	// Optional private-repo credentials (Phase 12): resolve the chosen git provider
	// into a per-clone auth (token/SSH key) used only for this scan.
	var auth *gitsync.Auth
	if body.ProviderID != 0 {
		p, perr := gitproviders.Get(h.db, h.cryptoKey, body.ProviderID)
		if perr != nil || p == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "git provider not found"})
			return
		}
		if auth, perr = p.BuildAuth(strings.TrimSpace(body.Repo)); perr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": perr.Error()})
			return
		}
		if auth.Cleanup != nil {
			defer auth.Cleanup()
		}
	}

	tmp, err := os.MkdirTemp("", "rigger-scan-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer os.RemoveAll(tmp)

	var log bytes.Buffer
	src, err := gitsync.Sync(tmp, strings.TrimSpace(body.Repo), strings.TrimSpace(body.Branch), auth, &log)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "couldn't clone repository: " + err.Error() +
				" — check the URL/branch, or select a Git provider for a private repo",
		})
		return
	}

	writeJSON(w, http.StatusOK, detect.Detect(src))
}

// ParseCompose parses pasted docker-compose.yml content into the same draft service
// graph a repo scan produces, so the New Project image stack can import a compose file
// (and round-trip back to YAML in the UI). Pure parse — no clone, no code execution.
//
// POST /api/parse-compose  { "content": "<yaml>" }
func (h *Handler) ParseCompose(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Content) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "compose content is required"})
		return
	}
	draft, err := detect.DetectComposeBytes([]byte(body.Content))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, draft)
}
