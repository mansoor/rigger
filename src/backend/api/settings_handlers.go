package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/settings"
)

// ── General Settings ──────────────────────────────────────────────────────────
// Key/value store in app_settings table. Used for ACME email and future config.

// GET /api/settings/general
func (h *Handler) GetGeneralSettings(w http.ResponseWriter, r *http.Request) {
	keys := []string{"acme_email", "rigger_domain", "traefik_enabled", "confirm_destructive", "appearance_prefs", "key_min_length", "key_max_length"}
	result := map[string]string{}
	for _, k := range keys {
		var val string
		h.db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, k).Scan(&val) //nolint:errcheck
		result[k] = val
	}
	// Always report the effective (defaulted, coherent) key-length bounds so the
	// UI shows real values even before an admin has set them.
	min, max := h.keyLengths()
	result["key_min_length"] = strconv.Itoa(min)
	result["key_max_length"] = strconv.Itoa(max)
	writeJSON(w, http.StatusOK, result)
}

// PUT /api/settings/general
func (h *Handler) PutGeneralSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	allowed := map[string]bool{"acme_email": true, "rigger_domain": true, "traefik_enabled": true, "confirm_destructive": true, "appearance_prefs": true, "key_min_length": true, "key_max_length": true}
	for k, v := range body {
		if !allowed[k] {
			continue
		}
		// Clamp the key-length bounds to a sane window on write; cross-coherence
		// (min<=max) is enforced at read time by keyLengths().
		if k == "key_min_length" || k == "key_max_length" {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				continue
			}
			if n < 1 {
				n = 1
			}
			if n > 12 {
				n = 12
			}
			v = strconv.Itoa(n)
		}
		h.db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
			k, v) //nolint:errcheck
	}
	h.GetGeneralSettings(w, r)
}

// ── Backup Targets ────────────────────────────────────────────────────────────

// GET /api/settings/backup-targets
func (h *Handler) ListBackupTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := settings.ListBackupTargets(h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, targets)
}

// POST /api/settings/backup-targets
func (h *Handler) CreateBackupTarget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string          `json:"name"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := readJSON(r, &body); err != nil || body.Name == "" || body.Type == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, type, and config are required"})
		return
	}
	if body.Config == nil {
		body.Config = json.RawMessage(`{}`)
	}
	t, err := settings.CreateBackupTarget(h.db, body.Name, body.Type, body.Config)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a backup target with that name already exists"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// PUT /api/settings/backup-targets/{id}
func (h *Handler) UpdateBackupTarget(w http.ResponseWriter, r *http.Request) {
	id, err := parseSettingsID(r.URL.Path, "/api/settings/backup-targets/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body struct {
		Name   string          `json:"name"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := readJSON(r, &body); err != nil || body.Name == "" || body.Type == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, type, and config are required"})
		return
	}
	if body.Config == nil {
		body.Config = json.RawMessage(`{}`)
	}
	t, err := settings.UpdateBackupTarget(h.db, id, body.Name, body.Type, body.Config)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// DELETE /api/settings/backup-targets/{id}
func (h *Handler) DeleteBackupTarget(w http.ResponseWriter, r *http.Request) {
	id, err := parseSettingsID(r.URL.Path, "/api/settings/backup-targets/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := settings.DeleteBackupTarget(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Docker Registries ─────────────────────────────────────────────────────────

// registryBody is the create/update payload. grants is admin-only — the workspace
// allowlist for a global registry ('*' = offered to all).
type registryBody struct {
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Username string   `json:"username"`
	Password string   `json:"password"` // empty on update = keep existing
	Grants   []string `json:"grants"`
}

// GET /api/settings/registries — admin view: all registries; global ones carry grants.
func (h *Handler) ListRegistries(w http.ResponseWriter, r *http.Request) {
	regs, err := settings.ListRegistries(h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for i := range regs {
		if regs[i].OwnerScope == "global" {
			regs[i].Grants, _ = settings.RegistryGrants(h.db, regs[i].ID) //nolint:errcheck
		}
	}
	writeJSON(w, http.StatusOK, regs)
}

// POST /api/settings/registries — admin create (global; grants default to '*').
func (h *Handler) CreateRegistry(w http.ResponseWriter, r *http.Request) {
	var body registryBody
	if err := readJSON(r, &body); err != nil || body.Name == "" || body.URL == "" || body.Username == "" || body.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, url, username, and password are required"})
		return
	}
	reg, err := settings.CreateRegistry(h.db, body.Name, body.URL, body.Username, body.Password, "global")
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a registry with that name already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	grants := body.Grants
	if grants == nil {
		grants = []string{"*"}
	}
	_ = settings.SetRegistryGrants(h.db, reg.ID, grants) //nolint:errcheck
	reg.Grants, _ = settings.RegistryGrants(h.db, reg.ID)
	// Auto-login after create
	_ = dockerLogin(body.URL, body.Username, body.Password)
	writeJSON(w, http.StatusCreated, reg)
}

// PUT /api/settings/registries/{id} — admin update incl. the workspace allowlist.
func (h *Handler) UpdateRegistry(w http.ResponseWriter, r *http.Request) {
	id, err := parseSettingsID(r.URL.Path, "/api/settings/registries/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body registryBody
	if err := readJSON(r, &body); err != nil || body.Name == "" || body.URL == "" || body.Username == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, url, and username are required"})
		return
	}
	reg, err := settings.UpdateRegistry(h.db, id, body.Name, body.URL, body.Username, body.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if reg == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if reg.OwnerScope == "global" && body.Grants != nil {
		_ = settings.SetRegistryGrants(h.db, id, body.Grants) //nolint:errcheck
	}
	reg.Grants, _ = settings.RegistryGrants(h.db, id)
	// Re-login if password changed
	if body.Password != "" {
		_ = dockerLogin(body.URL, body.Username, body.Password)
	}
	writeJSON(w, http.StatusOK, reg)
}

// DELETE /api/settings/registries/{id}
func (h *Handler) DeleteRegistry(w http.ResponseWriter, r *http.Request) {
	id, err := parseSettingsID(r.URL.Path, "/api/settings/registries/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := settings.DeleteRegistry(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/settings/registries/{id}/test
func (h *Handler) TestRegistry(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/test")
	id, err := parseSettingsID(path, "/api/settings/registries/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	reg, err := settings.GetRegistry(h.db, id)
	if err != nil || reg == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "registry not found"})
		return
	}
	if loginErr := dockerLogin(reg.URL, reg.Username, reg.Password); loginErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("docker login failed: %s", loginErr.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Login succeeded"})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func parseSettingsID(path, prefix string) (int64, error) {
	raw := strings.TrimPrefix(path, prefix)
	raw = strings.Split(raw, "/")[0]
	return strconv.ParseInt(raw, 10, 64)
}

// dockerLogin runs `docker login --username <u> --password-stdin <url>`
func dockerLogin(registryURL, username, password string) error {
	cmd := exec.Command("docker", "login", "--username", username, "--password-stdin", registryURL)
	cmd.Stdin = bytes.NewBufferString(password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}
