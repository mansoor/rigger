package api

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mansoor/rigger/ui/internal/settings"
)

// archiveNameSanitizer maps any character outside the safe set to "-".
var archiveNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// ── Job store ─────────────────────────────────────────────────────────────────

type BackupJob struct {
	ID        string     `json:"id"`
	Workspace string     `json:"workspace"`
	Status    string     `json:"status"` // running | completed | failed
	Error     string     `json:"error,omitempty"`
	Archive   string     `json:"archive,omitempty"` // basename of archive file when done
	SizeBytes int64      `json:"size_bytes,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	DoneAt    *time.Time `json:"done_at,omitempty"`
}

type JobStore struct {
	mu   sync.RWMutex
	jobs map[string]*BackupJob
}

func newJobStore() *JobStore { return &JobStore{jobs: make(map[string]*BackupJob)} }

func (s *JobStore) create(ws string) *BackupJob {
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	job := &BackupJob{ID: id, Workspace: ws, Status: "running", StartedAt: time.Now()}
	s.mu.Lock()
	s.jobs[id] = job
	s.mu.Unlock()
	return job
}

func (s *JobStore) get(id string) (*BackupJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

func (s *JobStore) update(id string, fn func(*BackupJob)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		fn(j)
	}
}

// ── Archive helpers ───────────────────────────────────────────────────────────

// archiveExt is the canonical extension for a full workspace backup
// ("rigger workspace backup"). Older archives may still carry ".tar.gz" —
// listing accepts both, new backups are always written as .rwb.
const archiveExt = ".rwb"

// archiveSuffixes are the extensions recognised as workspace backups.
var archiveSuffixes = []string{".rwb", ".tar.gz"}

func hasArchiveSuffix(name string) bool {
	for _, s := range archiveSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

func archivesDir(dataDir string) string {
	return filepath.Join(dataDir, "workspace-archives")
}

// newestSnapshotPerEnv maps each env to its most-recent snapshot dir name under
// wsDir/backups (dirs sort chronologically by name).
func newestSnapshotPerEnv(wsDir string) map[string]string {
	out := map[string]string{}
	envs, err := os.ReadDir(filepath.Join(wsDir, "backups"))
	if err != nil {
		return out
	}
	for _, env := range envs {
		if !env.IsDir() {
			continue
		}
		snaps, err := os.ReadDir(filepath.Join(wsDir, "backups", env.Name()))
		if err != nil {
			continue
		}
		newest := ""
		for _, s := range snaps {
			if s.IsDir() && s.Name() > newest {
				newest = s.Name()
			}
		}
		if newest != "" {
			out[env.Name()] = newest
		}
	}
	return out
}

// archiveExcluded reports whether a workspace-relative path is left out of a full
// archive. A .rwb carries the workspace definition (config, env/compose files)
// plus the *latest* per-env backup snapshot for restorability — but NOT the
// older snapshot history (that would grow the archive without bound), nor the
// legacy envs/<env>/backup path. `keep` is env → newest snapshot dir name.
func archiveExcluded(rel string, keep map[string]string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	// Legacy per-env backup directory.
	if len(parts) >= 3 && parts[0] == "envs" && parts[2] == "backup" {
		return true
	}
	// Per-env backup snapshots: keep only the newest dir per env.
	if len(parts) >= 3 && parts[0] == "backups" {
		if keep[parts[1]] != parts[2] {
			return true
		}
	}
	return false
}

func createArchive(wsDir, wsName, destPath string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return 0, err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	keep := newestSnapshotPerEnv(wsDir)

	err = filepath.Walk(wsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable files silently
		}
		rel, _ := filepath.Rel(wsDir, path)
		if rel == "." {
			return nil
		}
		if archiveExcluded(rel, keep) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Build tar header path: wsName/rel
		tarPath := wsName + "/" + filepath.ToSlash(rel)

		if info.IsDir() {
			return tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir,
				Name:     tarPath + "/",
				Mode:     0755,
				ModTime:  info.ModTime(),
			})
		}

		// Symlinks — store as regular file content
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     tarPath,
			Size:     info.Size(),
			Mode:     int64(info.Mode()),
			ModTime:  info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return nil // skip unreadable files
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
	if err != nil {
		return 0, err
	}
	tw.Close()
	gw.Close()

	fi, _ := f.Stat()
	return fi.Size(), nil
}

// ── Handlers ─────────────────────────────────────────────────────────────────

// POST /api/tools/workspace-backup
func (h *Handler) StartWorkspaceBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Workspace string `json:"workspace"`
		Name      string `json:"name"` // optional custom backup filename
	}
	if err := readJSON(r, &body); err != nil || body.Workspace == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace required"})
		return
	}

	wsDir := filepath.Join(h.workspacesDir, body.Workspace)
	if _, err := os.Stat(wsDir); os.IsNotExist(err) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}

	job := h.jobs.create(body.Workspace)

	// Resolve the archive filename: a custom name (sanitised, .rwb-suffixed,
	// collision-safe) or the default "<workspace>-<timestamp>.rwb".
	dir := archivesDir(h.dataDir)
	archiveName := strings.TrimSpace(body.Name)
	if archiveName != "" {
		archiveName = sanitizeArchiveName(filepath.Base(archiveName))
		if !hasArchiveSuffix(archiveName) {
			archiveName += archiveExt
		}
		archiveName = uniqueArchiveName(dir, archiveName)
	} else {
		ts := job.StartedAt.UTC().Format("20060102-150405")
		archiveName = fmt.Sprintf("%s-%s%s", body.Workspace, ts, archiveExt)
	}

	// Run backup asynchronously
	go func() {
		destPath := filepath.Join(dir, archiveName)

		size, err := createArchive(wsDir, body.Workspace, destPath)
		now := time.Now()
		if err != nil {
			h.jobs.update(job.ID, func(j *BackupJob) {
				j.Status = "failed"
				j.Error  = err.Error()
				j.DoneAt = &now
			})
			return
		}
		h.jobs.update(job.ID, func(j *BackupJob) {
			j.Status    = "completed"
			j.Archive   = archiveName
			j.SizeBytes = size
			j.DoneAt    = &now
		})

		// Auto-upload to the workspace's first configured remote target (11a). The
		// outcome (ok/fail) is recorded in archive_syncs for the UI badge; we
		// only log on failure for operability.
		if tid := h.firstScheduleTarget("", body.Workspace, ""); tid != nil {
			if target, err := settings.GetBackupTarget(h.db, *tid); err == nil && target != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				if _, ae := h.uploadArchive(ctx, archiveName, target); ae != nil {
					log.Printf("auto-sync of %s to %s failed: %v", archiveName, target.Name, ae)
				}
				cancel()
			}
		}
	}()

	writeJSON(w, http.StatusAccepted, job)
}

// GET /api/tools/backup-jobs/{id}
func (h *Handler) GetBackupJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := h.jobs.get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// ── Archive management ────────────────────────────────────────────────────────

type ArchiveInfo struct {
	Filename  string     `json:"filename"`
	Workspace string     `json:"workspace"`
	CreatedAt time.Time  `json:"created_at"`
	SizeBytes int64      `json:"size_bytes"`
	Sync      *syncState `json:"sync,omitempty"` // 11a: remote-sync state, if any
}

// GET /api/tools/workspace-archives
func (h *Handler) ListWorkspaceArchives(w http.ResponseWriter, r *http.Request) {
	dir := archivesDir(h.dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, http.StatusOK, []ArchiveInfo{})
		return
	}

	syncStates := h.archiveSyncStates()
	var archives []ArchiveInfo
	for _, e := range entries {
		if e.IsDir() || !hasArchiveSuffix(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		// Derive workspace name: everything before the last "-YYYYMMDD-HHMMSS.tar.gz"
		ws := wsNameFromArchive(e.Name())
		ai := ArchiveInfo{
			Filename:  e.Name(),
			Workspace: ws,
			CreatedAt: fi.ModTime(),
			SizeBytes: fi.Size(),
		}
		if st, ok := syncStates[e.Name()]; ok {
			s := st
			ai.Sync = &s
		}
		archives = append(archives, ai)
	}
	if archives == nil {
		archives = []ArchiveInfo{}
	}
	writeJSON(w, http.StatusOK, archives)
}

// wsNameFromArchive extracts the workspace name from "<name>-YYYYMMDD-HHMMSS.rwb"
// (or the legacy ".tar.gz").
func wsNameFromArchive(filename string) string {
	name := filename
	for _, s := range archiveSuffixes {
		if strings.HasSuffix(name, s) {
			name = strings.TrimSuffix(name, s)
			break
		}
	}
	// Strip trailing "-YYYYMMDD-HHMMSS" (16 chars)
	if len(name) > 16 {
		return name[:len(name)-16]
	}
	return name
}

// GET /api/tools/workspace-archives/{filename}
func (h *Handler) DownloadWorkspaceArchive(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	// Safety: reject path traversal
	if strings.Contains(filename, "/") || strings.Contains(filename, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename"})
		return
	}
	path := filepath.Join(archivesDir(h.dataDir), filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "archive not found"})
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeFile(w, r, path)
}

// DELETE /api/tools/workspace-archives/{filename}
func (h *Handler) DeleteWorkspaceArchive(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if strings.Contains(filename, "/") || strings.Contains(filename, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename"})
		return
	}
	path := filepath.Join(archivesDir(h.dataDir), filename)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "archive not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── Restore ───────────────────────────────────────────────────────────────────

// restoreFromReader extracts an archive stream into the workspaces directory.
// It returns the restored workspace name and an HTTP status code: 200 on
// success, 409 when the workspace exists and force is false, 400 for a bad
// archive, 500 for filesystem errors.
func (h *Handler) restoreFromReader(rd io.Reader, force bool) (string, int, error) {
	tmpDir, err := os.MkdirTemp("", "rigger-restore-*")
	if err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("could not create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	wsName, err := extractArchive(rd, tmpDir)
	if err != nil {
		return "", http.StatusBadRequest, fmt.Errorf("invalid archive: %w", err)
	}

	destDir := filepath.Join(h.workspacesDir, wsName)
	if _, statErr := os.Stat(destDir); statErr == nil {
		if !force {
			return wsName, http.StatusConflict, fmt.Errorf("workspace already exists")
		}
		if err := os.RemoveAll(destDir); err != nil {
			return wsName, http.StatusInternalServerError, fmt.Errorf("could not remove existing workspace: %w", err)
		}
	}

	srcDir := filepath.Join(tmpDir, wsName)
	if err := os.Rename(srcDir, destDir); err != nil {
		// Rename may fail across filesystems — fall back to copy
		if err2 := copyDir(srcDir, destDir); err2 != nil {
			return wsName, http.StatusInternalServerError, fmt.Errorf("failed to restore: %w", err2)
		}
	}
	return wsName, http.StatusOK, nil
}

// POST /api/tools/workspace-restore  (multipart: field "archive")
func (h *Handler) RestoreWorkspace(w http.ResponseWriter, r *http.Request) {
	// 4 GB max upload
	if err := r.ParseMultipartForm(4 << 30); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to parse upload: " + err.Error()})
		return
	}

	force := r.FormValue("force") == "true"

	file, _, err := r.FormFile("archive")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "archive field required"})
		return
	}
	defer file.Close()

	wsName, code, err := h.restoreFromReader(file, force)
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error(), "workspace": wsName})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored", "workspace": wsName})
}

// POST /api/tools/workspace-archives/{filename}/restore  (body: {force})
// Restores directly from an archive already stored on the server — no re-upload.
func (h *Handler) RestoreWorkspaceFromArchive(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if strings.Contains(filename, "/") || strings.Contains(filename, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename"})
		return
	}
	var body struct {
		Force bool `json:"force"`
	}
	_ = readJSON(r, &body)

	path := filepath.Join(archivesDir(h.dataDir), filename)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "archive not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	defer f.Close()

	wsName, code, err := h.restoreFromReader(f, body.Force)
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error(), "workspace": wsName})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored", "workspace": wsName})
}

// POST /api/tools/workspace-archives/upload  (multipart: field "archive")
// Stores an uploaded backup archive on the server (validated) without
// restoring it — the user then restores it from the list like any other.
func (h *Handler) UploadWorkspaceArchive(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(4 << 30); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to parse upload: " + err.Error()})
		return
	}
	file, hdr, err := r.FormFile("archive")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "archive field required"})
		return
	}
	defer file.Close()

	// Stream to a temp file first so we can both validate it and move it into place.
	dir := archivesDir(h.dataDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed away
	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to store upload: " + err.Error()})
		return
	}
	tmp.Close()

	// Validate it's a real workspace backup and recover the workspace name.
	wsName, err := validateArchiveFile(tmpPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a valid workspace backup: " + err.Error()})
		return
	}

	// Pick a destination filename: keep the uploaded basename if it's a recognised
	// archive name, otherwise synthesise one. Avoid collisions with a -N suffix.
	base := filepath.Base(hdr.Filename)
	base = sanitizeArchiveName(base)
	if base == "" || !hasArchiveSuffix(base) {
		base = wsName + "-uploaded" + archiveExt
	}
	dest := uniqueArchiveName(dir, base)

	if err := os.Rename(tmpPath, filepath.Join(dir, dest)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save archive: " + err.Error()})
		return
	}

	fi, _ := os.Stat(filepath.Join(dir, dest))
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	writeJSON(w, http.StatusOK, ArchiveInfo{
		Filename:  dest,
		Workspace: wsName,
		SizeBytes: size,
	})
}

// sanitizeArchiveName strips any directory parts and disallowed characters.
func sanitizeArchiveName(name string) string {
	name = filepath.Base(name)
	return archiveNameSanitizer.ReplaceAllString(name, "-")
}

// validateArchiveFile confirms the file at path is a gzip+tar workspace backup
// (a single top-level directory containing config.json) and returns that
// directory name (the workspace name).
func validateArchiveFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("not a gzip archive")
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	wsName := ""
	hasConfig := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("not a valid tar archive")
		}
		clean := filepath.ToSlash(filepath.Clean(hdr.Name))
		if strings.HasPrefix(clean, "..") {
			continue
		}
		parts := strings.SplitN(clean, "/", 2)
		if wsName == "" && parts[0] != "" {
			wsName = parts[0]
		}
		if len(parts) == 2 && parts[1] == "config.json" {
			hasConfig = true
		}
	}
	if wsName == "" {
		return "", fmt.Errorf("archive is empty")
	}
	if !hasConfig {
		return "", fmt.Errorf("missing config.json (not a Rigger workspace)")
	}
	return wsName, nil
}

// uniqueArchiveName returns base, or base with a -2/-3… suffix inserted before
// the extension if a file by that name already exists in dir.
func uniqueArchiveName(dir, base string) string {
	if _, err := os.Stat(filepath.Join(dir, base)); os.IsNotExist(err) {
		return base
	}
	ext := ""
	for _, s := range archiveSuffixes {
		if strings.HasSuffix(base, s) {
			ext = s
			break
		}
	}
	stem := strings.TrimSuffix(base, ext)
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if _, err := os.Stat(filepath.Join(dir, cand)); os.IsNotExist(err) {
			return cand
		}
	}
	return base
}

// extractArchive reads a .tar.gz from r, writes files under destDir,
// and returns the top-level directory name (= workspace name).
func extractArchive(r io.Reader, destDir string) (string, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("not a valid gzip archive: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	wsName := ""

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		// Sanitise path
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") {
			continue // refuse path traversal
		}

		// Capture top-level directory name
		parts := strings.SplitN(filepath.ToSlash(clean), "/", 2)
		if wsName == "" && parts[0] != "" {
			wsName = parts[0]
		}

		target := filepath.Join(destDir, clean)

		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0755) //nolint:errcheck
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return "", err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return "", err
			}
			f.Close()
		}
	}

	if wsName == "" {
		return "", fmt.Errorf("archive appears empty or has no top-level directory")
	}

	// Validate: config.json must exist
	if _, err := os.Stat(filepath.Join(destDir, wsName, "config.json")); err != nil {
		return "", fmt.Errorf("archive does not contain a valid Rigger workspace (missing config.json)")
	}

	return wsName, nil
}

// copyDir copies src directory tree to dst (fallback for cross-device rename)
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()
	d, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer d.Close()
	_, err = io.Copy(d, s)
	return err
}

