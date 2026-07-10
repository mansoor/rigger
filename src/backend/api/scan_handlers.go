package api

import (
	"bytes"
	"log"
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
		Repo       string   `json:"repo"`
		Branch     string   `json:"branch"`
		Subdir     string   `json:"subdir"`          // optional: app lives in this subdir (monorepo/nested); "" = auto-discover
		Overlays   []string `json:"compose_overlays"` // optional multi-file compose overlays to merge; nil = auto (safe override)
		ProviderID int64    `json:"provider_id"`     // optional git provider for private repos
	}
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Repo) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo is required"})
		return
	}

	// Optional private-repo credentials (Phase 12): resolve the chosen git provider
	// into a per-clone auth (token/SSH key) used only for this scan.
	repo := strings.TrimSpace(body.Repo)
	var auth *gitsync.Auth
	if body.ProviderID != 0 {
		p, perr := gitproviders.Get(h.db, h.cryptoKey, body.ProviderID)
		if perr != nil || p == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "git provider not found"})
			return
		}
		// Adapt the URL to the provider's auth form (e.g. HTTPS URL + SSH deploy key →
		// git@host:owner/repo.git) so the credential actually applies.
		repo = p.NormalizeRepoURL(repo)
		if auth, perr = p.BuildAuth(repo); perr != nil {
			log.Printf("scan: build auth for provider %d (%s) repo %q: %v", body.ProviderID, p.Kind, repo, perr)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "git provider auth failed: " + perr.Error()})
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

	var syncLog bytes.Buffer
	src, err := gitsync.Sync(tmp, repo, strings.TrimSpace(body.Branch), auth, &syncLog)
	if err != nil {
		log.Printf("scan: sync repo %q branch %q (provider %d): %v\n--- git output ---\n%s", repo, body.Branch, body.ProviderID, err, syncLog.String())
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "couldn't scan the repository: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, detect.DetectRepo(src, strings.TrimSpace(body.Subdir), body.Overlays))
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
