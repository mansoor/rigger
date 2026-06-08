package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
)

// User management (Phase 5 RBAC, roadmap 10a). All routes are admin-gated at the
// router; these handlers add data-integrity guards (last-admin, self-delete).

// GET /api/users
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.auth.ListUsers()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// POST /api/users  {email, role, username?} — invites a user (no password set).
// Returns the invite link when SMTP isn't configured so the admin can share it.
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	// The inviter must have a verified email (pragmatic gate).
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !h.auth.IsVerified(claims.UserID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "verify your own email before inviting other users"})
		return
	}
	var body struct {
		Email    string `json:"email"`
		Role     string `json:"role"`
		Username string `json:"username"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	u, raw, err := h.auth.InviteUser(body.Email, body.Role, body.Username)
	if err != nil {
		writeJSON(w, userErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	resp := map[string]any{"user": u}
	link := h.baseURL(r) + "/register?token=" + raw
	if sent, _ := h.sendUserLink(u.Email, "You're invited to Rigger", "You've been invited to Rigger. Set your password to finish creating your account.", "Complete your registration", link); sent {
		resp["email_sent"] = true
	} else {
		resp["invite_link"] = link
	}
	writeJSON(w, http.StatusCreated, resp)
}

// POST /api/users/{id}/resend-invite — new invite link for a pending user.
func (h *Handler) ResendInvite(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/resend-invite")
	id, err := parseTrailingID(path, "/api/users/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	raw, err := h.auth.ResendInvite(id)
	if err != nil {
		writeJSON(w, userErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	u, _ := h.auth.GetUser(id)
	resp := map[string]any{"status": "ok"}
	link := h.baseURL(r) + "/register?token=" + raw
	if u != nil {
		if sent, _ := h.sendUserLink(u.Email, "Your Rigger invitation", "Here's your Rigger invitation link.", "Complete your registration", link); sent {
			resp["email_sent"] = true
		} else {
			resp["invite_link"] = link
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// PUT /api/users/{id}  {role, password?}
func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := parseTrailingID(r.URL.Path, "/api/users/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body struct {
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	u, err := h.auth.UpdateUser(id, body.Role, body.Password)
	if err != nil {
		writeJSON(w, userErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// DELETE /api/users/{id}
func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := parseTrailingID(r.URL.Path, "/api/users/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && claims.UserID == id {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "you cannot delete your own account"})
		return
	}
	if err := h.auth.DeleteUser(id); err != nil {
		writeJSON(w, userErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func userErrStatus(err error) int {
	switch {
	case errors.Is(err, auth.ErrUserExists):
		return http.StatusConflict
	case errors.Is(err, auth.ErrLastAdmin):
		return http.StatusConflict
	case errors.Is(err, auth.ErrInvalidRole):
		return http.StatusBadRequest
	case errors.Is(err, auth.ErrUserNotFound):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}
