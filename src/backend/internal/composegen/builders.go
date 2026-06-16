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
	dashService     = 50
	dashPostgres    = 48
	dashMySQL       = 54
	dashRedis       = 54
	dashGarage      = 38
)

// buildStack emits the volumes + services blocks for the unified service graph:
// every app service in cfg.Services, then the managed-dependency toggles
// (database / redis / garage) which stay as env flags until Phase 3.
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
	g.buildGarageWebUI(prefix, rp, registry, tag, isSwarm)
}

// buildAdminer synthesizes the Adminer web-SQL service from the project-level
// web_sql flag — the unified representation, emitted for ANY stack that has a
// database. Reuses buildService so its routing/ports/networks/env match an app
// service: apex web entry on a pure database-hosting project (no app service routes),
// else the "adminer" subdomain so it coexists with the app's web entry. Skipped when
// a literal "adminer" service already exists in Services (legacy projects — that
// service already rendered), avoiding a duplicate (invalid) compose key.
func (g *gen) buildAdminer(prefix, rp, registry, tag string, isSwarm bool) {
	if !g.cfg.Project.WebSQL || g.hasService("adminer") {
		return
	}
	engine := g.dbEngine()
	if engine == "" || engine == "none" {
		return // Adminer needs a database to connect to
	}
	svc := Service{
		Name:      "adminer",
		Image:     "adminer",
		Tag:       "4.8.1", // PINNED — 5.x changed the plugin surface the login PHP targets
		Port:      "8080",  // Adminer's native HTTP port (Traefik / host-port target)
		WebRouted: true,
		HostPort:  "8978", // host publish for the no-Traefik case (8080 would clash with Rigger)
		EnvFile:   true,   // inject DB creds + ADMINER_LOGIN_SECRET from .env
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

// buildGarageWebUI synthesizes the optional Garage web admin UI as a Service routed
// through Traefik (subdomain "garage"), reusing buildService so it gets the same
// routing/ports/networks as Adminer. It's a root-mounted SPA, so it's ALWAYS a
// subdomain (never apex / never a stripped path). With Traefik off it falls back to
// publishing host port 3909 (dev-only). Gated on the project's GarageWebUI flag +
// Garage being enabled. See [[garage-webui-optional]].
func (g *gen) buildGarageWebUI(prefix, rp, registry, tag string, isSwarm bool) {
	if !g.cfg.Project.GarageWebUI || !g.garageOn() {
		return
	}
	ver := g.cfg.version("garage_webui", "latest")
	svc := Service{
		Name:      "garage_webui",
		Image:     "khairul169/garage-webui",
		Tag:       ver,
		Port:      "3909", // the UI's listen port (Traefik / host-port target)
		WebRouted: true,
		Subdomain: "garage", // always a subdomain — the SPA assumes it's served at /
		HostPort:  "3909",   // host publish only when Traefik is off (no collisions under Traefik)
		DependsOn: []string{"garage"},
		Volumes:   []string{"${RIGGER_BIND_ROOT:-.}/garage.toml:/etc/garage.toml:ro"},
		// khairul169/garage-webui contract: admin API (3903) + key, S3 endpoint (3900);
		// region matches the generated garage.toml ([s3_api] s3_region="garage").
		EnvVars: map[string]flexStr{
			"API_BASE_URL":    flexStr("http://" + prefix + "_garage:3903"),
			"API_ADMIN_KEY":   flexStr("${GARAGE_ADMIN_TOKEN}"),
			"S3_ENDPOINT_URL": flexStr("http://" + prefix + "_garage:3900"),
			"S3_REGION":       flexStr("garage"),
		},
		// Admin UI → eligible for the per-env basic-auth middleware (Slice 2).
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
	if g.redisOn() {
		add(prefix + "_redis_data")
	}
	if g.garageOn() {
		add(prefix + "_garage_data")
		add(prefix + "_garage_meta")
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
	g.line("    container_name: " + cname)
	if svc.Command != "" {
		// Single-quoted YAML scalar; escape embedded single quotes ('' is the YAML
		// escape) so commands like `sh -c 'echo hi'` stay valid.
		g.line("    command: '" + strings.ReplaceAll(svc.Command, "'", "''") + "'")
	}
	if svc.EnvFile {
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
	if svc.WebRouted && e.TraefikEnabled {
		g.line("      " + e.TraefikNetwork + ": {}")
	}

	g.emitDependsOn(prefix, svc.DependsOn, isSwarm)

	// Volumes (named volumes get the prefix; bind/env-var mounts pass through).
	firstVol := true
	for _, vol := range svc.Volumes {
		if vol == "" {
			continue
		}
		if firstVol {
			g.line("    volumes:")
			firstVol = false
		}
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
	// Optionally materialise the env's generated .env as a physical file in the
	// app's workdir. Some frameworks re-read .env from disk and ignore process
	// env — notably Laravel's `php artisan serve`, whose request subprocess only
	// sees keys present in a .env file. The vars are always injected as process
	// env via env_file; this also delivers them on disk (read-only) via a compose
	// `config` carrying the .env content inline. Inline content (not a host bind)
	// because Rigger runs in a container and the host daemon can't resolve a
	// Rigger-side bind path. Skipped when no .env content was supplied.
	if svc.EnvFileMount != "" && g.envFile != "" {
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

	g.deployBlock(isSwarm, svc.Name, string(svc.Replicas), restart)

	// extra_compose: service-level then env-level override.
	g.emitExtraCompose(svc.ExtraCompose)
	if ov, ok := e.ServiceOverrides[svc.Name]; ok {
		g.emitExtraCompose(ov.ExtraCompose)
	}
	g.line("")
}

// emitServicePorts handles web routing and port publishing, emitting at most ONE
// `ports:` block (duplicate keys are invalid YAML). A web-routed apex service under
// Traefik gets labels (no host port); without Traefik it binds a single host port —
// the service's own host_port if set, else the env HTTP port (so editing the
// service port just changes the published port, not adds a second one). Non-web
// services publish host_port/extra_ports, or expose the container port.
func (g *gen) emitServicePorts(router string, svc Service) {
	e := g.e
	port := string(svc.Port)
	var publishes []string
	switch {
	case svc.WebRouted && e.TraefikEnabled:
		host := e.Domain
		if svc.Subdomain != "" && e.Domain != "" {
			host = svc.Subdomain + "." + e.Domain
		}
		// Admin sidecars (Adminer / Garage UI) get a basic-auth middleware when this
		// env opts into protection; real app services never do.
		// Wildcard cert: only the apex web entry (no subdomain) requests *.{base};
		// sidecars on deeper subdomains fall back to per-host issuance via the same resolver.
		wildcard := ""
		if e.wildcardBase != "" && svc.Subdomain == "" {
			wildcard = e.wildcardBase
		}
		g.traefikLabels(router, host, port, svc.AuthProtect && e.ProtectAdminUIs, e.certResolver, wildcard)
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
	if !svc.WebRouted && port != "" {
		g.line("    expose:")
		g.line("      - \"" + port + "\"")
	}
}

// emitDependsOn emits the depends_on block keyed by the bare service name (service
// keys are short now — the project/stack namespaces them). Compose uses the
// condition form (service_healthy when the target has a healthcheck, else
// service_started); swarm uses the bare list form.
func (g *gen) emitDependsOn(prefix string, deps []string, isSwarm bool) {
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
		if g.depHasHealthcheck(dep) {
			g.line("        condition: service_healthy")
		} else {
			g.line("        condition: service_started")
		}
	}
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
	case "postgres", "mysql", "mariadb", "redis", "garage":
		return true // managed deps always carry a healthcheck (see buildManagedDeps)
	}
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

// redisOn / garageOn report whether the dependency is enabled (project OR legacy env).
func (g *gen) redisOn() bool  { return g.cfg.Project.Redis || g.e.RedisEnabled }
func (g *gen) garageOn() bool { return g.cfg.Project.Garage || g.e.GarageEnabled }

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
	verGarage := c.version("garage", "v1.0.1")

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
		g.deployBlock(isSwarm, "postgres", "1", "unless-stopped")
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
		g.deployBlock(isSwarm, engine, "1", "unless-stopped")
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
		g.deployBlock(isSwarm, "redis", "1", "unless-stopped")
		g.line("")
	}

	if g.garageOn() {
		g.line(sectionComment("Garage "+verGarage+" (S3-compatible)", dashGarage))
		g.line("  garage:")
		g.line("    image: dxflrs/garage:" + verGarage)
		g.line("    container_name: " + prefix + "_garage")
		g.line("    volumes:")
		g.line("      - " + prefix + "_garage_data:/data")
		g.line("      - " + prefix + "_garage_meta:/meta")
		// garage.toml is Rigger-generated in the env dir root (not _src), so root it
		// at ${RIGGER_BIND_ROOT} directly so the host daemon can resolve it.
		g.line("      - ${RIGGER_BIND_ROOT:-.}/garage.toml:/etc/garage.toml:ro")
		g.line("    environment:")
		g.line(g.dbEnvLine("GARAGE_ADMIN_TOKEN"))
		g.managedNet(prefix, "garage")
		g.healthcheck("curl -sf http://localhost:3903/health -o /dev/null || exit 1", "30s", "5s", "3", "60s", "")
		g.deployBlock(isSwarm, "garage", "1", "unless-stopped")
		g.line("")
	}
}

// ── Service links ─────────────────────────────────────────────────────────────────

// serviceEnv merges a service's static EnvVars with its resolved service-link URLs
// into one string map. A link wins over an env_vars key of the same name, so the
// caller emits a single sorted environment: block with no duplicate (invalid) keys.
func (g *gen) serviceEnv(prefix string, svc Service) map[string]string {
	out := make(map[string]string, len(svc.EnvVars)+len(svc.Links))
	for k, v := range svc.EnvVars {
		out[k] = string(v)
	}
	for k, v := range g.resolveLinks(prefix, svc) {
		out[k] = v
	}
	return out
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
	case "redis":
		return "6379"
	case "garage":
		return "3900"
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
