package composegen

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Generate produces docker-compose.yml content for one environment from
// config.json bytes. Byte-for-byte replacement for scripts/compose-gen.sh.
// RouteOpts carries the env-routing context that lives OUTSIDE config.json: the
// workspace's apps base domain (DB setting) and whether local *.localhost envs
// should use self-signed HTTPS. Zero value = local HTTP on *.localhost.
type RouteOpts struct {
	BaseDomain string // e.g. "apps.example.com"; "" → fall back to AutoURL/localhost
	LocalTLS   bool   // serve the local *.localhost route over self-signed HTTPS
	// AutoURLMode / AutoURLHost build a cross-machine auto-URL when no base domain is
	// set (the magic-DNS fallback). Mode: "sslip"|"nip"|"traefikme"|"localhost"|"off"
	// (default "localhost"). Host is the IP other machines reach this host on
	// (LAN/public/Tailscale) embedded into {label}.{host}.sslip.io. Empty host ⇒
	// magic-DNS modes degrade to *.localhost.
	AutoURLMode string
	AutoURLHost string
	// DNSProvider, when set (e.g. "cloudflare"), switches base-domain envs to the
	// Traefik DNS-01 certresolver and requests a single wildcard cert *.{base}
	// instead of per-host HTTP-01. "" ⇒ per-host Let's Encrypt (unchanged).
	DNSProvider string
	// EnvFile is the env's generated .env content. When a service sets
	// env_file_mount, the generator embeds this verbatim as a compose `config`
	// (content:) and mounts it at the target path. Delivered as inline content —
	// not a host bind — because Rigger runs in a container and the host daemon
	// can't resolve Rigger's bind paths. Empty ⇒ no .env file mount is emitted.
	EnvFile string
}

func Generate(configJSON []byte, env string) ([]byte, error) {
	return generate(configJSON, env, RouteOpts{}, time.Now().UTC())
}

// GenerateAt is Generate with an injectable timestamp (for tests / determinism).
func GenerateAt(configJSON []byte, env string, now time.Time) ([]byte, error) {
	return generate(configJSON, env, RouteOpts{}, now)
}

// GenerateRouted is Generate with explicit routing context (the deploy path
// passes the workspace base domain + the project's local-TLS preference).
func GenerateRouted(configJSON []byte, env string, ro RouteOpts) ([]byte, error) {
	return generate(configJSON, env, ro, time.Now().UTC())
}

// EnvRouteURL returns the URL an environment is reachable at when it routes
// through Traefik, and whether it routes at all (false ⇒ host-port binding, no
// single URL). baseDomain is the workspace's apps base domain. Mirrors
// resolveRoute so the UI shows exactly what gets deployed.
func EnvRouteURL(configJSON []byte, env, baseDomain, autoMode, autoHost string) (string, bool) {
	cfg, err := parseConfig(configJSON)
	if err != nil {
		return "", false
	}
	e, ok := cfg.Environments[env]
	if !ok {
		return "", false
	}
	ro := RouteOpts{BaseDomain: baseDomain, AutoURLMode: autoMode, AutoURLHost: autoHost}
	if cfg.Project.LocalTLS {
		ro.LocalTLS = true
	}
	resolveRoute(&e, cfg.resourcePrefix(), env, ro)
	if !e.TraefikEnabled || e.Domain == "" {
		return "", false // host-port binding, or no route
	}
	scheme := "http"
	if e.SSLEnabled {
		scheme = "https"
	}
	return scheme + "://" + e.Domain, true
}

func generate(configJSON []byte, env string, ro RouteOpts, now time.Time) ([]byte, error) {
	cfg, err := parseConfig(configJSON)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	e, ok := cfg.Environments[env]
	if !ok {
		return nil, fmt.Errorf("unknown environment %q", env)
	}
	// local-TLS is a project setting; OR it into the route context so callers only
	// need to supply the (DB-sourced) base domain.
	if cfg.Project.LocalTLS {
		ro.LocalTLS = true
	}
	resolveRoute(&e, cfg.resourcePrefix(), env, ro)
	applyWebEntryFallback(cfg, e)
	g := &gen{cfg: cfg, env: env, e: e, now: now, envFile: ro.EnvFile}
	g.build()
	return []byte(g.b.String()), nil
}

// applyWebEntryFallback promotes the sole service to the apex web entry when
// Traefik routing resolved a domain but nothing is marked web_routed — otherwise
// an image stack (e.g. a single `nginx` service added without a web entry) would
// generate no router and Traefik would 404. Only fires for exactly one service
// (multi-service stacks must pick a web entry explicitly) and defaults the
// container port to 80 when unset. No-op when a web entry already exists.
func applyWebEntryFallback(cfg *Config, e Env) {
	if !e.TraefikEnabled || e.Domain == "" {
		return
	}
	web := -1
	for i := range cfg.Services {
		if cfg.Services[i].WebRouted {
			web = i
			break
		}
	}
	if web == -1 {
		// Nothing marked — promote the sole service (multi-service stacks must
		// pick a web entry explicitly, so leave those alone).
		if len(cfg.Services) != 1 {
			return
		}
		cfg.Services[0].WebRouted = true
		web = 0
	}
	// A web entry with no container port can't form a valid Traefik service port —
	// default to 80 (the near-universal HTTP default for images like nginx).
	if string(cfg.Services[web].Port) == "" {
		cfg.Services[web].Port = flexStr("80")
	}
}

// resolveRoute derives an env's domain + TLS mode when the user enabled Traefik
// routing but left the domain blank (the Render-style "just give it a URL" case).
// Activation is the existing traefik_enabled toggle, so envs that host-bind
// (Traefik off) and envs with an explicit domain are untouched. Derived host:
//   - base domain set → {prefix}-{env}.{base}, HTTPS via Let's Encrypt
//   - no base domain  → {prefix}-{env}.localhost, HTTP (or self-signed if LocalTLS)
// Underscores in the prefix become hyphens (valid DNS label).
func resolveRoute(e *Env, rp, env string, ro RouteOpts) {
	if !e.TraefikEnabled || e.Domain != "" {
		return
	}
	label := strings.ReplaceAll(rp, "_", "-") + "-" + env
	// magicSuffix is the wildcard-DNS suffix for an auto-URL mode, or "" if the mode
	// isn't a magic-DNS one (or no host is configured to embed).
	magicSuffix := func() string {
		if ro.AutoURLHost == "" {
			return ""
		}
		switch ro.AutoURLMode {
		case "sslip":
			return ro.AutoURLHost + ".sslip.io"
		case "nip":
			return ro.AutoURLHost + ".nip.io"
		case "traefikme":
			return ro.AutoURLHost + ".traefik.me"
		}
		return ""
	}()
	switch {
	case ro.BaseDomain != "":
		// Real domain (admin global default or workspace override) → HTTPS via Let's Encrypt.
		e.Domain = label + "." + ro.BaseDomain
		e.SSLEnabled = true
		e.SSLSelfSigned = false
		// With a DNS provider configured, issue ONE wildcard cert (*.{base}) via DNS-01
		// instead of a per-host HTTP-01 cert per app. The apex router carries the
		// wildcard request; sibling apex domains reuse the same cert.
		if ro.DNSProvider != "" {
			e.certResolver = "dns"
			e.wildcardBase = ro.BaseDomain
		}
	case magicSuffix != "":
		// Cross-machine magic-DNS auto-URL. HTTP in Phase 1 (real certs for these come
		// later: per-host LE if publicly reachable, or traefik.me's shared cert).
		e.Domain = label + "." + magicSuffix
		e.SSLEnabled = false
		e.SSLSelfSigned = false
	default:
		// Host-local default (also when a magic-DNS mode is selected but no host set).
		e.Domain = label + ".localhost"
		e.SSLEnabled = ro.LocalTLS
		e.SSLSelfSigned = ro.LocalTLS
	}
}

type gen struct {
	cfg *Config
	env string
	e   Env
	now time.Time
	b   strings.Builder
	// envFile is the env's .env content, embedded as a compose config when a
	// service sets env_file_mount; envCfgUsed records whether any service did.
	envFile    string
	envCfgUsed bool
}

// line appends s followed by a newline (echo "s").
func (g *gen) line(s string) { g.b.WriteString(s); g.b.WriteByte('\n') }

// raw appends s verbatim (already contains its own newlines).
func (g *gen) raw(s string) { g.b.WriteString(s) }

func (g *gen) build() {
	c := g.cfg
	e := g.e
	registry := c.Project.Registry
	ver := c.versionString()
	tag := ver + "-" + g.env
	// All Docker resource names derive from the immutable resource prefix
	// ({workspace}_{project}), NOT the editable display name, so names stay
	// globally unique when project display names repeat across workspaces.
	rp := c.resourcePrefix()
	prefix := rp + "_" + g.env
	isSwarm := e.Deployment == "swarm"

	// ── Header ──
	sep := "# " + strings.Repeat("=", 60)
	g.line(sep)
	g.line("# docker-compose.yml — " + g.env + " environment")
	g.line("# Project : " + c.Project.Name)
	g.line("# Version : " + ver)
	g.line("# Generated: " + g.now.Format("2006-01-02 15:04:05") + " UTC")
	g.line("# Regenerate: ./run.sh refresh " + g.env)
	g.line(sep)
	g.line("")

	// ── Networks ──
	// Swarm services require a swarm-scoped network; bridge is local-only and is
	// rejected by `docker stack deploy`.
	g.line("networks:")
	g.line("  " + prefix + "_net:")
	if isSwarm {
		g.line("    driver: overlay")
	} else {
		g.line("    driver: bridge")
	}
	if e.TraefikEnabled {
		g.line("  " + e.TraefikNetwork + ":")
		g.line("    external: true")
	}
	g.line("")

	// ── Secrets (swarm only) ──
	g.emitTopLevelSecrets()

	// ── Services (unified graph) + managed dependencies ──
	// rp is the image-name base so pushed tags (registry/<prefix>-<service>) stay
	// globally unique across workspaces.
	g.buildStack(prefix, rp, registry, tag, isSwarm)

	// ── Configs ── the env's .env, embedded inline for services that opted into
	// a physical .env mount (env_file_mount). Set during buildStack.
	if g.envCfgUsed {
		g.line("")
		g.line("configs:")
		g.line("  " + prefix + "_dotenv:")
		g.line("    content: |")
		for _, ln := range strings.Split(strings.TrimRight(g.envFile, "\n"), "\n") {
			g.line("      " + ln)
		}
	}
}

// ── Shared emit helpers (mirror lib.sh / compose-gen.sh helpers) ─────────────────

// deployBlock emits the per-service deploy section. For compose it's a single
// `restart:`. For swarm it emits replicas + (optional) placement + restart_policy +
// update_config + (optional) rollback_config, sourced from the env's SwarmConfig
// (g.e.Swarm) with a per-service override for replicas/placement. When no SwarmConfig
// is set the output is byte-identical to the historical hardcoded block.
func (g *gen) deployBlock(isSwarm bool, svc, replicas, restart string) {
	if replicas == "" {
		replicas = "1"
	}
	if restart == "" {
		restart = "unless-stopped"
	}
	if !isSwarm {
		g.line("    restart: " + restart)
		g.emitServiceSecrets()
		return
	}

	sw := g.e.Swarm
	var placement []string
	if so, ok := sw.Services[svc]; ok {
		if r := string(so.Replicas); r != "" {
			replicas = r
		}
		placement = so.Placement
	}

	g.raw("    deploy:\n      replicas: " + replicas + "\n")
	if len(placement) > 0 {
		g.raw("      placement:\n        constraints:\n")
		for _, c := range placement {
			g.raw("          - " + c + "\n")
		}
	}

	// restart_policy (defaults match the historical block).
	cond, delay, maxAtt, window := "on-failure", "5s", "3", ""
	if rp := sw.RestartPolicy; rp != nil {
		if rp.Condition != "" {
			cond = rp.Condition
		}
		if rp.Delay != "" {
			delay = rp.Delay
		}
		if r := string(rp.MaxAttempts); r != "" {
			maxAtt = r
		}
		window = rp.Window
	}
	g.raw("      restart_policy:\n        condition: " + cond + "\n        delay: " + delay + "\n        max_attempts: " + maxAtt + "\n")
	if window != "" {
		g.raw("        window: " + window + "\n")
	}

	// update_config (defaults match the historical block).
	par, udelay, order, fail := "1", "10s", "", "rollback"
	if uc := sw.UpdateConfig; uc != nil {
		if r := string(uc.Parallelism); r != "" {
			par = r
		}
		if uc.Delay != "" {
			udelay = uc.Delay
		}
		order = uc.Order
		if uc.FailureAction != "" {
			fail = uc.FailureAction
		}
	}
	g.raw("      update_config:\n        parallelism: " + par + "\n        delay: " + udelay + "\n")
	if order != "" {
		g.raw("        order: " + order + "\n")
	}
	g.raw("        failure_action: " + fail + "\n")

	// rollback_config — only when explicitly configured (omitted by default).
	if rc := sw.RollbackConfig; rc != nil {
		rpar, rdelay := "1", "0s"
		if r := string(rc.Parallelism); r != "" {
			rpar = r
		}
		if rc.Delay != "" {
			rdelay = rc.Delay
		}
		g.raw("      rollback_config:\n        parallelism: " + rpar + "\n        delay: " + rdelay + "\n")
		if rc.Order != "" {
			g.raw("        order: " + rc.Order + "\n")
		}
	}

	g.emitServiceSecrets()
}

// traefikLabels emits the routing labels for a web service. Three modes by
// (SSLEnabled, SSLSelfSigned):
//   - HTTP only           → a single `web` (:80) router.
//   - HTTPS + Let's Encrypt → `websecure` (:443) router with the letsencrypt
//     resolver, plus a companion `web` router that redirects http→https.
//   - HTTPS + self-signed   → same as above but no certresolver (Traefik serves
//     its default cert) — for local *.localhost envs that need HTTPS.
// The per-router redirect replaces Traefik's old global web→websecure redirect,
// so HTTP-only (local) envs are no longer forced onto a cert-less HTTPS.
func (g *gen) traefikLabels(router, host, port string, auth bool, certResolver, wildcard string) {
	if !g.e.TraefikEnabled {
		return
	}
	if port == "" {
		port = "80"
	}
	if certResolver == "" {
		certResolver = "letsencrypt" // per-host HTTP-01 (default; DNS-01 sets "dns")
	}
	rule := "Host(`" + host + "`)"
	g.line("    labels:")
	g.line("      - \"traefik.enable=true\"")
	// Optional HTTP basic-auth middleware (admin sidecars when the env protects them).
	// The htpasswd line comes from .env (${ADMIN_UI_USERS}) so the bcrypt '$' chars are
	// inserted literally rather than written inline (which would need '$$' doubling).
	if auth {
		g.line("      - \"traefik.http.middlewares." + router + "_auth.basicauth.users=${ADMIN_UI_USERS}\"")
	}
	if g.e.SSLEnabled {
		g.line("      - \"traefik.http.routers." + router + ".rule=" + rule + "\"")
		g.line("      - \"traefik.http.routers." + router + ".entrypoints=websecure\"")
		g.line("      - \"traefik.http.routers." + router + ".tls=true\"")
		if !g.e.SSLSelfSigned {
			g.line("      - \"traefik.http.routers." + router + ".tls.certresolver=" + certResolver + "\"")
			// Request a single wildcard cert for the apex base-domain router (DNS-01).
			if wildcard != "" {
				g.line("      - \"traefik.http.routers." + router + ".tls.domains[0].main=" + wildcard + "\"")
				g.line("      - \"traefik.http.routers." + router + ".tls.domains[0].sans=*." + wildcard + "\"")
			}
		}
		if auth {
			g.line("      - \"traefik.http.routers." + router + ".middlewares=" + router + "_auth\"")
		}
		g.line("      - \"traefik.http.services." + router + ".loadbalancer.server.port=" + port + "\"")
		// Companion HTTP router → redirect to HTTPS (per-router, not global).
		g.line("      - \"traefik.http.routers." + router + "_web.rule=" + rule + "\"")
		g.line("      - \"traefik.http.routers." + router + "_web.entrypoints=web\"")
		g.line("      - \"traefik.http.routers." + router + "_web.middlewares=" + router + "_redirect\"")
		g.line("      - \"traefik.http.middlewares." + router + "_redirect.redirectscheme.scheme=https\"")
	} else {
		g.line("      - \"traefik.http.routers." + router + ".rule=" + rule + "\"")
		g.line("      - \"traefik.http.routers." + router + ".entrypoints=web\"")
		if auth {
			g.line("      - \"traefik.http.routers." + router + ".middlewares=" + router + "_auth\"")
		}
		g.line("      - \"traefik.http.services." + router + ".loadbalancer.server.port=" + port + "\"")
	}
}

func (g *gen) portMapping(hostPort, containerPort string) {
	g.line("    ports:")
	g.line("      - \"" + hostPort + ":" + containerPort + "\"")
}

// healthcheck mirrors healthcheck_block: defaults interval 30s, timeout 10s,
// retries 3, start_period 30s; start_interval emitted only when non-empty.
func (g *gen) healthcheck(cmd, interval, timeout, retries, startPeriod, startInterval string) {
	if interval == "" {
		interval = "30s"
	}
	if timeout == "" {
		timeout = "10s"
	}
	if retries == "" {
		retries = "3"
	}
	if startPeriod == "" {
		startPeriod = "30s"
	}
	safe := strings.ReplaceAll(cmd, "\"", "\\\"")
	g.raw("    healthcheck:\n" +
		"      test: [\"CMD-SHELL\", \"" + safe + "\"]\n" +
		"      interval: " + interval + "\n" +
		"      timeout: " + timeout + "\n" +
		"      retries: " + retries + "\n" +
		"      start_period: " + startPeriod + "\n")
	if startInterval != "" {
		g.line("      start_interval: " + startInterval)
	}
}

// healthcheckExec emits an exec-form (CMD) healthcheck — no shell. Required for
// distroless images that ship only their binary (no /bin/sh, no curl), where the
// default CMD-SHELL form can't run at all and the container is wrongly reported
// unhealthy forever (e.g. dxflrs/garage).
func (g *gen) healthcheckExec(args []string, interval, timeout, retries, startPeriod string) {
	if interval == "" {
		interval = "30s"
	}
	if timeout == "" {
		timeout = "10s"
	}
	if retries == "" {
		retries = "3"
	}
	if startPeriod == "" {
		startPeriod = "30s"
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "\"" + strings.ReplaceAll(a, "\"", "\\\"") + "\""
	}
	g.raw("    healthcheck:\n" +
		"      test: [\"CMD\", " + strings.Join(quoted, ", ") + "]\n" +
		"      interval: " + interval + "\n" +
		"      timeout: " + timeout + "\n" +
		"      retries: " + retries + "\n" +
		"      start_period: " + startPeriod + "\n")
}

// sectionComment emits a "  # ── <label> <N dashes>" service separator. The
// trailing dash counts are fixed per service type to match compose-gen.sh's
// hand-written heredocs exactly (they are NOT padded to a constant width).
func sectionComment(label string, dashes int) string {
	return "  # ── " + label + " " + strings.Repeat("─", dashes)
}

// emitExtraCompose appends raw YAML, indenting every line by 4 spaces (sed 's/^/    /').
func (g *gen) emitExtraCompose(raw string) {
	if raw == "" || raw == "null" || raw == "empty" {
		return
	}
	for _, ln := range strings.Split(raw, "\n") {
		g.line("    " + ln)
	}
}

// sortedStringKeys returns map keys sorted ascending (string-valued variant).
func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
