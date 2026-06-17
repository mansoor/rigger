package api

import (
	"bytes"
	"net/http"
	"os"
	"strings"

	"github.com/mansoor/rigger/ui/internal/detect"
	"github.com/mansoor/rigger/ui/internal/gitsync"
)

// ScanRepo clones a source repository to a temp dir (shallow, time-bounded),
// statically detects its stack, and returns a draft service graph for the wizard
// to pre-fill. It never executes repo code; the temp checkout is deleted after.
//
// POST /api/scan-repo  { "repo": "...", "branch": "..." }
func (h *Handler) ScanRepo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Repo) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo is required"})
		return
	}

	tmp, err := os.MkdirTemp("", "rigger-scan-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer os.RemoveAll(tmp)

	var log bytes.Buffer
	src, err := gitsync.Sync(tmp, strings.TrimSpace(body.Repo), strings.TrimSpace(body.Branch), &log)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "couldn't clone repository: " + err.Error() +
				" — check the URL/branch (private repos need a token in the HTTPS URL; SSH keys aren't supported yet)",
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
