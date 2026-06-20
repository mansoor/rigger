package api

import (
	"net/http"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
)

// Invite registration + email verification + self-service profile (Phase 5.1b).

// GET /api/register/info?token=  — unauthenticated; returns the invitee's email
// so the registration page can show who it's for.
func (h *Handler) RegisterInfo(w http.ResponseWriter, r *http.Request) {
	u, err := h.auth.RegisterInfo(r.URL.Query().Get("token"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this invite link is invalid or has expired"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"email": u.Email, "username": u.Username})
}

// POST /api/register/complete  {token, password, phone?, username?} — unauthenticated.
// Sets the password, activates the account (email verified), and logs the user in.
func (h *Handler) CompleteRegistration(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
		Phone    string `json:"phone"`
		Username string `json:"username"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	uid, err := h.auth.CompleteRegistration(body.Token, body.Password, body.Phone, body.Username)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	access, refresh, err := h.auth.IssueSession(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	setRefreshCookie(w, r, refresh)
	writeJSON(w, http.StatusOK, map[string]string{"token": access})
}

// POST /api/auth/verify-email  {token} — unauthenticated (the token proves ownership).
func (h *Handler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &body); err != nil || body.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token is required"})
		return
	}
	if _, err := h.auth.VerifyEmail(body.Token); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this verification link is invalid or has expired"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /api/auth/resend-verification — authenticated; re-sends/surfaces a verify link.
func (h *Handler) ResendVerification(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	u, err := h.auth.GetUser(claims.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if u.EmailVerified {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "already_verified": true})
		return
	}
	h.sendVerifyAndRespond(w, r, u.ID, u.Email)
}

// GET /api/auth/profile — the signed-in user's own record (email, display name,
// phone, role, verification). The profile screen reads this to pre-fill all
// fields from the DB rather than the JWT, so editing one field doesn't blank the
// others.
func (h *Handler) GetProfile(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	u, err := h.auth.GetUser(claims.UserID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// PUT /api/auth/profile  {email?, phone?, username?} — authenticated self-service.
func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		Username string `json:"username"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	u, emailChanged, err := h.auth.UpdateProfile(claims.UserID, body.Email, body.Phone, body.Username)
	if err != nil {
		writeJSON(w, userErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	resp := map[string]any{"user": u}
	if emailChanged {
		if raw, terr := h.auth.CreateVerifyToken(u.ID); terr == nil {
			link := h.baseURL(r) + "/verify-email?token=" + raw
			if sent, _ := h.sendUserLink(u.Email, "Verify your Rigger email", "Please verify your new email address.", "Verify your email", link); sent {
				resp["email_sent"] = true
			} else {
				resp["verify_link"] = link
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// sendVerifyAndRespond mints a verify token, emails it if possible, and returns
// either email_sent or the surfaced link.
func (h *Handler) sendVerifyAndRespond(w http.ResponseWriter, r *http.Request, uid int64, email string) {
	raw, err := h.auth.CreateVerifyToken(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	link := h.baseURL(r) + "/verify-email?token=" + raw
	resp := map[string]any{"status": "ok"}
	if sent, _ := h.sendUserLink(strings.ToLower(email), "Verify your Rigger email", "Please verify your email address.", "Verify your email", link); sent {
		resp["email_sent"] = true
	} else {
		resp["verify_link"] = link
	}
	writeJSON(w, http.StatusOK, resp)
}
