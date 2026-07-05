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

func overlayByFile(d Draft, file string) *ComposeOverlay {
	for i := range d.ComposeOverlays {
		if d.ComposeOverlays[i].File == file {
			return &d.ComposeOverlays[i]
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
	d := Detect(dir, nil)
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
	d := Detect(dir, nil)

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

// Port specs with an env-var default (${APP_PORT:-80}) must not be split on the colon
// inside ${...} — a common shape in uploaded compose files.
func TestSplitPortEnvDefault(t *testing.T) {
	cases := []struct{ in, host, cont string }{
		{"${APP_PORT:-80}:80", "${APP_PORT:-80}", "80"},
		{"8080:${VITE_PORT}", "8080", "${VITE_PORT}"},
		{"8080:${VITE_PORT:-5173}", "8080", "${VITE_PORT:-5173}"},
		{"80", "", "80"},
		{"127.0.0.1:8080:80", "8080", "80"},
	}
	for _, c := range cases {
		h, cont := splitPort(c.in)
		if h != c.host || cont != c.cont {
			t.Errorf("splitPort(%q) = (%q,%q), want (%q,%q)", c.in, h, cont, c.host, c.cont)
		}
	}
}

// A Laravel Sail docker-compose.yml builds from ./vendor/laravel/sail/runtimes/<v>,
// but marketplace apps ship no vendor/. The detector must rewrite it to a clean root
// Laravel build (scaffolded Dockerfile) and strip Sail's dev env + the source bind.
func TestDetectLaravelSailRewrite(t *testing.T) {
	dir := repo(t, map[string]string{
		"artisan":       "#!/usr/bin/env php",
		"composer.json": `{"require":{"laravel/framework":"^11"}}`,
		"docker-compose.yml": `
services:
  laravel.test:
    build:
      context: ./vendor/laravel/sail/runtimes/8.3
      dockerfile: Dockerfile
      args:
        WWWGROUP: '${WWWGROUP}'
    ports:
      - '${APP_PORT:-80}:80'
    environment:
      LARAVEL_SAIL: 1
      XDEBUG_MODE: '${SAIL_XDEBUG_MODE:-off}'
    volumes:
      - '.:/var/www/html'
`,
	})
	d := Detect(dir, nil)
	if len(d.Services) != 1 {
		t.Fatalf("want 1 service, got %d: %+v", len(d.Services), d.Services)
	}
	svc := d.Services[0]
	if svc.Build == nil || svc.Build.Context != "." || svc.Build.Template != "laravel" {
		t.Fatalf("Sail service not rewritten to a root laravel build: %+v", svc.Build)
	}
	if svc.EnvVars["LARAVEL_SAIL"] != "" || svc.EnvVars["XDEBUG_MODE"] != "" {
		t.Errorf("Sail dev env not stripped: %v", svc.EnvVars)
	}
	for _, v := range svc.Volumes {
		if v == ".:/var/www/html" {
			t.Errorf("whole-repo source bind should be dropped: %v", svc.Volumes)
		}
	}
}

func TestDetectDockerfileMonorepo(t *testing.T) {
	dir := repo(t, map[string]string{
		"apps/api/Dockerfile":   "FROM golang:1.25\nEXPOSE 9090\n",
		"apps/api/go.mod":       "module api\n",
		"apps/web/Dockerfile":   "FROM node:20\nEXPOSE 3000\n",
		"apps/web/package.json": `{"dependencies":{"next":"14"}}`,
	})
	d := Detect(dir, nil)
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
	d := Detect(dir, nil)
	app := svcByName(d, "app")
	if app == nil || app.Build == nil || app.Build.Template != "go" {
		t.Fatalf("expected a Go build service, got %+v (detected %q)", app, d.Detected)
	}
	if app.Port != "8080" || !app.WebRouted {
		t.Errorf("go service should be web-routed on 8080: %+v", app)
	}
}

// TestDetectLaravelSelfContained guards the Laravel blueprint shape: it's now a
// self-contained image served via `php artisan serve` (web-routed directly, NO
// separate nginx front — see templates/dockerfiles/laravel + the blueprint rework).
func TestDetectLaravelSelfContained(t *testing.T) {
	dir := repo(t, map[string]string{
		"artisan":       "#!/usr/bin/env php\n",
		"composer.json": `{"require":{"laravel/framework":"^11","doctrine/dbal":"*"},"name":"app"}`,
		".env.example":  "DB_CONNECTION=pgsql\nDATABASE_URL=postgres://...\n",
	})
	d := Detect(dir, nil)
	app := svcByName(d, "app")
	if app == nil || app.Build == nil || app.Build.Template != "laravel" {
		t.Fatalf("expected laravel app service, got %+v", app)
	}
	if !app.WebRouted {
		t.Errorf("self-contained laravel app should be web-routed directly")
	}
	if nginx := svcByName(d, "app-nginx"); nginx != nil {
		t.Errorf("self-contained laravel must NOT add an nginx front, got %+v", nginx)
	}
	if d.Database != "postgres" {
		t.Errorf("expected postgres, got %q", d.Database)
	}
}

// TestDetectSpringBuildTool: a Maven project (pom.xml) scaffolds the "spring" template
// (mvn build); a Gradle project (build.gradle / .kts) scaffolds "spring-gradle" (gradle
// bootJar). Both share the Spring Boot runtime contract; only the build tool differs.
func TestDetectSpringBuildTool(t *testing.T) {
	mvn := svcByName(Detect(repo(t, map[string]string{"pom.xml": "<project/>"}), nil), "app")
	if mvn == nil || mvn.Build == nil || mvn.Build.Template != "spring" {
		t.Errorf("pom.xml should scaffold the Maven 'spring' template, got %+v", mvn)
	}
	for _, f := range []string{"build.gradle", "build.gradle.kts"} {
		gr := svcByName(Detect(repo(t, map[string]string{f: "plugins {}"}), nil), "app")
		if gr == nil || gr.Build == nil || gr.Build.Template != "spring-gradle" {
			t.Errorf("%s should scaffold the 'spring-gradle' template, got %+v", f, gr)
		}
	}
}

// TestDetectFrameworkTemplateIds: the scaffold ids must match a templates/dockerfiles
// folder AND a blueprint key. Guards the drift where a Vite SPA scaffolded as "static"
// (no such folder) and Rails had a blueprint but no Dockerfile.
func TestDetectFrameworkTemplateIds(t *testing.T) {
	cases := []struct{ file, content, want string }{
		{"package.json", `{"devDependencies":{"vite":"5"}}`, "react"},
		{"Gemfile", "source 'https://rubygems.org'\ngem 'rails'", "rails"},
	}
	for _, c := range cases {
		app := svcByName(Detect(repo(t, map[string]string{c.file: c.content}), nil), "app")
		if app == nil || app.Build == nil || app.Build.Template != c.want {
			t.Errorf("%s should scaffold template %q, got %+v", c.file, c.want, app)
		}
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
	web := svcByName(Detect(manifest, nil), "web")
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
		"docker-compose.yml": "services:\n  web:\n    build: ./web\n    ports:\n      - \"3000:3000\"\n",
		"web/Dockerfile":     "FROM node:20\n",
		"web/package.json":   `{"dependencies":{"next":"14.2.5"}}`,
	})
	cweb := svcByName(Detect(compose, nil), "web")
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
	d := Detect(dir, nil)
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

// An imported compose that already gates its app on a one-shot migrate (via
// service_completed_successfully) must (a) round-trip the depends_on condition rather
// than silently downgrade it, and (b) flag HasPreDeploy so the UI can advise against a
// duplicate and composegen skips synthesis.
func TestDetectPreDeployGate(t *testing.T) {
	dir := repo(t, map[string]string{
		"package.json": `{"dependencies":{"express":"4"}}`,
		"docker-compose.yml": `
services:
  db:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready"]
  migrate:
    build: .
    command: npm run migrate
    restart: "no"
    depends_on:
      db:
        condition: service_healthy
  app:
    build: .
    command: npm start
    ports:
      - "3000:3000"
    depends_on:
      db:
        condition: service_healthy
      migrate:
        condition: service_completed_successfully
`,
	})
	d := Detect(dir, nil)
	app := svcByName(d, "app")
	if app == nil {
		t.Fatalf("app service not detected: %+v", d.Services)
	}
	// The app→migrate completed_successfully condition must survive (not downgraded).
	if got := app.DependsOnConditions["migrate"]; got != "service_completed_successfully" {
		t.Errorf("app depends_on migrate condition = %q; want service_completed_successfully (conditions: %+v)", got, app.DependsOnConditions)
	}
	mig := svcByName(d, "migrate")
	if mig == nil || mig.Restart != "no" {
		t.Errorf("migrate service should be a run-once container (restart no), got %+v", mig)
	}
	if !d.HasPreDeploy || d.PreDeployService != "migrate" {
		t.Errorf("expected HasPreDeploy with PreDeployService=migrate, got has=%v svc=%q", d.HasPreDeploy, d.PreDeployService)
	}
}

func TestDetectUnknown(t *testing.T) {
	dir := repo(t, map[string]string{"README.md": "# nothing to see"})
	d := Detect(dir, nil)
	if len(d.Services) != 0 {
		t.Errorf("expected no services for an unrecognised repo, got %+v", d.Services)
	}
	if len(d.Notes) == 0 {
		t.Errorf("expected a note explaining nothing was detected")
	}
}

// TestDetectSeedDumps verifies bundled SQL dumps are surfaced from the repo root
// and known seed dirs, ranked largest-first, with migration code and tiny stubs
// excluded.
func TestDetectSeedDumps(t *testing.T) {
	big := strings.Repeat("INSERT INTO x VALUES (1);\n", 1000)    // ~26 KiB > floor
	bigger := strings.Repeat("INSERT INTO x VALUES (1);\n", 2000) // ~52 KiB
	d := Detect(repo(t, map[string]string{
		"composer.json":                       `{"require":{"php":"^8.2"}}`,
		"database.sql":                        big,
		"database-home2.sql":                  bigger,
		"install/seed.sql":                    big,
		"database/migrations/0001_create.sql": big, // excluded: migration code
		"database/migrations/0001_create.php": "<?php",
		"stub.sql":                            "SELECT 1;", // excluded: below size floor
		"app/Models/User.php":                 "<?php",
	}), nil)
	got := map[string]bool{}
	for _, c := range d.SeedCandidates {
		got[c.Path] = true
	}
	for _, want := range []string{"database.sql", "database-home2.sql", "install/seed.sql"} {
		if !got[want] {
			t.Errorf("expected seed candidate %q, got %+v", want, d.SeedCandidates)
		}
	}
	if got["database/migrations/0001_create.sql"] {
		t.Errorf("migration .sql must be excluded, got %+v", d.SeedCandidates)
	}
	if got["stub.sql"] {
		t.Errorf("below-floor stub.sql must be excluded, got %+v", d.SeedCandidates)
	}
	// Largest first: database-home2.sql (~52 KiB) outranks database.sql (~26 KiB).
	if len(d.SeedCandidates) == 0 || d.SeedCandidates[0].Path != "database-home2.sql" {
		t.Errorf("expected largest dump first (database-home2.sql), got %+v", d.SeedCandidates)
	}
}

// TestRebaseHost verifies host rewriting handles bare values and URL/DSN host
// positions, and leaves longer hostnames containing the token untouched.
func TestRebaseHost(t *testing.T) {
	cases := []struct{ in, old, neu, want string }{
		{"postgresql+asyncpg://u:p@db:5432/app", "db", "postgres", "postgresql+asyncpg://u:p@postgres:5432/app"},
		{"mysql://u:p@db/app", "db", "mysql", "mysql://u:p@mysql/app"},
		{"db", "db", "postgres", "postgres"},                                                       // bare host value
		{"redis://cache:6379", "cache", "redis", "redis://redis:6379"},                             // //host: form
		{"postgresql://u:p@database:5432/x", "db", "postgres", "postgresql://u:p@database:5432/x"}, // substring untouched
		{"keep@me", "db", "postgres", "keep@me"},                                                   // unrelated @ untouched
	}
	for _, c := range cases {
		if got := rebaseHost(c.in, c.old, c.neu); got != c.want {
			t.Errorf("rebaseHost(%q,%q,%q) = %q, want %q", c.in, c.old, c.neu, got, c.want)
		}
	}
}

func TestParseDotenvValueInlineComments(t *testing.T) {
	cases := map[string]string{
		"http://localhost:3000           # Next.js frontend origin": "http://localhost:3000",
		`"postgresql://qrhub:qrhub@localhost:5432/qrhub?schema=public"`: "postgresql://qrhub:qrhub@localhost:5432/qrhub?schema=public",
		`"QR Hub <no-reply@qrhub.local>"`:                              "QR Hub <no-reply@qrhub.local>",
		"QR Hub":                                                       "QR Hub",   // legit space, no comment
		"development":                                                  "development",
		"0                   # 0 = disabled (use external cron); >0":  "0",     // '#' comment even though it contains '='
		"false                                # truncate/anonymize":    "false",
		"a#b":                                                          "a#b",       // '#' with no leading space stays
		"":                                                             "",
	}
	for in, want := range cases {
		if got := parseDotenvValue(in); got != want {
			t.Errorf("parseDotenvValue(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestComposeDropsEdgeProxy: an imported compose that ships its own Traefik/edge
// proxy has it removed (Rigger's Traefik replaces it), a note explains, and a real
// service becomes the web entry.
func TestComposeDropsEdgeProxy(t *testing.T) {
	yml := `
services:
  proxy:
    image: traefik:v3.0
    ports: ["80:80", "443:443", "8080:8080"]
    volumes: ["/var/run/docker.sock:/var/run/docker.sock:ro"]
  backend:
    build: ./backend
    ports: ["8000:8000"]
  frontend:
    build: ./frontend
    ports: ["3000:3000"]
  db:
    image: mongo:7
`
	d, err := DetectComposeBytes([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range d.Services {
		if s.Name == "proxy" {
			t.Fatalf("edge proxy 'proxy' must be dropped; services: %+v", d.Services)
		}
	}
	if joined := strings.Join(d.Notes, " | "); !strings.Contains(joined, "Removed the app's bundled reverse proxy") {
		t.Errorf("expected proxy-removal note; got: %s", joined)
	}
	web := ""
	for _, s := range d.Services {
		if s.WebRouted {
			web = s.Name
		}
	}
	if web == "" || web == "proxy" {
		t.Errorf("expected a real web entry after dropping the proxy, got %q", web)
	}
}

// TestComposeKeepsStaticNginx: a plain nginx serving static files (publishes :80 but
// does NOT watch the docker socket) is NOT an edge proxy — it must be kept.
func TestComposeKeepsStaticNginx(t *testing.T) {
	yml := `
services:
  web:
    image: nginx:alpine
    ports: ["80:80"]
    volumes: ["./site:/usr/share/nginx/html:ro"]
  api:
    build: ./api
`
	d, err := DetectComposeBytes([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range d.Services {
		if s.Name == "web" {
			found = true
		}
	}
	if !found {
		t.Errorf("plain static nginx (no docker.sock) must be kept, not dropped; services: %+v", d.Services)
	}
}

// TestDetectCookiecutterTemplate: a cookiecutter repo (cookiecutter.json + a
// {{cookiecutter.*}} dir that itself contains a compose) is flagged TemplateOnly with no
// services — the templated compose inside must NOT be mapped into a bogus stack.
func TestDetectCookiecutterTemplate(t *testing.T) {
	dir := repo(t, map[string]string{
		"cookiecutter.json": `{"project_slug":"my_app"}`,
		"{{cookiecutter.project_slug}}/docker-compose.yml": "services:\n  web:\n    build: .\n    ports: [\"8000:8000\"]\n",
		"{{cookiecutter.project_slug}}/Dockerfile":         "FROM python",
	})
	d := DetectRepo(dir, "", nil)
	if d.TemplateOnly != "Cookiecutter" {
		t.Errorf("expected TemplateOnly=Cookiecutter; got %q (detected=%q)", d.TemplateOnly, d.Detected)
	}
	if len(d.Services) != 0 {
		t.Errorf("a template must yield NO services; got %+v", d.Services)
	}
	if joined := strings.Join(d.Notes, " | "); !strings.Contains(joined, "project template") {
		t.Errorf("expected an explanatory template note; got: %s", joined)
	}
}

// TestDetectCopierTemplate: a copier.yml at the root flags the repo as a template.
func TestDetectCopierTemplate(t *testing.T) {
	dir := repo(t, map[string]string{
		"copier.yml":            "project_name:\n  type: str\n",
		"template/Dockerfile.jinja": "FROM node",
	})
	d := DetectRepo(dir, "", nil)
	if d.TemplateOnly != "Copier" {
		t.Errorf("expected TemplateOnly=Copier; got %q", d.TemplateOnly)
	}
}

// TestDetectNotATemplate: a real app that merely has a normal compose is NOT flagged —
// the guard must not swallow ordinary repos.
func TestDetectNotATemplate(t *testing.T) {
	dir := repo(t, map[string]string{
		"docker-compose.yml": "services:\n  app:\n    build: .\n    ports: [\"8080:80\"]\n",
	})
	d := DetectRepo(dir, "", nil)
	if d.TemplateOnly != "" {
		t.Errorf("a normal repo must not be flagged as a template; got %q", d.TemplateOnly)
	}
	if len(d.Services) == 0 {
		t.Errorf("a normal repo must still map its services")
	}
}

// TestComposeMergesSafeOverrideAndEnvOverlay: a repo with base + a safe override +
// an env-specific overlay. With no selection (nil), the safe override auto-applies and
// the env overlay is discovered-but-unapplied; with an explicit selection, exactly the
// chosen file merges (Compose override semantics: scalars replaced, maps merged).
func TestComposeMergesSafeOverrideAndEnvOverlay(t *testing.T) {
	files := map[string]string{
		"docker-compose.yml":          "services:\n  app:\n    image: myapp:latest\n    environment:\n      FOO: base\n",
		"docker-compose.override.yml": "services:\n  app:\n    environment:\n      OVR: \"yes\"\n", // no host binds → safe
		"docker-compose.prod.yml":     "services:\n  app:\n    image: myapp:1.2.3\n",
	}
	// Auto (nil): safe override merged, env overlay offered but not applied.
	d := DetectRepo(repo(t, files), "", nil)
	app := svcByName(d, "app")
	if app == nil {
		t.Fatalf("app service missing: %+v", d.Services)
	}
	if app.EnvVars["FOO"] != "base" || app.EnvVars["OVR"] != "yes" {
		t.Errorf("safe override should be auto-merged (maps merged): %v", app.EnvVars)
	}
	if app.Tag != "latest" {
		t.Errorf("env overlay must NOT be auto-applied; tag=%q want latest", app.Tag)
	}
	ov := overlayByFile(d, "docker-compose.override.yml")
	if ov == nil || !ov.Applied || !ov.Recommended {
		t.Errorf("safe override should be applied+recommended: %+v", ov)
	}
	if pv := overlayByFile(d, "docker-compose.prod.yml"); pv == nil || pv.Applied || pv.Kind != "environment" {
		t.Errorf("prod overlay should be discovered, unapplied, kind=environment: %+v", pv)
	}

	// Explicit selection: merge only prod; the override is left out.
	d2 := DetectRepo(repo(t, files), "", []string{"docker-compose.prod.yml"})
	app2 := svcByName(d2, "app")
	if app2 == nil || app2.Tag != "1.2.3" {
		t.Errorf("explicit prod overlay should replace the image tag: %+v", app2)
	}
	if _, ok := app2.EnvVars["OVR"]; ok {
		t.Errorf("override must NOT be applied under an explicit selection that omits it: %v", app2.EnvVars)
	}
}

// TestComposeSkipsDevOverride: an override that bind-mounts host source is dev-scoped and
// must NOT auto-apply (nil selection) — it's offered for opt-in instead.
func TestComposeSkipsDevOverride(t *testing.T) {
	d := DetectRepo(repo(t, map[string]string{
		"docker-compose.yml":          "services:\n  app:\n    image: myapp:latest\n",
		"docker-compose.override.yml": "services:\n  app:\n    volumes:\n      - ./:/app\n", // host bind → dev-scoped
	}), "", nil)
	ov := overlayByFile(d, "docker-compose.override.yml")
	if ov == nil || !ov.DevScoped {
		t.Fatalf("override with a host bind should be flagged DevScoped: %+v", ov)
	}
	if ov.Applied || ov.Recommended {
		t.Errorf("a dev-scoped override must not auto-apply / be recommended: %+v", ov)
	}
}

// TestComposeFoldsMongo: a mongo data service is folded into the managed MongoDB engine
// (dropped as a plain service, database set, version captured), and a hardcoded host
// reference in the app's env is repointed onto the managed "mongodb" service name. A
// mongo-express console in the same file must NOT be mistaken for the data service.
func TestComposeFoldsMongo(t *testing.T) {
	// Managed-dep folding is the repo-scan behaviour (foldManaged=true), so drive it
	// through DetectRepo with a compose at the root — the paste path keeps everything.
	dir := repo(t, map[string]string{
		"api/Dockerfile": "FROM python\nEXPOSE 8000",
		"docker-compose.yml": "services:\n" +
			"  api:\n" +
			"    build: ./api\n" +
			"    environment:\n" +
			"      MONGODB_URL: mongodb://root:pass@mongo:27017/app?authSource=admin\n" +
			"  mongo:\n" +
			"    image: mongo:6\n" +
			"  mongo-express:\n" +
			"    image: mongo-express:1.0\n" +
			"    ports: [\"8081:8081\"]\n",
	})
	d := DetectRepo(dir, "", nil)
	if d.Database != "mongodb" {
		t.Errorf("mongo must fold into the managed engine: database=%q", d.Database)
	}
	if d.DBVersion != "6" {
		t.Errorf("mongo version must be captured from the image tag: db_version=%q", d.DBVersion)
	}
	for _, s := range d.Services {
		if s.Name == "mongo" {
			t.Errorf("mongo data service must be dropped (folded), not kept: %+v", d.Services)
		}
	}
	// The console is NOT a data service — it must survive as a plain service.
	foundConsole := false
	for _, s := range d.Services {
		if s.Name == "mongo-express" {
			foundConsole = true
		}
	}
	if !foundConsole {
		t.Errorf("mongo-express console must NOT be folded as a DB; services: %+v", d.Services)
	}
	// The app's MONGODB_URL host (mongo) is repointed onto the managed "mongodb" host.
	for _, s := range d.Services {
		if s.Name == "api" {
			if got := s.EnvVars["MONGODB_URL"]; !strings.Contains(got, "@mongodb:27017/") {
				t.Errorf("MONGODB_URL host must be rebased onto managed 'mongodb': %q", got)
			}
		}
	}
}

// TestDetectRepoNestedCompose: no stack at the repo root, but an app with a
// docker-compose.yml lives in a subdir → auto-discovered, and build contexts are
// prefixed with the subdir so they resolve from the repo root at build time.
func TestDetectRepoNestedCompose(t *testing.T) {
	dir := repo(t, map[string]string{
		"README.md": "x",
		"myapp/docker-compose.yml": "services:\n  web:\n    build: ./frontend\n    ports: [\"3000:3000\"]\n  api:\n    build: ./backend\n    ports: [\"8000:8000\"]\n",
		"myapp/frontend/Dockerfile": "FROM node",
		"myapp/backend/Dockerfile":  "FROM python",
	})
	d := DetectRepo(dir, "", nil)
	if d.SourceSubdir != "myapp" {
		t.Fatalf("expected SourceSubdir=myapp; got %q (detected=%q, %d svcs)", d.SourceSubdir, d.Detected, len(d.Services))
	}
	for _, s := range d.Services {
		if s.Build != nil && !strings.HasPrefix(s.Build.Context, "./myapp/") {
			t.Errorf("service %s build context %q must be prefixed with ./myapp/", s.Name, s.Build.Context)
		}
	}
}

// TestDetectRepoRootWins: a stack at the root is used as-is (no subdir).
func TestDetectRepoRootWins(t *testing.T) {
	dir := repo(t, map[string]string{
		"docker-compose.yml": "services:\n  app:\n    build: .\n    ports: [\"8080:80\"]\n",
	})
	d := DetectRepo(dir, "", nil)
	if d.SourceSubdir != "" {
		t.Errorf("root stack must have empty SourceSubdir; got %q", d.SourceSubdir)
	}
}

// TestDetectRepoExplicitSubdir: an explicit subdir scans there and prefixes contexts.
func TestDetectRepoExplicitSubdir(t *testing.T) {
	dir := repo(t, map[string]string{
		"packages/web/Dockerfile":   "FROM node\nEXPOSE 3000",
		"packages/web/package.json": `{"dependencies":{"next":"14"}}`,
	})
	d := DetectRepo(dir, "packages/web", nil)
	if d.SourceSubdir != "packages/web" {
		t.Fatalf("expected SourceSubdir=packages/web; got %q", d.SourceSubdir)
	}
	if len(d.Services) == 0 {
		t.Fatal("expected a build service")
	}
	for _, s := range d.Services {
		if s.Build != nil && !strings.HasPrefix(s.Build.Context, "./packages/web") {
			t.Errorf("build context %q must be under ./packages/web", s.Build.Context)
		}
	}
}

// TestDetectRepoSkipsJunkDirs: a compose only under examples/ or node_modules/ must
// NOT be picked as the app root.
func TestDetectRepoSkipsJunkDirs(t *testing.T) {
	dir := repo(t, map[string]string{
		"README.md":                           "x",
		"examples/docker-compose.yml":          "services:\n  x:\n    image: nginx\n",
		"node_modules/foo/docker-compose.yml":  "services:\n  y:\n    image: nginx\n",
	})
	d := DetectRepo(dir, "", nil)
	if d.SourceSubdir != "" {
		t.Errorf("compose under examples/node_modules must be skipped; got subdir %q", d.SourceSubdir)
	}
}

// TestComposeTranslatesTraefikRoutes: the app's Traefik router labels become Rigger
// routes — / → frontend, /api → backend (stripped), /docs → backend (NOT stripped,
// since api-strip only strips /api). The proxy is not a route target.
func TestComposeTranslatesTraefikRoutes(t *testing.T) {
	yml := "services:\n" +
		"  proxy:\n" +
		"    image: traefik:v3.0\n" +
		"    ports: [\"80:80\"]\n" +
		"    volumes: [\"/var/run/docker.sock:/var/run/docker.sock:ro\"]\n" +
		"  backend:\n" +
		"    build: ./backend\n" +
		"    labels:\n" +
		"      - \"traefik.http.routers.backend.rule=PathPrefix(`/api`) || PathPrefix(`/docs`)\"\n" +
		"      - \"traefik.http.routers.backend.middlewares=api-strip\"\n" +
		"      - \"traefik.http.middlewares.api-strip.stripprefix.prefixes=/api\"\n" +
		"  frontend:\n" +
		"    build: ./frontend\n" +
		"    labels:\n" +
		"      - \"traefik.http.routers.frontend.rule=PathPrefix(`/`)\"\n"
	d, err := DetectComposeBytes([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	get := func(svc, match string) (Route, bool) {
		for _, r := range d.Routes {
			if r.Service == svc && r.Match == match {
				return r, true
			}
		}
		return Route{}, false
	}
	if r, ok := get("frontend", "/"); !ok || r.Target != "" {
		t.Errorf("frontend '/' route missing or wrongly stripped: %+v ok=%v", r, ok)
	}
	if r, ok := get("backend", "/api"); !ok || r.Target != "/" {
		t.Errorf("backend '/api' must strip (Target=/): %+v ok=%v", r, ok)
	}
	if r, ok := get("backend", "/docs"); !ok || r.Target != "" {
		t.Errorf("backend '/docs' must NOT strip: %+v ok=%v", r, ok)
	}
	for _, r := range d.Routes {
		if r.Service == "proxy" {
			t.Errorf("dropped proxy must not be a route target: %+v", r)
		}
	}
}

// TestComposeRewritesLocalhostBuildArgs: a frontend that bakes its API base URL at build
// time via a hardcoded localhost URL is rewritten to ${ROUTE_URL} (path preserved), while
// an already-tokenized arg and an in-network service-name URL are left untouched.
func TestComposeRewritesLocalhostBuildArgs(t *testing.T) {
	yml := "services:\n" +
		"  frontend:\n" +
		"    build:\n" +
		"      context: ./frontend\n" +
		"      args:\n" +
		"        NEXT_PUBLIC_API_URL: http://localhost/api\n" +
		"        NEXT_PUBLIC_WS_URL: http://127.0.0.1:8000\n" +
		"        NEXT_PUBLIC_KEEP: http://backend:8000\n" +
		"        NEXT_PUBLIC_TOKENIZED: ${ROUTE_URL}/v2\n" +
		"    ports: [\"3000:3000\"]\n"
	d, err := DetectComposeBytes([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	var fe *Service
	for i := range d.Services {
		if d.Services[i].Name == "frontend" {
			fe = &d.Services[i]
		}
	}
	if fe == nil || fe.Build == nil {
		t.Fatalf("frontend build service missing: %+v", d.Services)
	}
	args := fe.Build.Args
	if got := args["NEXT_PUBLIC_API_URL"]; got != "${ROUTE_URL}/api" {
		t.Errorf("API_URL: want ${ROUTE_URL}/api, got %q", got)
	}
	if got := args["NEXT_PUBLIC_WS_URL"]; got != "${ROUTE_URL}" {
		t.Errorf("WS_URL (no path): want ${ROUTE_URL}, got %q", got)
	}
	if got := args["NEXT_PUBLIC_KEEP"]; got != "http://backend:8000" {
		t.Errorf("in-network service URL must be untouched, got %q", got)
	}
	if got := args["NEXT_PUBLIC_TOKENIZED"]; got != "${ROUTE_URL}/v2" {
		t.Errorf("already-tokenized arg must be untouched, got %q", got)
	}
	if joined := strings.Join(d.Notes, " | "); !strings.Contains(joined, "${ROUTE_URL}") {
		t.Errorf("expected a build-arg rewrite note; got: %s", joined)
	}
}
