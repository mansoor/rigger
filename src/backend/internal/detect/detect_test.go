package detect

import (
	"os"
	"path/filepath"
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
