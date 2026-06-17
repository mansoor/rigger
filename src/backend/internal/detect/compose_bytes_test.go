package detect

import "testing"

// Pasting a multi-image compose for the IMAGE stack keeps EVERY service as a plain
// image entry — including data services like postgres (the image stack has no managed-
// dependency concept, so folding would silently drop them). Ports/env/tags still parse.
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
	if len(d.Services) != 3 {
		t.Fatalf("want 3 image services (web, api, db), got %d: %+v", len(d.Services), d.Services)
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
	// postgres is kept as a plain image service — NOT folded into a managed dependency.
	if db, ok := byName["db"]; !ok || db.Image != "postgres" || db.Tag != "16" {
		t.Errorf("db should be a plain image service postgres:16, got %+v", db)
	}
	if d.Database != "none" {
		t.Errorf("expected no managed dep folding for the image stack, got database=%q", d.Database)
	}
	if len(d.ManagedCandidates) != 0 {
		t.Errorf("expected no managed candidates, got %d", len(d.ManagedCandidates))
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
