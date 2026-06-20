package api

import (
	"errors"
	"net/http"

	"github.com/mansoor/rigger/ui/internal/auth"
)

// Optional 2FA (TOTP) self-service: status, begin enrollment, enable, disable.
// All routes are JWT-authenticated and operate on the caller's own account.

// GET /api/auth/2fa — whether the caller has 2FA enabled.
func (h *Handler) TwoFAStatus(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": h.auth.TOTPEnabled(claims.UserID)})
}

// POST /api/auth/2fa/begin — generate a fresh secret (still disabled) and return it
// plus the otpauth URI for the authenticator app. Refused if 2FA is already on.
func (h *Handler) TwoFABegin(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	secret, uri, err := h.auth.BeginTOTPEnroll(claims.UserID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret, "otpauth_url": uri})
}

// POST /api/auth/2fa/enable {code} — verify a code and turn 2FA on.
func (h *Handler) TwoFAEnable(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := h.auth.EnableTOTP(claims.UserID, body.Code); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrTOTPInvalid) {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true})
}

// POST /api/auth/2fa/disable {code} — verify a current code and turn 2FA off.
func (h *Handler) TwoFADisable(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := h.auth.DisableTOTP(claims.UserID, body.Code); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
}
