package api

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/proxyroutes"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Workspace-scoped Access Lists (docs/design/workspace-plugins-and-access-lists.md).
// STRICT isolation: a workspace sees and manages ONLY its own lists — never the global
// (Proxy Service) lists nor another workspace's. Reusable auth + IP + GeoIP policy that a
// project env attaches by id; rendered as shared file-provider middlewares (RenderSharedACLs).
// Routes mounted under /api/workspaces/{ws}/access-lists (GateWorkspace + wsMinRole: viewer
// to read, admin to write). Reuses accessListReq / maskAccessList / toAccessList from
// proxy_handlers.go (passwords arrive plaintext, stored bcrypt, masked on read).

func wsACLID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("aclid"), 10, 64)
}

// ownsAccessList loads a list and reports whether it exists and belongs to wsKey.
func (h *Handler) ownsAccessList(wsKey string, id int64) (proxyroutes.AccessList, bool, bool) {
	a, ok, err := h.proxyStore().GetAccessList(id)
	if err != nil || !ok {
		return proxyroutes.AccessList{}, false, false
	}
	return a, true, a.Workspace == wsKey
}

// GET /api/workspaces/{ws}/access-lists — this workspace's lists only.
func (h *Handler) ListWorkspaceAccessLists(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	lists, err := h.proxyStore().ListAccessListsByScope(ws)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]proxyroutes.AccessList, len(lists))
	for i, a := range lists {
		out[i] = maskAccessList(a)
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/workspaces/{ws}/access-lists — create a list private to this workspace.
func (h *Handler) CreateWorkspaceAccessList(w http.ResponseWriter, r *http.Request) {
	defer recoverProxy(w, "create ws access list")
	ws := r.PathValue("workspace")
	if _, err := os.Stat(wspath.WorkspaceMeta(h.workspacesDir, ws)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	var req accessListReq
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	a, err := h.toAccessList(req, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(a.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	a.Workspace = ws // scope from the path, never the client
	id, err := h.proxyStore().CreateAccessList(a)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	a.ID = id
	if err := h.renderProxy(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "saved, but failed to apply: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, maskAccessList(a))
}

// PUT /api/workspaces/{ws}/access-lists/{id} — edit a list owned by this workspace.
func (h *Handler) UpdateWorkspaceAccessList(w http.ResponseWriter, r *http.Request) {
	defer recoverProxy(w, "update ws access list")
	ws := r.PathValue("workspace")
	id, err := wsACLID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	existing, found, owned := h.ownsAccessList(ws, id)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !owned {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this access list belongs to another scope"})
		return
	}
	var req accessListReq
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	a, err := h.toAccessList(req, &existing) // preserves scope (immutable on update)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(a.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if err := h.proxyStore().UpdateAccessList(a); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := h.renderProxy(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "saved, but failed to apply: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, maskAccessList(a))
}

// DELETE /api/workspaces/{ws}/access-lists/{id} — delete a list owned by this workspace.
func (h *Handler) DeleteWorkspaceAccessList(w http.ResponseWriter, r *http.Request) {
	defer recoverProxy(w, "delete ws access list")
	ws := r.PathValue("workspace")
	id, err := wsACLID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	_, found, owned := h.ownsAccessList(ws, id)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !owned {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this access list belongs to another scope"})
		return
	}
	if err := h.proxyStore().DeleteAccessList(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := h.renderProxy(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "deleted, but failed to apply: " + err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
