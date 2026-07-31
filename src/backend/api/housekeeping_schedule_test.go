package api

import (
	"testing"

	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/settings"
)

func scheduleTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func addHost(t *testing.T, d *db.DB, name string) *settings.Host {
	t.Helper()
	h, err := settings.CreateHost(d, name, name+".example", 22, "root", "enc", "", "global")
	if err != nil {
		t.Fatalf("create host %s: %v", name, err)
	}
	return h
}

func names(hosts []settings.Host) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, h.Name)
	}
	return out
}

// A registered host must NOT be swept into the nightly run by existing. Upgrading
// Rigger should never start pruning machines the operator didn't ask it to touch
// — a build host may belong to another team entirely.
func TestAutoRunHostsDefaultsToNone(t *testing.T) {
	d := scheduleTestDB(t)
	addHost(t, d, "web-1")
	addHost(t, d, "db-1")

	h := &Handler{db: d}
	got, err := h.autoRunHosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("no host should be included until opted in, got %v", names(got))
	}
}

func TestAutoRunHostsIncludesOnlyOptedIn(t *testing.T) {
	d := scheduleTestDB(t)
	web := addHost(t, d, "web-1")
	addHost(t, d, "db-1")
	builder := addHost(t, d, "builder-1")

	for _, id := range []int64{web.ID, builder.ID} {
		if err := settings.SetHostHousekeeping(d, id, true); err != nil {
			t.Fatal(err)
		}
	}

	h := &Handler{db: d}
	got, err := h.autoRunHosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 opted-in hosts, got %v", names(got))
	}
	for _, g := range got {
		if g.Name == "db-1" {
			t.Error("db-1 was never opted in")
		}
	}

	// And opting back out removes it — the toggle has to work in both directions
	// or the only way to stop a nightly prune is deleting the host.
	if err := settings.SetHostHousekeeping(d, web.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ = h.autoRunHosts()
	if len(got) != 1 || got[0].Name != "builder-1" {
		t.Errorf("after opting web-1 out, expected only builder-1, got %v", names(got))
	}
}

// The opt-in must survive being read back through the normal host queries, not
// just the one the scheduler uses — the UI reads GetHost and ListHosts.
func TestHousekeepingFlagRoundTrips(t *testing.T) {
	d := scheduleTestDB(t)
	host := addHost(t, d, "web-1")

	if host.HousekeepingEnabled {
		t.Error("a freshly created host must not be enrolled")
	}
	if err := settings.SetHostHousekeeping(d, host.ID, true); err != nil {
		t.Fatal(err)
	}

	got, err := settings.GetHost(d, host.ID)
	if err != nil || got == nil {
		t.Fatalf("get host: %v", err)
	}
	if !got.HousekeepingEnabled {
		t.Error("GetHost lost the flag")
	}

	all, err := settings.ListHosts(d)
	if err != nil || len(all) != 1 {
		t.Fatalf("list hosts: %v (%d rows)", err, len(all))
	}
	if !all[0].HousekeepingEnabled {
		t.Error("ListHosts lost the flag")
	}
}

// The partial-success model. A host that can't be reached records one skipped
// row and nothing else — and crucially does not prevent another host from being
// processed. Before fan-out a run either worked or didn't; now it partly works,
// every night, and the log has to say which part.
func TestUnreachableHostIsSkippedNotFailed(t *testing.T) {
	d := scheduleTestDB(t)
	down := addHost(t, d, "down-1")
	alsoDown := addHost(t, d, "down-2")

	// No crypto key, so dialling fails before any network I/O — the same branch
	// an unreachable box takes, without needing one.
	h := &Handler{db: d}
	h.runAutoTasksOnHost(*down)
	h.runAutoTasksOnHost(*alsoDown)

	rows, err := d.Query(`SELECT host, task, status, output FROM housekeeping_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []struct{ host, task, status, output string }
	for rows.Next() {
		var r struct{ host, task, status, output string }
		if err := rows.Scan(&r.host, &r.task, &r.status, &r.output); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}

	// One row each, not one per task: a box down for a fortnight should read as
	// fourteen missed nights, not twenty-eight failures.
	if len(got) != 2 {
		t.Fatalf("expected 1 row per unreachable host, got %d: %+v", len(got), got)
	}
	for i, want := range []string{"down-1", "down-2"} {
		if got[i].host != want {
			t.Errorf("row %d host = %q, want %q — the second host must still be attempted", i, got[i].host, want)
		}
		// "skipped", not "error": nothing was run. The machine being unreachable
		// is a fact about the fleet, not a fault in the cleanup.
		if got[i].status != "skipped" {
			t.Errorf("row %d status = %q, want skipped", i, got[i].status)
		}
		if got[i].task != "auto-run" {
			t.Errorf("row %d task = %q, want auto-run", i, got[i].task)
		}
		if got[i].output == "" {
			t.Errorf("row %d has no reason recorded", i)
		}
	}
}

// The nightly tasks are the safe ones — they remove only what nothing
// references. Anything needing a judgement call belongs in the approval-gated
// Safety Center, not in something that runs unattended at 03:00.
func TestAutoTasksAreNonDestructive(t *testing.T) {
	if len(hkAutoTasks) != 2 {
		t.Fatalf("expected 2 automated tasks, got %d", len(hkAutoTasks))
	}
	for _, task := range hkAutoTasks {
		last := task.args[len(task.args)-1]
		if last != "-f" {
			t.Errorf("%s: expected an unattended prune, got %v", task.name, task.args)
		}
		// `image prune -a` would remove every image not currently running,
		// including ones a stopped environment needs to start again.
		for _, a := range task.args {
			if a == "-a" || a == "--all" {
				t.Errorf("%s must not prune all images unattended: %v", task.name, task.args)
			}
		}
	}
	if got := autoRunTaskNames(); len(got) != len(hkAutoTasks) {
		t.Errorf("task names = %v, want one per task", got)
	}
}
