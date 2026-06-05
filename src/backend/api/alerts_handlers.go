package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/alerts"
)

// ── Alert Rules (6a) ─────────────────────────────────────────────────────────────

// GET /api/alerts/rules
func (h *Handler) ListAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := alerts.ListRules(h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// POST /api/alerts/rules
func (h *Handler) CreateAlertRule(w http.ResponseWriter, r *http.Request) {
	// Presence-aware decode: a freshly created rule should be active by default,
	// so an omitted "enabled" means true. An explicit `false` is still honoured.
	// The outer *bool field shadows the embedded Rule.Enabled for JSON decoding
	// (shallower field wins), letting us distinguish omitted from false.
	var body struct {
		alerts.Rule
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	rule := body.Rule
	rule.Enabled = body.Enabled == nil || *body.Enabled

	created, err := alerts.CreateRule(h.db, rule)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// PUT /api/alerts/rules/{id}
func (h *Handler) UpdateAlertRule(w http.ResponseWriter, r *http.Request) {
	id, err := parseTrailingID(r.URL.Path, "/api/alerts/rules/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body alerts.Rule
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	rule, err := alerts.UpdateRule(h.db, id, body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if rule == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

// DELETE /api/alerts/rules/{id}
func (h *Handler) DeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id, err := parseTrailingID(r.URL.Path, "/api/alerts/rules/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := alerts.DeleteRule(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Alert Events / Inbox (6c) ────────────────────────────────────────────────────

// GET /api/alerts/events?resolved=true&dismissed=true&limit=100
func (h *Handler) ListAlertEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opt := alerts.ListEventsOptions{
		IncludeResolved:  q.Get("resolved") == "true",
		IncludeDismissed: q.Get("dismissed") == "true",
		Limit:            200,
	}
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			opt.Limit = n
		}
	}
	events, err := alerts.ListEvents(h.db, opt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// GET /api/alerts/events/unread-count
func (h *Handler) AlertUnreadCount(w http.ResponseWriter, r *http.Request) {
	n, err := alerts.UnreadCount(h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"unread_count": n})
}

// POST /api/alerts/events/{id}/dismiss
func (h *Handler) DismissAlert(w http.ResponseWriter, r *http.Request) {
	// path: /api/alerts/events/{id}/dismiss
	raw := strings.TrimPrefix(r.URL.Path, "/api/alerts/events/")
	raw = strings.TrimSuffix(raw, "/dismiss")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := alerts.DismissEvent(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.broadcastUnread("dismissed")
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/alerts/events/dismiss-all
func (h *Handler) DismissAllAlerts(w http.ResponseWriter, r *http.Request) {
	n, err := alerts.DismissAll(h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.broadcastUnread("dismissed")
	writeJSON(w, http.StatusOK, map[string]int64{"dismissed": n})
}

// GET /api/alerts/summary — active-alert aggregation for the dashboard (6e).
func (h *Handler) AlertSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := alerts.Summary(h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// GET /api/alerts/meta — condition types & severities for the rule form.
func (h *Handler) AlertMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"conditions": []map[string]any{
			{"value": alerts.CondContainerDown, "label": "Container down", "numeric": false, "scope": "stack"},
			{"value": alerts.CondRestartCount, "label": "Restart count above", "numeric": true, "scope": "stack", "unit": "restarts"},
			{"value": alerts.CondCPUAbovePct, "label": "CPU usage above", "numeric": true, "scope": "stack", "unit": "%"},
			{"value": alerts.CondMemoryAbovePct, "label": "Memory usage above", "numeric": true, "scope": "stack", "unit": "%"},
			{"value": alerts.CondDiskAbovePct, "label": "Host disk usage above", "numeric": true, "scope": "host", "unit": "%"},
			{"value": alerts.CondBackupFailed, "label": "Backup failed", "numeric": false, "scope": "stack"},
			{"value": alerts.CondImageUpdate, "label": "Image update available", "numeric": false, "scope": "stack"},
		},
		"severities": []string{alerts.SeverityInfo, alerts.SeverityWarning, alerts.SeverityCritical},
	})
}

// broadcastUnread pushes the current unread count to SSE subscribers so the bell
// badge updates immediately after a dismiss.
func (h *Handler) broadcastUnread(action string) {
	if h.alertBroker == nil {
		return
	}
	n, _ := alerts.UnreadCount(h.db)
	h.alertBroker.Publish(alerts.BuildAlertSSE(action, nil, n))
}

// parseTrailingID extracts the integer id immediately after prefix in path,
// ignoring any further segments. e.g. ("/api/alerts/rules/7", "/api/alerts/rules/") → 7
func parseTrailingID(path, prefix string) (int64, error) {
	raw := strings.TrimPrefix(path, prefix)
	raw = strings.Split(raw, "/")[0]
	return strconv.ParseInt(raw, 10, 64)
}
