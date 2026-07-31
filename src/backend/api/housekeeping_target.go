package api

// Which daemon a housekeeping request acts on.
//
// Housekeeping used to call exec.Command("docker", …) directly, which pinned it
// to the control plane — the one machine whose disk is usually the least
// interesting. Everything else in the codebase reaches Docker through
// internal/executor, which already abstracts local vs. over-SSH; routing
// housekeeping through the same seam is what makes remote hosts work.
//
// The target comes from ?host=<id>: absent or 0 means the control plane.
//
// Docker-level operations run through hkTarget.docker. Host-OS operations (apt,
// journal, kernels, /tmp) run through hkTarget.hostRun, which picks its own
// transport — nsenter locally, SSH with optional `sudo -n` remotely — and probes
// what the target can actually do. See housekeeping_hostos.go.

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/remotehost"
	"github.com/mansoor/rigger/ui/internal/settings"
)

// controlPlaneLabel is how the control plane is recorded and displayed. Stored
// (rather than left empty) so a log row always answers "which machine?".
const controlPlaneLabel = "control plane"

// hkTarget is a resolved housekeeping target: an executor to run docker through,
// a label for the audit log, and a closer for the SSH connection when remote.
type hkTarget struct {
	ex     executor.Executor
	Host   string // display label — controlPlaneLabel, or the host's name
	Remote bool
	closer func()

	// Host-OS capabilities, probed on first use and reused for the rest of the
	// request — see housekeeping_hostos.go. capsOnce rather than sync.Once
	// because a target is per-request and never shared across goroutines.
	capsOnce  bool
	capsCache hostCaps
}

// Close releases the SSH connection. Safe on a local target.
func (t *hkTarget) Close() {
	if t.closer != nil {
		t.closer()
	}
}

// docker runs a docker command on the target and returns its combined output.
// Combined because a prune's useful detail and its errors both matter, and the
// callers parse the text either way.
func (t *hkTarget) docker(args ...string) (string, error) {
	var b bytes.Buffer
	err := executor.Default(t.ex).Docker(executor.Spec{Args: args, Stdout: &b, Stderr: &b})
	return strings.TrimSpace(b.String()), err
}

// dockerLocal runs docker against the CONTROL PLANE, whatever host a request
// names. For questions about Rigger's own container or daemon — self-inspection,
// not fleet management — where a remote target would be simply wrong.
func dockerLocal(args ...string) (string, error) {
	t := &hkTarget{ex: executor.Local{}, Host: controlPlaneLabel}
	return t.docker(args...)
}

// hkTarget resolves ?host=<id> for a housekeeping request. A missing, zero or
// unparseable value is the control plane — the historical behavior, so an older
// client that doesn't send the parameter keeps working unchanged.
func (h *Handler) hkTarget(r *http.Request) (*hkTarget, error) {
	id, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("host")), 10, 64)
	if id <= 0 {
		return &hkTarget{ex: executor.Local{}, Host: controlPlaneLabel}, nil
	}
	host, err := settings.GetHost(h.db, id)
	if err != nil || host == nil {
		return nil, errHostNotFound
	}
	return h.hkTargetForHost(host)
}

// hkTargetForHost dials a host and wraps it as a target. Split from hkTarget so
// the nightly scheduler — which iterates hosts rather than reading a request —
// reaches a machine exactly the way a UI action does.
func (h *Handler) hkTargetForHost(host *settings.Host) (*hkTarget, error) {
	rh, err := h.dialHost(host.ID)
	if err != nil {
		return nil, err
	}
	return &hkTarget{
		ex:     remotehost.NewRemote(rh, h.workspacesDir, h.hostWorkspacesDir(host)),
		Host:   host.Name,
		Remote: true,
		closer: func() { rh.Close() },
	}, nil
}

// resolveHKTarget resolves the target and writes the error response itself, so
// each handler is one guard clause rather than five lines of plumbing. ok is
// false when a response has already been written.
func (h *Handler) resolveHKTarget(w http.ResponseWriter, r *http.Request) (t *hkTarget, ok bool) {
	t, err := h.hkTarget(r)
	if err != nil {
		// An unreachable host is the operator's to fix (wrong key, box down), not
		// a server fault — say which host and why.
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "host unreachable: " + err.Error()})
		return nil, false
	}
	return t, true
}
