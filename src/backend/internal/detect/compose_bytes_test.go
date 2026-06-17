package detect

import "testing"

// Pasting a multi-image compose should map cleanly to image services, fold a recognised
// data service into a managed dependency, pick a web entry, and parse ports/env.
func TestDetectComposeBytes(t *testing.T) {
	yaml := `
services:
  web:
    image: nginx:1.27-alpine
    ports:
      - "8080:80"
    environment:
      FOO: bar
  api:
    image: ghcr.io/acme/api:v2
    ports: ["9000:9000"]
  db:
    image: postgres:16
`
	d, err := DetectComposeBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	byName := map[string]Service{}
	for _, s := range d.Services {
		byName[s.Name] = s
	}
	if len(d.Services) != 2 {
		t.Fatalf("want 2 app services (web, api), got %d: %+v", len(d.Services), d.Services)
	}
	web, ok := byName["web"]
	if !ok || web.Image != "nginx" || web.Tag != "1.27-alpine" {
		t.Errorf("web image/tag wrong: %+v", web)
	}
	if web.Port != "80" || web.HostPort != "8080" {
		t.Errorf("web ports wrong: port=%q host=%q", web.Port, web.HostPort)
	}
	if web.EnvVars["FOO"] != "bar" {
		t.Errorf("web env not parsed: %+v", web.EnvVars)
	}
	if !web.WebRouted { // publishes :80 → picked as web entry
		t.Errorf("expected web to be the web entry")
	}
	if api := byName["api"]; api.Image != "ghcr.io/acme/api" || api.Tag != "v2" {
		t.Errorf("api registry image split wrong: %+v", api)
	}
	// postgres folds into a managed dependency, not an app service.
	if d.Database != "postgres" {
		t.Errorf("expected postgres managed dep, got %q", d.Database)
	}
	if _, isApp := byName["db"]; isApp {
		t.Errorf("db should be a managed dep, not an app service")
	}
}

func TestDetectComposeBytesErrors(t *testing.T) {
	if _, err := DetectComposeBytes([]byte("not: [valid")); err == nil {
		t.Error("expected error on invalid YAML")
	}
	if _, err := DetectComposeBytes([]byte("version: '3'\n")); err == nil {
		t.Error("expected error when no services present")
	}
}
