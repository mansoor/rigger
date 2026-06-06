package api

import (
	"archive/tar"
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
)

// Workspace configuration snapshots (.rws = Rigger Workspace Snapshot).
//
// A snapshot captures ONLY the workspace's configuration — config.json plus each
// environment's .env file (which holds secrets like DB passwords) — NOT the data
// volumes. It's a lightweight, fast counterpart to the full data backup/restore.
// The archive also carries a meta.json so the originating workspace is always
// known regardless of a custom filename.

func snapshotsDir(dataDir string) string {
	return filepath.Join(dataDir, "workspace-snapshots")
}

const snapshotExt = ".rws"

// SnapshotInfo is the API shape for one saved snapshot.
type SnapshotInfo struct {
	Filename  string    `json:"filename"`
	Workspace string    `json:"workspace"`
	CreatedAt time.Time `json:"created_at"`
	SizeBytes int64     `json:"size_bytes"`
}

type snapshotMeta struct {
	Workspace string    `json:"workspace"`
	CreatedAt time.Time `json:"created_at"`
}

var snapshotNameSafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// sanitizeSnapshotName turns a user-supplied name into a safe single-segment
// filename ending in .rws.
func sanitizeSnapshotName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, snapshotExt)
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
		Name      string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Workspace) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace is required"})
		return
	}
	wsDir := filepath.Join(h.workspacesDir, body.Workspace)
	if _, err := os.Stat(filepath.Join(wsDir, "config.json")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}

	filename := sanitizeSnapshotName(body.Name)
	if filename == "" {
		filename = fmt.Sprintf("%s_%s%s", body.Workspace, time.Now().Format("20060102-150405"), snapshotExt)
	}

	destPath := filepath.Join(snapshotsDir(h.dataDir), filename)
	if _, err := os.Stat(destPath); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a snapshot with that name already exists"})
		return
	}

	size, err := createConfigSnapshot(wsDir, body.Workspace, destPath)
	if err != nil {
		os.Remove(destPath) //nolint:errcheck
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create snapshot: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, SnapshotInfo{
		Filename:  filename,
		Workspace: body.Workspace,
		CreatedAt: time.Now(),
		SizeBytes: size,
	})
}

// createConfigSnapshot writes a .rws (tar.gz) containing meta.json, config.json
// and every envs/<env>/.env file. Returns the file size.
func createConfigSnapshot(wsDir, wsName, destPath string) (int64, error) {
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
	meta, _ := json.Marshal(snapshotMeta{Workspace: wsName, CreatedAt: time.Now()})
	if err := writeBytes("meta.json", meta, time.Now()); err != nil {
		f.Close()
		return 0, err
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

// GET /api/tools/workspace-snapshots
func (h *Handler) ListWorkspaceSnapshots(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(snapshotsDir(h.dataDir))
	out := []SnapshotInfo{}
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), snapshotExt) {
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
	wsDir := filepath.Join(h.workspacesDir, meta.Workspace)
	if _, err := os.Stat(wsDir); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("workspace %q no longer exists", meta.Workspace)})
		return
	}

	restored, err := extractConfigSnapshot(path, wsDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "rollback failed: " + err.Error()})
		return
	}

	// Regenerate compose from the restored config (best-effort, non-fatal).
	if cfgBytes, err := os.ReadFile(filepath.Join(wsDir, "config.json")); err == nil {
		go h.regenCompose(meta.Workspace, string(cfgBytes))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"workspace": meta.Workspace,
		"restored":  restored,
	})
}

// extractConfigSnapshot unpacks config.json + envs/<env>/.env from a .rws into
// wsDir, overwriting existing files. Whitelisted paths only; never escapes wsDir.
func extractConfigSnapshot(srcPath, wsDir string) ([]string, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)

	var restored []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return restored, err
		}
		name := filepath.ToSlash(hdr.Name)
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
			return restored, err
		}
		out, err := os.Create(dest)
		if err != nil {
			return restored, err
		}
		if _, err := io.Copy(out, tr); err != nil { //nolint:gosec — whitelisted, size-bounded config files
			out.Close()
			return restored, err
		}
		out.Close()
		restored = append(restored, name)
	}
	return restored, nil
}

func safeSnapshotFilename(name string) bool {
	return name != "" &&
		!strings.Contains(name, "/") &&
		!strings.Contains(name, "\\") &&
		!strings.Contains(name, "..") &&
		strings.HasSuffix(name, snapshotExt)
}
