package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// imageConfig is a minimal pull-image (no build services) config.json — so
// bootstrap should write .env + compose but no Dockerfiles or nginx.conf.
const imageConfig = `{
  "project": { "name": "wp" },
  "services": [ { "name": "app", "image": "wordpress", "tag": "latest", "port": "80", "host_port": "8080", "env_file": true } ],
  "environments": { "prod": { "env_vars": { "WP_PASSWORD": "CHANGE_ME", "WP_PORT": "8080" } } }
}`

func TestBootstrapImageStack(t *testing.T) {
	wsDir := t.TempDir()
	tmplDir := t.TempDir() // unused for image stacks
	wsRoot := filepath.Join(wsDir, "ws", "projects", "wp")
	if err := os.MkdirAll(wsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsRoot, "config.json"), []byte(imageConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := Bootstrap(wsDir, tmplDir, "ws", "wp", "prod", false, "", "", "", "", &out); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	envDir := filepath.Join(wsRoot, "envs", "prod")
	for _, f := range []string{".env", ".env.example", "docker-compose.yml"} {
		if _, err := os.Stat(filepath.Join(envDir, f)); err != nil {
			t.Errorf("expected %s to be generated: %v", f, err)
		}
	}
	// Image stacks must NOT produce Dockerfiles or nginx.
	for _, f := range []string{"backend", "nginx.conf"} {
		if _, err := os.Stat(filepath.Join(envDir, f)); err == nil {
			t.Errorf("image stack should not generate %s", f)
		}
	}
	// Secret placeholder was resolved in .env.
	envContent, _ := os.ReadFile(filepath.Join(envDir, ".env"))
	if strings.Contains(string(envContent), "WP_PASSWORD=CHANGE_ME") {
		t.Error(".env still has placeholder WP_PASSWORD")
	}
}

// Regression: env-var edits must be mirrored into config.json so they survive a
// refresh (which regenerates .env FROM config.json). Without this, an edit to e.g.
// AP_FRONTEND_URL — or a bundled DB password — reverts to the create-time seed on
// the next refresh, breaking the running stack.
func TestUpdateConfigEnvVars(t *testing.T) {
	wsDir := t.TempDir()
	wsRoot := filepath.Join(wsDir, "ws", "projects", "act")
	if err := os.MkdirAll(wsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	const cfgJSON = `{
  "project": { "name": "act", "type": "image" },
  "environments": { "dev": { "deployment": "compose", "env_vars": {
    "AP_FRONTEND_URL": "http://localhost:8080",
    "POSTGRES_PASSWORD": "rigger-original",
    "AP_JWT_SECRET": "keepme"
  } } }
}`
	cfgPath := filepath.Join(wsRoot, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	updates := map[string]string{
		"AP_FRONTEND_URL":   "http://act.example.com", // user edit must persist
		"POSTGRES_PASSWORD": "rigger-swarm-secret",    // secured below → must NOT land in config
		"NEW_KEY":           "added",
	}
	skip := map[string]bool{"POSTGRES_PASSWORD": true} // secured as a Docker secret
	if err := UpdateConfigEnvVars(wsDir, "ws", "act", "dev", updates, []string{"AP_JWT_SECRET"}, skip); err != nil {
		t.Fatalf("UpdateConfigEnvVars: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Project      map[string]any `json:"project"`
		Environments map[string]struct {
			Deployment string            `json:"deployment"`
			EnvVars    map[string]string `json:"env_vars"`
		} `json:"environments"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("config.json no longer valid JSON: %v", err)
	}
	ev := root.Environments["dev"].EnvVars
	if ev["AP_FRONTEND_URL"] != "http://act.example.com" {
		t.Errorf("AP_FRONTEND_URL = %q, want the persisted edit", ev["AP_FRONTEND_URL"])
	}
	if ev["NEW_KEY"] != "added" {
		t.Errorf("NEW_KEY = %q, want added", ev["NEW_KEY"])
	}
	if _, ok := ev["AP_JWT_SECRET"]; ok {
		t.Error("AP_JWT_SECRET should have been deleted from config env_vars")
	}
	if _, ok := ev["POSTGRES_PASSWORD"]; ok {
		t.Error("POSTGRES_PASSWORD is secured as a Docker secret — must NOT be written to config.json")
	}
	// Other config fields must be preserved.
	if root.Project["name"] != "act" {
		t.Errorf("project.name = %v, want act (other fields must survive)", root.Project["name"])
	}
	if root.Environments["dev"].Deployment != "compose" {
		t.Errorf("env deployment = %q, want compose (other env fields must survive)", root.Environments["dev"].Deployment)
	}
}

// Regression: the create/bootstrap compose must honour the auto-URL settings, so a
// brand-new Traefik env is routed at the configured magic-DNS host — not silently
// `.localhost` until a manual Refresh.
func TestBootstrapThreadsAutoURL(t *testing.T) {
	wsDir := t.TempDir()
	wsRoot := filepath.Join(wsDir, "ws", "projects", "wp")
	envDir := filepath.Join(wsRoot, "envs", "prod")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{
  "project": { "name": "wp" },
  "services": [ { "name": "app", "image": "wordpress", "tag": "latest", "port": "80", "web_routed": true, "host_port": "8080" } ],
  "environments": { "prod": { "traefik_enabled": true, "traefik_network": "rigger-traefik" } }
}`
	os.WriteFile(filepath.Join(wsRoot, "config.json"), []byte(cfg), 0o644) //nolint:errcheck
	if err := Bootstrap(wsDir, t.TempDir(), "ws", "wp", "prod", false, "", "", "nip", "10.10.10.111", nil); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	compose, _ := os.ReadFile(filepath.Join(envDir, "docker-compose.yml"))
	if !strings.Contains(string(compose), ".10.10.10.111.nip.io`)") {
		t.Errorf("expected a nip.io Host rule in the bootstrap compose, got:\n%s", compose)
	}
	if strings.Contains(string(compose), ".localhost`)") {
		t.Errorf("compose should NOT fall back to .localhost when auto-URL is configured:\n%s", compose)
	}
}

func TestBootstrapPreservesEnvWithoutRegen(t *testing.T) {
	wsDir := t.TempDir()
	wsRoot := filepath.Join(wsDir, "ws", "projects", "wp")
	envDir := filepath.Join(wsRoot, "envs", "prod")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wsRoot, "config.json"), []byte(imageConfig), 0o644) //nolint:errcheck
	os.WriteFile(filepath.Join(envDir, ".env"), []byte("SENTINEL=keepme\n"), 0o644) //nolint:errcheck

	if err := Bootstrap(wsDir, t.TempDir(), "ws", "wp", "prod", false, "", "", "", "", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(envDir, ".env"))
	if !strings.Contains(string(got), "SENTINEL=keepme") {
		t.Errorf(".env was regenerated without regenEnv; got:\n%s", got)
	}
}

func TestRenderNginxSubstitution(t *testing.T) {
	tmplDir := t.TempDir()
	nginxDir := filepath.Join(tmplDir, "nginx")
	os.MkdirAll(nginxDir, 0o755) //nolint:errcheck
	tmpl := "server { server_name {{DOMAIN}}; # {{PREFIX}} {{PROJECT}} {{ENV}}\n}"
	os.WriteFile(filepath.Join(nginxDir, "nodejs.conf"), []byte(tmpl), 0o644) //nolint:errcheck

	outDir := t.TempDir()
	if err := renderNginx(tmplDir, outDir, "nodejs", "ex.com", "myapp_prod", "myapp", "prod"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(outDir, "nginx.conf"))
	want := "server { server_name ex.com; # myapp_prod myapp prod\n}"
	if string(got) != want {
		t.Errorf("nginx render = %q, want %q", got, want)
	}
}

func TestInstallDockerfilePrefersDevVariant(t *testing.T) {
	tmplDir := t.TempDir()
	os.WriteFile(filepath.Join(tmplDir, "Dockerfile"), []byte("PROD"), 0o644)         //nolint:errcheck
	os.WriteFile(filepath.Join(tmplDir, "Dockerfile.dev"), []byte("DEV"), 0o644)      //nolint:errcheck
	os.WriteFile(filepath.Join(tmplDir, ".dockerignore"), []byte("ig"), 0o644)        //nolint:errcheck
	os.WriteFile(filepath.Join(tmplDir, ".dockerignore.dev"), []byte("igdev"), 0o644) //nolint:errcheck

	dev := t.TempDir()
	if err := installDockerfile(tmplDir, dev, "dev"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dev, "Dockerfile")); string(b) != "DEV" {
		t.Errorf("dev Dockerfile = %q, want DEV", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dev, ".dockerignore")); string(b) != "igdev" {
		t.Errorf("dev .dockerignore = %q, want igdev", b)
	}

	prod := t.TempDir()
	if err := installDockerfile(tmplDir, prod, "prod"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(prod, "Dockerfile")); string(b) != "PROD" {
		t.Errorf("prod Dockerfile = %q, want PROD", b)
	}
	if b, _ := os.ReadFile(filepath.Join(prod, ".dockerignore")); string(b) != "ig" {
		t.Errorf("prod .dockerignore = %q, want ig", b)
	}
}
