package composegen

import "strings"

// Trailing dash counts for section comments. App services use a fixed width
// (no historical parity to match — the unified model regenerates goldens); the
// managed-dependency widths are kept from the old output for tidy diffs.
const (
	dashService     = 50
	dashPostgres    = 48
	dashMySQL       = 54
	dashRedis       = 54
	dashGarage      = 38
	dashGarageWebUI = 61
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
}

// emitVolumes writes the top-level volumes block: named volumes referenced by
// services, the managed-dependency volumes (toggle-driven), and explicit
// named_volumes[]. Bind mounts and env-var paths are skipped.
func (g *gen) emitVolumes(prefix string) {
	c, e := g.cfg, g.e
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
	if e.Database == "postgres" {
		add(prefix + "_pg_data")
	}
	if e.Database == "mysql" {
		add(prefix + "_mysql_data")
	}
	if e.RedisEnabled {
		add(prefix + "_redis_data")
	}
	if e.GarageEnabled {
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
	key := prefix + "_" + svc.Name
	restart := svc.Restart
	if restart == "" {
		restart = "unless-stopped"
	}

	g.line(sectionComment(svc.Name+" ("+serviceLabel(svc)+")", dashService))
	g.line("  " + key + ":")
	g.line("    image: " + serviceImageRef(svc, rp, registry, tag))
	if svc.Command != "" {
		g.line("    command: '" + svc.Command + "'")
	}
	if svc.EnvFile {
		g.line("    env_file: .env")
	}

	// Networks — long form with a short-name alias; join the Traefik network when
	// this service is the web entry under Traefik.
	g.line("    networks:")
	g.line("      " + prefix + "_net:")
	g.line("        aliases:")
	g.line("          - " + svc.Name)
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
		} else {
			g.line("      - " + vol)
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

	// Environment (keys sorted for deterministic output).
	if keys := sortedKeys(svc.EnvVars); len(keys) > 0 {
		g.line("    environment:")
		for _, k := range keys {
			g.line("      - " + k + "=" + string(svc.EnvVars[k]))
		}
	}

	g.emitServicePorts(key, svc)

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

// emitServicePorts handles web routing and port publishing. A web-routed service
// gets Traefik labels (when Traefik is on) or binds the env HTTP port; otherwise
// host_port/extra_ports are published, or the container port is exposed.
func (g *gen) emitServicePorts(key string, svc Service) {
	e := g.e
	port := string(svc.Port)
	if svc.WebRouted {
		if e.TraefikEnabled {
			host := e.Domain
			if svc.Subdomain != "" && e.Domain != "" {
				host = svc.Subdomain + "." + e.Domain
			}
			g.traefikLabels(key, host, port)
		} else if svc.Subdomain == "" && string(e.HTTPPort) != "" {
			// Apex web service without Traefik binds the env HTTP port. Subdomain
			// services need Traefik, so without it they aren't published here.
			g.portMapping(string(e.HTTPPort), portOr(port, "80"))
		}
	}
	var publishes []string
	if string(svc.HostPort) != "" && port != "" {
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

// emitDependsOn resolves short dependency names to {prefix}_{name} and emits the
// block: compose uses condition (service_healthy when the target has a
// healthcheck, else service_started); swarm uses the bare list form.
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
			g.line("      - " + prefix + "_" + dep)
			continue
		}
		g.line("      " + prefix + "_" + dep + ":")
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
	case "postgres", "mysql", "redis", "garage":
		return true // managed deps always carry a healthcheck (see buildManagedDeps)
	}
	return false
}

// serviceImageRef resolves a service's `image:` value from its source.
func serviceImageRef(svc Service, rp, registry, tag string) string {
	switch {
	case svc.ImageFrom != "":
		return "${" + imageEnvVar(svc.ImageFrom) + ":-" + registry + "/" + rp + "-" + svc.ImageFrom + ":" + tag + "}"
	case svc.Build != nil:
		return "${" + imageEnvVar(svc.Name) + ":-" + registry + "/" + rp + "-" + svc.Name + ":" + tag + "}"
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

// ── Managed dependencies (db / redis / garage) — env toggles until Phase 3 ───────

func (g *gen) buildManagedDeps(prefix string, isSwarm bool) {
	c, e := g.cfg, g.e
	verPostgres := c.version("postgres", "15-alpine")
	verMySQL := c.version("mysql", "8.0")
	verRedis := c.version("redis", "7-alpine")
	verGarage := c.version("garage", "v1.0.1")
	verGarageWebUI := c.version("garage_webui", "latest")

	if e.Database == "postgres" {
		g.line(sectionComment("PostgreSQL "+verPostgres, dashPostgres))
		g.line("  " + prefix + "_postgres:")
		g.line("    image: postgres:" + verPostgres)
		g.line("    container_name: " + prefix + "_postgres")
		g.line("    environment:")
		g.line(g.dbEnvLine("POSTGRES_DB"))
		g.line(g.dbEnvLine("POSTGRES_USER"))
		g.line(g.dbEnvLine("POSTGRES_PASSWORD"))
		g.line("    volumes:")
		g.line("      - " + prefix + "_pg_data:/var/lib/postgresql/data")
		g.line("    networks:")
		g.line("      - " + prefix + "_net")
		g.healthcheck("pg_isready -U ${POSTGRES_USER} -d ${POSTGRES_DB}", "10s", "5s", "5", "30s", "")
		g.deployBlock(isSwarm, "postgres", "1", "unless-stopped")
		g.line("")
	}

	if e.Database == "mysql" {
		g.line(sectionComment("MySQL "+verMySQL, dashMySQL))
		g.line("  " + prefix + "_mysql:")
		g.line("    image: mysql:" + verMySQL)
		g.line("    container_name: " + prefix + "_mysql")
		g.line("    environment:")
		g.line(g.dbEnvLine("MYSQL_DATABASE"))
		g.line(g.dbEnvLine("MYSQL_USER"))
		g.line(g.dbEnvLine("MYSQL_PASSWORD"))
		g.line(g.dbEnvLine("MYSQL_ROOT_PASSWORD"))
		g.line("    command: --default-authentication-plugin=mysql_native_password")
		g.line("    volumes:")
		g.line("      - " + prefix + "_mysql_data:/var/lib/mysql")
		g.line("    networks:")
		g.line("      - " + prefix + "_net")
		g.healthcheck("mysqladmin ping -h localhost --silent", "10s", "5s", "5", "30s", "")
		g.deployBlock(isSwarm, "mysql", "1", "unless-stopped")
		g.line("")
	}

	if e.RedisEnabled {
		g.line(sectionComment("Redis "+verRedis, dashRedis))
		g.line("  " + prefix + "_redis:")
		g.line("    image: redis:" + verRedis)
		g.line("    container_name: " + prefix + "_redis")
		g.line("    command: [\"redis-server\", \"--appendonly\", \"yes\"]")
		g.line("    volumes:")
		g.line("      - " + prefix + "_redis_data:/data")
		g.line("    networks:")
		g.line("      - " + prefix + "_net")
		g.healthcheck("redis-cli ping | grep -q PONG || exit 1", "10s", "3s", "3", "10s", "")
		g.deployBlock(isSwarm, "redis", "1", "unless-stopped")
		g.line("")
	}

	if e.GarageEnabled {
		g.line(sectionComment("Garage "+verGarage+" (S3-compatible)", dashGarage))
		g.line("  " + prefix + "_garage:")
		g.line("    image: dxflrs/garage:" + verGarage)
		g.line("    container_name: " + prefix + "_garage")
		g.line("    volumes:")
		g.line("      - " + prefix + "_garage_data:/data")
		g.line("      - " + prefix + "_garage_meta:/meta")
		g.line("      - ./garage.toml:/etc/garage.toml:ro")
		g.line("    environment:")
		g.line(g.dbEnvLine("GARAGE_ADMIN_TOKEN"))
		g.line("    networks:")
		g.line("      - " + prefix + "_net")
		g.healthcheck("curl -sf http://localhost:3903/health -o /dev/null || exit 1", "30s", "5s", "3", "60s", "")
		g.deployBlock(isSwarm, "garage", "1", "unless-stopped")
		g.line("")

		g.line(sectionComment("Garage WebUI", dashGarageWebUI))
		g.line("  " + prefix + "_garage_webui:")
		g.line("    image: khofesh/garage-webui:" + verGarageWebUI)
		g.line("    container_name: " + prefix + "_garage_webui")
		g.line("    environment:")
		g.line("      GARAGE_API_URL: http://" + prefix + "_garage:3900")
		g.line("      GARAGE_API_TOKEN: ${GARAGE_ADMIN_TOKEN}")
		g.line("    depends_on:")
		g.line("      - " + prefix + "_garage")
		g.line("    networks:")
		g.line("      - " + prefix + "_net")
		g.deployBlock(isSwarm, "garage_webui", "1", "unless-stopped")
		g.line("")
	}
}

// ── small helpers ────────────────────────────────────────────────────────────────

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

func strOr(f flexStr, def string) string {
	if string(f) == "" {
		return def
	}
	return string(f)
}
