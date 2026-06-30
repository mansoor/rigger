package composegen

import (
	"fmt"
	"sort"
	"strconv"
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
	// OverrideCert, when true, makes SSL routers use Traefik's file-provider cert
	// (issued out-of-band under a per-env/workspace ACME email) instead of an ACME
	// resolver: tls=true with NO certresolver. The bridge sets it only for an SSL env
	// with an explicit public domain whose effective ACME email differs from the global.
	OverrideCert bool
	// CustomDomains are VERIFIED external domains (e.g. app.example.com) attached to
	// this env in addition to its auto subdomain. Each gets its own websecure router on
	// the apex web service with a per-host Let's Encrypt (HTTP-01) cert. Empty ⇒ no
	// extra routers (golden parity). Loaded from the DB by the bridge on the deploy path.
	CustomDomains []string
	// EnvFile is the env's generated .env content. When a service sets
	// env_file_mount, the generator embeds this verbatim as a compose `config`
	// (content:) and mounts it at the target path. Delivered as inline content —
	// not a host bind — because Rigger runs in a container and the host daemon
	// can't resolve Rigger's bind paths. Empty ⇒ no .env file mount is emitted.
	EnvFile string
	// Registry is the EFFECTIVE registry for this project (settings.EffectiveRegistry:
	// project → workspace-system → global-system), resolved by the caller (which has
	// DB access). When non-empty it overrides config.json's `registry` for image
	// refs (the baked `image:` defaults), so a project that inherits a system
	// registry tags/pulls against it. Empty ⇒ use config.json's own `registry`
	// (today's behavior; Phase 0 always resolves to this).
	Registry string
	// RouterMiddlewares are extra Traefik file-provider middleware refs (e.g.
	// "acl-acme-5-auth@file", "proxy-waf@file") attached to THIS env's APP routers —
	// the resolved workspace access list + WAF/cache plugins for the env. Resolved by
	// the API/bridge (which has DB access); empty -> nothing extra (golden parity).
	// See docs/design/workspace-plugins-and-access-lists.md.
	RouterMiddlewares []string
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
	// Effective registry (project → workspace/global system) is resolved by the
	// caller and passed in; when set it overrides config.json's own `registry` for
	// every image ref so a project inheriting a system registry tags against it.
	if ro.Registry != "" {
		cfg.Project.Registry = ro.Registry
	}
	resolveRoute(&e, cfg.resourcePrefix(), env, ro)
	// Per-email override: serve the out-of-band file-provider cert (no ACME resolver).
	// Only meaningful for an SSL env with an explicit public domain — the bridge has
	// already verified the effective email differs from the global before setting this.
	if ro.OverrideCert && e.SSLEnabled && !e.SSLSelfSigned {
		e.useFileCert = true
	}
	e.CustomDomains = ro.CustomDomains
	e.RouterMiddlewares = ro.RouterMiddlewares
	applyWebEntryFallback(cfg, e)
	applyPreDeploy(cfg, e)
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
		// Nothing is marked as the web entry, yet Traefik resolved a domain — without a
		// router the env 404s on its domain (Traefik silently drops an empty/failed router,
		// so the whole stack looks "up" but is unreachable). Promote a sensible default:
		//   • single service           → it's unambiguously the entry.
		//   • multi-service image stack → the first NON-datastore service, so an app+db
		//     template (Gitea/Ghost/WordPress/…) routes to the app, not its postgres/mysql.
		// Skipped when the project drives ingress from the routing table (Config.Routes —
		// web_routed is ignored in route mode) or when every service looks like a datastore.
		if len(cfg.Routes) > 0 {
			return
		}
		web = defaultWebEntry(cfg.Services)
		if web == -1 {
			return
		}
		cfg.Services[web].WebRouted = true
	}
	// A web entry with no container port can't form a valid Traefik service port —
	// default to 80 (the near-universal HTTP default for images like nginx).
	if string(cfg.Services[web].Port) == "" {
		cfg.Services[web].Port = flexStr("80")
	}
}

// defaultWebEntry picks the implicit web entry for a stack that marked none. A single
// service is unambiguously the entry; among several, the first that isn't a recognized
// datastore/cache wins (so an app+db stack routes to the app, never the database).
// Returns -1 when no suitable entry exists (every service looks like a datastore).
func defaultWebEntry(svcs []Service) int {
	if len(svcs) == 1 {
		return 0
	}
	for i := range svcs {
		if !isDatastoreImage(svcs[i].Image) {
			return i
		}
	}
	return -1
}

// datastoreImages name backing infrastructure (DB / cache / queue / search) that never
// serves the public HTTP entry — used to skip them when guessing a stack's web entry.
var datastoreImages = map[string]bool{
	"postgres": true, "postgresql": true, "mysql": true, "mariadb": true, "percona": true,
	"mongo": true, "mongodb": true, "redis": true, "valkey": true, "keydb": true,
	"memcached": true, "clickhouse": true, "rabbitmq": true, "nats": true, "kafka": true,
	"zookeeper": true, "elasticsearch": true, "opensearch": true, "etcd": true,
	"cassandra": true, "influxdb": true, "victoriametrics": true, "meilisearch": true,
	"typesense": true, "qdrant": true,
}

// datastoreSubstrings catch engine forks/variants whose image name embeds the engine —
// e.g. clickhouse/clickhouse-server, tensorchord/pgvecto-rs, postgis/postgis, a pinned
// "postgres-15". High-confidence: no common web/UI image embeds these as a substring.
var datastoreSubstrings = []string{
	"postgres", "postgis", "pgvecto", "pgvector", "timescale",
	"clickhouse", "mariadb", "mysql",
}

// isDatastoreImage reports whether an image reference names a known datastore/cache/queue,
// matched on the final path segment with any registry/org prefix and tag/digest stripped
// (so "bitnami/postgresql" and "docker.io/library/postgres:15" both match "postgres"), plus
// a substring pass for engine forks ("clickhouse/clickhouse-server", "tensorchord/pgvecto-rs").
func isDatastoreImage(image string) bool {
	base := image
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexAny(base, ":@"); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(base)
	if datastoreImages[base] {
		return true
	}
	for _, p := range datastoreSubstrings {
		if strings.Contains(base, p) {
			return true
		}
	}
	return false
}

// applyPreDeploy synthesizes a one-shot "{svc}-migrate" service for each BUILD service
// that declares a PreDeploy command, and gates the app (plus any sibling reusing its
// image) on it via depends_on service_completed_successfully — so the release/migrate
// command runs once, before the app starts, in the app's own image + env. The migrate
// service waits for the same dependencies the app does (the managed DB, etc.), so a
// migration only runs once the database is healthy.
//
// Compose only: docker stack deploy ignores depends_on conditions, so the gate can't be
// enforced under Swarm — we skip synthesis there (the UI surfaces this). Synthesis is
// also skipped when the app already ships its own release/migrate gate (an imported
// compose), so Rigger never emits a duplicate or conflicting one-shot service.
func applyPreDeploy(cfg *Config, e Env) {
	if e.Deployment == "swarm" {
		return
	}
	existing := map[string]bool{}
	for _, s := range cfg.Services {
		existing[s.Name] = true
	}
	managed := managedDBName(cfg, e)
	var synthesized []Service
	for i := range cfg.Services {
		s := &cfg.Services[i]
		if s.Build == nil || strings.TrimSpace(s.PreDeploy) == "" {
			continue
		}
		if appAlreadyGated(cfg, *s) {
			continue // the app brings its own migrate/release gate — don't duplicate
		}
		migrateName := s.Name + "-migrate"
		if existing[migrateName] {
			continue // a service of that name already exists (mirrors buildAdminer)
		}
		// The migrate waits for whatever the app waits for (its DB/redis, managed or
		// app-owned), snapshotted BEFORE we add the migrate to the app's own deps.
		deps := append([]string{}, s.DependsOn...)
		if managed != "" {
			deps = appendUnique(deps, managed)
		}
		synthesized = append(synthesized, Service{
			Name:      migrateName,
			Role:      "predeploy",
			ImageFrom: s.Name,
			Command:   s.PreDeploy,
			EnvFile:   true,
			Restart:   "no",
			DependsOn: deps,
			// Run with the SAME environment as the app it precedes: the flat .env
			// (env_file) PLUS the app's per-service env_vars and service links (e.g. a
			// DATABASE_URL link to postgres). Without these the migrate could see a
			// different DB config than the app — a release command must not.
			EnvVars: s.EnvVars,
			Links:   s.Links,
		})
		existing[migrateName] = true
		// Gate the build service and any sibling that reuses its image (e.g. a `web`).
		gate := func(svc *Service) {
			svc.DependsOn = appendUnique(svc.DependsOn, migrateName)
			if svc.DependsOnConditions == nil {
				svc.DependsOnConditions = map[string]string{}
			}
			svc.DependsOnConditions[migrateName] = "service_completed_successfully"
		}
		gate(s)
		for j := range cfg.Services {
			if cfg.Services[j].ImageFrom == s.Name {
				gate(&cfg.Services[j])
			}
		}
	}
	cfg.Services = append(cfg.Services, synthesized...)
}

// appAlreadyGated reports whether service s already depends on a one-shot
// release/migrate service — either via an explicit service_completed_successfully
// condition (captured by the detector from an imported compose) or a dependency that
// is itself a predeploy/run-once service. Used to suppress duplicate synthesis.
func appAlreadyGated(cfg *Config, s Service) bool {
	for _, cond := range s.DependsOnConditions {
		if cond == "service_completed_successfully" {
			return true
		}
	}
	for _, dep := range s.DependsOn {
		for _, o := range cfg.Services {
			if o.Name == dep && (o.Role == "predeploy" || strings.EqualFold(o.Restart, "no")) {
				return true
			}
		}
	}
	return false
}

// managedDBName returns the synthesized managed-database service name for this project
// (postgres/mysql/mariadb/mongodb), or "" when there's no managed DB. Reads the
// effective engine (project-level, else the legacy per-env field). A pre-deploy migrate
// service depends on it so migrations run only once the DB is healthy.
func managedDBName(cfg *Config, e Env) string {
	eng := cfg.Project.Database
	if eng == "" {
		eng = e.Database
	}
	switch eng {
	case "postgres", "mysql", "mariadb", "mongodb", "opensearch", "victoriametrics":
		return eng
	}
	return ""
}

// appendUnique appends v to s if not already present.
func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// resolveRoute derives an env's domain + TLS mode when the user enabled Traefik
// routing but left the domain blank (the Render-style "just give it a URL" case).
// Activation is the existing traefik_enabled toggle, so envs that host-bind
// (Traefik off) and envs with an explicit domain are untouched. Derived host:
//   - base domain set → {prefix}-{env}.{base}, HTTPS via Let's Encrypt
//   - no base domain  → {prefix}-{env}.localhost, HTTP (or self-signed if LocalTLS)
//
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
	// Local HTTPS (Traefik's self-signed default cert) for an auto URL that can't get
	// a real Let's Encrypt cert (localhost / magic-DNS). Per-env opt-in (e.SSLSelfSigned
	// from config), OR the legacy project-level default (ro.LocalTLS). Captured before
	// the switch overwrites the SSL fields.
	localHTTPS := ro.LocalTLS || e.SSLSelfSigned
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
		// Cross-machine magic-DNS auto-URL — HTTP by default; self-signed HTTPS when the
		// env opts into local HTTPS (real LE certs for these come later: per-host HTTP-01
		// if publicly reachable, or traefik.me's shared cert).
		e.Domain = label + "." + magicSuffix
		e.SSLEnabled = localHTTPS
		e.SSLSelfSigned = localHTTPS
	default:
		// Host-local default (also when a magic-DNS mode is selected but no host set).
		e.Domain = label + ".localhost"
		e.SSLEnabled = localHTTPS
		e.SSLSelfSigned = localHTTPS
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
	// appMW is the current service's extra file-provider middleware refs (a workspace
	// access list + WAF/cache plugins, resolved by the API into Env.RouterMiddlewares).
	// Set per-service in buildService — populated only for app routers, never admin
	// sidecars — and prepended to their middleware chain. See
	// docs/design/workspace-plugins-and-access-lists.md.
	appMW []string
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
	// A user-chosen external network (expose_mode=none "attach to network") so an
	// outside proxy / another stack can reach the app in-network. Must already exist.
	if e.AttachNetwork != "" && !(e.TraefikEnabled && e.AttachNetwork == e.TraefikNetwork) {
		g.line("  " + e.AttachNetwork + ":")
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
//
// singleInstance pins the service to its passed replica count regardless of any
// per-service override: Rigger-managed stateful services (db / redis / object storage /
// search / TSDB) run single-instance — scaling a single-volume stateful container
// corrupts data and gives no HA. Placement IS still honored so they can be pinned to
// their volume's node. Real scaling for those needs a clustered topology (future).
func (g *gen) deployBlock(isSwarm bool, svc, replicas, restart string, singleInstance bool) {
	if replicas == "" {
		replicas = "1"
	}
	if restart == "" {
		restart = "unless-stopped"
	}
	if !isSwarm {
		// Quote the one-shot value: bare `no` is parsed as a YAML boolean (false) and
		// docker rejects it; "no" is the documented run-once restart policy.
		if restart == "no" {
			restart = "\"no\""
		}
		g.line("    restart: " + restart)
		g.emitServiceSecrets()
		return
	}

	sw := g.e.Swarm
	var placement []string
	if so, ok := sw.Services[svc]; ok {
		if !singleInstance { // managed stateful services ignore the replica override
			if r := string(so.Replicas); r != "" {
				replicas = r
			}
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
//
// The per-router redirect replaces Traefik's old global web→websecure redirect,
// so HTTP-only (local) envs are no longer forced onto a cert-less HTTPS.
func (g *gen) traefikLabels(router, host, port string, usersVar, certResolver, wildcard string) {
	if !g.e.TraefikEnabled {
		return
	}
	g.line("    labels:")
	g.line("      - \"traefik.enable=true\"")
	g.emitTraefikRouter(routerSpec{name: router, rule: "Host(`" + host + "`)", port: port, usersVar: usersVar, certResolver: certResolver, wildcard: wildcard})
}

// routerSpec describes one Traefik router for emitTraefikRouter. The zero value matches the
// pre-routes generator (no strip/addPrefix/priority, an own loadbalancer service), so legacy
// callers produce byte-identical output.
type routerSpec struct {
	name         string
	rule         string
	port         string
	usersVar     string
	certResolver string
	wildcard     string   // request a *.{wildcard} cert (catch-all/apex only)
	strip        []string // stripprefix.prefixes (path rewrite: remove these)
	addPrefix    string   // addprefix.prefix (path rewrite: prepend, after strip)
	priority     int      // explicit router priority (0 = omit)
	serviceRef   string   // reference an already-defined service; "" = define our own {name}
}

// emitTraefikRouter emits the per-router labels (basic-auth / strip / addprefix middlewares, the
// router rule/entrypoints/tls/middlewares + loadbalancer-or-service-ref, and the SSL http→https
// companion) for ONE router. It does NOT emit the `labels:` header or `traefik.enable` — the
// caller does that once per service, so a service can carry multiple routers (route-driven mode).
func (g *gen) emitTraefikRouter(s routerSpec) {
	router := s.name
	port := s.port
	if port == "" {
		port = "80"
	}
	certResolver := s.certResolver
	if certResolver == "" {
		certResolver = "letsencrypt" // per-host HTTP-01 (default; DNS-01 sets "dns")
	}
	auth := s.usersVar != ""
	// Optional HTTP basic-auth middleware: admin sidecars (${ADMIN_UI_USERS}) when the
	// env protects them, or an app web service (${APP_AUTH_USERS}) when auth_gate=basic.
	// The htpasswd line comes from .env so the bcrypt '$' chars are inserted literally
	// rather than written inline (which would need '$$' doubling).
	if auth {
		g.line("      - \"traefik.http.middlewares." + router + "_auth.basicauth.users=${" + s.usersVar + "}\"")
	}
	// Path rewrite (route mode): stripprefix removes the matched prefix, then addprefix prepends
	// the Target. Sub-path remainder and query string are preserved by Traefik. Default Target =
	// Match ⇒ neither middleware is emitted (passthrough); Target "/" ⇒ strip only.
	if len(s.strip) > 0 {
		g.line("      - \"traefik.http.middlewares." + router + "_strip.stripprefix.prefixes=" + strings.Join(s.strip, ",") + "\"")
	}
	if s.addPrefix != "" {
		g.line("      - \"traefik.http.middlewares." + router + "_addprefix.addprefix.prefix=" + s.addPrefix + "\"")
	}
	// Middleware chain (applied left→right): basic-auth, strip, addprefix, then the shared
	// "loading" errors page so a starting/crash-looping backend shows a friendly 502/503/504 retry.
	mws := "rigger-loading@file"
	if s.addPrefix != "" {
		mws = router + "_addprefix," + mws
	}
	if len(s.strip) > 0 {
		mws = router + "_strip," + mws
	}
	if auth {
		mws = router + "_auth," + mws
	}
	// Workspace access list + WAF/cache plugins (resolved by the API) run FIRST — auth/IP/geo
	// gate before the app sees the request. Only set for app routers (never admin sidecars).
	if len(g.appMW) > 0 {
		mws = strings.Join(g.appMW, ",") + "," + mws
	}
	// service line: define our own loadbalancer, or reference a shared one (route mode).
	emitService := func() {
		if s.serviceRef != "" {
			g.line("      - \"traefik.http.routers." + router + ".service=" + s.serviceRef + "\"")
		} else {
			g.line("      - \"traefik.http.services." + router + ".loadbalancer.server.port=" + port + "\"")
		}
	}
	if g.e.SSLEnabled {
		g.line("      - \"traefik.http.routers." + router + ".rule=" + s.rule + "\"")
		g.line("      - \"traefik.http.routers." + router + ".entrypoints=websecure\"")
		g.line("      - \"traefik.http.routers." + router + ".tls=true\"")
		// useFileCert ⇒ Traefik serves the out-of-band file-provider cert (matched by
		// SNI); emit NO certresolver so it doesn't also try its own ACME account.
		if !g.e.SSLSelfSigned && !g.e.useFileCert {
			g.line("      - \"traefik.http.routers." + router + ".tls.certresolver=" + certResolver + "\"")
			// Request a single wildcard cert for the apex base-domain router (DNS-01).
			if s.wildcard != "" {
				g.line("      - \"traefik.http.routers." + router + ".tls.domains[0].main=" + s.wildcard + "\"")
				g.line("      - \"traefik.http.routers." + router + ".tls.domains[0].sans=*." + s.wildcard + "\"")
			}
		}
		if s.priority > 0 {
			g.line("      - \"traefik.http.routers." + router + ".priority=" + strconv.Itoa(s.priority) + "\"")
		}
		g.line("      - \"traefik.http.routers." + router + ".middlewares=" + mws + "\"")
		emitService()
		// Companion HTTP router → redirect to HTTPS (per-router, not global).
		g.line("      - \"traefik.http.routers." + router + "_web.rule=" + s.rule + "\"")
		g.line("      - \"traefik.http.routers." + router + "_web.entrypoints=web\"")
		g.line("      - \"traefik.http.routers." + router + "_web.middlewares=" + router + "_redirect\"")
		g.line("      - \"traefik.http.middlewares." + router + "_redirect.redirectscheme.scheme=https\"")
	} else {
		g.line("      - \"traefik.http.routers." + router + ".rule=" + s.rule + "\"")
		g.line("      - \"traefik.http.routers." + router + ".entrypoints=web\"")
		if s.priority > 0 {
			g.line("      - \"traefik.http.routers." + router + ".priority=" + strconv.Itoa(s.priority) + "\"")
		}
		g.line("      - \"traefik.http.routers." + router + ".middlewares=" + mws + "\"")
		emitService()
	}
}

// emitRouteLabels emits Traefik labels for a service from the project-level routing table
// (Config.Routes), used instead of the single host-based traefikLabels when any routes exist.
// Every route becomes its OWN router (so each can rewrite its path independently): a path route
// is `Host(domain) && PathPrefix(/api)` (plain `Host(domain)` for the catch-all "/"/""), a
// subdomain route is `Host(sub.domain)`. All routers share ONE loadbalancer service ({cname}).
// A route's Target rewrites the matched prefix: backend receives Target + (path − Match), with
// the remainder sub-path and query string preserved (Target "" or == Match ⇒ passthrough; "/"
// ⇒ strip). The catch-all owns the wildcard cert + verified custom domains (emitted once).
// No-op when Traefik is disabled.
func (g *gen) emitRouteLabels(cname string, svc Service, routes []Route) {
	if !g.e.TraefikEnabled || len(routes) == 0 {
		return
	}
	port := string(svc.Port)
	if port == "" {
		port = "80"
	}
	// Basic-auth middleware selection mirrors the legacy web branch.
	usersVar := ""
	switch {
	case svc.AuthProtect && g.e.ProtectAdminUIs:
		usersVar = "ADMIN_UI_USERS"
	case !svc.AuthProtect && g.authGate() == "basic":
		usersVar = "APP_AUTH_USERS"
	}
	g.line("    labels:")
	g.line("      - \"traefik.enable=true\"")
	// One shared backend service for all of this service's routers.
	g.line("      - \"traefik.http.services." + cname + ".loadbalancer.server.port=" + port + "\"")
	for i, r := range routes {
		name := cname + "_r" + strconv.Itoa(i)
		if r.Type == "subdomain" {
			host := g.e.Domain
			if m := strings.TrimSpace(r.Match); m != "" {
				host = m + "." + g.e.Domain
			}
			g.emitTraefikRouter(routerSpec{name: name, rule: "Host(`" + host + "`)", port: port, usersVar: usersVar, certResolver: g.e.certResolver, serviceRef: cname})
			continue
		}
		// path route
		m := strings.TrimSpace(r.Match)
		catchAll := m == "" || m == "/"
		rule := "Host(`" + g.e.Domain + "`)"
		priority := 0
		var strip []string
		addPrefix := ""
		if !catchAll {
			rule += " && PathPrefix(`" + m + "`)"
			priority = 100 + len(m) // outrank the bare-Host catch-all, deterministically
			// Target rewrite: backend sees Target + (path − Match). Default Target = Match ⇒ pass
			// through (no middleware); Target "/" ⇒ strip only; else strip then addprefix(Target).
			target := strings.TrimSpace(r.Target)
			if target != "" && target != m {
				strip = []string{m}
				if target != "/" {
					addPrefix = target
				}
			}
		}
		wildcard := ""
		if catchAll && g.e.wildcardBase != "" {
			wildcard = g.e.wildcardBase
		}
		g.emitTraefikRouter(routerSpec{
			name: name, rule: rule, port: port, usersVar: usersVar, certResolver: g.e.certResolver,
			wildcard: wildcard, strip: strip, addPrefix: addPrefix, priority: priority, serviceRef: cname,
		})
		if catchAll {
			g.traefikCustomDomains(cname, port, usersVar, g.e.CustomDomains)
		}
	}
}

// traefikCustomDomains emits, for each VERIFIED custom domain attached to the env, an
// additional HTTPS router on the apex web service. Each custom domain gets a per-host
// Let's Encrypt (HTTP-01) cert — the base-domain wildcard/DNS cert doesn't cover an
// external apex — plus a companion HTTP router that redirects to HTTPS (and lets the
// :80 ACME challenge through). All routers reuse the apex service + its basic-auth
// middleware (when the env gates it). No-op when the list is empty (golden parity).
func (g *gen) traefikCustomDomains(router, port, usersVar string, domains []string) {
	if !g.e.TraefikEnabled || len(domains) == 0 {
		return
	}
	if port == "" {
		port = "80"
	}
	// One shared http→https redirect middleware for all custom-domain HTTP routers
	// (the apex router's own _redirect only exists when the apex is SSL).
	redirect := router + "_cdredirect"
	g.line("      - \"traefik.http.middlewares." + redirect + ".redirectscheme.scheme=https\"")
	mws := "rigger-loading@file"
	if usersVar != "" {
		mws = router + "_auth," + mws // reuse the apex router's basic-auth middleware
	}
	if len(g.appMW) > 0 {
		mws = strings.Join(g.appMW, ",") + "," + mws // workspace access list + plugins (app routers)
	}
	for i, d := range domains {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		rt := router + "_cd" + strconv.Itoa(i)
		rule := "Host(`" + d + "`)"
		// HTTPS router with a per-host Let's Encrypt cert, pointing at the apex service.
		g.line("      - \"traefik.http.routers." + rt + ".rule=" + rule + "\"")
		g.line("      - \"traefik.http.routers." + rt + ".entrypoints=websecure\"")
		g.line("      - \"traefik.http.routers." + rt + ".tls=true\"")
		g.line("      - \"traefik.http.routers." + rt + ".tls.certresolver=letsencrypt\"")
		g.line("      - \"traefik.http.routers." + rt + ".service=" + router + "\"")
		g.line("      - \"traefik.http.routers." + rt + ".middlewares=" + mws + "\"")
		// Companion HTTP router → redirect to HTTPS.
		g.line("      - \"traefik.http.routers." + rt + "_web.rule=" + rule + "\"")
		g.line("      - \"traefik.http.routers." + rt + "_web.entrypoints=web\"")
		g.line("      - \"traefik.http.routers." + rt + "_web.middlewares=" + redirect + "\"")
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
