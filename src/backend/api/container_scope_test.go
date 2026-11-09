package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// A listing of every container on a host, in composeListingFormat. Includes
// Rigger's own control-plane container (compose project "src") and another
// workspace's database — the two targets the old fallback exposed.
const hostListing = `` +
	`aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111|mcl_sdm_dev_app|mcl_sdm_dev|app
bbbbbbbbbbbb2222222222222222222222222222222222222222222222222222|mcl_sdm_dev_mysql|mcl_sdm_dev|mysql
cccccccccccc3333333333333333333333333333333333333333333333333333|rigger|src|rigger
dddddddddddd4444444444444444444444444444444444444444444444444444|mcl_kmb_prod_mysql|mcl_kmb_prod|mysql
eeeeeeeeeeee5555555555555555555555555555555555555555555555555555|portainer||
`

// The regression. Whatever name a request supplies, only containers labelled
// with this environment's stack can come back — never Rigger itself, never
// another project's database, never an unmanaged container.
func TestMatchStackContainerNeverLeavesTheStack(t *testing.T) {
	for _, svc := range []string{
		"rigger",             // the control plane: its env holds JWT_SECRET
		"mcl_kmb_prod_mysql", // another workspace's database, by container name
		"mysql_",             // near-miss on a real service
		"portainer",          // an unmanaged container with no compose labels
		"cccccccccccc",       // Rigger's own container, by short id
		"dddddddddddd4444",   // a foreign container, by id prefix
		"src_rigger",         // the alias form, aimed at the wrong stack
	} {
		if id, ok := matchStackContainer(hostListing, "mcl_sdm_dev", svc); ok {
			t.Errorf("svc %q escaped the stack: resolved to %s", svc, id)
		}
	}
}

func TestMatchStackContainerAcceptsEveryLegitimateIdentifier(t *testing.T) {
	const app = "aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111"
	const mysql = "bbbbbbbbbbbb2222222222222222222222222222222222222222222222222222"
	for svc, want := range map[string]string{
		"app":               app,   // compose service — the terminal, files, inspect
		"mysql":             mysql, // compose service
		"mcl_sdm_dev_mysql": mysql, // container name — managed services set it
		"mcl_sdm_dev_app":   app,   // {stack}_{service} alias — what DB_HOST holds
		"aaaaaaaaaaaa":      app,   // short id
		app:                 app,   // full id
	} {
		got, ok := matchStackContainer(hostListing, "mcl_sdm_dev", svc)
		if !ok || got != want {
			t.Errorf("svc %q = %q (ok=%v), want %q", svc, got, ok, want)
		}
	}
}

// An id prefix shorter than a short id is too ambiguous to honour.
func TestMatchStackContainerRejectsShortIDPrefixes(t *testing.T) {
	if _, ok := matchStackContainer(hostListing, "mcl_sdm_dev", "aaaaaaaaaaa"); ok {
		t.Error("an 11-character id prefix must not match")
	}
}

// The project label is checked per row, so a daemon that ignored the filter —
// or a listing that somehow included everything — still cannot leak.
func TestMatchStackContainerChecksTheLabelItself(t *testing.T) {
	// Same service name as ours, different stack: must not match.
	listing := "ffffffffffff6666666666666666666666666666666666666666666666666666|x|mcl_other_dev|app\n"
	if _, ok := matchStackContainer(listing, "mcl_sdm_dev", "app"); ok {
		t.Error("a same-named service in another stack must not match")
	}
	for _, bad := range []string{"", "garbage", "a|b|c", "|name|mcl_sdm_dev|app"} {
		if _, ok := matchStackContainer(bad, "mcl_sdm_dev", "app"); ok {
			t.Errorf("malformed listing %q must not match", bad)
		}
	}
	if _, ok := matchStackContainer(hostListing, "", "app"); ok {
		t.Error("an empty stack must match nothing")
	}
}

// ── Through the real resolver, with a fake Docker ──────────────────────────────

type fakeDocker struct {
	listing string
	swarm   []string // `docker stack services` names
	calls   [][]string
}

func (f *fakeDocker) Docker(s executor.Spec) error {
	f.calls = append(f.calls, s.Args)
	return nil
}

func (f *fakeDocker) DockerOutput(s executor.Spec) ([]byte, error) {
	f.calls = append(f.calls, s.Args)
	switch {
	case len(s.Args) > 0 && s.Args[0] == "stack":
		return []byte(strings.Join(f.swarm, "\n")), nil
	case len(s.Args) > 0 && s.Args[0] == "ps" && containsArg(s.Args, "-q"):
		return []byte("swarmtask0001\n"), nil
	default:
		// Deliberately returns EVERY container on the host, ignoring filters, so
		// the tests prove containment does not depend on the daemon.
		return []byte(f.listing), nil
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func writeProjectConfig(t *testing.T, root, body string) {
	t.Helper()
	p := wspath.ConfigPath(root, "mcl", "sdm")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const sdmConfig = `{
  "project": {"name": "sdm", "resource_prefix": "mcl_sdm"},
  "environments": {"dev": {}, "prod": {"deployment": "swarm"}}
}`

// The exploit, end to end through resolveContainerRef: a request for "rigger"
// against a project the caller can see must fail with errNotInEnv. The old code
// returned "rigger" itself, and the caller then inspected or exec'd into it.
func TestResolveContainerRefRefusesTheControlPlane(t *testing.T) {
	root := t.TempDir()
	writeProjectConfig(t, root, sdmConfig)
	h := &Handler{workspacesDir: root}
	fd := &fakeDocker{listing: hostListing}

	for _, svc := range []string{"rigger", "mcl_kmb_prod_mysql", "portainer"} {
		ref, err := h.resolveContainerRef(fd, "mcl", "sdm", "dev", svc)
		if err == nil {
			t.Fatalf("svc %q resolved to %q — must be refused", svc, ref)
		}
		if ref == svc {
			t.Fatalf("svc %q was echoed back: the raw-name fallback is still present", svc)
		}
		if !errors.Is(err, errNotInEnv) {
			t.Errorf("svc %q: error %v should wrap errNotInEnv (maps to 404)", svc, err)
		}
	}

	// And the legitimate path still works.
	ref, err := h.resolveContainerRef(fd, "mcl", "sdm", "dev", "app")
	if err != nil || !strings.HasPrefix(ref, "aaaaaaaaaaaa") {
		t.Fatalf("own app: ref=%q err=%v", ref, err)
	}
}

// Every way the old code fell back to the raw name now fails closed instead.
func TestContainerScopeFailsClosed(t *testing.T) {
	root := t.TempDir()
	h := &Handler{workspacesDir: root}
	fd := &fakeDocker{listing: hostListing}

	// No config at all: the old code returned svc unchanged.
	if ref, err := h.resolveContainerRef(fd, "mcl", "sdm", "dev", "rigger"); err == nil {
		t.Fatalf("missing config resolved %q", ref)
	}

	writeProjectConfig(t, root, `{not json`)
	if ref, err := h.resolveContainerRef(fd, "mcl", "sdm", "dev", "rigger"); err == nil {
		t.Fatalf("malformed config resolved %q", ref)
	}

	writeProjectConfig(t, root, `{"project": {}, "environments": {"dev": {}}}`)
	if _, err := h.containerScope("mcl", "sdm", "dev"); err == nil {
		t.Error("a project with no prefix must be refused")
	}

	writeProjectConfig(t, root, sdmConfig)
	for _, env := range []string{"staging", "../kmb/envs/prod", "dev/..", "-dev", ""} {
		if _, err := h.containerScope("mcl", "sdm", env); err == nil {
			t.Errorf("environment %q must be refused", env)
		}
	}
}

func TestContainerScopeResolvesKnownEnvironments(t *testing.T) {
	root := t.TempDir()
	writeProjectConfig(t, root, sdmConfig)
	h := &Handler{workspacesDir: root}

	got, err := h.containerScope("mcl", "sdm", "dev")
	if err != nil || got.stack != "mcl_sdm_dev" || got.swarm {
		t.Errorf("dev = %+v, %v", got, err)
	}
	got, err = h.containerScope("mcl", "sdm", "prod")
	if err != nil || got.stack != "mcl_sdm_prod" || !got.swarm {
		t.Errorf("prod = %+v, %v", got, err)
	}

	// The env list is the union of config and envs/ directories: a directory-only
	// environment is real and must still resolve.
	if err := os.MkdirAll(wspath.EnvDir(root, "mcl", "sdm", "legacy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := h.containerScope("mcl", "sdm", "legacy"); err != nil || got.stack != "mcl_sdm_legacy" {
		t.Errorf("directory-only env = %+v, %v", got, err)
	}
}

// Swarm lookups must carry the stack-namespace filter, so the task container
// found is guaranteed to belong to this stack.
func TestResolveContainerRefSwarmStaysInNamespace(t *testing.T) {
	root := t.TempDir()
	writeProjectConfig(t, root, sdmConfig)
	h := &Handler{workspacesDir: root}
	fd := &fakeDocker{swarm: []string{"mcl_sdm_prod_app"}}

	if _, err := h.resolveContainerRef(fd, "mcl", "sdm", "prod", "app"); err != nil {
		t.Fatalf("own swarm service: %v", err)
	}
	var psArgs []string
	for _, c := range fd.calls {
		if len(c) > 0 && c[0] == "ps" {
			psArgs = c
		}
	}
	if !containsArg(psArgs, "label=com.docker.stack.namespace=mcl_sdm_prod") {
		t.Errorf("swarm task lookup is missing the namespace filter: %v", psArgs)
	}
}
