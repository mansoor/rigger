package api

import (
	"net/http"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/settings"
)

// Appearance precedence (W7): a user's own prefs win; otherwise the selected
// workspace's default; otherwise the global default. All three are JSON blobs
// (theme + typography) produced by the frontend; the backend only stores/returns
// them as opaque strings.

// GET /api/auth/appearance?ws={key} — the caller's EFFECTIVE appearance prefs.
func (h *Handler) GetAppearance(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	// 1) the user's own override
	prefs, _ := h.auth.GetAppearance(claims.UserID)
	source := "user"
	// 2) the selected workspace's default
	if strings.TrimSpace(prefs) == "" {
		if ws := r.URL.Query().Get("ws"); ws != "" {
			if vals, err := settings.GetWorkspaceSettings(h.db, ws); err == nil {
				if v := strings.TrimSpace(vals["appearance_prefs"]); v != "" {
					prefs, source = v, "workspace"
				}
			}
		}
	}
	// 3) the global default
	if strings.TrimSpace(prefs) == "" {
		if v := strings.TrimSpace(h.appSetting("appearance_prefs")); v != "" {
			prefs, source = v, "global"
		} else {
			source = ""
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"appearance_prefs": prefs, "source": source})
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
