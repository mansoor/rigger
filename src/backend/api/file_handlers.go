package api

// Wave D — container file browser. Read + write access to files inside a
// container, on whichever daemon it runs on (local or a remote host over SSH),
// all through the executor.Executor abstraction so every operation is cross-host
// by construction. Read paths (list/view/download) pass the target as a direct
// `docker exec` argument — no shell, so no injection surface; the write paths
// need a `cat >` redirect and single-quote the target carefully.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/executor"
)

// octalMode matches a 3- or 4-digit octal chmod mode (e.g. 644, 0755).
var octalMode = regexp.MustCompile(`^[0-7]{3,4}$`)

const (
	// maxViewBytes caps inline text preview / inline save (1 MiB). Bigger files
	// must be downloaded; bigger edits aren't supported inline.
	maxViewBytes = 1 << 20
	// maxUploadBytes caps a single uploaded file (100 MiB).
	maxUploadBytes = 100 << 20
)

// FileEntry is one directory entry inside a container.
type FileEntry struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // dir | file | link | other
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`  // e.g. -rw-r--r--
	ModTime string `json:"mtime"` // as `ls` prints it
	Link    string `json:"link,omitempty"`
}

// cleanAbsPath requires an absolute container path (no NUL bytes) and returns it
// cleaned. Absolute-only means it can never be mistaken for a docker flag; there
// is no traversal concern (the user could reach the same paths via the terminal).
func cleanAbsPath(p string) (string, bool) {
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") || strings.IndexByte(p, 0) >= 0 {
		return "", false
	}
	return path.Clean(p), true
}

// fileTarget resolves executor + validated service + absolute path (from the
// `path` query param). It writes an error response and returns ok=false on
// failure.
func (h *Handler) fileTarget(w http.ResponseWriter, r *http.Request) (ex executor.Executor, svc, p string, ok bool) {
	ex, svc, ok = h.containerExec(w, r)
	if !ok {
		return nil, "", "", false
	}
	p, valid := cleanAbsPath(r.URL.Query().Get("path"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return nil, "", "", false
	}
	return ex, svc, p, true
}

// runCapture runs a docker command capturing stdout and stderr separately, so we
// can surface the container's own error message (e.g. "No such file") instead of
// a bare exit code. Works identically for local and remote executors.
func runCapture(ex executor.Executor, args ...string) (stdout []byte, stderr string, err error) {
	var out, errb bytes.Buffer
	err = ex.Docker(executor.Spec{Args: args, Stdout: &out, Stderr: &errb})
	return out.Bytes(), strings.TrimSpace(errb.String()), err
}

// fileGatewayErr writes a 502 with the container's stderr when available.
func fileGatewayErr(w http.ResponseWriter, stderr string, err error) {
	msg := stderr
	if msg == "" {
		msg = err.Error()
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": msg})
}

// shSingleQuote single-quotes s for safe embedding in a remote/`sh -c` command,
// escaping any embedded single quotes.
func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// FileList — GET .../containers/{service}/files?path=/dir
// Lists a directory inside the container via `ls -la` (parsed).
func (h *Handler) FileList(w http.ResponseWriter, r *http.Request) {
	ex, svc, p, ok := h.fileTarget(w, r)
	if !ok {
		return
	}
	out, errMsg, err := runCapture(ex, "exec", svc, "ls", "-la", p)
	if err != nil {
		fileGatewayErr(w, errMsg, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "entries": parseLsLong(string(out))})
}

// parseLsLong parses `ls -la` output into entries. It tolerates both GNU coreutils
// and busybox formats (perms links owner group size  month day time/year  name).
func parseLsLong(out string) []FileEntry {
	entries := []FileEntry{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		perms := fields[0]
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		mtime := strings.Join(fields[5:8], " ")
		name := restAfterFields(line, 8) // preserves spaces in filenames
		typ, link := "other", ""
		switch perms[0] {
		case 'd':
			typ = "dir"
		case '-':
			typ = "file"
		case 'l':
			typ = "link"
			if i := strings.Index(name, " -> "); i >= 0 {
				link = name[i+4:]
				name = name[:i]
			}
		}
		if name == "" || name == "." {
			continue // keep ".." for navigation; drop "." and the "total" line
		}
		entries = append(entries, FileEntry{Name: name, Type: typ, Size: size, Mode: perms, ModTime: mtime, Link: link})
	}
	return entries
}

// restAfterFields returns the remainder of line after the first n whitespace-
// separated fields, preserving any internal spaces (so filenames with spaces
// survive — strings.Fields would collapse them).
func restAfterFields(line string, n int) string {
	i, L := 0, len(line)
	isWS := func(b byte) bool { return b == ' ' || b == '\t' }
	for f := 0; f < n; f++ {
		for i < L && isWS(line[i]) {
			i++
		}
		for i < L && !isWS(line[i]) {
			i++
		}
	}
	for i < L && isWS(line[i]) {
		i++
	}
	return line[i:]
}

// FileView — GET .../containers/{service}/file?path=/dir/file
// Returns a size-capped text preview; flags binary files and truncation.
func (h *Handler) FileView(w http.ResponseWriter, r *http.Request) {
	ex, svc, p, ok := h.fileTarget(w, r)
	if !ok {
		return
	}
	// Read one byte past the cap so we can detect truncation.
	out, errMsg, err := runCapture(ex, "exec", svc, "head", "-c", strconv.Itoa(maxViewBytes+1), p)
	if err != nil {
		fileGatewayErr(w, errMsg, err)
		return
	}
	truncated := false
	if len(out) > maxViewBytes {
		out = out[:maxViewBytes]
		truncated = true
	}
	if bytes.IndexByte(out, 0) >= 0 { // NUL ⇒ binary; don't ship as text
		writeJSON(w, http.StatusOK, map[string]any{"path": p, "binary": true, "size": len(out)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path": p, "content": string(out), "truncated": truncated, "size": len(out),
	})
}

// FileDownload — GET .../containers/{service}/file-download?path=/dir/file
// Streams the file to the browser as an attachment (no size cap).
func (h *Handler) FileDownload(w http.ResponseWriter, r *http.Request) {
	ex, svc, p, ok := h.fileTarget(w, r)
	if !ok {
		return
	}
	// Confirm it's a regular file before we commit response headers — once we
	// start streaming we can no longer change the status code.
	if _, errMsg, err := runCapture(ex, "exec", svc, "test", "-f", p); err != nil {
		msg := errMsg
		if msg == "" {
			msg = "not a regular file or not found"
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(p)))
	// Stream `cat` straight to the response (no buffering of the whole file).
	_ = ex.Docker(executor.Spec{Args: []string{"exec", svc, "cat", p}, Stdout: w})
}

// writeContainerFile streams src into target inside the container via `cat >`.
// target is single-quoted; the remote executor additionally quotes the whole
// `sh -c` argument, so the redirect target is safe on both local and remote.
func writeContainerFile(ex executor.Executor, svc, target string, src io.Reader) error {
	var errb bytes.Buffer
	err := ex.Docker(executor.Spec{
		Args:   []string{"exec", "-i", svc, "sh", "-c", "cat > " + shSingleQuote(target)},
		Stdin:  src,
		Stderr: &errb,
	})
	if err != nil {
		if m := strings.TrimSpace(errb.String()); m != "" {
			return fmt.Errorf("%s", m)
		}
		return err
	}
	return nil
}

// FileUpload — POST .../containers/{service}/files-upload (multipart: file + path)
// Writes the uploaded file into the directory given by the `path` form field.
func (h *Handler) FileUpload(w http.ResponseWriter, r *http.Request) {
	ex, svc, ok := h.containerExec(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or oversized upload"})
		return
	}
	dir, valid := cleanAbsPath(r.FormValue("path"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no file provided"})
		return
	}
	defer file.Close()
	name := path.Base(hdr.Filename)
	if name == "" || name == "." || name == "/" || strings.IndexByte(name, 0) >= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename"})
		return
	}
	target := path.Join(dir, name)
	if err := writeContainerFile(ex, svc, target, file); err != nil {
		fileGatewayErr(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": target})
}

// FileSave — PUT .../containers/{service}/file?path=/dir/file (body: new content)
// Overwrites a text file with the request body (capped at maxViewBytes).
func (h *Handler) FileSave(w http.ResponseWriter, r *http.Request) {
	ex, svc, p, ok := h.fileTarget(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxViewBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body too large or unreadable"})
		return
	}
	if err := writeContainerFile(ex, svc, p, bytes.NewReader(body)); err != nil {
		fileGatewayErr(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "size": len(body)})
}

// FileDelete — DELETE .../containers/{service}/file?path=/dir/file
// Removes a file or directory (`rm -rf`). Refuses "/".
func (h *Handler) FileDelete(w http.ResponseWriter, r *http.Request) {
	ex, svc, p, ok := h.fileTarget(w, r)
	if !ok {
		return
	}
	if p == "/" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "refusing to delete /"})
		return
	}
	if _, errMsg, err := runCapture(ex, "exec", svc, "rm", "-rf", "--", p); err != nil {
		fileGatewayErr(w, errMsg, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": p})
}

// FileRename — POST .../containers/{service}/file-rename?path=<from>&to=<absPath>
// Moves/renames a file or directory (`mv`). Both paths must be absolute; refuses
// to touch "/".
func (h *Handler) FileRename(w http.ResponseWriter, r *http.Request) {
	ex, svc, from, ok := h.fileTarget(w, r)
	if !ok {
		return
	}
	to, valid := cleanAbsPath(r.URL.Query().Get("to"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid destination path"})
		return
	}
	if from == "/" || to == "/" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "refusing to rename /"})
		return
	}
	if _, errMsg, err := runCapture(ex, "exec", svc, "mv", "--", from, to); err != nil {
		fileGatewayErr(w, errMsg, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to})
}

// FileChmod — POST .../containers/{service}/file-chmod?path=<p>&mode=<octal>
// Changes permissions (`chmod`). mode is validated as 3–4 octal digits.
func (h *Handler) FileChmod(w http.ResponseWriter, r *http.Request) {
	ex, svc, p, ok := h.fileTarget(w, r)
	if !ok {
		return
	}
	mode := r.URL.Query().Get("mode")
	if !octalMode.MatchString(mode) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be 3–4 octal digits (e.g. 644)"})
		return
	}
	if _, errMsg, err := runCapture(ex, "exec", svc, "chmod", mode, "--", p); err != nil {
		fileGatewayErr(w, errMsg, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "mode": mode})
}

// FileMkdir — POST .../containers/{service}/file-mkdir?path=<absDirPath>
// Creates a new directory (`mkdir`, errors if it already exists).
func (h *Handler) FileMkdir(w http.ResponseWriter, r *http.Request) {
	ex, svc, p, ok := h.fileTarget(w, r)
	if !ok {
		return
	}
	if p == "/" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}
	if _, errMsg, err := runCapture(ex, "exec", svc, "mkdir", "--", p); err != nil {
		fileGatewayErr(w, errMsg, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": p})
}

// FileNew — POST .../containers/{service}/file-new?path=<absFilePath>
// Creates a new empty file, failing if it already exists (noclobber).
func (h *Handler) FileNew(w http.ResponseWriter, r *http.Request) {
	ex, svc, p, ok := h.fileTarget(w, r)
	if !ok {
		return
	}
	if p == "/" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}
	// `set -C` (noclobber) makes the redirect fail if the file already exists.
	cmd := "set -C; : > " + shSingleQuote(p)
	if _, errMsg, err := runCapture(ex, "exec", svc, "sh", "-c", cmd); err != nil {
		if errMsg == "" {
			errMsg = "could not create file (it may already exist)"
		}
		fileGatewayErr(w, errMsg, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": p})
}
