package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/keygen"
	"github.com/mansoor/rigger/ui/internal/workspace"
)

// keyLengths returns the effective key-length bounds (defaults 3/4), clamped to
// [1,12] and made coherent (max>=min). Configurable via Settings → General.
func (h *Handler) keyLengths() (min, max int) {
	min, max = 3, 4
	read := func(k string, def int) int {
		var s string
		if h.db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, k).Scan(&s) == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 1 && n <= 12 {
				return n
			}
		}
		return def
	}
	min = read("key_min_length", 3)
	max = read("key_max_length", 4)
	if max < min {
		max = min
	}
	return
}

// workspaceKeyTaken reports which workspace keys (= top-level dir names) exist.
func (h *Handler) workspaceKeyTaken() func(string) bool {
	wss, _ := workspace.ListWorkspaces(h.workspacesDir)
	m := make(map[string]bool, len(wss))
	for _, w := range wss {
		m[w.Key] = true
	}
	return func(s string) bool { return m[s] }
}

// projectKeyTaken reports which project keys exist within one workspace.
func (h *Handler) projectKeyTaken(wsKey string) func(string) bool {
	ps, _ := workspace.ListProjects(h.workspacesDir, wsKey)
	m := make(map[string]bool, len(ps))
	for _, p := range ps {
		m[p.Name] = true // project dir name == project key
	}
	return func(s string) bool { return m[s] }
}

// taken resolves the right uniqueness scope from the request's type/workspace.
func (h *Handler) keyTakenFor(typ, wsKey string) func(string) bool {
	if typ == "project" {
		return h.projectKeyTaken(wsKey)
	}
	return h.workspaceKeyTaken()
}

// GET /api/keys/suggest?type=workspace&name=Acme%20Corp
// GET /api/keys/suggest?type=project&workspace=acc&name=Web%20App
// Returns a derived, collision-free, validated key for live preview.
func (h *Handler) SuggestKey(w http.ResponseWriter, r *http.Request) {
	typ := r.URL.Query().Get("type")
	name := r.URL.Query().Get("name")
	wsKey := r.URL.Query().Get("workspace")
	min, max := h.keyLengths()
	key := keygen.Suggest(name, min, max, h.keyTakenFor(typ, wsKey))
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "min": min, "max": max})
}

// GET /api/keys/check?type=workspace&key=acc
// GET /api/keys/check?type=project&workspace=acc&key=web
// Validates a user-supplied override: format (within min/max, lowercase alnum)
// and availability in scope.
func (h *Handler) CheckKey(w http.ResponseWriter, r *http.Request) {
	typ := r.URL.Query().Get("type")
	wsKey := r.URL.Query().Get("workspace")
	key := keygen.Normalize(r.URL.Query().Get("key"))
	min, max := h.keyLengths()

	resp := map[string]any{"key": key, "min": min, "max": max, "valid": false, "available": false}
	if !keygen.Valid(key, min, max) {
		resp["error"] = "Key must be " + strconv.Itoa(min) + "–" + strconv.Itoa(max) + " lowercase letters/digits."
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["valid"] = true
	if h.keyTakenFor(typ, wsKey)(key) {
		resp["error"] = "That key is already in use."
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["available"] = true
	writeJSON(w, http.StatusOK, resp)
}
