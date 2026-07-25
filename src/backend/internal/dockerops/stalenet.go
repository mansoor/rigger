package dockerops

// Stale container↔network bindings.
//
// A container records the ID — not the name — of every network it is attached
// to. When a network is deleted and recreated, the name still resolves but the
// recorded ID is dead, and starting the container fails with:
//
//	Error response from daemon: failed to set up container networking:
//	Could not attach to network <id>: ... not found
//
// (the "rpc error" wording appears once the host is in swarm mode, because
// network lookups then go through the cluster store).
//
// This is not recoverable by any command that reuses the existing container.
// Compose's config hash covers the network NAME, so `compose up` considers such
// a container up-to-date and merely starts it — which fails. start, deploy,
// restart and refresh all funnel into that same up, so the stack is stuck until
// the containers are removed and recreated.
//
// The triggers are mostly out-of-band, which is why this has to be detected
// rather than hooked: `docker network prune`, a manual `docker network rm`, a
// Docker Desktop reset, the host being switched into swarm mode, or a project
// restored onto a fresh host.
//
// So: staleNetworkServices finds the affected services so refresh can recreate
// exactly those, and networkAttachHint turns the raw daemon error into one that
// says what to do.

import (
	"fmt"
	"io"
	"strings"

	"github.com/mansoor/rigger/ui/internal/executor"
)

// networkAttachHint returns an explanation to append to a failed compose
// command's error when its output shows a container failing to attach to a
// network that no longer exists. Empty when the output doesn't look like that.
//
// Matched loosely (two independent substrings rather than one exact sentence)
// because the daemon's phrasing varies between standalone and swarm hosts.
func networkAttachHint(output string) string {
	if !strings.Contains(output, "Could not attach to network") {
		return ""
	}
	if !strings.Contains(output, "not found") {
		return ""
	}
	return "hint: a network this stack uses was deleted and recreated, so its containers still " +
		"reference a network ID that no longer exists.\n" +
		"Start/Deploy/Restart cannot fix this — they reuse the existing containers. " +
		"Run Refresh, which recreates the affected containers against the current network " +
		"(data volumes are kept)."
}

// tailWriter forwards everything to w while retaining the last max bytes, so a
// streamed command's output can still be inspected after it fails.
type tailWriter struct {
	w   io.Writer
	buf []byte
	max int
}

func newTailWriter(w io.Writer, max int) *tailWriter {
	return &tailWriter{w: w, max: max}
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	if t.w == nil {
		return len(p), nil
	}
	return t.w.Write(p)
}

func (t *tailWriter) String() string { return string(t.buf) }

// dockerOutput runs a raw (non-compose) docker command and captures stdout.
func (r *runner) dockerOutput(args ...string) ([]byte, error) {
	if err := r.ensureSynced(); err != nil {
		return nil, err
	}
	return executor.Default(r.opts.Exec).DockerOutput(executor.Spec{
		Args: args, Dir: r.envDir, Env: r.opts.EnvVars,
	})
}

// liveNetworkIDs is the set of network IDs the daemon currently knows about.
func (r *runner) liveNetworkIDs() (map[string]bool, error) {
	out, err := r.dockerOutput("network", "ls", "--quiet", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, line := range strings.Fields(string(out)) {
		ids[line] = true
	}
	return ids, nil
}

// staleNetworkServices returns the compose service names whose containers are
// attached to at least one network ID the daemon no longer has. Services are
// returned in the order docker reports them, deduplicated.
func (r *runner) staleNetworkServices() ([]string, error) {
	ids, err := r.composeOutput("ps", "--all", "--quiet")
	if err != nil {
		return nil, err
	}
	containers := strings.Fields(string(ids))
	if len(containers) == 0 {
		return nil, nil
	}

	live, err := r.liveNetworkIDs()
	if err != nil {
		return nil, err
	}

	// One line per container: "<service> <netID> <netID> …". A container on
	// network_mode host/none contributes no IDs and can never be stale.
	args := append([]string{
		"inspect",
		"--format", `{{index .Config.Labels "com.docker.compose.service"}}{{range .NetworkSettings.Networks}} {{.NetworkID}}{{end}}`,
	}, containers...)
	out, err := r.dockerOutput(args...)
	if err != nil {
		return nil, err
	}

	var stale []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		svc := f[0]
		for _, netID := range f[1:] {
			if !live[netID] && !seen[svc] {
				seen[svc] = true
				stale = append(stale, svc)
				break
			}
		}
	}
	return stale, nil
}

// recreateStaleNetworkContainers removes any container bound to a network that
// no longer exists, so the caller's `up` recreates it against the current one.
//
// Best-effort by design: this runs ahead of every refresh, and a stack that
// can't be inspected (an unreachable remote host, a docker version that formats
// differently) must still be allowed to deploy. A detection failure warns and
// returns nil; the up then either succeeds or fails with networkAttachHint
// attached, which is no worse than before this existed.
func (r *runner) recreateStaleNetworkContainers() {
	stale, err := r.staleNetworkServices()
	if err != nil {
		fmt.Fprintf(r.opts.Stdout, "⚠ could not check for stale container networks: %v\n", err)
		return
	}
	if len(stale) == 0 {
		return
	}
	r.info("Stale network binding on %d service(s): %s", len(stale), strings.Join(stale, ", "))
	r.info("Removing them so they are recreated on the current network (volumes are kept)...")
	// `rm --stop --force` without -v: named and anonymous volumes both survive.
	if err := r.compose(append([]string{"rm", "--stop", "--force"}, stale...)...); err != nil {
		fmt.Fprintf(r.opts.Stdout, "⚠ could not remove stale containers (%v) — deploying anyway\n", err)
	}
}
