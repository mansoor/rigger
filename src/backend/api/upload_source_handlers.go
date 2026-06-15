package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/detect"
	"github.com/mansoor/rigger/ui/internal/srcarchive"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// maxUploadSourceBytes hard-caps an uploaded source archive request body.
const maxUploadSourceBytes = int64(1) << 30 // 1 GiB

// sourceUploadsDir is the staging root for uploaded source archives, keyed by token,
// until the create call adopts one into a project (or the reaper removes it).
func (h *Handler) sourceUploadsDir() string { return filepath.Join(h.dataDir, "source-uploads") }

// UploadSource accepts an application source archive (.zip / .tar / .tar.gz),
// extracts it to a staging dir, runs the SAME detector the git-scan path uses, and
// returns the draft service graph + a one-time token. The wizard reviews the draft,
// then references the token on create so the archive is adopted into the project (see
// adoptUploadedSource). The archive is never executed — pure file reads.
//
// POST /api/upload-source  (multipart: field "archive")
func (h *Handler) UploadSource(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSourceBytes+(1<<20))
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "upload too large or malformed: " + err.Error()})
		return
	}
	file, _, err := r.FormFile("archive")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "archive field required"})
		return
	}
	defer file.Close()

	token, err := randomToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.reapSourceUploads() // best-effort sweep of abandoned stagings

	stage := filepath.Join(h.sourceUploadsDir(), token)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	archivePath := filepath.Join(stage, "archive")
	dst, err := os.Create(archivePath)
	if err != nil {
		os.RemoveAll(stage)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if _, err := io.Copy(dst, file); err != nil {
		dst.Close()
		os.RemoveAll(stage)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to store upload (too large?): " + err.Error()})
		return
	}
	dst.Close()

	srcDir := filepath.Join(stage, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		os.RemoveAll(stage)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := srcarchive.Extract(archivePath, srcDir); err != nil {
		os.RemoveAll(stage)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "couldn't read the archive (expected .zip / .tar / .tar.gz): " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"draft":        detect.Detect(srcDir),
		"upload_token": token,
	})
}

// adoptUploadedSource moves a staged uploaded archive (by token) into the project's
// _source/ so build can extract it. Called by the create handler after config.json is
// written, when source_kind == "upload". Copies (not renames) so it works across mount
// boundaries (staging is under dataDir, the project under workspacesDir).
func (h *Handler) adoptUploadedSource(workspace, projectKey, token string) error {
	if token == "" {
		return fmt.Errorf("missing source token")
	}
	if token != filepath.Base(token) || strings.ContainsAny(token, `/\.`) {
		return fmt.Errorf("invalid source token")
	}
	staged := filepath.Join(h.sourceUploadsDir(), token, "archive")
	if _, err := os.Stat(staged); err != nil {
		return fmt.Errorf("uploaded source not found or expired — re-upload and try again")
	}
	dest := wspath.SourceArchive(h.workspacesDir, workspace, projectKey)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := copyFile(staged, dest, 0o644); err != nil {
		return err
	}
	os.RemoveAll(filepath.Join(h.sourceUploadsDir(), token))
	return nil
}

// ReplaceSource overwrites an upload-source project's stored archive with a freshly
// uploaded one (validated by a throwaway extraction). The next Build wipes _src and
// re-extracts it — so updating an uploaded app is: replace source, then build.
// POST /api/workspaces/{workspace}/projects/{name}/source  (multipart: "archive")
func (h *Handler) ReplaceSource(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSourceBytes+(1<<20))
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "upload too large or malformed: " + err.Error()})
		return
	}
	file, _, err := r.FormFile("archive")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "archive field required"})
		return
	}
	defer file.Close()

	tmpDir, err := os.MkdirTemp("", "rigger-resrc-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer os.RemoveAll(tmpDir)
	archiveTmp := filepath.Join(tmpDir, "archive")
	dst, err := os.Create(archiveTmp)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if _, err := io.Copy(dst, file); err != nil {
		dst.Close()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to store upload (too large?): " + err.Error()})
		return
	}
	dst.Close()
	// Validate it's a real, safe archive before overwriting the project's source.
	if err := srcarchive.Extract(archiveTmp, filepath.Join(tmpDir, "verify")); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "couldn't read the archive: " + err.Error()})
		return
	}

	dest := wspath.SourceArchive(h.workspacesDir, ws, name)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := copyFile(archiveTmp, dest, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "replaced — build to apply the new source"})
}

// reapSourceUploads removes staging dirs older than 2h (abandoned scans that never
// reached create). Best-effort; errors are ignored.
func (h *Handler) reapSourceUploads() {
	entries, err := os.ReadDir(h.sourceUploadsDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-2 * time.Hour)
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			os.RemoveAll(filepath.Join(h.sourceUploadsDir(), e.Name()))
		}
	}
}

// randomToken returns a 32-hex-char opaque token (path-safe).
func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
