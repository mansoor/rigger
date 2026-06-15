package envgen

import (
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/wsconfig"
)

// fixedRand returns deterministic bytes so secret generation is reproducible.
func fixedRand(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 0xAB
	}
	return b
}

func cfg(t *testing.T, body string) *wsconfig.Config {
	t.Helper()
	c, err := wsconfig.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return c
}

func TestImageEnvGeneration(t *testing.T) {
	c := cfg(t, `{
      "project": { "name": "wp", "type": "image", "registry": "r" },
      "environments": { "prod": { "env_vars": {
        "WP_PORT": "8080",
        "DB_HOST": "CHANGE_ME",
        "MYSQL_PASSWORD": "CHANGE_ME",
        "API_TOKEN": "CHANGE_ME",
        "REAL_SECRET": "kept-value"
      } } }
    }`)

	env, example, err := Generate(c, "prod", nil, fixedRand)
	if err != nil {
		t.Fatal(err)
	}
	m := ParseEnv([]byte(env))

	if m["WP_PORT"] != "8080" {
		t.Errorf("WP_PORT = %q, want 8080 (non-placeholder kept)", m["WP_PORT"])
	}
	if m["DB_HOST"] != "CHANGE_ME" {
		t.Errorf("DB_HOST = %q, want CHANGE_ME (skip-key placeholder kept)", m["DB_HOST"])
	}
	if !strings.HasPrefix(m["MYSQL_PASSWORD"], "rigger-") || len(m["MYSQL_PASSWORD"]) != len("rigger-")+24 {
		t.Errorf("MYSQL_PASSWORD = %q, want rigger-<24hex>", m["MYSQL_PASSWORD"])
	}
	if len(m["API_TOKEN"]) != 48 { // hex(24) = 48 chars
		t.Errorf("API_TOKEN = %q, want 48 hex chars", m["API_TOKEN"])
	}
	if m["REAL_SECRET"] != "kept-value" {
		t.Errorf("REAL_SECRET = %q, want kept-value", m["REAL_SECRET"])
	}

	// .env.example masks generated secrets but keeps non-secrets.
	ex := ParseEnv([]byte(example))
	if !strings.HasPrefix(ex["MYSQL_PASSWORD"], "CHANGE_ME") || !strings.HasPrefix(ex["API_TOKEN"], "CHANGE_ME") {
		t.Errorf("example secrets not masked: MYSQL_PASSWORD=%q API_TOKEN=%q", ex["MYSQL_PASSWORD"], ex["API_TOKEN"])
	}
	if ex["WP_PORT"] != "8080" {
		t.Errorf("example WP_PORT = %q, want 8080", ex["WP_PORT"])
	}
}

func TestImageSecretPreservation(t *testing.T) {
	c := cfg(t, `{
      "project": { "name": "wp", "type": "image" },
      "environments": { "prod": { "env_vars": { "DB_PASSWORD": "CHANGE_ME" } } }
    }`)
	existing := map[string]string{"DB_PASSWORD": "rigger-preexisting"}
	env, _, err := Generate(c, "prod", existing, fixedRand)
	if err != nil {
		t.Fatal(err)
	}
	if got := ParseEnv([]byte(env))["DB_PASSWORD"]; got != "rigger-preexisting" {
		t.Errorf("DB_PASSWORD = %q, want preserved rigger-preexisting", got)
	}
}

func TestCustomPostgresEnv(t *testing.T) {
	c := cfg(t, `{
      "project": { "name": "myapp", "registry": "reg",
        "version": { "major": 1, "minor": 0, "patch": 0, "build": 2 } },
      "services": [
        {"name":"backend","build":{"template":"nodejs"}},
        {"name":"frontend","build":{"template":"react"}}
      ],
      "environments": { "prod": {
        "domain": "myapp.com", "http_port": 80, "https_port": 443,
        "database": "postgres", "redis_enabled": true, "garage_enabled": false,
        "deployment": "compose",
        "env_vars": { "CUSTOM_FLAG": "yes" }
      } }
    }`)

	env, example, err := Generate(c, "prod", nil, fixedRand)
	if err != nil {
		t.Fatal(err)
	}
	m := ParseEnv([]byte(env))

	checks := map[string]string{
		"COMPOSE_PROJECT_NAME": "myapp_prod",
		"PROJECT_NAME":         "myapp",
		"ENV":                  "prod",
		"BACKEND_IMAGE":        "reg/myapp-backend:1.0.0-build.2-prod",
		"FRONTEND_IMAGE":       "reg/myapp-frontend:1.0.0-build.2-prod",
		"DOMAIN":               "myapp.com",
		"HTTP_PORT":            "80",
		"HTTPS_PORT":           "443",
		"DATABASE":             "postgres",
		"POSTGRES_HOST":        "myapp_prod_postgres",
		"POSTGRES_DB":          "myapp_prod",
		"POSTGRES_USER":        "myapp_user",
		"APP_ENV":              "prod",
		"APP_DEBUG":            "false",
		"APP_URL":              "http://myapp.com",
		"REDIS_ENABLED":        "true",
		"REDIS_HOST":           "myapp_prod_redis",
		"GARAGE_ENABLED":       "false",
		"NODE_ENV":             "production",
		"PORT":                 "3000",
		"CUSTOM_FLAG":          "yes",
	}
	for k, want := range checks {
		if m[k] != want {
			t.Errorf("%s = %q, want %q", k, m[k], want)
		}
	}
	if !strings.HasPrefix(m["POSTGRES_PASSWORD"], "changeme_") {
		t.Errorf("POSTGRES_PASSWORD = %q, want changeme_ prefix", m["POSTGRES_PASSWORD"])
	}
	if !strings.HasPrefix(m["APP_KEY"], "base64:") {
		t.Errorf("APP_KEY = %q, want base64: prefix", m["APP_KEY"])
	}
	// Garage disabled → no garage vars.
	if _, ok := m["GARAGE_HOST"]; ok {
		t.Error("GARAGE_HOST present but garage disabled")
	}

	// example masks the DB password.
	ex := ParseEnv([]byte(example))
	if ex["POSTGRES_PASSWORD"] != "CHANGE_ME_DB_PASSWORD" {
		t.Errorf("example POSTGRES_PASSWORD = %q, want CHANGE_ME_DB_PASSWORD", ex["POSTGRES_PASSWORD"])
	}
	if ex["APP_KEY"] != "base64:CHANGE_ME" {
		t.Errorf("example APP_KEY = %q, want base64:CHANGE_ME", ex["APP_KEY"])
	}
}

// TestAppURLNoDomain guards the empty-domain case: APP_URL must stay a valid
// absolute URI (not the malformed "http://", which crashes Laravel artisan with
// "Invalid URI"). With no domain it falls back to localhost + the HTTP port.
func TestAppURLNoDomain(t *testing.T) {
	withPort := cfg(t, `{
      "project": { "name": "app", "registry": "r" },
      "environments": { "dev": { "http_port": 8080, "deployment": "compose" } }
    }`)
	env, _, err := Generate(withPort, "dev", nil, fixedRand)
	if err != nil {
		t.Fatal(err)
	}
	if got := ParseEnv([]byte(env))["APP_URL"]; got != "http://localhost:8080" {
		t.Errorf("APP_URL = %q, want http://localhost:8080 (empty domain + port)", got)
	}

	port80 := cfg(t, `{
      "project": { "name": "app", "registry": "r" },
      "environments": { "dev": { "http_port": 80, "deployment": "compose" } }
    }`)
	env2, _, err := Generate(port80, "dev", nil, fixedRand)
	if err != nil {
		t.Fatal(err)
	}
	if got := ParseEnv([]byte(env2))["APP_URL"]; got != "http://localhost" {
		t.Errorf("APP_URL = %q, want http://localhost (empty domain, port 80)", got)
	}
}

// TestFrameworkEnvContract verifies the blueprint-declared env contract: a
// Laravel build service must get DB_*/REDIS_* keys wired from the managed deps
// (so an app reading standard Laravel env vars connects without hand-mapping),
// DB identifiers come from the dns-safe prefix (no display-name spaces), and the
// .env.example masks the generated DB_PASSWORD.
func TestFrameworkEnvContract(t *testing.T) {
	c := cfg(t, `{
      "project": { "name": "weather dashboard app", "registry": "r",
        "resource_prefix": "mcl_wda" },
      "services": [
        {"name":"backend","build":{"context":"./backend","template":"laravel"}},
        {"name":"worker","image_from":"backend","command":"php artisan queue:work"}
      ],
      "environments": { "dev": {
        "http_port": 8080, "database": "mysql", "redis_enabled": true,
        "deployment": "compose"
      } }
    }`)

	env, example, err := Generate(c, "dev", nil, fixedRand)
	if err != nil {
		t.Fatal(err)
	}
	m := ParseEnv([]byte(env))

	want := map[string]string{
		"DB_CONNECTION":  "mysql",
		"DB_HOST":        "mcl_wda_dev_mysql",
		"DB_PORT":        "3306",
		"DB_DATABASE":    "mcl_wda_dev",      // prefix-derived, NOT "weather dashboard app_dev"
		"DB_USERNAME":    "mcl_wda_user",     // must match the provisioned MYSQL_USER
		"REDIS_HOST":     "mcl_wda_dev_redis",
		"REDIS_PORT":     "6379",
		"MYSQL_DATABASE": "mcl_wda_dev", // mysql container provisioning vars stay
		"MYSQL_USER":     "mcl_wda_user",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	// DB_PASSWORD must equal the same secret as MYSQL_PASSWORD (one source).
	if m["DB_PASSWORD"] == "" || m["DB_PASSWORD"] != m["MYSQL_PASSWORD"] {
		t.Errorf("DB_PASSWORD = %q, want = MYSQL_PASSWORD %q", m["DB_PASSWORD"], m["MYSQL_PASSWORD"])
	}
	// Identifier-class values must never contain a space (would break dotenv).
	for k, v := range m {
		if (strings.HasPrefix(k, "DB_") || strings.HasPrefix(k, "MYSQL_") ||
			strings.HasPrefix(k, "REDIS_") || strings.HasPrefix(k, "POSTGRES_")) &&
			strings.Contains(v, " ") {
			t.Errorf("identifier env %s=%q contains a space", k, v)
		}
	}
	// .env.example masks the generated DB_PASSWORD.
	ex := ParseEnv([]byte(example))
	if !strings.HasPrefix(ex["DB_PASSWORD"], "CHANGE_ME") {
		t.Errorf("example DB_PASSWORD = %q, want CHANGE_ME*", ex["DB_PASSWORD"])
	}
}

// TestFrameworkEnvURLRedaction verifies a DATABASE_URL framework value carries
// real credentials in .env but is redacted in .env.example.
func TestFrameworkEnvURLRedaction(t *testing.T) {
	c := cfg(t, `{
      "project": { "name": "api", "registry": "r", "resource_prefix": "ws_api" },
      "services": [ {"name":"app","build":{"context":".","template":"nodejs"}} ],
      "environments": { "prod": { "database": "postgres", "deployment": "compose" } }
    }`)
	env, example, err := Generate(c, "prod", nil, fixedRand)
	if err != nil {
		t.Fatal(err)
	}
	got := ParseEnv([]byte(env))["DATABASE_URL"]
	if !strings.HasPrefix(got, "postgresql://ws_api_user:") || !strings.Contains(got, "@ws_api_prod_postgres:5432/ws_api_prod") {
		t.Errorf("DATABASE_URL = %q, want postgresql://ws_api_user:<pass>@ws_api_prod_postgres:5432/ws_api_prod", got)
	}
	exURL := ParseEnv([]byte(example))["DATABASE_URL"]
	if !strings.Contains(exURL, ":CHANGE_ME@") {
		t.Errorf("example DATABASE_URL = %q, want password redacted to CHANGE_ME", exURL)
	}
}

func TestCustomDevAndMysqlAndGarage(t *testing.T) {
	c := cfg(t, `{
      "project": { "name": "app", "type": "custom", "registry": "reg",
        "version": { "major": 0, "minor": 1, "patch": 0, "build": 0 } },
      "environments": { "dev": {
        "domain": "dev.app", "backend": "php", "frontend_enabled": false,
        "database": "mysql", "redis_enabled": false, "garage_enabled": true,
        "deployment": "compose", "replicas": { "backend": 1, "frontend": 1 }
      } }
    }`)

	env, _, err := Generate(c, "dev", nil, fixedRand)
	if err != nil {
		t.Fatal(err)
	}
	m := ParseEnv([]byte(env))

	if m["APP_DEBUG"] != "true" {
		t.Errorf("dev APP_DEBUG = %q, want true", m["APP_DEBUG"])
	}
	if m["NODE_ENV"] != "development" {
		t.Errorf("dev NODE_ENV = %q, want development", m["NODE_ENV"])
	}
	if m["MYSQL_HOST"] != "app_dev_mysql" || m["MYSQL_DATABASE"] != "app_dev" {
		t.Errorf("mysql block wrong: HOST=%q DB=%q", m["MYSQL_HOST"], m["MYSQL_DATABASE"])
	}
	if _, ok := m["MYSQL_ROOT_PASSWORD"]; !ok {
		t.Error("MYSQL_ROOT_PASSWORD missing for mysql")
	}
	if _, ok := m["FRONTEND_IMAGE"]; ok {
		t.Error("FRONTEND_IMAGE present but frontend disabled")
	}
	if m["GARAGE_BUCKET"] != "app-dev" {
		t.Errorf("GARAGE_BUCKET = %q, want app-dev", m["GARAGE_BUCKET"])
	}
	if _, ok := m["REDIS_HOST"]; ok {
		t.Error("REDIS_HOST present but redis disabled")
	}
}

// A free-form project display name with spaces must be quoted in the .env so a
// strict dotenv parser (phpdotenv, when the .env is mounted via env_file_mount)
// doesn't choke on "unexpected whitespace". Docker's env_file stays happy too.
func TestProjectNameQuotedWhenSpaced(t *testing.T) {
	c := cfg(t, `{
      "project": { "name": "weather dashboard app", "type": "custom", "registry": "reg",
        "version": { "major": 0, "minor": 1, "patch": 0, "build": 0 } },
      "environments": { "dev": { "domain": "dev.app", "database": "none",
        "deployment": "compose" } }
    }`)
	env, _, err := Generate(c, "dev", nil, fixedRand)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env, `PROJECT_NAME="weather dashboard app"`) {
		t.Errorf("PROJECT_NAME with spaces must be quoted:\n%s", env)
	}
	// A plain name stays unquoted (no churn for the common case).
	c2 := cfg(t, `{
      "project": { "name": "app", "type": "custom", "registry": "reg",
        "version": { "major": 0, "minor": 1, "patch": 0, "build": 0 } },
      "environments": { "dev": { "domain": "dev.app", "database": "none", "deployment": "compose" } }
    }`)
	env2, _, err := Generate(c2, "dev", nil, fixedRand)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env2, "PROJECT_NAME=app\n") {
		t.Errorf("plain PROJECT_NAME should be unquoted:\n%s", env2)
	}
}

// TestRebaseImageRegistry covers re-basing .env image pointers to a new registry —
// the fix for "changing the project registry was silently ignored, so compose kept
// pulling stale {SVC}_IMAGE values from the old registry".
func TestRebaseImageRegistry(t *testing.T) {
	const prefix = "mcl_kyt"
	in := []byte("REGISTRY=ghcr.io/old/ns\n" +
		"IMAGE_TAG=1.0.0-build.0-dev\n" +
		"BACKEND_IMAGE=ghcr.io/old/ns/mcl_kyt-backend:1.0.0-build.0-dev\n" +
		"FRONTEND_IMAGE=ghcr.io/old/ns/mcl_kyt-frontend:1.0.0-build.0-dev\n" +
		"DOMAIN=x\n")

	// → local (no registry): strip the prefix, keep the local tag + version.
	out, changed := RebaseImageRegistry(in, "", prefix)
	if !changed {
		t.Fatal("expected change when going local")
	}
	m := ParseEnv(out)
	if m["REGISTRY"] != "" {
		t.Errorf("REGISTRY = %q, want empty", m["REGISTRY"])
	}
	if m["BACKEND_IMAGE"] != "mcl_kyt-backend:1.0.0-build.0-dev" {
		t.Errorf("BACKEND_IMAGE = %q, want local tag", m["BACKEND_IMAGE"])
	}
	if m["IMAGE_TAG"] != "1.0.0-build.0-dev" || m["DOMAIN"] != "x" {
		t.Errorf("non-image lines must be untouched: %+v", m)
	}

	// → a new registry: re-prefix with it, version preserved.
	out2, changed2 := RebaseImageRegistry(out, "registry.example.com", prefix)
	if !changed2 {
		t.Fatal("expected change when setting a registry")
	}
	m2 := ParseEnv(out2)
	if m2["FRONTEND_IMAGE"] != "registry.example.com/mcl_kyt-frontend:1.0.0-build.0-dev" {
		t.Errorf("FRONTEND_IMAGE = %q, want re-prefixed", m2["FRONTEND_IMAGE"])
	}

	// Idempotent: re-basing to the same registry changes nothing.
	if _, changed3 := RebaseImageRegistry(out2, "registry.example.com", prefix); changed3 {
		t.Error("expected no change re-basing to the same registry")
	}
}

// A scanned repo's .env.example routinely ships MYSQL_*/DB_* defaults. Those land in
// the env's config.json env_vars and MUST NOT override Rigger's managed-DB contract —
// otherwise the DB container initializes with the repo's creds while the app connects
// with Rigger's (the "Access denied" failure). Non-infra extras still pass through.
func TestExtraVarsDoNotOverrideManagedDB(t *testing.T) {
	c := cfg(t, `{
      "project": { "name": "myapp",
        "version": { "major": 1, "minor": 0, "patch": 0, "build": 0 } },
      "services": [{"name":"backend","build":{"template":"laravel"}}],
      "environments": { "dev": {
        "database": "mysql", "deployment": "compose",
        "env_vars": {
          "MYSQL_USER": "weather", "MYSQL_PASSWORD": "weatherpass",
          "MYSQL_DATABASE": "weather_dashboard", "DB_PASSWORD": "weatherpass",
          "MAIL_HOST": "smtp.example.com", "WEATHER_LAT": "51.5"
        }
      } }
    }`)
	env, _, err := Generate(c, "dev", nil, fixedRand)
	if err != nil {
		t.Fatal(err)
	}
	m := ParseEnv([]byte(env))

	// Managed DB creds win; the repo defaults must not leak in or duplicate.
	if m["MYSQL_USER"] != "myapp_user" {
		t.Errorf("MYSQL_USER=%q, want managed myapp_user", m["MYSQL_USER"])
	}
	if m["MYSQL_PASSWORD"] == "weatherpass" || m["MYSQL_DATABASE"] == "weather_dashboard" {
		t.Errorf("repo MYSQL_* overrode the managed contract: %v", m)
	}
	if n := strings.Count(env, "\nMYSQL_USER="); n != 1 {
		t.Errorf("MYSQL_USER emitted %d times, want 1 (duplicate keys let the last win)", n)
	}
	// Framework DB contract (DB_PASSWORD) is also protected.
	if m["DB_PASSWORD"] == "weatherpass" {
		t.Errorf("DB_PASSWORD overridden by extra var: %q", m["DB_PASSWORD"])
	}
	// Non-infra extras remain user-overridable.
	if m["MAIL_HOST"] != "smtp.example.com" || m["WEATHER_LAT"] != "51.5" {
		t.Errorf("non-infra extras should pass through: MAIL_HOST=%q WEATHER_LAT=%q", m["MAIL_HOST"], m["WEATHER_LAT"])
	}
}
