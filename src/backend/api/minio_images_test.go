package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteMinIOComposeImages(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"    image: minio/minio:latest", "    image: pgsty/minio:latest"},
		{"    image: minio/mc:latest", "    image: pgsty/mc:latest"},
		{"    image: minio/minio", "    image: pgsty/minio"},
		{"    image: quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z", "    image: pgsty/minio:RELEASE.2025-09-07T16-13-09Z"},
		{"    image: docker.io/minio/mc:latest", "    image: pgsty/mc:latest"},
		{`    image: "minio/minio:latest"`, `    image: "pgsty/minio:latest"`},
		{"    image: 'minio/mc'", "    image: 'pgsty/mc'"},
		{"    image: minio/minio:latest  # pinned by hand", "    image: pgsty/minio:latest  # pinned by hand"},
		// Not MinIO's withdrawn server or client: left alone.
		{"    image: pgsty/minio:latest", "    image: pgsty/minio:latest"},
		{"    image: opens3/console:latest", "    image: opens3/console:latest"},
		{"    image: minio/minio-operator:v5", "    image: minio/minio-operator:v5"},
		{"    image: bitnami/minio:latest", "    image: bitnami/minio:latest"},
		{"    image: ${MINIO_IMAGE:-minio/minio}", "    image: ${MINIO_IMAGE:-minio/minio}"},
		{"    # image: minio/minio:latest", "    # image: minio/minio:latest"},
		{"    labels: [minio/minio:latest]", "    labels: [minio/minio:latest]"},
	} {
		got, _ := rewriteMinIOImages([]byte(c.in), upstreamMinIOComposeRe)
		if string(got) != c.want {
			t.Errorf("%q\n  got  %q\n  want %q", c.in, got, c.want)
		}
	}
}

// A real environment's compose, as v0.1.48 generated it: only the two image
// lines change, byte for byte, and a second pass changes nothing.
func TestRewriteMinIOComposeLeavesTheRestAlone(t *testing.T) {
	in := strings.Join([]string{
		"services:",
		"  app:",
		"    image: ${APP_IMAGE:-localhost:5001/mcl_sdm-app:1.0.0-build.7-dev}",
		"  minio:",
		"    image: minio/minio:latest",
		"    container_name: mcl_sdm_dev_minio",
		`    command: server /data --console-address ":9001"`,
		"  minio_init:",
		"    image: minio/mc:latest",
		`    entrypoint: ["/bin/sh", "-c", "until mc alias set rigger http://minio:9000 \"$$MINIO_ROOT_USER\"; do sleep 2; done"]`,
		"  storage_console:",
		"    image: opens3/console:latest",
		"    environment:",
		"      - CONSOLE_MINIO_SERVER=http://minio:9000",
		"",
	}, "\n")
	want := strings.Replace(strings.Replace(in, "image: minio/minio:latest", "image: pgsty/minio:latest", 1),
		"image: minio/mc:latest", "image: pgsty/mc:latest", 1)

	got, n := rewriteMinIOImages([]byte(in), upstreamMinIOComposeRe)
	if n != 2 || string(got) != want {
		t.Fatalf("rewrote %d reference(s); got\n%s", n, got)
	}
	if again, n := rewriteMinIOImages(got, upstreamMinIOComposeRe); n != 0 || string(again) != want {
		t.Errorf("a second pass changed %d reference(s)", n)
	}
}

func TestRewriteMinIOConfigImages(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`"image": "minio/minio"`, `"image": "pgsty/minio"`},
		{`"image":"quay.io/minio/mc:latest"`, `"image":"pgsty/mc:latest"`},
		{`"image": "minio/minio-operator"`, `"image": "minio/minio-operator"`},
		{`"label": "minio/minio"`, `"label": "minio/minio"`},
	} {
		got, _ := rewriteMinIOImages([]byte(c.in), upstreamMinIOConfigRe)
		if string(got) != c.want {
			t.Errorf("%s\n  got  %s\n  want %s", c.in, got, c.want)
		}
	}
}

// The boot step finds every environment's compose file and every project's
// config.json in the workspaces tree, rewrites them in place, and is a no-op on
// the next boot.
func TestMigrateMinIOImagesRewritesExistingEnvironments(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) string {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	devCompose := write("mcl/projects/sdm/envs/dev/docker-compose.yml",
		"services:\n  minio:\n    image: minio/minio:latest\n  minio_init:\n    image: minio/mc:latest\n")
	prodCompose := write("mcl/projects/sdm/envs/prod/docker-compose.yml",
		"services:\n  minio:\n    image: quay.io/minio/minio:latest\n")
	stackConfig := write("tec/projects/files/config.json",
		`{"services": [{"name": "app", "image": "minio/minio", "tag": "latest"}]}`)
	untouched := write("tec/projects/blog/envs/dev/docker-compose.yml",
		"services:\n  web:\n    image: nginx:1.27-alpine\n")
	write("README.txt", "a stray file at the workspaces root")

	h := &Handler{workspacesDir: root}
	h.MigrateMinIOImages()

	for path, want := range map[string]string{
		devCompose:  "services:\n  minio:\n    image: pgsty/minio:latest\n  minio_init:\n    image: pgsty/mc:latest\n",
		prodCompose: "services:\n  minio:\n    image: pgsty/minio:latest\n",
		stackConfig: `{"services": [{"name": "app", "image": "pgsty/minio", "tag": "latest"}]}`,
		untouched:   "services:\n  web:\n    image: nginx:1.27-alpine\n",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s:\n%s\nwant:\n%s", path, got, want)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(root, "*", "projects", "*", "envs", "*", "*.tmp")); len(leftovers) > 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}

	before, _ := os.Stat(devCompose)
	h.MigrateMinIOImages()
	after, _ := os.Stat(devCompose)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the second boot rewrote a file that was already migrated")
	}
}
