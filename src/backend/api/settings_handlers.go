package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/managedregistry"
	"github.com/mansoor/rigger/ui/internal/settings"
)

// ── General Settings ──────────────────────────────────────────────────────────
// Key/value store in app_settings table. Used for ACME email and future config.

// GET /api/settings/general
func (h *Handler) GetGeneralSettings(w http.ResponseWriter, r *http.Request) {
	keys := []string{"acme_email", "rigger_domain", "app_host", "traefik_enabled", "confirm_destructive", "appearance_prefs", "key_min_length", "key_max_length", "apps_base_domain", "auto_url_mode", "auto_url_host", "apps_dns_provider"}
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

// GET /api/settings/detect-host-ip — query the Docker host for its primary
// LAN/public IP (runs the rigger binary host-networked). Used by the App host
// "Detect" button. Always 200; { ip } on success, { error } when detection fails
// (offline, no docker, not on a Linux host) so the UI can fall back to manual.
func (h *Handler) DetectHostIP(w http.ResponseWriter, r *http.Request) {
	ip, err := h.bridge.DetectHostIP()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ip": "", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ip": ip})
}

// PUT /api/settings/general
func (h *Handler) PutGeneralSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	allowed := map[string]bool{"acme_email": true, "rigger_domain": true, "app_host": true, "traefik_enabled": true, "confirm_destructive": true, "appearance_prefs": true, "key_min_length": true, "key_max_length": true, "apps_base_domain": true, "auto_url_mode": true, "auto_url_host": true, "apps_dns_provider": true,
		// Password policy (auth Group A): min length + complexity requirements.
		"pw_min_length": true, "pw_require_upper": true, "pw_require_lower": true, "pw_require_number": true, "pw_require_symbol": true}
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

// backupTargetBody is the create/update payload. grants is admin-only — the
// workspace allowlist for a global target ('*' = offered to all).
type backupTargetBody struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
	Grants []string        `json:"grants"`
}

// GET /api/settings/backup-targets — admin view: all targets; global ones carry grants.
func (h *Handler) ListBackupTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := settings.ListBackupTargets(h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for i := range targets {
		if targets[i].OwnerScope == "global" {
			targets[i].Grants, _ = settings.TargetGrants(h.db, targets[i].ID) //nolint:errcheck
		}
	}
	writeJSON(w, http.StatusOK, targets)
}

// POST /api/settings/backup-targets — admin create (global; grants default to '*').
func (h *Handler) CreateBackupTarget(w http.ResponseWriter, r *http.Request) {
	var body backupTargetBody
	if err := readJSON(r, &body); err != nil || body.Name == "" || body.Type == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, type, and config are required"})
		return
	}
	if body.Config == nil {
		body.Config = json.RawMessage(`{}`)
	}
	t, err := settings.CreateBackupTarget(h.db, body.Name, body.Type, body.Config, "global")
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a backup target with that name already exists"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	grants := body.Grants
	if grants == nil {
		grants = []string{"*"}
	}
	_ = settings.SetTargetGrants(h.db, t.ID, grants) //nolint:errcheck
	t.Grants, _ = settings.TargetGrants(h.db, t.ID)
	writeJSON(w, http.StatusCreated, t)
}

// PUT /api/settings/backup-targets/{id} — admin update incl. the workspace allowlist.
func (h *Handler) UpdateBackupTarget(w http.ResponseWriter, r *http.Request) {
	id, err := parseSettingsID(r.URL.Path, "/api/settings/backup-targets/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body backupTargetBody
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
	if t.OwnerScope == "global" && body.Grants != nil {
		_ = settings.SetTargetGrants(h.db, id, body.Grants) //nolint:errcheck
	}
	t.Grants, _ = settings.TargetGrants(h.db, id)
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

// POST /api/settings/registries/{id}/system — admin: designate (or clear) the
// GLOBAL system registry, used wherever a project sets no registry (image
// distribution). Body: {"system": bool}. At most one global system registry —
// marking one clears any previous (enforced in the store).
func (h *Handler) MarkRegistrySystem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/system")
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
	if reg.WorkspaceScope() != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "only a global registry can be the global system registry — set a workspace system registry from Manage Workspace"})
		return
	}
	var body struct {
		System bool `json:"system"`
	}
	_ = readJSON(r, &body) //nolint:errcheck — absent/invalid body ⇒ unset (false)
	if err := settings.SetRegistrySystem(h.db, id, body.System); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	updated, _ := settings.GetRegistry(h.db, id)
	if updated != nil {
		updated.Password = ""
		updated.Grants, _ = settings.RegistryGrants(h.db, id) //nolint:errcheck
	}
	writeJSON(w, http.StatusOK, updated)
}

// ── Rigger-managed registry (image-distribution Phase 2) ──────────────────────

type managedRegistryStatus struct {
	Running    bool   `json:"running"`
	Exists     bool   `json:"exists"`
	URL        string `json:"url"`         // the registries-entry URL (image-tag prefix)
	HTTPS      bool   `json:"https"`       // fronted by Traefik over TLS (base-domain mode)
	System     bool   `json:"system"`      // designated the system registry
	BaseDomain string `json:"base_domain"` // global apps base domain ('' ⇒ local-only HTTP)
	DiskUsage  string `json:"disk_usage"`  // data-volume size, e.g. "42M"
}

// managedConfig builds the managed-registry Config from the global settings: the
// managed registry is instance-wide, so it uses the GLOBAL apps base domain (not a
// workspace override) + the DNS provider for the cert resolver choice.
func (h *Handler) managedConfig() managedregistry.Config {
	return managedregistry.Config{
		BaseDomain:  settings.AppSetting(h.db, "apps_base_domain"),
		DNSProvider: settings.AppsDNSProvider(h.db),
	}
}

// GET /api/settings/registries/managed — status of the Rigger-managed registry.
func (h *Handler) GetManagedRegistry(w http.ResponseWriter, r *http.Request) {
	mgr := managedregistry.New(nil)
	cfg := h.managedConfig()
	st := managedRegistryStatus{
		Running:    mgr.Running(),
		Exists:     mgr.Exists(),
		BaseDomain: cfg.BaseDomain,
		HTTPS:      cfg.HTTPS(),
		URL:        cfg.URL(),
	}
	if entry, _ := settings.GetRegistryByName(h.db, managedregistry.EntryName); entry != nil {
		st.URL = entry.URL // the URL actually in use (may predate a base-domain change)
		st.System = entry.System
	}
	if st.Running {
		st.DiskUsage = mgr.DiskUsage()
	}
	writeJSON(w, http.StatusOK, st)
}

// POST /api/settings/registries/managed — run/stop/garbage-collect the managed
// registry. Body: {"action": "up"|"down"|"gc"}.
func (h *Handler) ManagedRegistryAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	mgr := managedregistry.New(nil)
	switch body.Action {
	case "up":
		h.managedRegistryUp(w, mgr)
	case "down":
		if err := mgr.Down(); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		// Stop pointing projects at a now-stopped registry: clear the system flag but
		// keep the entry (its URL/password persist for a later "up").
		if entry, _ := settings.GetRegistryByName(h.db, managedregistry.EntryName); entry != nil {
			_ = settings.SetRegistrySystem(h.db, entry.ID, false) //nolint:errcheck
		}
		h.GetManagedRegistry(w, r)
	case "gc":
		out, err := mgr.GC()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "output": out})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "output": out})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action must be up, down, or gc"})
	}
}

// managedRegistryUp starts (or restarts) the managed registry, upserts its
// registries entry, and marks it the system registry. The password is reused across
// runs when an entry already exists, so a restart doesn't invalidate prior logins.
func (h *Handler) managedRegistryUp(w http.ResponseWriter, mgr *managedregistry.Manager) {
	cfg := h.managedConfig()
	entry, _ := settings.GetRegistryByName(h.db, managedregistry.EntryName)
	password := ""
	if entry != nil {
		password = entry.Password
	}
	if password == "" {
		buf := make([]byte, 24)
		if _, err := rand.Read(buf); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "generate password: " + err.Error()})
			return
		}
		password = hex.EncodeToString(buf)
	}
	if err := mgr.Up(cfg, managedregistry.Username, password); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	// Upsert the registries entry (global) so the build host can authenticate and
	// EffectiveRegistry can resolve it.
	if entry == nil {
		created, err := settings.CreateRegistry(h.db, managedregistry.EntryName, cfg.URL(), managedregistry.Username, password, "global")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save registry: " + err.Error()})
			return
		}
		_ = settings.SetRegistryGrants(h.db, created.ID, []string{"*"}) //nolint:errcheck — offer to all workspaces
		entry = created
	} else if _, err := settings.UpdateRegistry(h.db, entry.ID, managedregistry.EntryName, cfg.URL(), managedregistry.Username, password); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update registry: " + err.Error()})
		return
	}
	// Designate it the global system registry (the whole point of one-click).
	if err := settings.SetRegistrySystem(h.db, entry.ID, true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mark system: " + err.Error()})
		return
	}
	// Authenticate the local daemon now so the first build can push.
	_ = dockerLogin(cfg.URL(), managedregistry.Username, password) //nolint:errcheck
	writeJSON(w, http.StatusOK, managedRegistryStatus{
		Running: mgr.Running(), Exists: true, URL: cfg.URL(), HTTPS: cfg.HTTPS(),
		System: true, BaseDomain: cfg.BaseDomain, DiskUsage: mgr.DiskUsage(),
	})
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
