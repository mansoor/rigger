package dockerops

import (
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/composeimg"
)

// Reference splitting and the built-vs-pulled rule live in internal/composeimg
// and are tested there. This covers only what dockerops adds: narrowing an
// update's target list.
const generated = `services:
  app:
    image: ${APP_IMAGE:-localhost:5001/mcl_sdm-app:1.0.0-build.7-dev}
  mysql:
    image: mysql:8.0
  redis:
    image: redis:7-alpine
  adminer:
    image: adminer:4.8.1
`

func pullableFor(t *testing.T) []composeimg.Ref {
	t.Helper()
	refs, err := composeimg.Pullable([]byte(generated))
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

func TestPullTargetsSingleService(t *testing.T) {
	pullable := pullableFor(t)
	// The per-service up-arrow. This is the case that used to silently do
	// nothing: with no registry configured the pull was skipped for the whole
	// stack, so Update on mysql recreated the same image and reported success.
	if got := pullTargets([]string{"mysql"}, pullable); len(got) != 1 || got[0] != "mysql" {
		t.Errorf("pullTargets([mysql]) = %v, want [mysql]", got)
	}
	// A Rigger-built service targeted directly yields nothing to pull, so the
	// caller recreates from the local image instead of failing on a name that
	// was never pushed to a registry.
	if got := pullTargets([]string{"app"}, pullable); len(got) != 0 {
		t.Errorf("pullTargets([app]) = %v, want empty", got)
	}
}

func TestPullTargetsWholeStackExcludesBuilt(t *testing.T) {
	got := pullTargets(nil, pullableFor(t))
	if want := "adminer mysql redis"; strings.Join(got, " ") != want {
		t.Errorf("pullTargets(nil) = %v, want %q", got, want)
	}
}

// Nothing pullable must yield an empty list so the caller keeps the old
// skip-and-recreate path rather than running a bare `compose pull`.
func TestPullTargetsEmptyForAllBuiltStack(t *testing.T) {
	refs, err := composeimg.Pullable([]byte(`services:
  app:
    image: ${APP_IMAGE:-mcl_sdm-app:1.0.0}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := pullTargets(nil, refs); len(got) != 0 {
		t.Errorf("expected nothing pullable, got %v", got)
	}
	if got := pullTargets([]string{"app"}, refs); len(got) != 0 {
		t.Errorf("expected nothing pullable for an explicit built target, got %v", got)
	}
}
