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
