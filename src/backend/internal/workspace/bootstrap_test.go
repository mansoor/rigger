package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// imageConfig is a minimal image-stack config.json that composegen can render.
const imageConfig = `{
  "project": { "name": "wp", "type": "image" },
  "images": [ { "name": "app", "image": "wordpress", "tag": "latest", "port": 80, "host_port": 8080 } ],
  "environments": { "prod": { "env_vars": { "WP_PASSWORD": "CHANGE_ME", "WP_PORT": "8080" } } }
}`

func TestBootstrapImageStack(t *testing.T) {
	wsDir := t.TempDir()
	tmplDir := t.TempDir() // unused for image stacks
	wsRoot := filepath.Join(wsDir, "wp")
	if err := os.MkdirAll(wsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsRoot, "config.json"), []byte(imageConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := Bootstrap(wsDir, tmplDir, "wp", "prod", false, &out); err != nil {
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

func TestBootstrapPreservesEnvWithoutRegen(t *testing.T) {
	wsDir := t.TempDir()
	wsRoot := filepath.Join(wsDir, "wp")
	envDir := filepath.Join(wsRoot, "envs", "prod")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wsRoot, "config.json"), []byte(imageConfig), 0o644) //nolint:errcheck
	os.WriteFile(filepath.Join(envDir, ".env"), []byte("SENTINEL=keepme\n"), 0o644) //nolint:errcheck

	if err := Bootstrap(wsDir, t.TempDir(), "wp", "prod", false, nil); err != nil {
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
