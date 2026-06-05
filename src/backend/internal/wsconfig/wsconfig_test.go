package wsconfig

import (
	"reflect"
	"testing"
)

const sample = `{
  "project": { "name": "myapp", "type": "custom", "registry": "registry.example.com",
    "version": { "major": 1, "minor": 2, "patch": 3, "build": 7 } },
  "versions": { "postgres": "16-alpine" },
  "environments": {
    "prod": { "domain": "myapp.com", "http_port": 80, "https_port": "443",
      "backend": "nodejs", "database": "postgres", "deployment": "swarm",
      "replicas": { "backend": 3, "frontend": 2 },
      "env_vars": { "API_PORT": 8080, "API_KEY": "CHANGE_ME" } },
    "dev": { "domain": "dev.myapp.com", "backend": "nodejs", "deployment": "compose" }
  }
}`

func parse(t *testing.T) *Config {
	t.Helper()
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func TestVersionString(t *testing.T) {
	if got := parse(t).VersionString(); got != "1.2.3-build.7" {
		t.Errorf("VersionString = %q, want 1.2.3-build.7", got)
	}
}

func TestImageTag(t *testing.T) {
	got := parse(t).ImageTag("backend", "prod")
	want := "registry.example.com/myapp-backend:1.2.3-build.7-prod"
	if got != want {
		t.Errorf("ImageTag = %q, want %q", got, want)
	}
}

func TestStackName(t *testing.T) {
	if got := parse(t).StackName("prod"); got != "myapp_prod" {
		t.Errorf("StackName = %q, want myapp_prod", got)
	}
}

func TestEnvNamesSorted(t *testing.T) {
	got := parse(t).EnvNames()
	want := []string{"dev", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnvNames = %v, want %v", got, want)
	}
}

func TestValidateEnv(t *testing.T) {
	c := parse(t)
	if err := c.ValidateEnv("prod"); err != nil {
		t.Errorf("ValidateEnv(prod) = %v, want nil", err)
	}
	if err := c.ValidateEnv("staging"); err == nil {
		t.Error("ValidateEnv(staging) = nil, want error")
	}
}

func TestProjectTypeDefault(t *testing.T) {
	c, _ := Parse([]byte(`{"project":{"name":"x"}}`))
	if got := c.ProjectType(); got != "custom" {
		t.Errorf("ProjectType = %q, want custom", got)
	}
}

func TestStrFromNumberAndString(t *testing.T) {
	c := parse(t)
	prod := c.Environments["prod"]
	// http_port came in as a JSON number, https_port as a string.
	if prod.HTTPPort.String() != "80" {
		t.Errorf("HTTPPort = %q, want 80", prod.HTTPPort)
	}
	if prod.HTTPSPort.String() != "443" {
		t.Errorf("HTTPSPort = %q, want 443", prod.HTTPSPort)
	}
	if prod.Replicas.Backend.String() != "3" {
		t.Errorf("Replicas.Backend = %q, want 3", prod.Replicas.Backend)
	}
	// env_vars values: number renders without decimal, matching jq -r.
	if prod.EnvVars["API_PORT"].String() != "8080" {
		t.Errorf("env_vars API_PORT = %q, want 8080", prod.EnvVars["API_PORT"])
	}
	if prod.EnvVars["API_KEY"].String() != "CHANGE_ME" {
		t.Errorf("env_vars API_KEY = %q, want CHANGE_ME", prod.EnvVars["API_KEY"])
	}
}

func TestVersionFallback(t *testing.T) {
	c := parse(t)
	if got := c.Version("postgres", "15"); got != "16-alpine" {
		t.Errorf("Version(postgres) = %q, want 16-alpine", got)
	}
	if got := c.Version("redis", "7-alpine"); got != "7-alpine" {
		t.Errorf("Version(redis) fallback = %q, want 7-alpine", got)
	}
}
