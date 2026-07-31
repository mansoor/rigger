package composeimg

import (
	"strings"
	"testing"
)

// Verbatim shape of a generated compose file. The critical detail: NO service has
// a `build:` section. Rigger builds out of band and references the result through
// the env's {SVC}_IMAGE pointer, so a built service reaches compose as a variable
// and everything pulled is a literal reference.
const generated = `services:
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
`

func services(refs []Ref) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Service)
	}
	return out
}

// The regression this file exists for. Testing for a `build:` section — the
// obvious rule — matches nothing in a generated file, so every Rigger-built
// service would be classified pullable. In a project with no registry, a
// whole-stack update would then run `compose pull app` against Docker Hub for an
// image that was never pushed anywhere, and abort.
func TestPullableExcludesBuiltPointerRefs(t *testing.T) {
	refs, err := Pullable([]byte(generated))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range refs {
		if r.Service == "app" {
			t.Fatalf("app is a ${APP_IMAGE} pointer and must not be pullable: %+v", r)
		}
	}
	want := "adminer mailpit minio minio_init mysql redis storage_console"
	if got := strings.Join(services(refs), " "); got != want {
		t.Errorf("pullable = %q, want %q", got, want)
	}
}

// Managed dependencies and sidecars are synthesised into compose and never appear
// in the project's own services[] — reading compose is what makes them visible at
// all. sdm's config lists exactly one service (app); its compose runs eight.
func TestPullableSeesSynthesisedServices(t *testing.T) {
	refs, _ := Pullable([]byte(generated))
	byName := map[string]Ref{}
	for _, r := range refs {
		byName[r.Service] = r
	}
	for _, c := range []struct{ svc, image, tag string }{
		{"mysql", "mysql", "8.0"},
		{"redis", "redis", "7-alpine"},
		{"minio", "minio/minio", "latest"},
		{"adminer", "adminer", "4.8.1"},
		{"storage_console", "opens3/console", "latest"},
		{"mailpit", "axllent/mailpit", "latest"},
	} {
		got, ok := byName[c.svc]
		if !ok {
			t.Errorf("%s missing", c.svc)
			continue
		}
		if got.Image != c.image || got.Tag != c.tag {
			t.Errorf("%s = %s:%s, want %s:%s", c.svc, got.Image, got.Tag, c.image, c.tag)
		}
	}
}

func TestParseRefRejectsWhatCannotBeChecked(t *testing.T) {
	for _, ref := range []string{
		"",
		"   ",
		"${APP_IMAGE:-localhost:5001/app:1.0}", // Rigger-built pointer
		"${APP_IMAGE}",
		// Digest pins cannot drift, so "update available" is never true and a
		// pull changes nothing.
		"mysql@sha256:abc123",
		"mysql:8.0@sha256:abc123",
	} {
		if _, _, ok := ParseRef(ref); ok {
			t.Errorf("ParseRef(%q) accepted, want rejected", ref)
		}
	}
}

func TestParseRefSplitsCorrectly(t *testing.T) {
	cases := []struct{ ref, image, tag string }{
		{"mysql:8.0", "mysql", "8.0"},
		{"minio/minio:latest", "minio/minio", "latest"},
		{"redis", "redis", "latest"}, // no tag ⇒ compose's default
		{"minio/mc", "minio/mc", "latest"},
		// A registry host with a port has a colon that is NOT a tag separator.
		{"localhost:5001/mcl_sdm-app", "localhost:5001/mcl_sdm-app", "latest"},
		{"localhost:5001/mcl_sdm-app:1.0.0", "localhost:5001/mcl_sdm-app", "1.0.0"},
		{"ghcr.io/mansoor/rigger:0.1.44", "ghcr.io/mansoor/rigger", "0.1.44"},
	}
	for _, c := range cases {
		image, tag, ok := ParseRef(c.ref)
		if !ok {
			t.Errorf("ParseRef(%q) rejected", c.ref)
			continue
		}
		if image != c.image || tag != c.tag {
			t.Errorf("ParseRef(%q) = %s / %s, want %s / %s", c.ref, image, tag, c.image, c.tag)
		}
	}
}

func TestPullableTolerantOfJunk(t *testing.T) {
	if _, err := Pullable([]byte("\tnot: [valid")); err == nil {
		t.Error("expected a parse error for malformed YAML")
	}
	for _, in := range []string{"", "services:", "services: {}"} {
		got, err := Pullable([]byte(in))
		if err != nil {
			t.Errorf("Pullable(%q) errored: %v", in, err)
		}
		if len(got) != 0 {
			t.Errorf("Pullable(%q) = %v, want empty", in, got)
		}
	}
}

// A generated file that DOES carry a build section must still exclude it — the
// pointer rule is what matches today, but the build rule stays correct.
func TestPullableStillHonoursBuildSection(t *testing.T) {
	const withBuild = `services:
  app:
    image: farm_backend:v3
    build:
      context: ./src
  mongodb:
    image: mongo:latest
`
	refs, _ := Pullable([]byte(withBuild))
	if got := strings.Join(services(refs), " "); got != "mongodb" {
		t.Errorf("pullable = %q, want %q", got, "mongodb")
	}
}
