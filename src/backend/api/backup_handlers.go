package api

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

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

func archivesDir(dataDir string) string {
	return filepath.Join(dataDir, "workspace-archives")
}

// shouldExclude returns true for paths inside envs/<env>/backup/
func shouldExclude(rel string) bool {
	// rel looks like: envs/prod/backup/... or envs/prod/backup
	parts := strings.Split(filepath.ToSlash(rel), "/")
	// parts[0]=="envs", parts[1]==<env>, parts[2]=="backup"
	return len(parts) >= 3 && parts[0] == "envs" && parts[2] == "backup"
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

	err = filepath.Walk(wsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable files silently
		}
		rel, _ := filepath.Rel(wsDir, path)
		if rel == "." {
			return nil
		}
		if shouldExclude(rel) {
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

	// Run backup asynchronously
	go func() {
		ts := job.StartedAt.UTC().Format("20060102-150405")
		archiveName := fmt.Sprintf("%s-%s.tar.gz", body.Workspace, ts)
		destPath := filepath.Join(archivesDir(h.dataDir), archiveName)

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
	Filename  string    `json:"filename"`
	Workspace string    `json:"workspace"`
	CreatedAt time.Time `json:"created_at"`
	SizeBytes int64     `json:"size_bytes"`
}

// GET /api/tools/workspace-archives
func (h *Handler) ListWorkspaceArchives(w http.ResponseWriter, r *http.Request) {
	dir := archivesDir(h.dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, http.StatusOK, []ArchiveInfo{})
		return
	}

	var archives []ArchiveInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		// Derive workspace name: everything before the last "-YYYYMMDD-HHMMSS.tar.gz"
		ws := wsNameFromArchive(e.Name())
		archives = append(archives, ArchiveInfo{
			Filename:  e.Name(),
			Workspace: ws,
			CreatedAt: fi.ModTime(),
			SizeBytes: fi.Size(),
		})
	}
	if archives == nil {
		archives = []ArchiveInfo{}
	}
	writeJSON(w, http.StatusOK, archives)
}

// wsNameFromArchive extracts the workspace name from "<name>-YYYYMMDD-HHMMSS.tar.gz"
func wsNameFromArchive(filename string) string {
	name := strings.TrimSuffix(filename, ".tar.gz")
	// Strip trailing "-YYYYMMDD-HHMMSS" (17 chars)
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

	// Extract to temp directory
	tmpDir, err := os.MkdirTemp("", "rigger-restore-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create temp dir"})
		return
	}
	defer os.RemoveAll(tmpDir)

	wsName, err := extractArchive(file, tmpDir)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid archive: " + err.Error()})
		return
	}

	destDir := filepath.Join(h.workspacesDir, wsName)
	if _, statErr := os.Stat(destDir); statErr == nil {
		if !force {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":     "workspace already exists",
				"workspace": wsName,
			})
			return
		}
		if err := os.RemoveAll(destDir); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not remove existing workspace"})
			return
		}
	}

	srcDir := filepath.Join(tmpDir, wsName)
	if err := os.Rename(srcDir, destDir); err != nil {
		// Rename may fail across filesystems — fall back to copy
		if err2 := copyDir(srcDir, destDir); err2 != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to restore: " + err2.Error()})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "restored", "workspace": wsName})
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

