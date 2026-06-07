package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// POST /api/workspaces/{name}/envs/{env}/restore-verify  body {date}
// Non-destructive dry-run (Phase 11c): confirms a snapshot is complete and
// restorable — every archive file is present, non-empty, and gzip-intact. Does
// not touch the running environment.
func (h *Handler) VerifyRestore(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env := r.PathValue("env")
	var body struct {
		Date string `json:"date"`
	}
	_ = readJSON(r, &body)
	if name == "" || env == "" || body.Date == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace, env and date are required"})
		return
	}
	if strings.Contains(body.Date, "/") || strings.Contains(body.Date, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid date"})
		return
	}

	snapDir := filepath.Join(h.workspacesDir, name, "backups", env, body.Date)
	if fi, err := os.Stat(snapDir); err != nil || !fi.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "snapshot not found"})
		return
	}

	type fileReport struct {
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		OK    bool   `json:"ok"`
		Issue string `json:"issue,omitempty"`
	}

	entries, _ := os.ReadDir(snapDir)
	var files []fileReport
	allOK := true
	var totalBytes int64

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		fr := fileReport{Name: e.Name(), Size: fi.Size(), OK: true}
		totalBytes += fi.Size()

		switch {
		case fi.Size() == 0:
			fr.OK, fr.Issue = false, "file is empty"
		case strings.HasSuffix(e.Name(), ".gz"):
			if err := gzipIntact(filepath.Join(snapDir, e.Name())); err != nil {
				fr.OK, fr.Issue = false, "corrupt gzip: "+err.Error()
			}
		}
		if !fr.OK {
			allOK = false
		}
		files = append(files, fr)
	}

	if len(files) == 0 {
		allOK = false
	}
	if files == nil {
		files = []fileReport{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"date":        body.Date,
		"ok":          allOK,
		"files":       files,
		"total_bytes": totalBytes,
	})
}

// gzipIntact streams a gzip file through the decompressor to confirm it isn't
// truncated or corrupt.
func gzipIntact(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()
	_, err = io.Copy(io.Discard, gr)
	return err
}
