package imagecheck

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mansoor/rigger/ui/internal/wspath"
)

// writeProject lays down a project config the way Rigger stores one.
func writeProject(t *testing.T, root, ws, project, body string) {
	t.Helper()
	path := wspath.ConfigPath(root, ws, project)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeCompose lays down a generated compose file for one environment — the
// source Check actually reads.
func writeCompose(t *testing.T, root, ws, project, env, body string) {
	t.Helper()
	dir := wspath.EnvDir(root, ws, project, env)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func serviceNames(ups []ServiceUpdate) []string {
	out := make([]string, 0, len(ups))
	for _, u := range ups {
		out = append(out, u.Service)
	}
	return out
}

// The bug this fixes: a custom (git-source / upload / stack) project returned no
// results at all, because Check bailed out unless project.type was "image". A
// project that builds its own app still runs mongo, rabbitmq and a proxy beside
// it, and those were never checked — no badge, no button, no signal.
//
// Digest comparison needs a registry, which a test has no business reaching, so
// this asserts what gets ENUMERATED: the pulled services are considered, the
// built ones are not.
//
// No compose file here, so this also covers the config fallback for an
// environment that has never been generated.
func TestCheckEnumeratesPulledServicesInCustomProject(t *testing.T) {
	root := t.TempDir()
	// Shape of a real custom project: three built services, three pulled.
	writeProject(t, root, "mcl", "farm", `{
	  "project": {"type": "custom"},
	  "services": [
	    {"name": "backend",      "image": "",            "tag": ""},
	    {"name": "celeryworker", "image": "",            "tag": ""},
	    {"name": "flower",       "image": "mher/flower", "tag": "2.0.1"},
	    {"name": "frontend",     "image": "",            "tag": ""},
	    {"name": "mongodb",      "image": "mongo",       "tag": "latest"},
	    {"name": "queue",        "image": "rabbitmq",    "tag": "3"}
	  ],
	  "environments": {"prod": {}}
	}`)

	got := Check(root, "mcl", "farm", "prod")
	if got == nil {
		t.Fatal("a custom project must be checked, not skipped outright")
	}
	if len(got) != 3 {
		t.Fatalf("expected the 3 pulled services, got %v", serviceNames(got))
	}
	for i, want := range []string{"flower", "mongodb", "queue"} {
		if got[i].Service != want {
			t.Errorf("result %d = %q, want %q", i, got[i].Service, want)
		}
	}
	// Services with no upstream image are excluded: there is nothing to compare a
	// locally-built image against, and the answer for one is Build, not pull.
	for _, u := range got {
		if u.Image == "" {
			t.Errorf("%s has no upstream image and should not have been checked", u.Service)
		}
	}
}

// A project whose services are ALL built must return an empty, non-nil result.
// Callers cache only non-nil, so nil here would mean the entry is never stored
// and the UI polls every 4s forever waiting for a result that cannot arrive.
func TestCheckReturnsEmptyNotNilWhenNothingPullable(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "mcl", "sdm", `{
	  "project": {"type": "custom"},
	  "services": [{"name": "app", "image": "", "tag": ""}],
	  "environments": {"dev": {}}
	}`)

	got := Check(root, "mcl", "sdm", "dev")
	if got == nil {
		t.Fatal("nil means 'could not read config' — an all-built project is a real, cacheable answer")
	}
	if len(got) != 0 {
		t.Errorf("expected no results, got %v", serviceNames(got))
	}
}

// nil stays reserved for "couldn't read it", so a genuinely broken config is not
// cached as a confident empty answer.
func TestCheckReturnsNilOnUnreadableConfig(t *testing.T) {
	root := t.TempDir()
	if got := Check(root, "mcl", "missing", "dev"); got != nil {
		t.Errorf("absent config = %v, want nil", got)
	}
	writeProject(t, root, "mcl", "broken", `{not json`)
	if got := Check(root, "mcl", "broken", "dev"); got != nil {
		t.Errorf("malformed config = %v, want nil", got)
	}
}

// Older image stacks stored their images under `images` rather than `services`.
func TestCheckFallsBackToLegacyImagesKey(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "mcl", "old", `{
	  "project": {"type": "image"},
	  "images": [{"name": "app", "image": "nginx", "tag": "1.27"}],
	  "environments": {"prod": {}}
	}`)

	got := Check(root, "mcl", "old", "prod")
	if len(got) != 1 || got[0].Service != "app" || got[0].Image != "nginx" {
		t.Fatalf("legacy images key not read: %v", got)
	}
	// An empty tag defaults to latest rather than producing a bare "nginx:".
	writeProject(t, root, "mcl", "notag", `{
	  "project": {"type": "image"},
	  "images": [{"name": "app", "image": "nginx", "tag": ""}],
	  "environments": {"prod": {}}
	}`)
	if got := Check(root, "mcl", "notag", "prod"); len(got) != 1 || got[0].Tag != "latest" {
		t.Errorf("empty tag = %v, want latest", got)
	}
}

// The real gap the config-based enumeration left. sdm declares ONE service (its
// built app) but its environment runs eight images: mysql, redis, minio, the
// minio init one-shot, adminer, the storage console and mailpit are all
// synthesised by composegen and appear nowhere in project config.
//
// Reading compose is what makes them checkable — and it must still exclude the
// built app, which reaches compose as a ${APP_IMAGE} pointer rather than a
// registry reference.
func TestCheckReadsGeneratedComposeForManagedServices(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "mcl", "sdm", `{
	  "project": {"type": "custom"},
	  "services": [{"name": "app", "image": "", "tag": ""}],
	  "environments": {"dev": {}}
	}`)
	writeCompose(t, root, "mcl", "sdm", "dev", `services:
  app:
    image: ${APP_IMAGE:-localhost:5001/mcl_sdm-app:1.0.0-build.7-dev}
  mysql:
    image: mysql:8.0
  redis:
    image: redis:7-alpine
  minio:
    image: minio/minio:latest
  minio_init:
    image: minio/mc:latest
  adminer:
    image: adminer:4.8.1
  storage_console:
    image: opens3/console:latest
  mailpit:
    image: axllent/mailpit:latest
`)

	got := Check(root, "mcl", "sdm", "dev")
	if len(got) != 7 {
		t.Fatalf("expected the 7 pulled services, got %d: %v", len(got), serviceNames(got))
	}
	for _, u := range got {
		if u.Service == "app" {
			t.Error("the built app is a pointer ref and must not be checked")
		}
	}
	byName := map[string]ServiceUpdate{}
	for _, u := range got {
		byName[u.Service] = u
	}
	if u := byName["mysql"]; u.Image != "mysql" || u.Tag != "8.0" {
		t.Errorf("mysql = %s:%s, want mysql:8.0", u.Image, u.Tag)
	}
	if u := byName["storage_console"]; u.Image != "opens3/console" || u.Tag != "latest" {
		t.Errorf("storage_console = %s:%s", u.Image, u.Tag)
	}
}

// Compose wins over config when both exist — it describes what is deployed.
func TestCheckPrefersComposeOverConfig(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "mcl", "drift", `{
	  "project": {"type": "image"},
	  "services": [{"name": "app", "image": "nginx", "tag": "1.24"}],
	  "environments": {"prod": {}}
	}`)
	writeCompose(t, root, "mcl", "drift", "prod", `services:
  app:
    image: nginx:1.27
`)
	got := Check(root, "mcl", "drift", "prod")
	if len(got) != 1 || got[0].Tag != "1.27" {
		t.Fatalf("expected the deployed tag 1.27, got %v", got)
	}
}

// An unparseable compose must fall back to config rather than silently disabling
// update checks for the environment.
func TestCheckFallsBackWhenComposeIsUnreadable(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "mcl", "broke", `{
	  "project": {"type": "image"},
	  "services": [{"name": "app", "image": "nginx", "tag": "1.27"}],
	  "environments": {"prod": {}}
	}`)
	writeCompose(t, root, "mcl", "broke", "prod", "\tnot: [valid")
	got := Check(root, "mcl", "broke", "prod")
	if len(got) != 1 || got[0].Image != "nginx" {
		t.Fatalf("expected the config fallback, got %v", got)
	}
}
