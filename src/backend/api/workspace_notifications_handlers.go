package api

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/notify"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Workspace-scoped Notification Channels (Phase 3). A workspace sees its own
// channels (owner_scope='ws:{key}') plus any global channel granted to it. It
// may create/edit/delete only its own; globals are read-only here.

func wsChannelID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("channelid"), 10, 64)
}

func (h *Handler) ownsChannel(wsKey string, id int64) (*notify.Channel, bool) {
	ch, err := notify.GetChannel(h.db, id)
	if err != nil || ch == nil {
		return nil, false
	}
	return ch, ch.WorkspaceScope() == wsKey
}

// GET /api/workspaces/{ws}/notification-channels — the workspace's channel pool.
func (h *Handler) ListWorkspaceNotificationChannels(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	channels, err := notify.ListChannelsForWorkspace(h.db, ws)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, channels)
}

// POST /api/workspaces/{ws}/notification-channels — create a channel private to this workspace.
func (h *Handler) CreateWorkspaceNotificationChannel(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if _, err := os.Stat(wspath.WorkspaceMeta(h.workspacesDir, ws)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	var body struct {
		notify.Channel
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	ch := body.Channel
	ch.Enabled = body.Enabled == nil || *body.Enabled
	ch.OwnerScope = notify.WorkspaceOwnerScope(ws)

	created, err := notify.CreateChannel(h.db, ch)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "UNIQUE") {
			status = http.StatusConflict
			err = errChannelNameTaken
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// PUT /api/workspaces/{ws}/notification-channels/{id} — edit a channel owned by this workspace.
func (h *Handler) UpdateWorkspaceNotificationChannel(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsChannelID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ch, owned := h.ownsChannel(ws, id)
	if ch == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !owned {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this is a shared global channel — manage it from Settings"})
		return
	}
	var body notify.Channel
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	updated, err := notify.UpdateChannel(h.db, id, body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DELETE /api/workspaces/{ws}/notification-channels/{id} — delete a channel owned by this workspace.
func (h *Handler) DeleteWorkspaceNotificationChannel(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsChannelID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ch, owned := h.ownsChannel(ws, id)
	if ch == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !owned {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this is a shared global channel — manage it from Settings"})
		return
	}
	if err := notify.DeleteChannel(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/workspaces/{ws}/notification-channels/{id}/test — send a test, gated to the pool.
func (h *Handler) TestWorkspaceNotificationChannel(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsChannelID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if inPool, _ := notify.ChannelInWorkspacePool(h.db, ws, id); !inPool {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	h.testChannelByID(w, id)
}
