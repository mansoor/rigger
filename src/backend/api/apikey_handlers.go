package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/apikey"
	"github.com/mansoor/rigger/ui/internal/auth"
)

// Admin-only management of API keys (the external /api/v1 credentials). Mounted under
// /api/settings/api-keys, so it inherits the settings group's superadmin gate — a key
// can grant cross-project access, so only global admins mint them.

// GetApiKeyScopes: GET /api/settings/api-key-scopes — the scope catalog (groups + their
// operations) for the create form's expandable picker and the generated docs.
func (h *Handler) GetApiKeyScopes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, apikey.Groups)
}

// ListApiKeys: GET /api/settings/api-keys — all keys (never the raw token).
func (h *Handler) ListApiKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := apikey.List(h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

// CreateApiKey: POST /api/settings/api-keys. Body sets name, scopes (granular op ids),
// project access, optional specific projects, per-project rate limit, and validity in
// days (0 = never expires). Returns the created key WITH its raw token (shown once).
func (h *Handler) CreateApiKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string              `json:"name"`
		Scopes        []string            `json:"scopes"`
		ProjectAccess string              `json:"project_access"` // all | specific
		Projects      []apikey.ProjectRef `json:"projects"`
		RateLimit     int                 `json:"rate_limit"`
		ExpiresInDays int                 `json:"expires_in_days"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if len(body.Scopes) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "select at least one scope"})
		return
	}
	for _, s := range body.Scopes {
		if !apikey.ValidOp(s) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown scope: " + s})
			return
		}
	}
	if body.ProjectAccess != "specific" {
		body.ProjectAccess = "all"
	}
	if body.ProjectAccess == "specific" && len(body.Projects) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "select at least one project for specific access"})
		return
	}
	if body.RateLimit < 0 {
		body.RateLimit = 0
	}

	raw, hash, prefix, err := apikey.Generate()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	createdBy := "admin"
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		createdBy = claims.Username
	}
	var expiresAt int64
	if body.ExpiresInDays > 0 {
		expiresAt = time.Now().Add(time.Duration(body.ExpiresInDays) * 24 * time.Hour).Unix()
	}
	k := apikey.Key{
		Name: strings.TrimSpace(body.Name), KeyPrefix: prefix, Scopes: body.Scopes,
		ProjectAccess: body.ProjectAccess, Projects: body.Projects, RateLimit: body.RateLimit,
		Enabled: true, CreatedBy: createdBy, CreatedAt: time.Now().Unix(), ExpiresAt: expiresAt,
	}
	id, err := apikey.Create(h.db, hash, k)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	k.ID = id
	k.Token = raw // the one-time reveal
	writeJSON(w, http.StatusCreated, k)
}

// apiKeyIDFromPath extracts the trailing {id} from /api/settings/api-keys/{id}.
func apiKeyIDFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return parts[len(parts)-1]
}

// UpdateApiKey: PUT /api/settings/api-keys/{id} — currently supports flipping enabled
// (revoke/restore). Body: {"enabled": bool}.
func (h *Handler) UpdateApiKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(apiKeyIDFromPath(r.URL.Path), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil || body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enabled (bool) is required"})
		return
	}
	if err := apikey.SetEnabled(h.db, id, *body.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": *body.Enabled})
}

// DeleteApiKey: DELETE /api/settings/api-keys/{id}.
func (h *Handler) DeleteApiKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(apiKeyIDFromPath(r.URL.Path), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := apikey.Delete(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
