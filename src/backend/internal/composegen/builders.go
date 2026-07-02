package composegen

import (
	"fmt"
	"strings"

	"github.com/mansoor/rigger/ui/internal/databases"
)

// Trailing dash counts for section comments. App services use a fixed width
// (no historical parity to match — the unified model regenerates goldens); the
// managed-dependency widths are kept from the old output for tidy diffs.
const (
	dashService  = 50
	dashPostgres = 48
	dashMySQL    = 54
	dashRedis    = 54
	dashStorage  = 38
	dashMongo    = 51
	dashSearch   = 49
	dashTSDB     = 44
)

// buildStack emits the volumes + services blocks for the unified service graph:
// every app service in cfg.Services, then the managed-dependency toggles
// (database / redis / object storage) which stay as env flags until Phase 3.
func (g *gen) buildStack(prefix, rp, registry, tag string, isSwarm bool) {
	g.emitVolumes(prefix)
	g.line("services:")
	g.line("")
	for _, svc := range g.cfg.Services {
		if svc.Name == "" {
			continue
		}
		g.buildService(prefix, rp, registry, tag, svc, isSwarm)
	}
	g.buildManagedDeps(prefix, isSwarm)
	g.buildAdminer(prefix, rp, registry, tag, isSwarm)
	g.buildStorageConsole(prefix, rp, registry, tag, isSwarm)
	g.buildMailpit(prefix, rp, registry, tag, isSwarm)
	g.buildCloudflared(prefix, isSwarm)
}

// buildMailpit synthesizes the Mailpit test-SMTP sidecar (axllent/mailpit — the
// maintained MailHog successor) as a Traefik-routed service on the "mail" subdomain,
// reusing buildService. Mailpit catches ALL outbound mail (catch-all) and shows it in
// its web UI (:8025); the app sends to it over SMTP at mailpit:1025 (reachable in-network
// without publishing — see envgen MAIL_*). Per-env (Tier-2): gated on the effective
// mailpit toggle so it runs in dev/stage but not prod. AuthProtect → eligible for the
// per-env basic-auth middleware. Host-port 8025 only when Traefik is off.
func (g *gen) buildMailpit(prefix, rp, registry, tag string, isSwarm bool) {
	if !g.mailpitOn() {
		return
	}
	ver := g.cfg.version("mailpit", "latest")
	svc := Service{
		Name:        "mailpit",
		Image:       "axllent/mailpit",
		Tag:         ver,
		Port:        "8025", // the web UI (Traefik / host-port target); SMTP 1025 is in-network only
		WebRouted:   true,
		Subdomain:   "mail",
		HostPort:    "8025",
		AuthProtect: true,
	}
	g.buildService(prefix, rp, registry, tag, svc, isSwarm)
}

// buildAdminer synthesizes the Adminer web-SQL service from the project-level
// web_sql flag — the unified representation, emitted for ANY stack that has a
// database. Reuses buildService so its routing/ports/networks/env match an app
// service: apex web entry on a pure database-hosting project (no app service routes),
// else the "adminer" subdomain so it coexists with the app's web entry. Skipped when
// a literal "adminer" service already exists in Services (legacy projects — that
// service already rendered), avoiding a duplicate (invalid) compose key.
func (g *gen) buildAdminer(prefix, rp, registry, tag string, isSwarm bool) {
	if !g.webSQLOn() || g.hasService("adminer") {
		return
	}
	engine := g.dbEngine()
	if engine == "" || engine == "none" {
		return // Adminer needs a database to connect to
	}
	// Adminer is a SQL client — it can't talk to a document store like MongoDB. Skip
	// it for non-SQL engines (a future mongo-express sidecar covers Mongo separately).
	if eng, ok := databases.Get(engine); ok && eng.Driver != "postgres" && eng.Driver != "mysql" {
		return
	}
	svc := Service{
		Name:      "adminer",
		Image:     "adminer",
		Tag:       "4.8.1", // PINNED — 5.x changed the plugin surface the login PHP targets
		Port:      "8080",  // Adminer's native HTTP port (Traefik / host-port target)
		WebRouted: true,
		HostPort:  "8978", // host publish for the no-Traefik case (8080 would clash with Rigger)
		EnvFile:   true,   // inject DB creds + ADMINER_LOGIN_SECRET from .env
		// Also pin the auto-login secret as an explicit (interpolated) environment entry,
		// not just via env_file. compose's config-hash is computed from the YAML, so an
		// interpolated ${ADMINER_LOGIN_SECRET} value is part of it — meaning a changed or
		// newly-added secret forces `compose up -d` to RECREATE this container. env_file
		// content alone is NOT hashed, so a refresh that added/rotated the secret would
		// otherwise leave a stale Adminer running with no secret → auto-login silently
		// no-ops (the plugin reads an empty getenv and shows the normal login page).
		EnvVars:   map[string]flexStr{"ADMINER_LOGIN_SECRET": flexStr("${ADMINER_LOGIN_SECRET}")},
		Volumes:   []string{"${RIGGER_BIND_ROOT:-.}/adminer-login.php:/var/www/html/plugins-enabled/01-rigger-autologin.php:ro"},
		DependsOn: []string{engine},
		// Admin UI → eligible for the per-env basic-auth middleware (gated on ProtectAdminUIs).
		AuthProtect: true,
	}
	// If an app service already owns the apex web entry, route Adminer on a subdomain
	// so the two don't collide in Traefik.
	if g.hasAppWebEntry() {
		svc.Subdomain = "adminer"
	}
	g.buildService(prefix, rp, registry, tag, svc, isSwarm)
}

// buildStorageConsole synthesizes the optional MinIO admin console (opens3/console —
// the community fork that preserves the full pre-trim MinIO console feature set) as a
// Service routed through Traefik on the "storage" subdomain, reusing buildService so it
// gets the same routing/ports/networks as Adminer. It's a root-mounted SPA, so it's
// ALWAYS a subdomain. With Traefik off it falls back to publishing host port 9090
// (dev-only). Gated on the project's StorageUI flag + MinIO being enabled. The user logs
// into it with the MinIO root creds (shown in the managed-services info row).
func (g *gen) buildStorageConsole(prefix, rp, registry, tag string, isSwarm bool) {
	if !g.storageUIOn() || !g.minioOn() {
		return
	}
	ver := g.cfg.version("storage_console", "latest")
	svc := Service{
		Name:      "storage_console",
		Image:     "opens3/console",
		Tag:       ver,
		Port:      "9090", // the console's listen port (Traefik / host-port target)
		WebRouted: true,
		Subdomain: "storage", // always a subdomain — the SPA assumes it's served at /
		HostPort:  "9090",    // host publish only when Traefik is off (no collisions under Traefik)
		DependsOn: []string{"minio"},
		// opens3/console contract: point it at the in-network MinIO S3 API; the PBKDF
		// passphrase/salt (session crypto) come from .env via compose interpolation.
		EnvVars: map[string]flexStr{
			// Bare "minio" host — opens3/console / S3 reject underscore hostnames.
			"CONSOLE_MINIO_SERVER":     flexStr("http://minio:9000"),
			"CONSOLE_PBKDF_PASSPHRASE": flexStr("${MINIO_CONSOLE_PASSPHRASE}"),
			"CONSOLE_PBKDF_SALT":       flexStr("${MINIO_CONSOLE_SALT}"),
		},
		// Admin UI → eligible for the per-env basic-auth middleware.
		AuthProtect: true,
	}
	g.buildService(prefix, rp, registry, tag, svc, isSwarm)
}

// hasService reports whether a service with the given name exists in the graph.
func (g *gen) hasService(name string) bool {
	for _, s := range g.cfg.Services {
		if s.Name == name {
			return true
		}
	}
	return false
}

// hasAppWebEntry reports whether any app service is web-routed (so a synthesized
// Adminer must take a subdomain rather than the apex domain).
func (g *gen) hasAppWebEntry() bool {
	for _, s := range g.cfg.Services {
		if s.WebRouted {
			return true
		}
	}
	return false
}

// emitVolumes writes the top-level volumes block: named volumes referenced by
// services, the managed-dependency volumes (toggle-driven), and explicit
// named_volumes[]. Bind mounts and env-var paths are skipped.
func (g *gen) emitVolumes(prefix string) {
	c := g.cfg
	engine := g.dbEngine()
	var vols []string
	seen := map[string]bool{}
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			vols = append(vols, v)
		}
	}
	for _, svc := range c.Services {
		for _, vol := range svc.Volumes {
			if vol == "" {
				continue
			}
			if host := volHost(vol); isNamedVolume(host) {
				add(prefix + "_" + host)
			}
		}
	}
	if engine == "postgres" {
		add(prefix + "_pg_data")
	}
	if engine == "mysql" {
		add(prefix + "_mysql_data")
	}
	if engine == "mariadb" {
		add(prefix + "_mariadb_data")
	}
	if engine == "mongodb" {
		add(prefix + "_mongodb_data")
	}
	if engine == "opensearch" {
		add(prefix + "_opensearch_data")
	}
	if engine == "victoriametrics" {
		add(prefix + "_victoriametrics_data")
	}
	if g.redisOn() {
		add(prefix + "_redis_data")
	}
	if g.minioOn() {
		add(prefix + "_minio_data")
	}
	if g.localStorageOn() {
		add(prefix + "_storage")
	}
	for _, nv := range c.NamedVolumes {
		if nv.Name == "" || strings.HasPrefix(nv.Name, ".") || strings.HasPrefix(nv.Name, "/") {
			continue
		}
		add(prefix + "_" + nv.Name)
	}
	if len(vols) == 0 {
		return
	}
	g.line("volumes:")
	for _, v := range vols {
		g.line("  " + v + ":")
	}
	g.line("")
}

// buildService emits one app service from the unified model. The image line is
// resolved from the service's source (Build / Image / ImageFrom); web routing,
// ports, healthcheck, depends_on, volumes, env and the deploy block all derive
// from per-service fields — no language branching.
func (g *gen) buildService(prefix, rp, registry, tag string, svc Service, isSwarm bool) {
	e := g.e
	// The compose service KEY is the bare service name; the compose project / swarm
	// stack already namespaces it with {prefix}_{env}, so a prefixed key would double
	// it (container `{proj}-{proj}_app-1` / swarm service `{stack}_{stack}_app`).
	// container_name pins the clean, single-prefix name in compose — identical to the
	// swarm service name `{stack}_{key}` = `{prefix}_{env}_{name}`.
	key := svc.Name
	cname := prefix + "_" + svc.Name
	restart := svc.Restart
	if restart == "" {
		restart = "unless-stopped"
	}

	g.line(sectionComment(svc.Name+" ("+serviceLabel(svc)+")", dashService))
	g.line("  " + key + ":")
	g.line("    image: " + serviceImageRef(svc, rp, registry, tag))
	// When no EFFECTIVE registry resolves (project → workspace/global system all
	// empty), a Rigger-built service only ever exists on the local build daemon (built
	// by `docker build`, never pushed). Its tag has no registry prefix, so `compose up`
	// would resolve a missing/mis-tagged image against Docker Hub and fail. Pin
	// pull_policy=never: use the local image, or error clearly if it isn't built —
	// never silently pull. This is the automatic local single-node fallback (the
	// deploy gate already blocks build-service deploys to Swarm/remote when no registry
	// resolves). Image-type services keep the default so they still pull; once a
	// registry resolves, built images are pullable, so leave the default there too.
	if svc.Build != nil && registry == "" {
		g.line("    pull_policy: never")
	}
	g.line("    container_name: " + cname)
	if svc.Command != "" {
		// Single-quoted YAML scalar; escape embedded single quotes ('' is the YAML
		// escape) so commands like `sh -c 'echo hi'` stay valid.
		g.line("    command: '" + strings.ReplaceAll(svc.Command, "'", "''") + "'")
	}
	// envWritable: the app owns its .env at runtime — delivered as a writable bind
	// (below) instead of process env, so the app's own writes aren't overridden.
	envWritable := svc.EnvFileWritable && svc.EnvFileMount != ""
	if svc.EnvFile && !envWritable {
		g.line("    env_file: .env")
	}

	// Networks — long form with both the short service-name alias AND the fully
	// prefixed name ({prefix}_{name}). The prefixed alias makes the in-network host
	// Rigger advertises (e.g. POSTGRES_HOST={prefix}_postgres) actually resolve, so
	// apps can reach a service by either name. Join the Traefik network when this
	// service is the web entry under Traefik.
	g.line("    networks:")
	g.line("      " + prefix + "_net:")
	g.line("        aliases:")
	g.line("          - " + svc.Name)
	g.line("          - " + cname)
	if g.traefikRouted(svc) && e.TraefikEnabled && g.exposeMode() == "traefik" {
		g.line("      " + e.TraefikNetwork + ": {}")
	}
	// Join a user-chosen external network so an outside proxy / another stack can reach
	// this web service by name (e.g. expose_mode=none "attach to network").
	if svc.WebRouted && e.AttachNetwork != "" && e.AttachNetwork != e.TraefikNetwork {
		g.line("      " + e.AttachNetwork + ": {}")
	}

	g.emitDependsOn(prefix, svc.DependsOn, svc.DependsOnConditions, isSwarm)

	// Volumes (named volumes get the prefix; bind/env-var mounts pass through).
	// volumes: is opened lazily so the writable-.env bind below can share the block.
	volumesOpen := false
	openVolumes := func() {
		if !volumesOpen {
			g.line("    volumes:")
			volumesOpen = true
		}
	}
	for _, vol := range svc.Volumes {
		if vol == "" {
			continue
		}
		openVolumes()
		if host := volHost(vol); isNamedVolume(host) {
			g.line("      - " + prefix + "_" + host + ":" + volRest(vol))
		} else if strings.HasPrefix(host, ".") {
			// Relative bind mount (e.g. a scanned repo's ./mosquitto/mosquitto.conf):
			// rewrite the source so the HOST daemon can resolve it (see bindSource).
			g.line("      - " + g.bindSource(host) + ":" + volRest(vol))
		} else {
			g.line("      - " + vol) // absolute path or ${VAR} — pass through unchanged
		}
	}
	// Local object storage: persist the app's storage dir on a named volume so uploads
	// survive recreate/redeploy (an image-baked CodeCanyon app would otherwise lose them
	// on every deploy). Attached to app services (build or image-reuse) — never pulled
	// images / synthesized admin sidecars. Workers reusing the app image share the volume.
	if g.localStorageOn() && (svc.Build != nil || svc.ImageFrom != "") {
		openVolumes()
		g.line("      - " + prefix + "_storage:" + g.storageMountPath())
	}

	// Materialise the env's generated .env as a physical file at EnvFileMount. Two modes:
	//   - WRITABLE (env_file_writable): bind the env's real .env file rw so the app can
	//     persist its own writes (a CodeCanyon installer writing INSTALLED=true). The
	//     bind lives in the env dir → survives recreate AND migrates with the env on a
	//     host move (a named volume would not). Rooted at ${RIGGER_BIND_ROOT} like every
	//     other bind so the host daemon resolves it. Process env (env_file:) is suppressed
	//     above so nothing overrides the file's values.
	//   - READ-ONLY (default): deliver the .env as an inline compose `config` (some
	//     frameworks re-read .env from disk; Laravel's `php artisan serve` subprocess).
	switch {
	case envWritable:
		openVolumes()
		g.line("      - ${RIGGER_BIND_ROOT:-.}/.env:" + svc.EnvFileMount)
	case svc.EnvFileMount != "" && g.envFile != "":
		g.line("    configs:")
		g.line("      - source: " + prefix + "_dotenv")
		g.line("        target: " + svc.EnvFileMount)
		g.envCfgUsed = true
	}

	// Environment (keys sorted for deterministic output). Service links are merged
	// into the same keyspace — an explicit link wins over an env_vars key of the
	// same name — so we never emit a duplicate (invalid) YAML map key.
	if env := g.serviceEnv(prefix, svc); len(env) > 0 {
		g.line("    environment:")
		for _, k := range sortedStringKeys(env) {
			g.line("      - " + k + "=" + env[k])
		}
	}

	// Traefik router id must stay globally unique across the shared rigger-traefik
	// (it routes every project), so pass the long {prefix}_{env}_{name} — NOT the short key.
	g.emitServicePorts(cname, svc)

	if svc.Healthcheck != "" {
		hc := svc.HealthcheckConfig
		g.healthcheck(svc.Healthcheck,
			strOr(hc.Interval, "30s"), strOr(hc.Timeout, "10s"),
			strOr(hc.Retries, "3"), strOr(hc.StartPeriod, "30s"),
			string(hc.StartInterval))
	}

	g.deployBlock(isSwarm, svc.Name, string(svc.Replicas), restart, false) // app/build service — scalable

	// extra_compose: service-level then env-level override.
	g.emitExtraCompose(svc.ExtraCompose)
	if ov, ok := e.ServiceOverrides[svc.Name]; ok {
		g.emitExtraCompose(ov.ExtraCompose)
	}
	g.line("")
}

// traefikRouted reports whether this service has a PUBLIC Traefik router — and so must
// join the shared Traefik network to be reachable. It must stay in lock-step with the
// isRouted decision in emitServicePorts (which emits the labels): with a project routing
// table, "routed" means the table targets this service (or it's a web-entry keeping its
// own subdomain router); otherwise it's the legacy web_routed flag. A mismatch here
// produces a router whose backend container isn't on traefik_net → Traefik 502/404.
func (g *gen) traefikRouted(svc Service) bool {
	if len(g.cfg.Routes) > 0 {
		return len(g.cfg.routesFor(svc.Name)) > 0 || (svc.WebRouted && svc.Subdomain != "")
	}
	return svc.WebRouted
}

// emitServicePorts handles web routing and port publishing, emitting at most ONE
// `ports:` block (duplicate keys are invalid YAML). A web-routed apex service under
// Traefik gets labels AND, if an EXPLICIT host_port is set, publishes it too (so a
// user-run proxy can hit the host:port directly even while Traefik also routes it);
// with no host_port it's Traefik-only. Without Traefik it binds a single host port —
// the service's own host_port if set, else the env HTTP port (so editing the
// service port just changes the published port, not adds a second one). Non-web
// services publish host_port/extra_ports, or expose the container port.
func (g *gen) emitServicePorts(router string, svc Service) {
	e := g.e
	port := string(svc.Port)
	mode := g.exposeMode()
	var publishes []string
	// Route-driven ingress: when the project defines any routes (Config.Routes), a service's
	// Traefik labels come from the routes targeting it (emitRouteLabels), and "is this service
	// publicly routed" is decided by having ≥1 route — NOT the legacy web_routed flag. With no
	// routes, isRouted == svc.WebRouted, so the generated YAML is byte-identical to before.
	routes := g.cfg.routesFor(svc.Name)
	useRoutes := len(g.cfg.Routes) > 0
	// viaRouteTable: this service's ingress is governed by the project routing table.
	// Synthesized admin sidecars (Adminer / MinIO console / Mailpit) route on their own
	// subdomain and are NEVER in the user-facing path-routing table, so in route mode they
	// keep their legacy subdomain router instead of being dropped (len(routes)==0).
	viaRouteTable := useRoutes && len(routes) > 0
	// Same predicate that decides traefik_net attachment — keep them one source of truth.
	isRouted := g.traefikRouted(svc)
	switch {
	case svc.WebRouted && mode == "cloudflare_tunnel":
		// No public router — the cloudflared connector reaches the app in-network. Honor an
		// EXPLICIT host-port mapping on an APP service if set (not admin sidecars); else no
		// host port (falls through to `expose` below so it's reachable by service name).
		if hp := string(svc.HostPort); hp != "" && !svc.AuthProtect {
			publishes = append(publishes, hp+":"+portOr(port, "80"))
		}
	case svc.WebRouted && mode == "none":
		// Internal-only: publish an explicit host_port if the user set one; otherwise
		// just expose the container port (no auto env-HTTP-port publish, no router).
		if hp := string(svc.HostPort); hp != "" {
			publishes = append(publishes, hp+":"+portOr(port, "80"))
		}
	case isRouted && e.TraefikEnabled:
		// Attach the env's workspace access list + WAF/cache plugin middlewares to APP routers
		// only — never the synthesized admin sidecars (Adminer / MinIO console), whose
		// AuthProtect basic-auth shouldn't be double-gated. See workspace-plugins design doc.
		g.appMW = nil
		if !svc.AuthProtect {
			g.appMW = e.RouterMiddlewares
		}
		if viaRouteTable {
			// Labels come from the project routing table — one path-group router (Host &&
			// PathPrefix||…) plus a router per subdomain route. The legacy single-host path is
			// bypassed; web_routed/subdomain on the service are ignored in route mode.
			g.emitRouteLabels(router, svc, routes)
		} else {
			host := e.Domain
			if svc.Subdomain != "" && e.Domain != "" {
				host = svc.Subdomain + "." + e.Domain
			}
			// Basic-auth middleware: admin sidecars when the env protects them (uses
			// ${ADMIN_UI_USERS}); real app web services when the env's auth_gate is "basic"
			// (uses ${APP_AUTH_USERS}). Empty usersVar ⇒ no auth middleware (today's default).
			usersVar := ""
			switch {
			case svc.AuthProtect && e.ProtectAdminUIs:
				usersVar = "ADMIN_UI_USERS"
			case !svc.AuthProtect && g.authGate() == "basic":
				usersVar = "APP_AUTH_USERS"
			}
			// Wildcard cert: only the apex web entry (no subdomain) requests *.{base};
			// sidecars on deeper subdomains fall back to per-host issuance via the same resolver.
			wildcard := ""
			if e.wildcardBase != "" && svc.Subdomain == "" {
				wildcard = e.wildcardBase
			}
			g.traefikLabels(router, host, port, usersVar, e.certResolver, wildcard)
			// Verified custom domains (Render-style): the apex web service also answers on
			// each external domain via its own HTTPS router with a per-host Let's Encrypt
			// (HTTP-01) cert — the wildcard/DNS cert only covers the base domain. Subdomain
			// web services keep just their primary route.
			if svc.Subdomain == "" {
				g.traefikCustomDomains(router, port, usersVar, e.CustomDomains)
			}
		}
		// An explicit host-port mapping on an APP service can be published even under
		// Traefik routing so a user-run reverse proxy / DNS can target host:port directly
		// (e.g. when this Rigger isn't public-facing). But under Traefik the app is already
		// reached by domain, so the host port is redundant and the main cause of host-port
		// conflicts — it's stripped by default (e.keepHostPort=false), re-enabled per-env or
		// per-workspace ("keep"). Only this PRIMARY web-entry port is affected; extra_ports
		// below always publish. Synthesized admin sidecars (AuthProtect) are Traefik-only.
		if hp := string(svc.HostPort); hp != "" && !svc.AuthProtect && e.keepHostPort {
			publishes = append(publishes, hp+":"+portOr(port, "80"))
		}
	case svc.WebRouted && svc.Subdomain == "":
		// Apex web service without Traefik: publish one host port. host_port wins
		// (the user's chosen port), else the env HTTP port. Subdomain web services
		// need Traefik, so without it they aren't published here.
		hostPort := string(svc.HostPort)
		if hostPort == "" {
			hostPort = string(e.HTTPPort)
		}
		if hostPort != "" {
			publishes = append(publishes, hostPort+":"+portOr(port, "80"))
		}
	case !svc.WebRouted && string(svc.HostPort) != "" && port != "":
		publishes = append(publishes, string(svc.HostPort)+":"+port)
	}
	for _, ep := range svc.ExtraPorts {
		if string(ep) != "" {
			publishes = append(publishes, string(ep))
		}
	}
	if len(publishes) > 0 {
		g.line("    ports:")
		for _, p := range publishes {
			g.line("      - \"" + p + "\"")
		}
		return
	}
	// Expose the container port for in-network reach: non-web services always; a web
	// service under cloudflare_tunnel/none with no published port (so the connector or
	// a linked service can still reach it by name).
	if port != "" && (!isRouted || mode == "cloudflare_tunnel" || mode == "none") {
		g.line("    expose:")
		g.line("      - \"" + port + "\"")
	}
}

// emitDependsOn emits the depends_on block keyed by the bare service name (service
// keys are short now — the project/stack namespaces them). Compose uses the
// condition form (per depCondition); swarm uses the bare list form (conditions are
// ignored by docker stack deploy). conds is an optional per-dependency override map
// (nil for the conditionless synthesized callers like the minio init).
func (g *gen) emitDependsOn(prefix string, deps []string, conds map[string]string, isSwarm bool) {
	var names []string
	for _, d := range deps {
		if d != "" {
			names = append(names, d)
		}
	}
	if len(names) == 0 {
		return
	}
	g.line("    depends_on:")
	for _, dep := range names {
		if isSwarm {
			g.line("      - " + dep)
			continue
		}
		g.line("      " + dep + ":")
		g.line("        condition: " + g.depCondition(dep, conds))
	}
}

// depCondition resolves the compose depends_on `condition:` for one dependency:
// an explicit override (imported app's own condition, or the synthesized pre-deploy
// gate) wins; else a one-shot pre-deploy target ⇒ service_completed_successfully; else
// a target with a healthcheck ⇒ service_healthy; else service_started. With no override
// and no predeploy target this reproduces the historical healthcheck-or-started output,
// keeping existing goldens byte-identical.
func (g *gen) depCondition(dep string, conds map[string]string) string {
	if c := conds[dep]; c != "" {
		return c
	}
	for _, s := range g.cfg.Services {
		if s.Name == dep && s.Role == "predeploy" {
			return "service_completed_successfully"
		}
	}
	if g.depHasHealthcheck(dep) {
		return "service_healthy"
	}
	return "service_started"
}

// depHasHealthcheck reports whether a dependency (an app service or a managed dep)
// defines a healthcheck, so compose depends_on can wait for service_healthy.
func (g *gen) depHasHealthcheck(name string) bool {
	for _, s := range g.cfg.Services {
		if s.Name == name {
			return s.Healthcheck != ""
		}
	}
	switch name {
	case "postgres", "mysql", "mariadb", "mongodb", "opensearch", "redis":
		return true // these managed deps carry a healthcheck (see buildManagedDeps)
	}
	// minio (and its mc-init) intentionally have NO healthcheck — see buildManagedDeps.
	return false
}

// serviceImageRef resolves a service's `image:` value from its source.
func serviceImageRef(svc Service, rp, registry, tag string) string {
	// localTag builds {registry}/{rp}-{name}:{tag}, omitting the registry prefix when
	// empty (a leading slash is an invalid image reference — local-only builds).
	localTag := func(name string) string {
		ref := rp + "-" + name + ":" + tag
		if registry != "" {
			ref = registry + "/" + ref
		}
		return ref
	}
	switch {
	case svc.ImageFrom != "":
		return "${" + imageEnvVar(svc.ImageFrom) + ":-" + localTag(svc.ImageFrom) + "}"
	case svc.Build != nil:
		return "${" + imageEnvVar(svc.Name) + ":-" + localTag(svc.Name) + "}"
	default:
		if svc.Tag != "" {
			return svc.Image + ":" + svc.Tag
		}
		return svc.Image
	}
}

// serviceLabel is the short descriptor in a service's section comment.
func serviceLabel(svc Service) string {
	switch {
	case svc.ImageFrom != "":
		return "reuses " + svc.ImageFrom
	case svc.Build != nil:
		return "build"
	default:
		if svc.Tag != "" {
			return svc.Image + ":" + svc.Tag
		}
		return svc.Image
	}
}

// imageEnvVar maps a service name to its image-override env var (api → API_IMAGE).
func imageEnvVar(name string) string {
	return strings.ReplaceAll(strings.ToUpper(name), "-", "_") + "_IMAGE"
}

func portOr(p, def string) string {
	if p == "" {
		return def
	}
	return p
}

// ── Managed dependencies (db / redis / garage) — env toggles ─────────────────────

// Managed dependencies are project-level (consistent across envs); only DBExternal
// stays per-env. These getters prefer the project value and fall back to the env's
// legacy field, so configs written before the move generate identical compose.

// dbEngine returns the active database engine (project-level, else legacy per-env).
func (g *gen) dbEngine() string {
	if g.cfg.Project.Database != "" {
		return g.cfg.Project.Database
	}
	return g.e.Database
}

// redisOn reports whether Redis is enabled (project OR legacy env).
func (g *gen) redisOn() bool { return g.cfg.Project.Redis || g.e.RedisEnabled }

// webSQLOn / storageUIOn resolve the per-env TRI-STATE override of the project-level
// dev/admin sidecar defaults: the env override wins when set (non-nil), else the
// project default. Lets Adminer / the MinIO console run in dev/stage but not prod.
func (g *gen) webSQLOn() bool {
	if g.e.WebSQL != nil {
		return *g.e.WebSQL
	}
	return g.cfg.Project.WebSQL
}
func (g *gen) storageUIOn() bool {
	if g.e.StorageUI != nil {
		return *g.e.StorageUI
	}
	return g.cfg.Project.StorageUI
}
func (g *gen) mailpitOn() bool {
	if g.e.Mailpit != nil {
		return *g.e.Mailpit
	}
	return g.cfg.Project.Mailpit
}

// exposeMode / authGate resolve the app-exposure model (per-env override → project
// default → baseline). Mirror wsconfig.EffExposeMode/EffAuthGate so composegen, which
// reads config.json directly, agrees with the rest of the backend. "traefik"/"none"
// baselines reproduce today's output (golden parity when unset).
func (g *gen) exposeMode() string {
	if g.e.ExposeMode != "" {
		return g.e.ExposeMode
	}
	if g.cfg.Project.ExposeMode != "" {
		return g.cfg.Project.ExposeMode
	}
	return "traefik"
}
func (g *gen) authGate() string {
	if g.e.AuthGate != "" {
		return g.e.AuthGate
	}
	if g.cfg.Project.AuthGate != "" {
		return g.cfg.Project.AuthGate
	}
	return "none"
}

// buildCloudflared synthesizes the Cloudflare Tunnel connector when this env's
// expose_mode is cloudflare_tunnel and there's a web entry to forward to. cloudflared
// is plain outbound TCP — no ports, no caps, no host networking — so it's Swarm-native
// and needs nothing published on the origin. The tunnel + public hostname + Access
// policy live in the user's Cloudflare Zero Trust dashboard; Rigger only runs the
// connector wired to the app over the env network (TUNNEL_TOKEN from .env / a Swarm
// secret). See docs/EXPOSURE_AND_REMOTE_ACCESS.md.
func (g *gen) buildCloudflared(prefix string, isSwarm bool) {
	if g.exposeMode() != "cloudflare_tunnel" || !g.hasAppWebEntry() {
		return
	}
	ver := g.cfg.version("cloudflared", "latest")
	g.line(sectionComment("Cloudflare Tunnel connector", dashService))
	g.line("  cloudflared:")
	g.line("    image: cloudflare/cloudflared:" + ver)
	g.line("    container_name: " + prefix + "_cloudflared")
	g.line("    command: tunnel --no-autoupdate run")
	g.line("    environment:")
	g.line("      - TUNNEL_TOKEN=${CF_TUNNEL_TOKEN}")
	g.managedNet(prefix, "cloudflared")
	g.deployBlock(isSwarm, "cloudflared", "1", "unless-stopped", true)
	g.line("")
}

// minioOn / localStorageOn report the active object-storage backends (project-level,
// INDEPENDENT — both may be on). New storage_local/storage_minio flags OR the legacy
// object_storage enum (back-compat). Legacy Garage flags are ignored (garage retired).
func (g *gen) minioOn() bool {
	return g.cfg.Project.StorageMinIO || g.cfg.Project.ObjectStorage == "minio"
}
func (g *gen) localStorageOn() bool {
	return g.cfg.Project.StorageLocal || g.cfg.Project.ObjectStorage == "local"
}

// storageMountPath is the container path the local persistent volume mounts at,
// defaulting to Laravel's storage dir (the common CodeCanyon case) when unset.
func (g *gen) storageMountPath() string {
	if p := g.cfg.Project.StoragePath; p != "" {
		return p
	}
	return "/var/www/html/storage"
}

// dbVersion resolves the managed DB's image tag: the project's explicit DBVersion
// (else the legacy per-env one), else the project versions map, else the catalog
// default. The catalog default matches the legacy hardcoded tag, so existing
// compose output is unchanged until a version is actually chosen.
func (g *gen) dbVersion(eng databases.Engine) string {
	if v := g.cfg.Project.DBVersion; v != "" {
		return databases.ResolveVersion(eng.ID, v)
	}
	if g.e.DBVersion != "" {
		return databases.ResolveVersion(eng.ID, g.e.DBVersion)
	}
	return g.cfg.version(eng.ID, eng.DefaultVersion)
}

// dbExternalPorts publishes the DB port on the host when DBExternal is set, so
// external clients can connect. The host port defaults to the engine's standard
// port and is overridable via DB_EXTERNAL_PORT in the env's .env.
func (g *gen) dbExternalPorts(eng databases.Engine) {
	if !g.e.DBExternal {
		return
	}
	g.line("    ports:")
	g.line(fmt.Sprintf("      - \"${DB_EXTERNAL_PORT:-%d}:%d\"", eng.Port, eng.Port))
}

func (g *gen) buildManagedDeps(prefix string, isSwarm bool) {
	c := g.cfg
	engine := g.dbEngine()
	verRedis := c.version("redis", "7-alpine")

	if engine == "postgres" {
		eng, _ := databases.Get("postgres")
		ver := g.dbVersion(eng)
		g.line(sectionComment("PostgreSQL "+ver, dashPostgres))
		g.line("  postgres:")
		g.line("    image: postgres:" + ver)
		g.line("    container_name: " + prefix + "_postgres")
		g.dbExternalPorts(eng)
		g.line("    environment:")
		g.line(g.dbEnvLine("POSTGRES_DB"))
		g.line(g.dbEnvLine("POSTGRES_USER"))
		g.line(g.dbEnvLine("POSTGRES_PASSWORD"))
		g.line("    volumes:")
		g.line("      - " + prefix + "_pg_data:/var/lib/postgresql/data")
		g.managedNet(prefix, "postgres")
		g.healthcheck("pg_isready -U ${POSTGRES_USER} -d ${POSTGRES_DB}", "10s", "5s", "5", "30s", "")
		g.deployBlock(isSwarm, "postgres", "1", "unless-stopped", true) // managed stateful — single-instance
		g.line("")
	}

	// MySQL and MariaDB share the MYSQL_* env contract + /var/lib/mysql volume; they
	// differ only in image, the native-password command (MySQL-only), the data
	// volume name, the container/alias name, and the healthcheck client.
	if engine == "mysql" || engine == "mariadb" {
		eng, _ := databases.Get(engine)
		ver := g.dbVersion(eng)
		g.line(sectionComment(eng.Label+" "+ver, dashMySQL))
		g.line("  " + engine + ":")
		g.line("    image: " + eng.Image + ":" + ver)
		g.line("    container_name: " + prefix + "_" + engine)
		g.dbExternalPorts(eng)
		g.line("    environment:")
		g.line(g.dbEnvLine("MYSQL_DATABASE"))
		g.line(g.dbEnvLine("MYSQL_USER"))
		g.line(g.dbEnvLine("MYSQL_PASSWORD"))
		g.line(g.dbEnvLine("MYSQL_ROOT_PASSWORD"))
		if engine == "mysql" {
			g.line("    command: --default-authentication-plugin=mysql_native_password")
		}
		g.line("    volumes:")
		g.line("      - " + prefix + "_" + engine + "_data:/var/lib/mysql")
		g.managedNet(prefix, engine)
		if engine == "mariadb" {
			// Newer MariaDB images ship mariadb-admin and may drop the mysqladmin symlink.
			g.healthcheck("mariadb-admin ping -h localhost --silent 2>/dev/null || mysqladmin ping -h localhost --silent", "10s", "5s", "5", "30s", "")
		} else {
			g.healthcheck("mysqladmin ping -h localhost --silent", "10s", "5s", "5", "30s", "")
		}
		g.deployBlock(isSwarm, engine, "1", "unless-stopped", true) // managed stateful — single-instance
		g.line("")
	}

	// MongoDB — document store. Init env keys (MONGO_INITDB_ROOT_*) differ from the
	// .env connection keys (MONGO_USER/PASSWORD/DB), so they're mapped explicitly
	// rather than via dbEnvLine. The root user IS the app connection identity
	// (no separate app-user provisioning in this first cut); it authenticates
	// against the admin database (authSource=admin in the URI envgen writes).
	if engine == "mongodb" {
		eng, _ := databases.Get("mongodb")
		ver := g.dbVersion(eng)
		g.line(sectionComment("MongoDB "+ver, dashMongo))
		g.line("  mongodb:")
		g.line("    image: mongo:" + ver)
		g.line("    container_name: " + prefix + "_mongodb")
		g.dbExternalPorts(eng)
		g.line("    environment:")
		g.line("      MONGO_INITDB_ROOT_USERNAME: ${MONGO_USER}")
		g.line("      MONGO_INITDB_ROOT_PASSWORD: ${MONGO_PASSWORD}")
		g.line("      MONGO_INITDB_DATABASE: ${MONGO_DB}")
		g.line("    volumes:")
		g.line("      - " + prefix + "_mongodb_data:/data/db")
		g.managedNet(prefix, "mongodb")
		// mongosh ships in 6+, the legacy `mongo` shell in 5 — try both so the probe
		// works across selectable versions.
		g.healthcheck("mongosh --quiet --eval \"db.adminCommand('ping').ok\" | grep -q 1 || mongo --quiet --eval \"db.adminCommand('ping').ok\" | grep -q 1", "10s", "5s", "5", "40s", "")
		g.deployBlock(isSwarm, "mongodb", "1", "unless-stopped", true) // managed stateful — single-instance
		g.line("")
	}

	// OpenSearch — search engine. Single-node with the security plugin ON: it serves
	// HTTPS on 9200 with a self-signed demo cert; the bootstrap admin user is `admin`
	// with OPENSEARCH_INITIAL_ADMIN_PASSWORD. A JVM heap floor is set; the host must
	// also have vm.max_map_count=262144 (documented in the console — not settable here).
	if engine == "opensearch" {
		eng, _ := databases.Get("opensearch")
		ver := g.dbVersion(eng)
		g.line(sectionComment("OpenSearch "+ver, dashSearch))
		g.line("  opensearch:")
		g.line("    image: opensearchproject/opensearch:" + ver)
		g.line("    container_name: " + prefix + "_opensearch")
		g.dbExternalPorts(eng)
		g.line("    environment:")
		g.line("      discovery.type: single-node")
		g.line("      OPENSEARCH_INITIAL_ADMIN_PASSWORD: ${OPENSEARCH_PASSWORD}")
		g.line("      OPENSEARCH_JAVA_OPTS: -Xms512m -Xmx512m")
		g.line("    ulimits:")
		g.line("      memlock: { soft: -1, hard: -1 }")
		g.line("      nofile: { soft: 65536, hard: 65536 }")
		g.line("    volumes:")
		g.line("      - " + prefix + "_opensearch_data:/usr/share/opensearch/data")
		g.managedNet(prefix, "opensearch")
		g.healthcheck("curl -ksf -u admin:${OPENSEARCH_PASSWORD} https://localhost:9200/_cluster/health || exit 1", "15s", "10s", "10", "60s", "")
		g.deployBlock(isSwarm, "opensearch", "1", "unless-stopped", true) // managed stateful — single-instance
		g.line("")
	}

	// VictoriaMetrics — time-series DB. Single-node, no auth on :8428. The image is
	// FROM scratch (no shell/curl), so NO healthcheck is emitted (depHasHealthcheck
	// returns false → dependents use service_started, like MinIO). Data persists in a
	// named volume via -storageDataPath.
	if engine == "victoriametrics" {
		eng, _ := databases.Get("victoriametrics")
		ver := g.dbVersion(eng)
		g.line(sectionComment("VictoriaMetrics "+ver, dashTSDB))
		g.line("  victoriametrics:")
		g.line("    image: victoriametrics/victoria-metrics:" + ver)
		g.line("    container_name: " + prefix + "_victoriametrics")
		g.line("    command: -storageDataPath=/victoria-metrics-data -retentionPeriod=1")
		g.dbExternalPorts(eng)
		g.line("    volumes:")
		g.line("      - " + prefix + "_victoriametrics_data:/victoria-metrics-data")
		g.managedNet(prefix, "victoriametrics")
		g.deployBlock(isSwarm, "victoriametrics", "1", "unless-stopped", true) // managed stateful — single-instance
		g.line("")
	}

	if g.redisOn() {
		g.line(sectionComment("Redis "+verRedis, dashRedis))
		g.line("  redis:")
		g.line("    image: redis:" + verRedis)
		g.line("    container_name: " + prefix + "_redis")
		g.line("    command: [\"redis-server\", \"--appendonly\", \"yes\"]")
		g.line("    volumes:")
		g.line("      - " + prefix + "_redis_data:/data")
		g.managedNet(prefix, "redis")
		g.healthcheck("redis-cli ping | grep -q PONG || exit 1", "10s", "3s", "3", "10s", "")
		g.deployBlock(isSwarm, "redis", "1", "unless-stopped", true) // managed stateful — single-instance
		g.line("")
	}

	if g.minioOn() {
		verMinIO := c.version("minio", "latest")
		verMC := c.version("minio_mc", "latest")
		g.line(sectionComment("MinIO "+verMinIO+" (S3-compatible)", dashStorage))
		g.line("  minio:")
		g.line("    image: minio/minio:" + verMinIO)
		g.line("    container_name: " + prefix + "_minio")
		// S3 API on :9000, built-in console on :9001 (the rich admin UI is the opt-in
		// opens3/console sidecar; this stock one is fine for a quick look).
		g.line("    command: server /data --console-address \":9001\"")
		g.line("    environment:")
		g.line(g.dbEnvLine("MINIO_ROOT_USER"))
		g.line(g.dbEnvLine("MINIO_ROOT_PASSWORD"))
		g.line("    volumes:")
		g.line("      - " + prefix + "_minio_data:/data")
		g.managedNet(prefix, "minio")
		// No Docker healthcheck: the minio image is distroless-ish (no curl/shell), so a
		// CMD-SHELL probe would fail and Traefik would drop it. A container with NO
		// healthcheck reads as healthy; the mc-init below retry-loops until MinIO is up.
		g.deployBlock(isSwarm, "minio", "1", "unless-stopped", true) // managed stateful — single-instance
		g.line("")

		// One-shot bucket init: wait for MinIO, then create the app's bucket (idempotent).
		// minio/mc ships /bin/sh; $$VAR keeps the .env values for the SHELL (compose would
		// otherwise interpolate $VAR at parse time). restart:"no" so it runs once and exits.
		g.line(sectionComment("MinIO bucket init (one-shot)", dashStorage))
		g.line("  minio_init:")
		g.line("    image: minio/mc:" + verMC)
		g.line("    container_name: " + prefix + "_minio_init")
		g.emitDependsOn(prefix, []string{"minio"}, nil, isSwarm)
		g.managedNet(prefix, "minio_init")
		g.line("    env_file: .env")
		// Use the BARE service name "minio" (a valid hostname) — NOT {prefix}_minio:
		// mc/S3 validate the hostname and reject underscores ("Invalid Request (invalid
		// hostname)"). The bare name is unique within the project's own network.
		g.line("    entrypoint: [\"/bin/sh\", \"-c\", \"until mc alias set rigger http://minio:9000 \\\"$$MINIO_ROOT_USER\\\" \\\"$$MINIO_ROOT_PASSWORD\\\"; do echo 'waiting for minio...'; sleep 2; done; mc mb --ignore-existing rigger/\\\"$$MINIO_BUCKET\\\"; echo 'bucket ready'; exit 0\"]")
		g.line("    restart: \"no\"")
		g.line("")
	}
}

// ── Service links ─────────────────────────────────────────────────────────────────

// serviceEnv merges a service's static EnvVars with its resolved service-link URLs
// into one string map. A link wins over an env_vars key of the same name, so the
// caller emits a single sorted environment: block with no duplicate (invalid) keys.
//
// A service's inline env_vars must NOT shadow a key Rigger MANAGES for an active managed
// dependency (DATABASE_URL, the POSTGRES_*/MYSQL_*/MONGO_*/REDIS_*/MINIO_* families, …):
// compose's environment: overrides env_file:, so a value an imported compose hardcoded
// (e.g. a repo's dev-default DATABASE_URL=postgres://qrhub:qrhub@db/qrhub) would otherwise
// shadow the correct managed credentials Rigger writes to .env, breaking DB auth. Such keys
// are dropped here so the authoritative .env value (via env_file) wins. Explicit service
// LINKS are intentional wiring and still apply.
func (g *gen) serviceEnv(prefix string, svc Service) map[string]string {
	managed := g.managedEnvKeys()
	out := make(map[string]string, len(svc.EnvVars)+len(svc.Links))
	for k, v := range svc.EnvVars {
		if managed[k] {
			continue // Rigger owns this key for an active managed dep — let .env win
		}
		out[k] = string(v)
	}
	for k, v := range g.resolveLinks(prefix, svc) {
		out[k] = v
	}
	return out
}

// managedEnvKeys is the set of connection/credential env keys Rigger writes
// authoritatively into .env for the active managed dependencies, so a service's inline
// env_vars can't shadow them (see serviceEnv). Mirrors the relevant families of
// envgen.managedContractKeys, gated on the dependency being active.
func (g *gen) managedEnvKeys() map[string]bool {
	keys := map[string]bool{}
	add := func(ks ...string) {
		for _, k := range ks {
			keys[k] = true
		}
	}
	if eng := g.dbEngine(); eng != "" && eng != "none" {
		add("DATABASE", "DATABASE_URL", "DB_EXTERNAL_PORT",
			"MYSQL_HOST", "MYSQL_PORT", "MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD", "MYSQL_ROOT_PASSWORD",
			"POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD",
			"MONGO_HOST", "MONGO_PORT", "MONGO_DB", "MONGO_USER", "MONGO_PASSWORD", "MONGO_URI")
	}
	if g.redisOn() {
		add("REDIS_ENABLED", "REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD", "REDIS_URL")
	}
	if g.minioOn() {
		add("MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD", "MINIO_BUCKET", "MINIO_ENDPOINT", "MINIO_REGION",
			"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_DEFAULT_REGION", "AWS_BUCKET", "AWS_ENDPOINT", "AWS_USE_PATH_STYLE_ENDPOINT")
	}
	return keys
}

// resolveLinks builds the env-var → URL map a service's links emit. The host is the
// fully prefixed {prefix}_{target} network alias (resolvable for both app and
// managed services — see managedNet / buildService aliases). The port defaults to
// the target service's own Port, or the managed-dep default when the target has no
// Service entry; if neither is known the :port segment is omitted. Links with an
// empty EnvVar or Service are skipped.
func (g *gen) resolveLinks(prefix string, svc Service) map[string]string {
	if len(svc.Links) == 0 {
		return nil
	}
	out := make(map[string]string, len(svc.Links))
	for _, ln := range svc.Links {
		if ln.EnvVar == "" || ln.Service == "" {
			continue
		}
		scheme := ln.Scheme
		if scheme == "" {
			scheme = "http"
		}
		port := ln.Port
		if port == "" {
			port = g.targetPort(ln.Service)
		}
		url := scheme + "://" + prefix + "_" + ln.Service
		if port != "" {
			url += ":" + port
		}
		url += ln.Path
		out[ln.EnvVar] = url
	}
	return out
}

// targetPort returns the in-network port for a link target: the matching app
// service's Port, else the managed-dependency default (managed deps have no Service
// entry — they're synthesized in buildManagedDeps).
func (g *gen) targetPort(name string) string {
	for _, s := range g.cfg.Services {
		if s.Name == name {
			return string(s.Port)
		}
	}
	return managedDepPort(name)
}

// managedDepPort maps a managed-dependency / Adminer target name to its in-network
// port, for resolving a service link whose target has no Service entry.
func managedDepPort(name string) string {
	switch name {
	case "postgres":
		return "5432"
	case "mysql", "mariadb":
		return "3306"
	case "mongodb":
		return "27017"
	case "opensearch":
		return "9200"
	case "victoriametrics":
		return "8428"
	case "redis":
		return "6379"
	case "minio":
		return "9000"
	case "adminer":
		return "8080"
	}
	return ""
}

// ── small helpers ────────────────────────────────────────────────────────────────

// managedNet writes a managed service's network block. Compose already makes the
// service reachable by its bare name; we add the fully prefixed {prefix}_{name}
// alias so the in-network host Rigger advertises in .env (POSTGRES_HOST,
// GARAGE_HOST, …) and uses in cross-service URLs actually resolves.
func (g *gen) managedNet(prefix, name string) {
	g.line("    networks:")
	g.line("      " + prefix + "_net:")
	g.line("        aliases:")
	g.line("          - " + prefix + "_" + name)
}

// volHost returns the host part of a volume spec (everything before the first colon).
func volHost(vol string) string {
	if i := strings.Index(vol, ":"); i >= 0 {
		return vol[:i]
	}
	return vol
}

// volRest returns the container part of a volume spec (after the first colon).
func volRest(vol string) string {
	if i := strings.Index(vol, ":"); i >= 0 {
		return vol[i+1:]
	}
	return ""
}

// isNamedVolume reports whether a volume host part is a named volume (not a bind
// mount path or env-var path).
func isNamedVolume(host string) bool {
	return !strings.HasPrefix(host, ".") && !strings.HasPrefix(host, "/") && !strings.HasPrefix(host, "$")
}

// bindSource rewrites a RELATIVE host bind-mount source so the host Docker daemon
// can resolve it. Rigger runs `docker compose` inside its own container, but the
// daemon resolves bind sources against the HOST filesystem — a Rigger-local path
// like /toolkit/workspaces/… doesn't exist there, so the daemon silently creates an
// empty dir and the mount fails. We root the source at ${RIGGER_BIND_ROOT}, which
// the deploy layer sets to the env dir's host-visible absolute path (see
// Bridge.hostBindRoot); it is left unset for a non-containerised or remote Rigger,
// where the default `.` resolves correctly against the compose project dir.
//
// For a source-backed project (a scanned git repo OR an uploaded archive) the
// referenced files live in the env checkout at envs/{env}/_src, so a relative source
// ("./mosquitto/mosquitto.conf") is re-rooted there
// ("${RIGGER_BIND_ROOT:-.}/_src/mosquitto/mosquitto.conf").
func (g *gen) bindSource(host string) string {
	rel := strings.TrimPrefix(host, "./")
	base := "${RIGGER_BIND_ROOT:-.}"
	if g.cfg.Project.GitRepo != "" || g.cfg.Project.SourceKind == "upload" {
		return base + "/_src/" + rel
	}
	return base + "/" + rel
}

func strOr(f flexStr, def string) string {
	if string(f) == "" {
		return def
	}
	return string(f)
}
