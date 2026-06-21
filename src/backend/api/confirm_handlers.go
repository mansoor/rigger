package api

import (
	"net/http"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/settings"
)

// Destructive-confirmation precedence: global default (Admin → Preferences) →
// workspace default (Manage Workspace → Preferences) → per-user override (Profile →
// General). A tier that turns confirmations ON can also LOCK it ("can users
// override? = no"), preventing lower tiers from changing it. A tier that turns
// confirmations OFF never locks — safety can only be tightened downward, so a
// cautious user can always re-enable. Default (unset) = ON + overridable.

// resolveConfirm computes the effective confirm setting for a user in workspace ws.
//   enabled     — does this user get a confirmation dialog?
//   canOverride — may the user change it (no higher tier locked it)?
//   inherited   — the value before the user's own override (the admin/ws default).
//   userVal     — the user's stored override: "" | "true" | "false".
func (h *Handler) resolveConfirm(userID int64, ws string) (enabled, canOverride, inherited bool, userVal string) {
	gVal := h.appSetting("confirm_destructive")
	gAllow := h.appSetting("confirm_destructive_allow_override")

	wsVal, wsAllow := "", ""
	if ws != "" {
		if vals, err := settings.GetWorkspaceSettings(h.db, ws); err == nil {
			wsVal, wsAllow = vals["confirm_destructive"], vals["confirm_destructive_allow_override"]
		}
	}

	enabled = gVal != "false" // default ON
	locked := enabled && gAllow == "false"
	if !locked && (wsVal == "true" || wsVal == "false") {
		enabled = wsVal == "true"
		locked = enabled && wsAllow == "false"
	}
	canOverride = !locked
	inherited = enabled // the tier default, before the user's own choice

	if v, err := h.auth.GetConfirmPref(userID); err == nil {
		userVal = v
		if canOverride && (v == "true" || v == "false") {
			enabled = v == "true"
		}
	}
	return enabled, canOverride, inherited, userVal
}

// GET /api/auth/confirm?ws={key} — the caller's effective destructive-confirm state.
func (h *Handler) GetConfirm(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	enabled, canOverride, inherited, userVal := h.resolveConfirm(claims.UserID, r.URL.Query().Get("ws"))
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":      enabled,
		"can_override": canOverride,
		"inherited":    inherited,
		"user_value":   userVal,
	})
}

// PUT /api/auth/confirm  {value: ""|"true"|"false"} — the caller's own override
// ("" clears it). Rejected when a higher tier has locked the setting.
func (h *Handler) PutConfirm(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Value string `json:"value"`
		WS    string `json:"ws"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if body.Value != "" && body.Value != "true" && body.Value != "false" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "value must be '', 'true' or 'false'"})
		return
	}
	if _, canOverride, _, _ := h.resolveConfirm(claims.UserID, body.WS); !canOverride {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this setting is locked by your administrator"})
		return
	}
	if err := h.auth.SetConfirmPref(claims.UserID, body.Value); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
