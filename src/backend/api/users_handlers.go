package api

import (
	"errors"
	"net/http"

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

// POST /api/users  {username, password, role}
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	u, err := h.auth.AdminCreateUser(body.Username, body.Password, body.Role)
	if err != nil {
		writeJSON(w, userErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, u)
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
