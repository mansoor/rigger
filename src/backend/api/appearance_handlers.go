package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/settings"
)

// Appearance precedence (W7): the effective prefs are merged FIELD-BY-FIELD across
// tiers — global default ⊕ workspace default ⊕ the user's own — so a later tier
// overrides individual fields while inheriting the rest (e.g. a workspace can set a
// default theme yet inherit the global typography). All three are JSON blobs (theme
// + typography) produced by the frontend; the backend stores them as opaque strings
// and only parses here to merge.

// GET /api/auth/appearance?ws={key} — the caller's EFFECTIVE appearance prefs.
func (h *Handler) GetAppearance(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	userPrefs, _ := h.auth.GetAppearance(claims.UserID)
	wsPrefs := ""
	if ws := r.URL.Query().Get("ws"); ws != "" {
		if vals, err := settings.GetWorkspaceSettings(h.db, ws); err == nil {
			wsPrefs = vals["appearance_prefs"]
		}
	}
	globalPrefs := h.appSetting("appearance_prefs")

	// Merge low→high so the highest tier present wins per field; source reports the
	// highest tier that contributed anything.
	merged := map[string]any{}
	source := ""
	apply := func(s, tier string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) != nil || len(m) == 0 {
			return
		}
		for k, v := range m {
			merged[k] = v
		}
		source = tier
	}
	apply(globalPrefs, "global")
	apply(wsPrefs, "workspace")
	apply(userPrefs, "user")

	out := ""
	if len(merged) > 0 {
		if b, err := json.Marshal(merged); err == nil {
			out = string(b)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"appearance_prefs": out, "source": source})
}

// PUT /api/auth/appearance  {appearance_prefs: "<json>"} — the caller's own prefs
// (empty string clears the override so they inherit the workspace/global default).
func (h *Handler) PutAppearance(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		AppearancePrefs string `json:"appearance_prefs"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := h.auth.SetAppearance(claims.UserID, body.AppearancePrefs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
