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
	if ex["MYSQL_PASSWORD"] != "CHANGE_ME" || ex["API_TOKEN"] != "CHANGE_ME" {
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
      "project": { "name": "myapp", "type": "custom", "registry": "reg",
        "version": { "major": 1, "minor": 0, "patch": 0, "build": 2 } },
      "environments": { "prod": {
        "domain": "myapp.com", "http_port": 80, "https_port": 443,
        "backend": "nodejs", "frontend_enabled": true, "frontend": "react",
        "database": "postgres", "redis_enabled": true, "garage_enabled": false,
        "deployment": "compose", "replicas": { "backend": 2, "frontend": 1 },
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
		"BACKEND_REPLICAS":     "2",
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
