package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/customdomains"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// customDomainChallenge tells the UI exactly which DNS/file records prove ownership.
type customDomainChallenge struct {
	TXTHost     string `json:"txt_host"`     // _rigger-challenge.<domain>
	TXTValue    string `json:"txt_value"`    // rigger-verify=<token>
	FilePath    string `json:"file_path"`    // /.well-known/rigger-verify/<token>
	FileToken   string `json:"file_token"`   // the token the file must contain
	CNAMETarget string `json:"cname_target"` // the env's auto subdomain to CNAME to
}

type customDomainView struct {
	customdomains.Domain
	Challenge customDomainChallenge `json:"challenge"`
}

// autoSubdomain returns the env's auto routing host (the {label}.{base} hostname),
// or "" when the env doesn't route through Traefik. Used as the CNAME target and to
// verify CNAME-based ownership.
func (h *Handler) autoSubdomain(ws, name, env string) string {
	cfgBytes, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err != nil {
		return ""
	}
	base := settings.EffectiveBaseDomain(h.db, ws)
	url, ok := composegen.EnvRouteURL(cfgBytes, env, base, settings.AutoURLMode(h.db), settings.AutoURLHost(h.db))
	if !ok {
		return ""
	}
	return strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
}

func (h *Handler) viewDomain(d customdomains.Domain, target string) customDomainView {
	return customDomainView{
		Domain: d,
		Challenge: customDomainChallenge{
			TXTHost:     customdomains.ChallengeHost(d.Domain),
			TXTValue:    customdomains.TXTValue(d.Token),
			FilePath:    customdomains.FilePath(d.Token),
			FileToken:   d.Token,
			CNAMETarget: target,
		},
	}
}

// GET /api/workspaces/{workspace}/projects/{name}/envs/{env}/domains
func (h *Handler) ListCustomDomains(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	list, err := customdomains.List(h.db, ws, name, env)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	target := h.autoSubdomain(ws, name, env)
	out := make([]customDomainView, 0, len(list))
	for _, d := range list {
		out = append(out, h.viewDomain(d, target))
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": out, "auto_subdomain": target})
}

// POST /api/workspaces/{workspace}/projects/{name}/envs/{env}/domains  {domain}
func (h *Handler) CreateCustomDomain(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	var body struct {
		Domain string `json:"domain"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	d, err := customdomains.Create(h.db, ws, name, env, body.Domain)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, h.viewDomain(*d, h.autoSubdomain(ws, name, env)))
}

// POST /api/workspaces/{workspace}/projects/{name}/envs/{env}/domains/{id}/verify
func (h *Handler) VerifyCustomDomain(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, err := customdomains.Get(h.db, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "domain not found"})
		return
	}
	target := h.autoSubdomain(ws, name, env)
	method, verr := customdomains.Verify(d.Domain, d.Token, target)
	if verr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"verified": false, "error": verr.Error()})
		return
	}
	if err := customdomains.SetVerified(h.db, id, true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Regenerate the env's compose so the new domain's router is present for the next
	// deploy/refresh. Routing goes live on redeploy (Traefik reads labels at up time).
	h.regenEnvCompose(ws, name, env)
	writeJSON(w, http.StatusOK, map[string]any{"verified": true, "method": method})
}

// POST /api/workspaces/{workspace}/projects/{name}/envs/{env}/domains/{id}/primary
// Body {primary: bool} — set/clear the ★ canonical domain (drives Open-app / APP_URL).
func (h *Handler) SetPrimaryCustomDomain(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, err := customdomains.Get(h.db, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "domain not found"})
		return
	}
	var body struct {
		Primary bool `json:"primary"`
	}
	_ = readJSON(r, &body)
	if body.Primary && !d.Verified {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "verify the domain before making it canonical"})
		return
	}
	if err := customdomains.SetPrimary(h.db, id, body.Primary); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"primary": body.Primary})
}

// DELETE /api/workspaces/{workspace}/projects/{name}/envs/{env}/domains/{id}
func (h *Handler) DeleteCustomDomain(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := customdomains.Delete(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.regenEnvCompose(ws, name, env)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// MigrateLegacyDomains is a one-shot boot migration: the per-env `domain` field (the old
// "this env's primary domain") is superseded by auto-URL-primary + verified custom domains.
// For every env with a non-empty `domain`, seed it as a VERIFIED PRIMARY custom domain
// (it was already live, so trusted) and clear the legacy field, so the env now routes at
// its auto URL with the domain as an additive custom-domain router. Idempotent.
func (h *Handler) MigrateLegacyDomains() {
	var done string
	h.db.QueryRow(`SELECT value FROM app_settings WHERE key='domains_migrated_v1'`).Scan(&done) //nolint:errcheck
	if done == "1" {
		return
	}
	if wsEntries, err := os.ReadDir(h.workspacesDir); err == nil {
		for _, we := range wsEntries {
			if !we.IsDir() {
				continue
			}
			projEntries, perr := os.ReadDir(wspath.ProjectsDir(h.workspacesDir, we.Name()))
			if perr != nil {
				continue
			}
			for _, pe := range projEntries {
				if pe.IsDir() {
					h.migrateProjectDomains(we.Name(), pe.Name())
				}
			}
		}
	}
	h.db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES ('domains_migrated_v1','1',CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value='1', updated_at=CURRENT_TIMESTAMP`) //nolint:errcheck
}

func (h *Handler) migrateProjectDomains(ws, proj string) {
	path := wspath.ConfigPath(h.workspacesDir, ws, proj)
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var cfg map[string]any
	if json.Unmarshal(raw, &cfg) != nil {
		return
	}
	envs, ok := cfg["environments"].(map[string]any)
	if !ok {
		return
	}
	changed := false
	for envName, ev := range envs {
		em, ok := ev.(map[string]any)
		if !ok {
			continue
		}
		dom, _ := em["domain"].(string)
		if strings.TrimSpace(dom) == "" {
			continue
		}
		if err := customdomains.SeedPrimary(h.db, ws, proj, envName, dom); err != nil {
			continue // keep the legacy field if we couldn't seed (retry next boot)
		}
		em["domain"] = ""
		envs[envName] = em
		changed = true
	}
	if changed {
		if out, merr := json.MarshalIndent(cfg, "", "  "); merr == nil {
			os.WriteFile(path, out, 0o644) //nolint:errcheck
		}
	}
}

// regenEnvCompose rewrites an env's docker-compose.yml from config.json + current
// settings INCLUDING its verified custom domains, so the on-disk compose reflects the
// latest routing without waiting for a full deploy. Best-effort.
func (h *Handler) regenEnvCompose(ws, name, env string) {
	cfgData, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err != nil {
		return
	}
	envDir := wspath.EnvDir(h.workspacesDir, ws, name, env)
	envContent, _ := os.ReadFile(filepath.Join(envDir, ".env"))
	reg := ""
	if cfg, perr := wsconfig.Parse(cfgData); perr == nil {
		reg = settings.EffectiveRegistry(h.db, ws, cfg.Project.Registry)
	}
	ro := composegen.RouteOpts{
		BaseDomain:    settings.EffectiveBaseDomain(h.db, ws),
		AutoURLMode:   settings.AutoURLMode(h.db),
		AutoURLHost:   settings.AutoURLHost(h.db),
		DNSProvider:   settings.AppsDNSProvider(h.db),
		CustomDomains: customdomains.VerifiedDomains(h.db, ws, name, env),
		Registry:      reg,
		EnvFile:       string(envContent),
	}
	if content, gerr := composegen.GenerateRouted(cfgData, env, ro); gerr == nil {
		os.WriteFile(filepath.Join(envDir, "docker-compose.yml"), content, 0o644) //nolint:errcheck
	}
}
