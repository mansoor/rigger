package api

// The nightly automated cleanup, across the fleet.
//
// The 03:00 run used to prune the control plane and nothing else — the same blind
// spot the manual operations had before they became host-aware, and the one that
// matters more, because nobody is watching. A machine nobody visits is exactly
// the one that fills up with dangling images.
//
// Fanning out changes the failure model. A single-machine run either worked or
// didn't; a fleet run partly works, every night, forever. One unreachable host
// must not stop the others, must not read as success, and must not look like a
// new problem each morning. So every machine gets its own outcome row and the
// run itself has no overall verdict — the log is the report.
//
// Membership is opt-in per host (hosts.housekeeping_enabled, default off). The
// control plane is always included, which is what the run did before.

import (
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/settings"
)

// hkAutoTasks are the tasks the nightly run performs. Both are safe by
// construction: they remove only what nothing references. Anything that needs a
// judgement call stays in the approval-gated Safety Center.
var hkAutoTasks = []struct {
	name string
	args []string
}{
	{"prune-networks", []string{"network", "prune", "-f"}},
	{"prune-dangling-images", []string{"image", "prune", "-f"}},
}

// maxAutoRunConcurrency bounds how many hosts are cleaned at once. The run has
// all night, so this is about not opening an SSH connection to every machine in
// the fleet simultaneously — and about a prune's disk churn not landing on
// twenty hosts at the same instant.
const maxAutoRunConcurrency = 3

// HousekeepingAutoRun runs the automated tasks on the control plane and every
// opted-in host. It never returns an error: a nightly job that can fail as a
// whole invites "the run failed" when nine of ten machines were cleaned. Each
// machine's result is a row in housekeeping_log, which is where the answer lives.
func (h *Handler) HousekeepingAutoRun() {
	h.runAutoTasks(&hkTarget{ex: executor.Local{}, Host: controlPlaneLabel})

	hosts, err := h.autoRunHosts()
	if err != nil {
		// The control plane has already been cleaned; losing the host list
		// shouldn't make that look like a failed run.
		log.Printf("housekeeping: list hosts for nightly run: %v", err)
		return
	}

	sem := make(chan struct{}, maxAutoRunConcurrency)
	var wg sync.WaitGroup
	for _, host := range hosts {
		wg.Add(1)
		go func(host settings.Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			h.runAutoTasksOnHost(host)
		}(host)
	}
	wg.Wait()
}

// autoRunHosts returns the hosts opted into the nightly run.
func (h *Handler) autoRunHosts() ([]settings.Host, error) {
	all, err := settings.ListHosts(h.db)
	if err != nil {
		return nil, err
	}
	var out []settings.Host
	for _, host := range all {
		if host.HousekeepingEnabled {
			out = append(out, host)
		}
	}
	return out, nil
}

// runAutoTasksOnHost dials a host and cleans it, recording a skip when it can't
// be reached.
func (h *Handler) runAutoTasksOnHost(host settings.Host) {
	t, err := h.hkTargetForHost(&host)
	if err != nil {
		// One row for the whole run, not one per task: a host that has been down
		// for a fortnight should read as fourteen missed nights, not
		// twenty-eight failures. Status "skipped" rather than "error" because
		// nothing was attempted — the machine is unreachable, which is a fact
		// about the fleet, not a fault in the cleanup.
		h.logHousekeeping(host.Name, "auto-run", "cron", "skipped",
			"Host unreachable, nothing was run: "+err.Error(), 0, 0)
		return
	}
	defer t.Close()
	h.runAutoTasks(t)
}

// runAutoTasks performs every automated task against one target, one log row
// each. A task that fails does not stop the next: they are independent, and
// pruning images is still worth doing when pruning networks failed.
func (h *Handler) runAutoTasks(t *hkTarget) {
	for _, task := range hkAutoTasks {
		out, err := t.docker(task.args...)
		status := "ok"
		if err != nil {
			status = "error"
			// docker's own message is the useful part; the command gives it
			// context in a log read weeks later.
			out = fmt.Sprintf("$ docker %s\n%s\n%v", joinArgs(task.args), out, err)
		}
		h.logHousekeeping(t.Host, task.name, "cron", status, out, extractFreedBytes(out), 0)
	}
}

// ── GET/POST /api/housekeeping/schedule/coverage ──────────────────────────────

// autoRunMachine is one row of "what the 03:00 run covers".
type autoRunMachine struct {
	HostID  int64  `json:"host_id"` // 0 = control plane
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Fixed is true for the control plane, which is always cleaned and has no
	// toggle — the machine Rigger runs on is the one it unambiguously owns.
	Fixed bool `json:"fixed"`
	// BuildOnly surfaces a host whose only job is building. Those accumulate
	// layers fastest, so they are the ones most worth opting in — but they may
	// also belong to someone else, which is why nothing is enabled by default.
	BuildOnly bool `json:"build_only"`
}

func (h *Handler) AutoRunCoverage(w http.ResponseWriter, r *http.Request) {
	out := []autoRunMachine{{Name: controlPlaneLabel, Enabled: true, Fixed: true}}
	hosts, err := settings.ListHosts(h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, host := range hosts {
		out = append(out, autoRunMachine{
			HostID: host.ID, Name: host.Name,
			Enabled: host.HousekeepingEnabled, BuildOnly: host.BuildOnly,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": out, "tasks": autoRunTaskNames()})
}

func (h *Handler) SetAutoRunCoverage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		HostID  int64 `json:"host_id"`
		Enabled bool  `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil || body.HostID <= 0 {
		// The control plane is not addressable here: it has no host row, and
		// excluding it would leave the scheduler with nothing it always does.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_id required"})
		return
	}
	if err := settings.SetHostHousekeeping(h.db, body.HostID, body.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"host_id": body.HostID, "enabled": body.Enabled})
}

// autoRunTaskNames lists what the nightly run does, so the UI describes the
// actual task list rather than a copy of it that can drift.
func autoRunTaskNames() []string {
	out := make([]string, 0, len(hkAutoTasks))
	for _, t := range hkAutoTasks {
		out = append(out, t.name)
	}
	return out
}

// joinArgs renders a command for the log. Not shell-quoted — these are fixed
// arguments, and this is a transcript rather than something to paste back.
func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
