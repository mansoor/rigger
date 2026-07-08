package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Project configuration snapshots (.rps = Rigger Project Snapshot).
//
// A snapshot captures ONLY the project's configuration — config.json plus each
// environment's .env file (which holds secrets like DB passwords) and the
// project's DB config rows — NOT the data volumes. It's a lightweight, fast
// counterpart to the full data backup/restore. The archive also carries a
// meta.json so the originating project is always known regardless of a custom
// filename.

func snapshotsDir(dataDir string) string {
	return filepath.Join(dataDir, "workspace-snapshots")
}

// snapshotExt is the canonical extension for new snapshots (was .rws pre-rename).
const snapshotExt = ".rps"

// snapshotSuffixes are recognised on list/upload/restore — the current .rps plus
// the legacy .rws so older snapshots keep working.
var snapshotSuffixes = []string{".rps", ".rws"}

func hasSnapshotSuffix(name string) bool {
	for _, s := range snapshotSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// SnapshotInfo is the API shape for one saved snapshot.
type SnapshotInfo struct {
	Filename  string    `json:"filename"`
	Workspace string    `json:"workspace"`
	Project   string    `json:"project"`
	CreatedAt time.Time `json:"created_at"`
	SizeBytes int64     `json:"size_bytes"`
}

type snapshotMeta struct {
	Workspace string    `json:"workspace"`
	Project   string    `json:"project"`
	CreatedAt time.Time `json:"created_at"`
}

var snapshotNameSafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// sanitizeSnapshotName turns a user-supplied name into a safe single-segment
// filename ending in .rws.
func sanitizeSnapshotName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".rws"), snapshotExt)
	name = snapshotNameSafe.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-._")
	if name == "" {
		return ""
	}
	return name + snapshotExt
}

// POST /api/tools/workspace-snapshots — { workspace, name? }
func (h *Handler) CreateWorkspaceSnapshot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Workspace string `json:"workspace"`
		Project   string `json:"project"`
		Name      string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Workspace) == "" || strings.TrimSpace(body.Project) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace and project are required"})
		return
	}
	wsDir := wspath.ProjectDir(h.workspacesDir, body.Workspace, body.Project)
	if _, err := os.Stat(filepath.Join(wsDir, "config.json")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}

	filename := sanitizeSnapshotName(body.Name)
	if filename == "" {
		filename = fmt.Sprintf("%s_%s_%s%s", body.Workspace, body.Project, time.Now().Format("20060102-150405"), snapshotExt)
	}

	destPath := filepath.Join(snapshotsDir(h.dataDir), filename)
	if _, err := os.Stat(destPath); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a snapshot with that name already exists"})
		return
	}

	dbJSON, _ := h.exportProjectDB(body.Workspace, body.Project)
	size, err := createConfigSnapshot(wsDir, body.Workspace, body.Project, destPath, dbJSON)
	if err != nil {
		os.Remove(destPath) //nolint:errcheck
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create snapshot: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, SnapshotInfo{
		Filename:  filename,
		Workspace: body.Workspace,
		Project:   body.Project,
		CreatedAt: time.Now(),
		SizeBytes: size,
	})
}

// createConfigSnapshot writes a .rws (tar.gz) containing meta.json, config.json
// and every envs/<env>/.env file. Returns the file size.
func createConfigSnapshot(wsDir, wsName, project, destPath string, dbJSON []byte) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return 0, err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return 0, err
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	writeBytes := func(name string, data []byte, mod time.Time) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: mod}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}

	// meta.json
	meta, _ := json.Marshal(snapshotMeta{Workspace: wsName, Project: project, CreatedAt: time.Now()})
	if err := writeBytes("meta.json", meta, time.Now()); err != nil {
		f.Close()
		return 0, err
	}

	// Portable project DB config — a snapshot is "configuration", so it belongs here.
	if len(dbJSON) > 0 {
		if err := writeBytes(projectDBFile, dbJSON, time.Now()); err != nil {
			f.Close()
			return 0, err
		}
	}

	// config.json + envs/<env>/.env — skip any that are missing.
	addFile := func(rel string) error {
		full := filepath.Join(wsDir, filepath.FromSlash(rel))
		info, err := os.Stat(full)
		if err != nil {
			return nil // not present — skip
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		return writeBytes(rel, data, info.ModTime())
	}
	if err := addFile("config.json"); err != nil {
		f.Close()
		return 0, err
	}
	if envs, err := os.ReadDir(filepath.Join(wsDir, "envs")); err == nil {
		for _, e := range envs {
			if e.IsDir() {
				if err := addFile("envs/" + e.Name() + "/.env"); err != nil {
					f.Close()
					return 0, err
				}
			}
		}
	}

	if err := tw.Close(); err != nil {
		f.Close()
		return 0, err
	}
	if err := gw.Close(); err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	fi, err := os.Stat(destPath)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// readSnapshotMeta extracts meta.json from a .rws without unpacking the rest.
func readSnapshotMeta(path string) (snapshotMeta, error) {
	var m snapshotMeta
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return m, err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, err
		}
		if filepath.ToSlash(hdr.Name) == "meta.json" {
			return m, json.NewDecoder(tr).Decode(&m)
		}
	}
	return m, nil
}

// POST /api/tools/workspace-snapshots/upload  (multipart: field "snapshot")
// Accepts a .rws file (e.g. downloaded from another server) and stores it so it
// can be rolled back like any locally-created snapshot.
func (h *Handler) UploadWorkspaceSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil { // 64 MB — config snapshots are tiny
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to parse upload: " + err.Error()})
		return
	}
	file, hdr, err := r.FormFile("snapshot")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "snapshot field required"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 64<<20))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Validate it's a real .rws: gzip+tar containing at least config.json.
	meta, err := validateSnapshotBytes(data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a valid project snapshot (.rps): " + err.Error()})
		return
	}

	// Prefer the uploaded filename; otherwise derive one. Ensure uniqueness.
	name := sanitizeSnapshotName(hdr.Filename)
	if name == "" {
		base := meta.Workspace
		if base == "" {
			base = "snapshot"
		}
		name = fmt.Sprintf("%s_%s%s", base, time.Now().Format("20060102-150405"), snapshotExt)
	}
	name = uniqueSnapshotName(h.dataDir, name)

	if err := os.MkdirAll(snapshotsDir(h.dataDir), 0755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	destPath := filepath.Join(snapshotsDir(h.dataDir), name)
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	createdAt := meta.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	writeJSON(w, http.StatusOK, SnapshotInfo{
		Filename:  name,
		Workspace: meta.Workspace,
		CreatedAt: createdAt,
		SizeBytes: int64(len(data)),
	})
}

// validateSnapshotBytes confirms data is a gzip+tar with at least config.json,
// returning the embedded meta.json (zero value if absent).
func validateSnapshotBytes(data []byte) (snapshotMeta, error) {
	var meta snapshotMeta
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return meta, fmt.Errorf("not a gzip archive")
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	hasConfig := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return meta, fmt.Errorf("not a valid tar archive")
		}
		switch filepath.ToSlash(hdr.Name) {
		case "meta.json":
			json.NewDecoder(tr).Decode(&meta) //nolint:errcheck
		case "config.json":
			hasConfig = true
		}
	}
	if !hasConfig {
		return meta, fmt.Errorf("missing config.json")
	}
	return meta, nil
}

// uniqueSnapshotName returns name, or name with a -2/-3/… suffix before .rws if
// a file with that name already exists.
func uniqueSnapshotName(dataDir, name string) string {
	dir := snapshotsDir(dataDir)
	if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
		return name
	}
	stem := strings.TrimSuffix(name, snapshotExt)
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, i, snapshotExt)
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d%s", stem, time.Now().UnixNano(), snapshotExt)
}

// GET /api/tools/workspace-snapshots
func (h *Handler) ListWorkspaceSnapshots(w http.ResponseWriter, r *http.Request) {
	wsFilter := r.URL.Query().Get("workspace")
	entries, err := os.ReadDir(snapshotsDir(h.dataDir))
	out := []SnapshotInfo{}
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !hasSnapshotSuffix(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		info := SnapshotInfo{Filename: e.Name(), CreatedAt: fi.ModTime(), SizeBytes: fi.Size()}
		if meta, err := readSnapshotMeta(filepath.Join(snapshotsDir(h.dataDir), e.Name())); err == nil {
			info.Workspace = meta.Workspace
			if !meta.CreatedAt.IsZero() {
				info.CreatedAt = meta.CreatedAt
			}
		}
		if wsFilter != "" && info.Workspace != wsFilter {
			continue
		}
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/tools/workspace-snapshots/{filename}
func (h *Handler) DownloadWorkspaceSnapshot(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if !safeSnapshotFilename(filename) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename"})
		return
	}
	path := filepath.Join(snapshotsDir(h.dataDir), filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "snapshot not found"})
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeFile(w, r, path)
}

// DELETE /api/tools/workspace-snapshots/{filename}
func (h *Handler) DeleteWorkspaceSnapshot(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if !safeSnapshotFilename(filename) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename"})
		return
	}
	if err := os.Remove(filepath.Join(snapshotsDir(h.dataDir), filename)); err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "snapshot not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// POST /api/tools/workspace-snapshots/{filename}/rollback
// Overwrites the originating workspace's config.json + env .env files with the
// snapshot's, then regenerates compose. Data volumes are untouched.
func (h *Handler) RollbackWorkspaceSnapshot(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if !safeSnapshotFilename(filename) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename"})
		return
	}
	path := filepath.Join(snapshotsDir(h.dataDir), filename)
	meta, err := readSnapshotMeta(path)
	if err != nil || strings.TrimSpace(meta.Workspace) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "snapshot is missing its workspace metadata"})
		return
	}
	wsDir := wspath.ProjectDir(h.workspacesDir, meta.Workspace, meta.Project)
	if _, err := os.Stat(wsDir); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("project %q/%q no longer exists", meta.Workspace, meta.Project)})
		return
	}

	restored, dbJSON, err := extractConfigSnapshot(path, wsDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "rollback failed: " + err.Error()})
		return
	}

	// Restore the project's portable DB config (pipelines, alerts, domains, host
	// bindings) captured in the snapshot. Best-effort.
	if len(dbJSON) > 0 {
		h.importProjectDB(meta.Workspace, meta.Project, dbJSON)
	}

	// Regenerate compose from the restored config (best-effort, non-fatal).
	if cfgBytes, err := os.ReadFile(filepath.Join(wsDir, "config.json")); err == nil {
		go h.regenCompose(meta.Workspace, meta.Project, string(cfgBytes))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"workspace": meta.Workspace,
		"restored":  restored,
	})
}

// extractConfigSnapshot unpacks config.json + envs/<env>/.env from a .rps into
// wsDir, overwriting existing files. Whitelisted paths only; never escapes wsDir.
// The portable project DB config (if present) is returned separately for the
// caller to import — it is never written to the project dir.
func extractConfigSnapshot(srcPath, wsDir string) ([]string, []byte, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, nil, err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)

	var restored []string
	var dbJSON []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return restored, dbJSON, err
		}
		name := filepath.ToSlash(hdr.Name)
		if name == projectDBFile {
			dbJSON, _ = io.ReadAll(io.LimitReader(tr, maxNotesBytes)) //nolint:errcheck — bounded config JSON
			continue
		}
		// Whitelist config.json and envs/<env>/.env only.
		ok := name == "config.json" ||
			(strings.HasPrefix(name, "envs/") && strings.HasSuffix(name, "/.env") && !strings.Contains(name, ".."))
		if !ok {
			continue
		}
		dest := filepath.Join(wsDir, filepath.FromSlash(name))
		// Belt-and-suspenders: ensure the resolved path stays inside wsDir.
		if rel, err := filepath.Rel(wsDir, dest); err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return restored, dbJSON, err
		}
		out, err := os.Create(dest)
		if err != nil {
			return restored, dbJSON, err
		}
		if _, err := io.Copy(out, tr); err != nil { //nolint:gosec — whitelisted, size-bounded config files
			out.Close()
			return restored, dbJSON, err
		}
		out.Close()
		restored = append(restored, name)
	}
	return restored, dbJSON, nil
}

func safeSnapshotFilename(name string) bool {
	return name != "" &&
		!strings.Contains(name, "/") &&
		!strings.Contains(name, "\\") &&
		!strings.Contains(name, "..") &&
		hasSnapshotSuffix(name)
}
