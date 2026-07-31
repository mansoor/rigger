package dockerops

import (
	"strings"
	"testing"
)

// The shape a custom project actually generates: services Rigger builds sitting
// alongside sidecars pulled straight from a registry. Taken from a real project
// (backend/celeryworker/frontend built; flower/mongodb/queue pulled).
const mixedCompose = `services:
  backend:
    image: farm_backend:v3
    build:
      context: ./src
  celeryworker:
    image: farm_backend:v3
    build: ./src
  frontend:
    image: farm_frontend:v3
    build:
      context: ./ui
  flower:
    image: mher/flower:2.0.1
  mongodb:
    image: mongo:latest
  queue:
    image: rabbitmq:3
`

func TestPullableServicesSeparatesBuiltFromPulled(t *testing.T) {
	got, err := pullableServices([]byte(mixedCompose))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"flower", "mongodb", "queue"} {
		if !got[name] {
			t.Errorf("%s comes from a registry and must be pullable", name)
		}
	}
	// A service with an image AND a build is one Rigger builds and tags itself.
	// The local build is the newer artefact; pulling would replace it with
	// whatever the registry has — for a no-registry project, nothing at all.
	for _, name := range []string{"backend", "celeryworker", "frontend"} {
		if got[name] {
			t.Errorf("%s is built locally and must not be pulled", name)
		}
	}
	if len(got) != 3 {
		t.Errorf("expected exactly 3 pullable services, got %v", got)
	}
}

// Both compose build forms must be recognised, or the long form would be treated
// as a registry image and pulled.
func TestPullableServicesHandlesBothBuildForms(t *testing.T) {
	got, _ := pullableServices([]byte(mixedCompose))
	if got["backend"] {
		t.Error("long-form build (mapping) not recognised")
	}
	if got["celeryworker"] {
		t.Error("short-form build (string) not recognised")
	}
}

func TestPullTargetsNarrowsToRegistryImages(t *testing.T) {
	pullable, _ := pullableServices([]byte(mixedCompose))

	// Single service — the per-service up-arrow. This is the case that used to
	// silently do nothing: no registry configured meant the pull was skipped for
	// the whole stack, so Update on mongodb recreated the same image.
	if got := pullTargets([]string{"mongodb"}, pullable); len(got) != 1 || got[0] != "mongodb" {
		t.Errorf("pullTargets([mongodb]) = %v, want [mongodb]", got)
	}

	// A built service targeted directly yields nothing to pull — the caller then
	// recreates from the local image instead of failing on an unresolvable name.
	if got := pullTargets([]string{"backend"}, pullable); len(got) != 0 {
		t.Errorf("pullTargets([backend]) = %v, want empty", got)
	}

	// Whole stack: every registry image, and none of the built ones. Sorted, so
	// the emitted command doesn't churn between runs on map order.
	got := pullTargets(nil, pullable)
	if want := "flower mongodb queue"; strings.Join(got, " ") != want {
		t.Errorf("pullTargets(nil) = %v, want %q", got, want)
	}
}

// A project with nothing but built services must yield no pull at all, so the
// caller keeps the old skip-and-recreate path rather than running a bare
// `compose pull` that would resolve names against Docker Hub.
func TestPullTargetsEmptyForAllBuiltStack(t *testing.T) {
	const allBuilt = `services:
  app:
    image: sdm_app:v9
    build:
      context: .
`
	pullable, err := pullableServices([]byte(allBuilt))
	if err != nil {
		t.Fatal(err)
	}
	if got := pullTargets(nil, pullable); len(got) != 0 {
		t.Errorf("expected nothing pullable, got %v", got)
	}
	if got := pullTargets([]string{"app"}, pullable); len(got) != 0 {
		t.Errorf("expected nothing pullable for an explicit built target, got %v", got)
	}
}

// Managed dependencies are synthesised into compose and never appear in the
// project's own services[] — reading compose rather than config is what makes
// them updatable.
func TestPullableServicesSeesSynthesisedDependencies(t *testing.T) {
	const withManagedDB = `services:
  app:
    image: kmb_app:v2
    build: .
  db:
    image: postgres:16
  redis:
    image: redis:7-alpine
`
	got, _ := pullableServices([]byte(withManagedDB))
	if !got["db"] || !got["redis"] {
		t.Errorf("managed dependencies must be pullable, got %v", got)
	}
	if got["app"] {
		t.Error("the built app must not be")
	}
}

func TestPullableServicesTolerantOfJunk(t *testing.T) {
	// Unparseable compose must not panic; the caller falls back to skipping the
	// pull, which is the pre-existing behaviour.
	if _, err := pullableServices([]byte("\tnot: [valid")); err == nil {
		t.Error("expected a parse error for malformed YAML")
	}
	for _, in := range []string{"", "services:", "services: {}"} {
		got, err := pullableServices([]byte(in))
		if err != nil {
			t.Errorf("pullableServices(%q) errored: %v", in, err)
		}
		if len(got) != 0 {
			t.Errorf("pullableServices(%q) = %v, want empty", in, got)
		}
	}
}
