package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repo builds a fixture repo tree from a path→content map and returns its dir.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func svcByName(d Draft, name string) *Service {
	for i := range d.Services {
		if d.Services[i].Name == name {
			return &d.Services[i]
		}
	}
	return nil
}

func TestDetectCompose(t *testing.T) {
	dir := repo(t, map[string]string{
		"docker-compose.yml": `
services:
  api:
    build: ./api
    command: node server.js
    ports:
      - "8080:3000"
    depends_on:
      - db
  db:
    image: postgres:16-alpine
`,
	})
	d := Detect(dir)
	if d.Detected != "docker-compose" {
		t.Fatalf("expected compose detection, got %q (notes %v)", d.Detected, d.Notes)
	}
	api := svcByName(d, "api")
	if api == nil || api.Build == nil || api.Build.Context != "./api" {
		t.Fatalf("api build service wrong: %+v", api)
	}
	if api.Command != "node server.js" || api.Port != "3000" || api.HostPort != "8080" {
		t.Errorf("api fields wrong: %+v", api)
	}
	if d.Database != "postgres" {
		t.Errorf("expected postgres managed dep, got %q", d.Database)
	}
	if svcByName(d, "db") != nil {
		t.Errorf("postgres service should be a managed dep, not a service")
	}
	// depends_on db (a managed dep) should be filtered out.
	for _, dep := range api.DependsOn {
		if dep == "db" {
			t.Errorf("managed-dep dependency should be filtered: %v", api.DependsOn)
		}
	}
}

// TestDetectComposeFidelity covers the faithful-import capture (Path 2): per-service
// environment, all ports (extra_ports), healthcheck, build dockerfile/args, named +
// bind volumes, profile skipping, DB version capture, the web-entry heuristic, and
// .env.example seeding. Modeled on a kyt-shaped compose.
func TestDetectComposeFidelity(t *testing.T) {
	dir := repo(t, map[string]string{
		".env.example": "POSTGRES_DB=teslamate\nSECRET_KEY=replace-me\n# a comment\nMQTT_HOST=mosquitto\n",
		"docker-compose.yml": `
services:
  db:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: ${POSTGRES_DB:-teslamate}
  backend:
    build:
      context: ./backend
      dockerfile: Dockerfile.prod
      args:
        VERSION: "1.2.3"
    environment:
      DATABASE_URL: postgresql://db:5432/app
      MQTT_PORT: 1883
      DEBUG: false
    depends_on:
      db:
        condition: service_healthy
    healthcheck:
      test: ["CMD-SHELL", "curl -f http://localhost:8000/health || exit 1"]
      interval: 15s
      timeout: 5s
      retries: 5
  proxy:
    image: caddy:2-alpine
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
  mosquitto:
    image: eclipse-mosquitto:2
    ports:
      - "1883:1883"
      - "9001:9001"
  photon:
    image: koodinikula/photon:latest
    profiles:
      - geocoder
`,
	})
	d := Detect(dir)

	// DB → managed dep with version captured from the image tag.
	if d.Database != "postgres" || d.DBVersion != "16-alpine" {
		t.Errorf("db: database=%q db_version=%q, want postgres/16-alpine", d.Database, d.DBVersion)
	}
	if svcByName(d, "db") != nil {
		t.Errorf("postgres should be a managed dep, not a service")
	}

	// Profile-gated service skipped.
	if svcByName(d, "photon") != nil {
		t.Errorf("profile-gated photon should be skipped")
	}

	// backend: env (incl. non-string scalars), healthcheck, build dockerfile+args.
	backend := svcByName(d, "backend")
	if backend == nil {
		t.Fatal("backend service missing")
	}
	// DATABASE_URL host is rebased from the dropped "db" service onto the managed
	// "postgres" service name (which resolves); other env values pass through.
	if backend.EnvVars["DATABASE_URL"] != "postgresql://postgres:5432/app" || backend.EnvVars["MQTT_PORT"] != "1883" || backend.EnvVars["DEBUG"] != "false" {
		t.Errorf("backend env not captured/rebased faithfully: %v", backend.EnvVars)
	}
	if backend.Healthcheck != "curl -f http://localhost:8000/health || exit 1" {
		t.Errorf("backend healthcheck = %q", backend.Healthcheck)
	}
	if backend.HealthcheckConfig == nil || backend.HealthcheckConfig.Interval != "15s" || backend.HealthcheckConfig.Retries != "5" {
		t.Errorf("backend healthcheck config = %+v", backend.HealthcheckConfig)
	}
	if backend.Build == nil || backend.Build.Context != "./backend" || backend.Build.Dockerfile != "Dockerfile.prod" || backend.Build.Args["VERSION"] != "1.2.3" {
		t.Errorf("backend build not captured: %+v", backend.Build)
	}

	// proxy: all ports (first → Port/HostPort, rest → ExtraPorts), bind+named volumes,
	// and chosen as the web entry (publishes 80).
	proxy := svcByName(d, "proxy")
	if proxy == nil || proxy.Port != "80" || proxy.HostPort != "80" {
		t.Fatalf("proxy port wrong: %+v", proxy)
	}
	if len(proxy.ExtraPorts) != 1 || proxy.ExtraPorts[0] != "443:443" {
		t.Errorf("proxy extra_ports = %v, want [443:443]", proxy.ExtraPorts)
	}
	if len(proxy.Volumes) != 2 || proxy.Volumes[0] != "./Caddyfile:/etc/caddy/Caddyfile:ro" {
		t.Errorf("proxy volumes not preserved: %v", proxy.Volumes)
	}
	if !proxy.WebRouted {
		t.Errorf("proxy (publishes :80) should be the web entry")
	}

	// mosquitto: both ports captured.
	mq := svcByName(d, "mosquitto")
	if mq == nil || mq.Port != "1883" || len(mq.ExtraPorts) != 1 || mq.ExtraPorts[0] != "9001:9001" {
		t.Errorf("mosquitto ports wrong: %+v", mq)
	}

	// .env.example seeded.
	if d.EnvVars["POSTGRES_DB"] != "teslamate" || d.EnvVars["MQTT_HOST"] != "mosquitto" {
		t.Errorf("env seeded from .env.example wrong: %v", d.EnvVars)
	}

	// Managed-dependency OFFER: the dropped "db" is recorded as a candidate carrying
	// the verbatim raw service + the host rewrite, so the wizard can offer keep-own.
	if len(d.ManagedCandidates) != 1 {
		t.Fatalf("want 1 managed candidate, got %d: %+v", len(d.ManagedCandidates), d.ManagedCandidates)
	}
	mc := d.ManagedCandidates[0]
	if mc.Role != "postgres" || mc.DetectedName != "db" || mc.ManagedName != "postgres" || mc.Image != "postgres" || mc.Tag != "16-alpine" {
		t.Errorf("candidate fields wrong: %+v", mc)
	}
	if mc.RawService.Image != "postgres" || mc.RawService.Tag != "16-alpine" {
		t.Errorf("candidate raw service not captured verbatim: %+v", mc.RawService)
	}
	// The DATABASE_URL rewrite is recorded both ways so keep-own can reverse it.
	var found bool
	for _, rw := range mc.Rewrites {
		if rw.Service == "backend" && rw.Key == "DATABASE_URL" &&
			rw.Original == "postgresql://db:5432/app" && rw.Managed == "postgresql://postgres:5432/app" {
			found = true
		}
	}
	if !found {
		t.Errorf("DATABASE_URL rewrite not recorded for reversal: %+v", mc.Rewrites)
	}

	// Profile OMIT: photon surfaced as a structured opt-in candidate (not in services[]).
	if len(d.ProfileOmitted) != 1 {
		t.Fatalf("want 1 profile-omitted service, got %d: %+v", len(d.ProfileOmitted), d.ProfileOmitted)
	}
	po := d.ProfileOmitted[0]
	if po.Name != "photon" || len(po.Profiles) != 1 || po.Profiles[0] != "geocoder" || po.Service.Image != "koodinikula/photon" {
		t.Errorf("profile-omitted photon wrong: %+v", po)
	}
}

func TestDetectDockerfileMonorepo(t *testing.T) {
	dir := repo(t, map[string]string{
		"apps/api/Dockerfile": "FROM golang:1.25\nEXPOSE 9090\n",
		"apps/api/go.mod":     "module api\n",
		"apps/web/Dockerfile": "FROM node:20\nEXPOSE 3000\n",
		"apps/web/package.json": `{"dependencies":{"next":"14"}}`,
	})
	d := Detect(dir)
	api, web := svcByName(d, "api"), svcByName(d, "web")
	if api == nil || api.Build == nil || api.Build.Context != "apps/api" {
		t.Fatalf("api service wrong: %+v", api)
	}
	if api.Port != "9090" { // EXPOSE overrides the blueprint default
		t.Errorf("api port from EXPOSE = %q, want 9090", api.Port)
	}
	if web == nil || web.Build == nil || web.Build.Context != "apps/web" {
		t.Fatalf("web service wrong: %+v", web)
	}
	if web.Port != "3000" {
		t.Errorf("web port = %q, want 3000", web.Port)
	}
}

func TestDetectManifestGo(t *testing.T) {
	dir := repo(t, map[string]string{"go.mod": "module example.com/app\n\ngo 1.25\n"})
	d := Detect(dir)
	app := svcByName(d, "app")
	if app == nil || app.Build == nil || app.Build.Template != "go" {
		t.Fatalf("expected a Go build service, got %+v (detected %q)", app, d.Detected)
	}
	if app.Port != "8080" || !app.WebRouted {
		t.Errorf("go service should be web-routed on 8080: %+v", app)
	}
}

func TestDetectLaravelAddsNginx(t *testing.T) {
	dir := repo(t, map[string]string{
		"artisan":      "#!/usr/bin/env php\n",
		"composer.json": `{"require":{"laravel/framework":"^11","doctrine/dbal":"*"},"name":"app"}`,
		".env.example": "DB_CONNECTION=pgsql\nDATABASE_URL=postgres://...\n",
	})
	d := Detect(dir)
	app := svcByName(d, "app")
	if app == nil || app.Build == nil || app.Build.Template != "laravel" {
		t.Fatalf("expected laravel app service, got %+v", app)
	}
	if app.WebRouted {
		t.Errorf("php-fpm app should not be web-routed directly")
	}
	nginx := svcByName(d, "app-nginx")
	if nginx == nil || !nginx.WebRouted || nginx.ConfigTemplate != "laravel" {
		t.Fatalf("expected an nginx front service for laravel, got %+v", nginx)
	}
	if d.Database != "postgres" {
		t.Errorf("expected postgres, got %q", d.Database)
	}
}

// TestDetectNextjsHostnameEnv guards the Next.js standalone footgun: the service
// must be seeded with HOSTNAME=0.0.0.0 (Docker otherwise sets HOSTNAME to the
// container id and Next's standalone server fails to bind). Covers both the
// Dockerfile/manifest path and the compose path.
func TestDetectNextjsHostnameEnv(t *testing.T) {
	manifest := repo(t, map[string]string{
		"apps/web/Dockerfile":   "FROM node:20\nEXPOSE 3000\n",
		"apps/web/package.json": `{"dependencies":{"next":"14.2.5"}}`,
	})
	web := svcByName(Detect(manifest), "web")
	if web == nil || web.Build == nil || web.Build.Template != "nextjs" {
		t.Fatalf("expected a nextjs web service, got %+v", web)
	}
	if web.EnvVars["HOSTNAME"] != "0.0.0.0" {
		t.Errorf("manifest path: web HOSTNAME = %q, want 0.0.0.0 (env %v)", web.EnvVars["HOSTNAME"], web.EnvVars)
	}
	// node stacks must use a node-based healthcheck (node images may lack wget).
	if !strings.Contains(web.Healthcheck, "node -e") {
		t.Errorf("nextjs healthcheck = %q, want a node -e probe", web.Healthcheck)
	}

	compose := repo(t, map[string]string{
		"docker-compose.yml":      "services:\n  web:\n    build: ./web\n    ports:\n      - \"3000:3000\"\n",
		"web/Dockerfile":          "FROM node:20\n",
		"web/package.json":        `{"dependencies":{"next":"14.2.5"}}`,
	})
	cweb := svcByName(Detect(compose), "web")
	if cweb == nil || cweb.Build == nil || cweb.Build.Template != "nextjs" {
		t.Fatalf("compose path: expected nextjs web service, got %+v", cweb)
	}
	if cweb.EnvVars["HOSTNAME"] != "0.0.0.0" {
		t.Errorf("compose path: web HOSTNAME = %q, want 0.0.0.0 (env %v)", cweb.EnvVars["HOSTNAME"], cweb.EnvVars)
	}
}

func TestDetectProcfileWorkers(t *testing.T) {
	dir := repo(t, map[string]string{
		"package.json": `{"dependencies":{"express":"4","ioredis":"5"}}`,
		"Procfile":     "web: node server.js\nworker: node worker.js\nscheduler: node cron.js\n",
	})
	d := Detect(dir)
	worker := svcByName(d, "worker")
	if worker == nil || worker.ImageFrom != "app" || worker.Command != "node worker.js" {
		t.Fatalf("expected worker reusing app image, got %+v", worker)
	}
	if svcByName(d, "scheduler") == nil {
		t.Errorf("expected scheduler service")
	}
	if svcByName(d, "web") != nil {
		t.Errorf("'web' Procfile line is the app service, not a separate one")
	}
	if !d.Redis {
		t.Errorf("expected redis suggested from ioredis dependency")
	}
}

func TestDetectUnknown(t *testing.T) {
	dir := repo(t, map[string]string{"README.md": "# nothing to see"})
	d := Detect(dir)
	if len(d.Services) != 0 {
		t.Errorf("expected no services for an unrecognised repo, got %+v", d.Services)
	}
	if len(d.Notes) == 0 {
		t.Errorf("expected a note explaining nothing was detected")
	}
}

// TestRebaseHost verifies host rewriting handles bare values and URL/DSN host
// positions, and leaves longer hostnames containing the token untouched.
func TestRebaseHost(t *testing.T) {
	cases := []struct{ in, old, neu, want string }{
		{"postgresql+asyncpg://u:p@db:5432/app", "db", "postgres", "postgresql+asyncpg://u:p@postgres:5432/app"},
		{"mysql://u:p@db/app", "db", "mysql", "mysql://u:p@mysql/app"},
		{"db", "db", "postgres", "postgres"},                                  // bare host value
		{"redis://cache:6379", "cache", "redis", "redis://redis:6379"},        // //host: form
		{"postgresql://u:p@database:5432/x", "db", "postgres", "postgresql://u:p@database:5432/x"}, // substring untouched
		{"keep@me", "db", "postgres", "keep@me"},                              // unrelated @ untouched
	}
	for _, c := range cases {
		if got := rebaseHost(c.in, c.old, c.neu); got != c.want {
			t.Errorf("rebaseHost(%q,%q,%q) = %q, want %q", c.in, c.old, c.neu, got, c.want)
		}
	}
}
