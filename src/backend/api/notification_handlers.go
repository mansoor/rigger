package api

import (
	"net/http"
	"strings"

	"github.com/mansoor/rigger/ui/internal/notify"
)

// ── Notification Channels (6b) ───────────────────────────────────────────────────

// GET /api/settings/notification-channels
func (h *Handler) ListNotificationChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := notify.ListChannels(h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, channels)
}

// POST /api/settings/notification-channels
func (h *Handler) CreateNotificationChannel(w http.ResponseWriter, r *http.Request) {
	// Presence-aware decode so an omitted "enabled" defaults to true.
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

// PUT /api/settings/notification-channels/{id}
func (h *Handler) UpdateNotificationChannel(w http.ResponseWriter, r *http.Request) {
	id, err := parseTrailingID(r.URL.Path, "/api/settings/notification-channels/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
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
	if updated == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DELETE /api/settings/notification-channels/{id}
func (h *Handler) DeleteNotificationChannel(w http.ResponseWriter, r *http.Request) {
	id, err := parseTrailingID(r.URL.Path, "/api/settings/notification-channels/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := notify.DeleteChannel(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/settings/notification-channels/{id}/test
// Sends a test notification through the channel and reports the result
// synchronously so the UI can show success/failure.
func (h *Handler) TestNotificationChannel(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/test")
	id, err := parseTrailingID(path, "/api/settings/notification-channels/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ch, err := notify.GetChannel(h.db, id)
	if err != nil || ch == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
		return
	}
	if h.notifier == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "notifications are not configured"})
		return
	}
	test := notify.Notification{
		Title: "Rigger test notification",
		Body:  "This is a test from Rigger. If you received it, the channel \"" + ch.Name + "\" is working.",
		Level: notify.LevelInfo,
	}
	if err := h.notifier.Send(*ch, test); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Test notification sent"})
}

var errChannelNameTaken = &channelErr{"a channel with that name already exists"}

type channelErr struct{ msg string }

func (e *channelErr) Error() string { return e.msg }
