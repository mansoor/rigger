package api

import (
	"net/http"
	"os"
	"strings"

	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Per-workspace general settings (Phase 3). Scalar key/value scoped to one
// workspace, mirroring the global General tab. Currently stored values (acme_email,
// domain) — like the global ACME email, they are reference/for future automation
// until the Traefik wiring consumes them per workspace.
var workspaceSettingKeys = map[string]bool{
	"acme_email": true,
	"domain":     true,
	// Phase 4: defaults new projects inherit (ids stored as strings; "" = unset).
	"default_registry_id":      true,
	"default_host_id":          true,
	"default_backup_target_id": true,
	// Image-distribution Phase 4: workspace default BUILD host (id; "" = unset →
	// projects build on their env's deploy host). Per-project override in Edit Project.
	"default_build_host_id": true,
	// Workspace default for whether a web-routed service still publishes its primary host
	// port under Traefik ("true" = keep; unset/"false" = strip — the app is reached by its
	// domain, so the host port is redundant and conflict-prone). Per-env override in Edit
	// Project. See settings.EffectiveKeepHostPortsUnderTraefik.
	"keep_host_ports_under_traefik": true,
	// W7: workspace default appearance (JSON blob; users can override per-account).
	"appearance_prefs": true,
	// Workspace default for destructive-action confirmations + whether members may
	// override it ("" inherit | "true" | "false"). See api/confirm_handlers.go.
	"confirm_destructive":                true,
	"confirm_destructive_allow_override": true,
	// Release pipeline: env tier names (low→high, comma/newline) driving the
	// auto-guess deploy order. Empty ⇒ envorder.DefaultTiers.
	"env_tier_names": true,
	// Domain/TLS override parity with the admin General tab (resolved workspace →
	// global via settings.Effective*). auto_url_mode/host = magic-DNS fallback when no
	// base domain; apps_dns_provider = DNS-01 wildcard provider; apps_dns_token =
	// the workspace's OWN Cloudflare token (secret, encrypted at rest, masked on read).
	"auto_url_mode":     true,
	"auto_url_host":     true,
	"apps_dns_provider": true,
	"apps_dns_token":    true,
	// apps_manage_dns = 'false' opts out of auto-managing public DNS A records for
	// public-host apps (default on when a base domain + token are set).
	"apps_manage_dns": true,
}

// GET /api/workspaces/{ws}/settings
func (h *Handler) GetWorkspaceSettings(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	vals, err := settings.GetWorkspaceSettings(h.db, ws)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Always report every known key so the UI binds cleanly.
	out := map[string]string{}
	for k := range workspaceSettingKeys {
		out[k] = vals[k]
	}
	// Never return the raw workspace Cloudflare token; mask when set (PUT treats the
	// mask as "unchanged"), mirroring the admin General tab.
	if out["apps_dns_token"] != "" {
		out["apps_dns_token"] = dnsTokenMask
	}
	writeJSON(w, http.StatusOK, out)
}

// PUT /api/workspaces/{ws}/settings  Body: { acme_email, domain }
func (h *Handler) PutWorkspaceSettings(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if _, err := os.Stat(wspath.WorkspaceMeta(h.workspacesDir, ws)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	var body map[string]string
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	for k, v := range body {
		if !workspaceSettingKeys[k] {
			continue
		}
		// Workspace Cloudflare token: secret, stored ENCRYPTED at rest (AES-GCM via the
		// JWT-derived key, like git/host secrets). Blank or the masked sentinel ⇒ keep the
		// current value. Unlike the global token it never touches the shared Traefik token
		// file and never restarts the proxy — it's consumed only by out-of-band lego
		// issuance for this workspace's own base domain (see settings.WorkspaceDNSToken).
		if k == "apps_dns_token" {
			tok := strings.TrimSpace(v)
			if tok == "" || tok == dnsTokenMask {
				continue
			}
			enc, err := crypto.Encrypt(h.cryptoKey, []byte(tok))
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "encrypt token: " + err.Error()})
				return
			}
			v = enc
		}
		if err := settings.SetWorkspaceSetting(h.db, ws, k, v); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	h.GetWorkspaceSettings(w, r)
}
