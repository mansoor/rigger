package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gomarkdown/markdown"
	mdhtml "github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
	"github.com/microcosm-cc/bluemonday"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Per-project Wiki / Notes — multiple NAMED Markdown documents stored under a
// notes/ folder in the project dir: one {id}.md file per note plus an ordered
// index (notes.json) that maps id -> display name (the header tab). Markdown is
// rendered + sanitized server-side (gomarkdown + bluemonday), so the UI ships no
// markdown library and notes can't become a stored-XSS vector.

const legacyNotesFile = "notes.md" // single-note layout (pre multi-note) — migrated on read
const notesIndexFile = "notes.json"

// maxNotesBytes caps one note. Generous for docs; guards against abuse.
const maxNotesBytes = 1 << 20 // 1 MiB

var notesSanitizer = bluemonday.UGCPolicy()

// renderMarkdown converts Markdown to sanitized HTML.
func renderMarkdown(md string) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}
	p := parser.NewWithExtensions(parser.CommonExtensions | parser.AutoHeadingIDs | parser.NoEmptyLineBeforeBlock)
	doc := p.Parse([]byte(md))
	renderer := mdhtml.NewRenderer(mdhtml.RendererOptions{Flags: mdhtml.CommonFlags | mdhtml.HrefTargetBlank})
	unsafe := markdown.Render(doc, renderer)
	return string(notesSanitizer.SanitizeBytes(unsafe))
}

type noteMeta struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (h *Handler) notesDir(ws, name string) string {
	return filepath.Join(wspath.ProjectDir(h.workspacesDir, ws, name), "notes")
}

func (h *Handler) noteFilePath(ws, name, id string) string {
	return filepath.Join(h.notesDir(ws, name), id+".md")
}

// loadNotes returns the ordered note index, migrating a legacy single notes.md
// into the multi-note layout on first read.
func (h *Handler) loadNotes(ws, name string) []noteMeta {
	dir := h.notesDir(ws, name)
	if data, err := os.ReadFile(filepath.Join(dir, notesIndexFile)); err == nil {
		var notes []noteMeta
		if json.Unmarshal(data, &notes) == nil {
			return notes
		}
	}
	// Migrate the old single-file notes.md → a note named "Notes".
	legacy := filepath.Join(wspath.ProjectDir(h.workspacesDir, ws, name), legacyNotesFile)
	if data, err := os.ReadFile(legacy); err == nil && strings.TrimSpace(string(data)) != "" {
		if os.MkdirAll(dir, 0o755) == nil && os.WriteFile(filepath.Join(dir, "notes.md"), data, 0o644) == nil {
			notes := []noteMeta{{ID: "notes", Name: "Notes"}}
			if h.saveNotes(ws, name, notes) == nil {
				_ = os.Remove(legacy) //nolint:errcheck
				return notes
			}
		}
	}
	return []noteMeta{}
}

func (h *Handler) saveNotes(ws, name string, notes []noteMeta) error {
	dir := h.notesDir(ws, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(notes, "", "  ")
	return os.WriteFile(filepath.Join(dir, notesIndexFile), data, 0o644)
}

// findNote returns the note's display name and whether it's a known id — the
// gate that keeps a client-supplied id from escaping the notes dir (path safety).
func findNote(notes []noteMeta, id string) (string, bool) {
	for _, n := range notes {
		if n.ID == id {
			return n.Name, true
		}
	}
	return "", false
}

// slugifyNote makes a filesystem-safe, unique id from a display name.
func slugifyNote(name string, taken map[string]bool) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "note"
	}
	base, i := slug, 2
	for taken[slug] {
		slug = fmt.Sprintf("%s-%d", base, i)
		i++
	}
	return slug
}

// GET /api/workspaces/{workspace}/projects/{name}/notes — the ordered note index.
func (h *Handler) ListProjectNotes(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": h.loadNotes(ws, name)})
}

// GET /api/workspaces/{workspace}/projects/{name}/notes/{noteId} — one note's
// raw markdown + rendered HTML.
func (h *Handler) GetProjectNote(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	id := r.PathValue("noteId")
	noteName, ok := findNote(h.loadNotes(ws, name), id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "note not found"})
		return
	}
	content := ""
	if data, err := os.ReadFile(h.noteFilePath(ws, name, id)); err == nil {
		content = string(data)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": noteName, "content": content, "html": renderMarkdown(content)})
}

// POST /api/workspaces/{workspace}/projects/{name}/notes   body: {"name": "..."}
func (h *Handler) CreateProjectNote(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required to add notes"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a note name is required"})
		return
	}
	dir := wspath.ProjectDir(h.workspacesDir, ws, name)
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	notes := h.loadNotes(ws, name)
	taken := map[string]bool{}
	for _, n := range notes {
		taken[n.ID] = true
	}
	note := noteMeta{ID: slugifyNote(body.Name, taken), Name: strings.TrimSpace(body.Name)}
	notes = append(notes, note)
	if err := h.saveNotes(ws, name, notes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, note)
}

// PUT /api/workspaces/{workspace}/projects/{name}/notes/{noteId}
// body: {"content": "...", "name": "..."(optional rename)}
func (h *Handler) UpdateProjectNote(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required to edit notes"})
		return
	}
	id := r.PathValue("noteId")
	notes := h.loadNotes(ws, name)
	if _, ok := findNote(notes, id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "note not found"})
		return
	}
	var body struct {
		Content string  `json:"content"`
		Name    *string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content is required"})
		return
	}
	if len(body.Content) > maxNotesBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "note is too large (max 1 MB)"})
		return
	}
	if err := os.MkdirAll(h.notesDir(ws, name), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := os.WriteFile(h.noteFilePath(ws, name, id), []byte(body.Content), 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	noteName := ""
	for i := range notes {
		if notes[i].ID == id {
			if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
				notes[i].Name = strings.TrimSpace(*body.Name)
			}
			noteName = notes[i].Name
		}
	}
	_ = h.saveNotes(ws, name, notes) //nolint:errcheck
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": noteName, "content": body.Content, "html": renderMarkdown(body.Content)})
}

// DELETE /api/workspaces/{workspace}/projects/{name}/notes/{noteId}
func (h *Handler) DeleteProjectNote(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required to delete notes"})
		return
	}
	id := r.PathValue("noteId")
	notes := h.loadNotes(ws, name)
	if _, ok := findNote(notes, id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "note not found"})
		return
	}
	kept := make([]noteMeta, 0, len(notes))
	for _, n := range notes {
		if n.ID != id {
			kept = append(kept, n)
		}
	}
	_ = os.Remove(h.noteFilePath(ws, name, id)) //nolint:errcheck
	if err := h.saveNotes(ws, name, kept); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/workspaces/{workspace}/projects/{name}/notes/render  body: {"content"}
// Live preview while editing — renders without persisting.
func (h *Handler) RenderProjectNotes(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	_ = readJSON(r, &body) //nolint:errcheck — empty ⇒ empty preview
	if len(body.Content) > maxNotesBytes {
		body.Content = body.Content[:maxNotesBytes]
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": renderMarkdown(body.Content)})
}
