package api

import (
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
	enabled := h.auth.TOTPEnabled(claims.UserID)
	resp := map[string]any{"enabled": enabled}
	if enabled {
		resp["recovery_remaining"] = h.auth.RecoveryCodeCount(claims.UserID)
	}
	writeJSON(w, http.StatusOK, resp)
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
	codes, err := h.auth.EnableTOTP(claims.UserID, body.Code)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "recovery_codes": codes})
}

// POST /api/auth/2fa/recovery-codes {code} — regenerate the recovery code set
// (invalidates the old), gated on a valid current TOTP or recovery code.
func (h *Handler) TwoFARegenerateCodes(w http.ResponseWriter, r *http.Request) {
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
	codes, err := h.auth.RegenerateRecoveryCodes(claims.UserID, body.Code)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
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
