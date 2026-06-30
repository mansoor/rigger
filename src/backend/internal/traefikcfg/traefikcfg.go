// Package traefikcfg makes the Proxy Service plugins (WAF / cache / GeoIP) activatable
// from the UI instead of by hand-editing docker-compose.yml. Traefik loads plugins from
// its STATIC config at startup, and Traefik's static-config sources (file / CLI / env)
// are mutually exclusive — so Rigger OWNS the full Traefik static config as a file on a
// shared volume (mounted at Traefik's /etc/traefik/traefik.yml), reproducing what used to
// be the compose `command:` args and appending an experimental.plugins block for whichever
// plugins are enabled. Toggling a plugin rewrites this file and restarts rigger-traefik
// (a brief routing blip — static config always needs a restart). Per-route / per-access-list
// ATTACH stays dynamic via the file provider (instant). See docs/design/proxy-service.md
// § Phase 3.1 and proxy-plugin-activation-model memory. No image bundling: plugins are
// fetched by Traefik from their module source at startup (online), pinned to the versions
// below (operator-overridable in Settings if a pin goes stale).
package traefikcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/settings"
)

// Plugin module paths (Traefik Plugin Catalog) + default pinned versions. The middleware
// instance names (coraza / souin / geoblock) must match what proxyroutes/render.go emits.
const (
	corazaModule   = "github.com/jcchavezs/coraza-http-wasm-traefik" // WAF (Coraza, WASM)
	souinModule    = "github.com/darkweak/souin"                     // HTTP cache (Souin) — repo-root module; plugin lives in /plugins/traefik
	geoblockModule = "github.com/nscuro/traefik-plugin-geoblock"     // GeoIP country policy

	defaultCorazaVersion   = "v0.2.1"
	defaultSouinVersion    = "v1.7.8"
	defaultGeoblockVersion = "v0.14.0"
)

// CacheSupported gates the Souin HTTP-cache plugin. It is FALSE: Souin (the only
// mainstream Traefik cache plugin) panics under Traefik's Yaegi plugin interpreter at
// load — and because a failed plugin poisons router building, enabling it drops ALL
// Docker-routed apps to 404 (observed 2026-06-30). Traefik is migrating plugins to
// WebAssembly; there is no Yaegi-compatible cache plugin to swap in today, so Rigger
// does not offer/emit Souin. This is the single source of truth: every read of the
// cache-enabled flag is AND-ed with it, so flipping this back to true (once a
// compatible build exists) re-enables the whole path. Robust caching alternatives that
// don't ride Yaegi: a CDN in front (Cloudflare), or a dedicated cache sidecar
// (Varnish / Nginx proxy_cache) — neither built yet.
const CacheSupported = false

// Settings keys: plugin enable flags (shared with proxy_handlers) + version overrides.
const (
	keyWAF   = "proxy_waf_enabled"
	keyCache = "proxy_cache_enabled"
	keyGeo   = "proxy_geoip_enabled"

	keyCorazaVer   = "proxy_waf_version"
	keySouinVer    = "proxy_cache_version"
	keyGeoblockVer = "proxy_geoip_version"
)

// DefaultConfigDir is where Rigger writes traefik.yml (the shared rigger-traefik-config
// volume, mounted at Traefik's /etc/traefik). Override via RIGGER_TRAEFIK_CONFIG_DIR.
const DefaultConfigDir = "/traefikcfg"

// ConfigDir returns the directory Rigger writes the Traefik static config into.
func ConfigDir() string {
	if d := strings.TrimSpace(os.Getenv("RIGGER_TRAEFIK_CONFIG_DIR")); d != "" {
		return d
	}
	return DefaultConfigDir
}

// ConfigPath is the absolute traefik.yml path Rigger writes.
func ConfigPath() string { return filepath.Join(ConfigDir(), "traefik.yml") }

// Options is the resolved static-config input.
type Options struct {
	ACMEEmail                       string
	WAF, Cache, GeoIP               bool
	CorazaVer, SouinVer, GeoblockVer string
}

// acmeEmail returns the instance ACME account email — the same value the compose passed
// to Traefik as ${ACME_EMAIL}. Rigger reads it from its own ACME_EMAIL env (mirrored in
// compose) so the generated resolver email matches the previous CLI behaviour exactly.
func acmeEmail() string {
	if e := strings.TrimSpace(os.Getenv("ACME_EMAIL")); e != "" {
		return e
	}
	return "admin@example.com"
}

func verOr(d *db.DB, key, def string) string {
	if v := strings.TrimSpace(settings.AppSetting(d, key)); v != "" {
		return v
	}
	return def
}

// FromSettings resolves Options from app settings + env.
func FromSettings(d *db.DB) Options {
	return Options{
		ACMEEmail:   acmeEmail(),
		WAF:         settings.AppSetting(d, keyWAF) == "true",
		Cache:       CacheSupported && settings.AppSetting(d, keyCache) == "true",
		GeoIP:       settings.AppSetting(d, keyGeo) == "true",
		CorazaVer:   verOr(d, keyCorazaVer, defaultCorazaVersion),
		SouinVer:    verOr(d, keySouinVer, defaultSouinVersion),
		GeoblockVer: verOr(d, keyGeoblockVer, defaultGeoblockVersion),
	}
}

// Generate renders the full Traefik static config (traefik.yml). It reproduces the static
// configuration that previously lived as compose `command:` CLI args, then appends the
// experimental.plugins block for the enabled plugins only (stock = no plugins → no GitHub
// fetch at startup). Output is deterministic.
func Generate(o Options) string {
	var b strings.Builder
	b.WriteString("# Auto-generated by Rigger — UI-managed Traefik static config. Do not edit by hand;\n")
	b.WriteString("# changes are overwritten when plugins are toggled in Proxy Service → Plugins.\n\n")

	b.WriteString("entryPoints:\n")
	b.WriteString("  web:\n    address: \":80\"\n")
	b.WriteString("  websecure:\n    address: \":443\"\n\n")

	b.WriteString("providers:\n")
	b.WriteString("  docker:\n")
	b.WriteString("    endpoint: \"tcp://socket-proxy:2375\"\n")
	b.WriteString("    network: \"traefik_net\"\n")
	b.WriteString("    exposedByDefault: false\n")
	b.WriteString("  file:\n")
	b.WriteString("    directory: \"/dynamic\"\n")
	b.WriteString("    watch: true\n\n")

	email := o.ACMEEmail
	if email == "" {
		email = "admin@example.com"
	}
	b.WriteString("certificatesResolvers:\n")
	b.WriteString("  letsencrypt:\n    acme:\n")
	fmt.Fprintf(&b, "      email: %q\n", email)
	b.WriteString("      storage: \"/certs/acme.json\"\n")
	b.WriteString("      httpChallenge:\n        entryPoint: web\n")
	b.WriteString("  dns:\n    acme:\n")
	fmt.Fprintf(&b, "      email: %q\n", email)
	b.WriteString("      storage: \"/certs/acme-dns.json\"\n")
	b.WriteString("      dnsChallenge:\n        provider: cloudflare\n\n")

	b.WriteString("log:\n  level: INFO\n\n")
	b.WriteString("api:\n  dashboard: false\n")

	// Plugins (static; only the enabled ones, so a stock instance never fetches them).
	type plug struct{ name, module, version string }
	var plugins []plug
	if o.WAF {
		plugins = append(plugins, plug{"coraza", corazaModule, o.CorazaVer})
	}
	if o.Cache && CacheSupported {
		// Never reached while CacheSupported is false — Souin panics under Yaegi (see const).
		plugins = append(plugins, plug{"souin", souinModule, o.SouinVer})
	}
	if o.GeoIP {
		plugins = append(plugins, plug{"geoblock", geoblockModule, o.GeoblockVer})
	}
	if len(plugins) > 0 {
		b.WriteString("\nexperimental:\n  plugins:\n")
		for _, p := range plugins {
			fmt.Fprintf(&b, "    %s:\n      moduleName: %q\n      version: %q\n", p.name, p.module, p.version)
		}
	}
	return b.String()
}

// Write resolves Options from settings and writes traefik.yml to the shared volume.
// Best-effort caller logs; a missing volume (dev box) just means no file.
func Write(d *db.DB) error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("traefikcfg: ensure %s: %w", dir, err)
	}
	if err := os.WriteFile(ConfigPath(), []byte(Generate(FromSettings(d))), 0o644); err != nil {
		return fmt.Errorf("traefikcfg: write %s: %w", ConfigPath(), err)
	}
	return nil
}
