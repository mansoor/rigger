package composegen

import "strings"

// Trailing dash counts for each section comment — measured from compose-gen.sh.
const (
	dashBackend     = 54
	dashNginx       = 66
	dashPostgres    = 48
	dashMySQL       = 54
	dashRedis       = 54
	dashGarage      = 38
	dashGarageWebUI = 61
	dashFrontend    = 52
	dashImageSvc    = 41
)

// ── Custom stack ─────────────────────────────────────────────────────────────────

func (g *gen) buildCustomStack(prefix, project, registry, tag string, isSwarm bool) {
	c := g.cfg
	e := g.e
	verNginx := c.version("nginx", "1.25-alpine")
	verPostgres := c.version("postgres", "15-alpine")
	verMySQL := c.version("mysql", "8.0")
	verRedis := c.version("redis", "7-alpine")
	verGarage := c.version("garage", "v1.0.1")
	verGarageWebUI := c.version("garage_webui", "latest")

	// ── Volumes ──
	g.line("volumes:")
	if e.Database == "postgres" {
		g.line("  " + prefix + "_pg_data:")
	}
	if e.Database == "mysql" {
		g.line("  " + prefix + "_mysql_data:")
	}
	if e.RedisEnabled {
		g.line("  " + prefix + "_redis_data:")
	}
	if e.GarageEnabled {
		g.line("  " + prefix + "_garage_data:")
		g.line("  " + prefix + "_garage_meta:")
	}
	g.line("  " + prefix + "_uploads:")
	g.line("")

	g.line("services:")
	g.line("")

	// ── Backend ──
	g.line(sectionComment("Backend ("+e.Backend+")", dashBackend))
	g.line("  " + prefix + "_backend:")
	g.line("    image: ${BACKEND_IMAGE:-" + registry + "/" + project + "-backend:" + tag + "}")
	g.line("    container_name: " + prefix + "_backend")
	g.line("    env_file: .env")
	g.line("    volumes:")
	g.line("      - " + prefix + "_uploads:/app/storage/uploads")
	g.line("    networks:")
	g.line("      - " + prefix + "_net")
	if e.Database == "postgres" {
		g.raw("    depends_on:\n      " + prefix + "_postgres:\n        condition: service_healthy\n")
	} else if e.Database == "mysql" {
		g.raw("    depends_on:\n      " + prefix + "_mysql:\n        condition: service_healthy\n")
	}
	if e.Backend == "nodejs" {
		g.healthcheck("wget -qO- http://localhost:3000/health >/dev/null 2>&1 || curl -sf http://localhost:3000/health >/dev/null 2>&1 || exit 1", "30s", "10s", "3", "40s", "")
	} else {
		g.healthcheck("php -r 'exit(0);' 2>/dev/null || exit 1", "30s", "5s", "3", "60s", "")
	}
	g.deployBlock(isSwarm, string(e.Replicas.Backend), "unless-stopped")
	g.line("")

	// ── Nginx ──
	g.line(sectionComment("Nginx", dashNginx))
	g.line("  " + prefix + "_nginx:")
	g.line("    image: nginx:" + verNginx)
	g.line("    container_name: " + prefix + "_nginx")
	g.line("    volumes:")
	g.line("      - ./nginx.conf:/etc/nginx/conf.d/default.conf:ro")
	g.line("      - " + prefix + "_uploads:/var/www/uploads:ro")
	g.line("    depends_on:")
	g.line("      - " + prefix + "_backend")
	g.line("    networks:")
	g.line("      - " + prefix + "_net")
	if e.TraefikEnabled {
		g.line("      - " + e.TraefikNetwork)
	}
	g.traefikLabels(prefix+"_nginx", e.Domain, "80")
	if !e.TraefikEnabled {
		g.portMapping(string(e.HTTPPort), "80")
	}
	g.healthcheck("curl -sf http://localhost/ -o /dev/null || exit 1", "30s", "5s", "3", "20s", "")
	g.deployBlock(isSwarm, "1", "unless-stopped")
	g.line("")

	// ── PostgreSQL ──
	if e.Database == "postgres" {
		g.line(sectionComment("PostgreSQL "+verPostgres, dashPostgres))
		g.line("  " + prefix + "_postgres:")
		g.line("    image: postgres:" + verPostgres)
		g.line("    container_name: " + prefix + "_postgres")
		g.line("    environment:")
		g.line("      POSTGRES_DB: ${POSTGRES_DB}")
		g.line("      POSTGRES_USER: ${POSTGRES_USER}")
		g.line("      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}")
		g.line("    volumes:")
		g.line("      - " + prefix + "_pg_data:/var/lib/postgresql/data")
		g.line("    networks:")
		g.line("      - " + prefix + "_net")
		g.healthcheck("pg_isready -U ${POSTGRES_USER} -d ${POSTGRES_DB}", "10s", "5s", "5", "30s", "")
		g.deployBlock(isSwarm, "1", "unless-stopped")
		g.line("")
	}

	// ── MySQL ──
	if e.Database == "mysql" {
		g.line(sectionComment("MySQL "+verMySQL, dashMySQL))
		g.line("  " + prefix + "_mysql:")
		g.line("    image: mysql:" + verMySQL)
		g.line("    container_name: " + prefix + "_mysql")
		g.line("    environment:")
		g.line("      MYSQL_DATABASE: ${MYSQL_DATABASE}")
		g.line("      MYSQL_USER: ${MYSQL_USER}")
		g.line("      MYSQL_PASSWORD: ${MYSQL_PASSWORD}")
		g.line("      MYSQL_ROOT_PASSWORD: ${MYSQL_ROOT_PASSWORD}")
		g.line("    command: --default-authentication-plugin=mysql_native_password")
		g.line("    volumes:")
		g.line("      - " + prefix + "_mysql_data:/var/lib/mysql")
		g.line("    networks:")
		g.line("      - " + prefix + "_net")
		g.healthcheck("mysqladmin ping -h localhost --silent", "10s", "5s", "5", "30s", "")
		g.deployBlock(isSwarm, "1", "unless-stopped")
		g.line("")
	}

	// ── Redis ──
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
		g.deployBlock(isSwarm, "1", "unless-stopped")
		g.line("")
	}

	// ── Garage (S3-compatible storage) ──
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
		g.line("      GARAGE_ADMIN_TOKEN: ${GARAGE_ADMIN_TOKEN}")
		g.line("    networks:")
		g.line("      - " + prefix + "_net")
		g.healthcheck("curl -sf http://localhost:3903/health -o /dev/null || exit 1", "30s", "5s", "3", "60s", "")
		g.deployBlock(isSwarm, "1", "unless-stopped")
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
		g.deployBlock(isSwarm, "1", "unless-stopped")
		g.line("")
	}

	// ── Frontend ──
	if e.FrontendEnabled {
		g.line(sectionComment("Frontend ("+e.Frontend+")", dashFrontend))
		g.line("  " + prefix + "_frontend:")
		g.line("    image: ${FRONTEND_IMAGE:-" + registry + "/" + project + "-frontend:" + tag + "}")
		g.line("    container_name: " + prefix + "_frontend")
		g.line("    env_file: .env")
		g.line("    networks:")
		g.line("      - " + prefix + "_net")
		if e.TraefikEnabled {
			g.line("      - " + e.TraefikNetwork)
			g.traefikLabels(prefix+"_frontend", "app."+e.Domain, "3000")
		}
		g.deployBlock(isSwarm, string(e.Replicas.Frontend), "unless-stopped")
		g.line("")
	}
}

// ── Image stack ──────────────────────────────────────────────────────────────────

func (g *gen) buildImageStack(prefix string, isSwarm bool) {
	c := g.cfg
	images := c.Images

	// Named volumes block — from image mounts, then named_volumes[].
	seen := map[string]bool{}
	hasNamedVol := false
	emit := func(vkey string) {
		if seen[vkey] {
			return
		}
		if !hasNamedVol {
			g.line("volumes:")
			hasNamedVol = true
		}
		g.line("  " + vkey + ":")
		seen[vkey] = true
	}
	for _, img := range images {
		for _, vol := range img.Volumes {
			if vol == "" {
				continue
			}
			if host := volHost(vol); isNamedVolume(host) {
				emit(prefix + "_" + host)
			}
		}
	}
	for _, nv := range c.NamedVolumes {
		if nv.Name == "" {
			continue
		}
		if !strings.HasPrefix(nv.Name, ".") && !strings.HasPrefix(nv.Name, "/") {
			emit(prefix + "_" + nv.Name)
		}
	}
	if hasNamedVol {
		g.line("")
	}

	g.line("services:")
	g.line("")

	for _, img := range images {
		g.buildImageService(prefix, img, images, isSwarm)
	}
}

func (g *gen) buildImageService(prefix string, img Image, images []Image, isSwarm bool) {
	e := g.e
	svc := img.Name
	port := string(img.Port)
	hport := string(img.HostPort)
	restart := img.Restart
	if restart == "" {
		restart = "unless-stopped"
	}

	g.line(sectionComment(svc+" ("+img.Image+":"+img.Tag+")", dashImageSvc))
	g.line("  " + prefix + "_" + svc + ":")
	g.line("    image: " + img.Image + ":" + img.Tag)
	g.line("    container_name: " + prefix + "_" + svc)
	g.line("    env_file: .env")
	if img.Command != "" {
		g.line("    command: '" + img.Command + "'")
	}

	// Networks — long-form map with alias = short service name.
	g.line("    networks:")
	g.line("      " + prefix + "_net:")
	g.line("        aliases:")
	g.line("          - " + svc)
	if hport != "" && e.TraefikEnabled {
		g.line("      " + e.TraefikNetwork + ": {}")
	}

	// depends_on — service_healthy if the dependency has a healthcheck.
	var deps []string
	for _, d := range img.DependsOn {
		if d != "" {
			deps = append(deps, d)
		}
	}
	if len(deps) > 0 {
		g.line("    depends_on:")
		for _, dep := range deps {
			depHasHC := false
			for _, di := range images {
				if di.Name == dep {
					depHasHC = di.Healthcheck != ""
					break
				}
			}
			g.line("      " + prefix + "_" + dep + ":")
			if depHasHC {
				g.line("        condition: service_healthy")
			} else {
				g.line("        condition: service_started")
			}
		}
	}

	// Volumes
	firstVol := true
	for _, vol := range img.Volumes {
		if vol == "" {
			continue
		}
		if firstVol {
			g.line("    volumes:")
			firstVol = false
		}
		host := volHost(vol)
		if isNamedVolume(host) {
			g.line("      - " + prefix + "_" + host + ":" + volRest(vol))
		} else {
			g.line("      - " + vol)
		}
	}

	// Environment (keys sorted to match jq `keys[]`)
	if keys := sortedKeys(img.EnvVars); len(keys) > 0 {
		g.line("    environment:")
		for _, k := range keys {
			g.line("      - " + k + "=" + string(img.EnvVars[k]))
		}
	}

	// Ports vs expose
	hasExt := hport != ""
	if !hasExt {
		for _, ep := range img.ExtraPorts {
			if string(ep) != "" {
				hasExt = true
				break
			}
		}
	}
	if hasExt {
		g.line("    ports:")
		if hport != "" {
			g.line("      - \"" + hport + ":" + port + "\"")
		}
		for _, ep := range img.ExtraPorts {
			if string(ep) == "" {
				continue
			}
			g.line("      - \"" + string(ep) + "\"")
		}
		if hport != "" && e.TraefikEnabled {
			g.traefikLabels(prefix+"_"+svc, e.Domain, port)
		}
	} else {
		g.line("    expose:")
		g.line("      - \"" + port + "\"")
	}

	// Healthcheck (image-stack defaults: start_period 40s)
	if img.Healthcheck != "" {
		hc := img.HealthcheckConfig
		g.healthcheck(img.Healthcheck,
			strOr(hc.Interval, "30s"), strOr(hc.Timeout, "10s"),
			strOr(hc.Retries, "3"), strOr(hc.StartPeriod, "40s"),
			string(hc.StartInterval))
	}

	g.deployBlock(isSwarm, "1", restart)

	// extra_compose: service-level then env-level override.
	g.emitExtraCompose(img.ExtraCompose)
	if ov, ok := e.ServiceOverrides[svc]; ok {
		g.emitExtraCompose(ov.ExtraCompose)
	}

	g.line("")
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
