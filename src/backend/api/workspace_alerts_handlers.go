package api

import (
	"net/http"
	"os"
	"strconv"

	"github.com/mansoor/rigger/ui/internal/alerts"
	"github.com/mansoor/rigger/ui/internal/notify"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Workspace-scoped Alert Rules (Phase 3). A workspace manages rules targeting its
// own tier (ws_key=wsKey). Host/infra-level conditions (e.g. disk) stay global and
// are managed from admin Settings. Notify channels must come from the workspace's
// channel pool.

func wsRuleID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("ruleid"), 10, 64)
}

// validateWorkspaceRule enforces the workspace-rule constraints: stack-scope
// condition only, and every notify channel must be in the workspace's pool.
// Returns an error message (empty if ok).
func (h *Handler) validateWorkspaceRule(ws string, rule alerts.Rule) string {
	if alerts.IsHostScoped(rule.ConditionType) {
		return "that condition is a host-level metric and is managed globally in Settings"
	}
	for _, id := range rule.NotifyChannelIDs {
		if inPool, _ := notify.ChannelInWorkspacePool(h.db, ws, id); !inPool {
			return "a selected notification channel is not available to this workspace"
		}
	}
	return ""
}

// GET /api/workspaces/{ws}/alerts/rules
func (h *Handler) ListWorkspaceAlertRules(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	rules, err := alerts.ListRulesForWorkspace(h.db, ws)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// POST /api/workspaces/{ws}/alerts/rules
func (h *Handler) CreateWorkspaceAlertRule(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if _, err := os.Stat(wspath.WorkspaceMeta(h.workspacesDir, ws)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
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
	rule.WorkspaceKey = ws // pin to this workspace tier
	if msg := h.validateWorkspaceRule(ws, rule); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	created, err := alerts.CreateRule(h.db, rule)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// PUT /api/workspaces/{ws}/alerts/rules/{id}
func (h *Handler) UpdateWorkspaceAlertRule(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsRuleID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	existing, err := alerts.GetRule(h.db, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if existing.WorkspaceKey != ws {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this rule belongs to a different scope"})
		return
	}
	var body alerts.Rule
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	body.WorkspaceKey = ws // can't move a rule out of its workspace
	if msg := h.validateWorkspaceRule(ws, body); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	rule, err := alerts.UpdateRule(h.db, id, body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

// DELETE /api/workspaces/{ws}/alerts/rules/{id}
func (h *Handler) DeleteWorkspaceAlertRule(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsRuleID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	existing, err := alerts.GetRule(h.db, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if existing.WorkspaceKey != ws {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this rule belongs to a different scope"})
		return
	}
	if err := alerts.DeleteRule(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
