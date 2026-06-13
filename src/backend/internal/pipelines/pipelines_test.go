package pipelines

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/shell"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestPipelineCRUD(t *testing.T) {
	d := openTestDB(t)

	p, err := Create(d, Pipeline{
		Workspace: "mcl", Project: "web", Name: "Ship dev", Enabled: true,
		Stages: []Stage{
			{Type: "deploy", Env: "dev"},
			{Type: "test", Env: "dev", Service: "app", Command: "curl -f localhost"},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == 0 || len(p.Stages) != 2 || !p.Enabled {
		t.Fatalf("unexpected created pipeline: %+v", p)
	}

	// Duplicate name in the same project is rejected.
	if _, err := Create(d, Pipeline{Workspace: "mcl", Project: "web", Name: "Ship dev",
		Stages: []Stage{{Type: "deploy", Env: "dev"}}}); err == nil {
		t.Fatal("expected duplicate-name error")
	}

	// List scoping.
	list, _ := List(d, "mcl", "web")
	if len(list) != 1 {
		t.Fatalf("expected 1 pipeline, got %d", len(list))
	}
	if other, _ := List(d, "mcl", "other"); len(other) != 0 {
		t.Fatalf("expected 0 pipelines for other project, got %d", len(other))
	}

	// Update.
	p.Name = "Ship dev v2"
	p.Stages = []Stage{{Type: "deploy", Env: "dev"}}
	up, err := Update(d, p.ID, *p)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if up.Name != "Ship dev v2" || len(up.Stages) != 1 {
		t.Fatalf("update not applied: %+v", up)
	}

	// Delete.
	if err := Delete(d, p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := List(d, "mcl", "web"); len(list) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(list))
	}
}

func TestPipelineValidate(t *testing.T) {
	cases := []struct {
		name string
		p    Pipeline
		ok   bool
	}{
		{"no name", Pipeline{Stages: []Stage{{Type: "deploy", Env: "dev"}}}, false},
		{"no stages", Pipeline{Name: "x"}, false},
		{"bad type", Pipeline{Name: "x", Stages: []Stage{{Type: "nope", Env: "dev"}}}, false},
		{"no env", Pipeline{Name: "x", Stages: []Stage{{Type: "deploy"}}}, false},
		{"test needs service+cmd", Pipeline{Name: "x", Stages: []Stage{{Type: "test", Env: "dev"}}}, false},
		{"ok", Pipeline{Name: "x", Stages: []Stage{{Type: "deploy", Env: "dev"}}}, true},
	}
	for _, c := range cases {
		err := c.p.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: expected ok, got %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

func TestValidateDefaultsOnFailureStop(t *testing.T) {
	p := Pipeline{Name: "x", Stages: []Stage{{Type: "deploy", Env: "dev"}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Stages[0].OnFailure != "stop" {
		t.Fatalf("expected on_failure defaulted to stop, got %q", p.Stages[0].OnFailure)
	}
}

func TestStageRunOptionsMapping(t *testing.T) {
	cases := []struct {
		stage   Stage
		command string
		extra   []string
	}{
		{Stage{Type: "deploy", Env: "prod"}, "start", nil},
		{Stage{Type: "deploy", Env: "prod", Service: "api"}, "start", []string{"api"}},
		{Stage{Type: "refresh", Env: "prod"}, "refresh", nil},
		{Stage{Type: "refresh", Env: "prod", Service: "api"}, "refresh", []string{"api"}},
		{Stage{Type: "update", Env: "prod"}, "update", nil},
		{Stage{Type: "build", Env: "prod"}, "build", nil},
		{Stage{Type: "build", Env: "prod", Service: "api"}, "build", []string{"api"}},
		{Stage{Type: "restart", Env: "prod", Service: "api"}, "restart", []string{"api"}},
		{Stage{Type: "restart", Env: "prod"}, "restart", nil},
		{Stage{Type: "backup", Env: "prod", Service: "db"}, "backup", []string{"db"}},
		{Stage{Type: "test", Env: "prod", Service: "app", Command: "ls"}, "test", []string{"app", "ls"}},
	}
	for _, c := range cases {
		o := StageRunOptions("mcl", "web", c.stage)
		if o.Command != c.command {
			t.Errorf("%s: command = %q, want %q", c.stage.Type, o.Command, c.command)
		}
		if o.Workspace != "mcl" || o.Project != "web" || o.Env != c.stage.Env {
			t.Errorf("%s: target mismatch %+v", c.stage.Type, o)
		}
		if strings.Join(o.Extra, ",") != strings.Join(c.extra, ",") {
			t.Errorf("%s: extra = %v, want %v", c.stage.Type, o.Extra, c.extra)
		}
	}
}

func TestStageRunOptionsVersionScript(t *testing.T) {
	v := StageRunOptions("mcl", "web", Stage{Type: "version", Part: "minor"})
	if v.Command != "version" || v.Env != "bump" || strings.Join(v.Extra, ",") != "minor" {
		t.Fatalf("version mapping: %+v", v)
	}
	s := StageRunOptions("mcl", "web", Stage{Type: "script", Env: "dev", Image: "aquasec/trivy", Command: "trivy image $RIGGER_IMAGES", Network: true})
	if s.Command != "script" || s.ScriptImage != "aquasec/trivy" || !s.ScriptNetwork || s.ScriptCommand == "" || s.Env != "dev" {
		t.Fatalf("script mapping: %+v", s)
	}

	// A build stage with a bump part must emit `--bump <part>` (and --push when set)
	// so the build owns the version change — the basis of the version-rollout fix.
	b := StageRunOptions("mcl", "web", Stage{Type: "build", Env: "dev", Part: "minor", Push: true})
	if b.Command != "build" || strings.Join(b.Extra, ",") != "--bump,minor,--push" {
		t.Fatalf("build+bump mapping: %+v", b)
	}
	// No bump part → no --bump.
	b2 := StageRunOptions("mcl", "web", Stage{Type: "build", Env: "dev"})
	if strings.Join(b2.Extra, ",") != "" {
		t.Fatalf("plain build mapping: %+v", b2)
	}
}

// fakeBridge records the commands it was asked to run and can fail a chosen one.
type fakeBridge struct {
	calls    []string
	failOn   string
	writeOut string
}

func (f *fakeBridge) Run(opts shell.RunOptions) error {
	f.calls = append(f.calls, opts.Command+" "+opts.Env)
	if f.writeOut != "" && opts.Stdout != nil {
		io.WriteString(opts.Stdout, f.writeOut)
	}
	if opts.Command == f.failOn {
		return errors.New("boom")
	}
	return nil
}

func TestRunHaltsOnFailure(t *testing.T) {
	p := Pipeline{Workspace: "mcl", Project: "web", Name: "x", Stages: []Stage{
		{Type: "deploy", Env: "dev", OnFailure: "stop"},
		{Type: "test", Env: "dev", Service: "app", Command: "false", OnFailure: "stop"},
		{Type: "restart", Env: "dev", OnFailure: "stop"},
	}}
	fb := &fakeBridge{failOn: "test"}
	results, outcome := Execute(fb, p, io.Discard, 0, nil)
	if outcome != OutcomeFail {
		t.Fatalf("expected fail outcome, got %q", outcome)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 stage results (incl skipped), got %d", len(results))
	}
	if results[0].Status != "ok" || results[1].Status != "fail" || results[2].Status != "skipped" {
		t.Fatalf("unexpected statuses: %s/%s/%s", results[0].Status, results[1].Status, results[2].Status)
	}
	// restart must NOT have run (halted after the failing test).
	for _, c := range fb.calls {
		if strings.HasPrefix(c, "restart") {
			t.Fatal("restart ran despite halt")
		}
	}
}

func TestRunContinueOnFailure(t *testing.T) {
	p := Pipeline{Workspace: "mcl", Project: "web", Name: "x", Stages: []Stage{
		{Type: "test", Env: "dev", Service: "app", Command: "false", OnFailure: "continue"},
		{Type: "restart", Env: "dev", OnFailure: "stop"},
	}}
	fb := &fakeBridge{failOn: "test"}
	results, outcome := Execute(fb, p, io.Discard, 0, nil)
	if outcome != OutcomeFail {
		t.Fatalf("expected fail outcome (one stage failed), got %q", outcome)
	}
	if results[0].Status != "fail" || results[1].Status != "ok" {
		t.Fatalf("expected fail then ok, got %s/%s", results[0].Status, results[1].Status)
	}
}

func TestRunProgressReportsRunningThenOk(t *testing.T) {
	p := Pipeline{Workspace: "mcl", Project: "web", Name: "x", Stages: []Stage{
		{Type: "deploy", Env: "dev", OnFailure: "stop"},
		{Type: "restart", Env: "dev", OnFailure: "stop"},
	}}
	fb := &fakeBridge{}

	// Collect the status of the last stage at every progress emission. We must see
	// a "running" status before the terminal "ok" for each stage.
	var sawRunning, sawOK bool
	_, outcome := Execute(fb, p, io.Discard, 0, func(res []StageResult) {
		if len(res) == 0 {
			return
		}
		switch res[len(res)-1].Status {
		case "running":
			sawRunning = true
		case "ok":
			sawOK = true
		}
	})
	if outcome != OutcomeOK {
		t.Fatalf("expected ok, got %q", outcome)
	}
	if !sawRunning {
		t.Fatal("progress callback never reported a running stage")
	}
	if !sawOK {
		t.Fatal("progress callback never reported an ok stage")
	}
}

func TestRunGatePauseResume(t *testing.T) {
	p := Pipeline{Workspace: "mcl", Project: "web", Name: "x", Stages: []Stage{
		{Type: "deploy", Env: "stage", OnFailure: "stop"},
		{Type: "gate", Command: "approve for prod"},
		{Type: "deploy", Env: "prod", OnFailure: "stop"},
	}}
	fb := &fakeBridge{}

	// First segment: runs deploy stage, then pauses at the gate.
	seg1, outcome := Execute(fb, p, io.Discard, 0, nil)
	if outcome != OutcomeAwaiting {
		t.Fatalf("expected awaiting, got %q", outcome)
	}
	if len(seg1) != 2 || seg1[0].Status != "ok" || seg1[1].Status != OutcomeAwaiting {
		t.Fatalf("unexpected segment 1: %+v", seg1)
	}
	if len(fb.calls) != 1 { // only the stage deploy ran; prod deploy must not
		t.Fatalf("expected 1 call before gate, got %v", fb.calls)
	}

	// Resume after approval: continue from index len(seg1) = 2.
	seg2, outcome2 := Execute(fb, p, io.Discard, len(seg1), nil)
	if outcome2 != OutcomeOK {
		t.Fatalf("expected ok after resume, got %q", outcome2)
	}
	if len(seg2) != 1 || seg2[0].Env != "prod" || seg2[0].Status != "ok" {
		t.Fatalf("unexpected segment 2: %+v", seg2)
	}
}

func TestRunRecords(t *testing.T) {
	d := openTestDB(t)
	id, err := CreateRun(d, Run{PipelineID: 1, Workspace: "mcl", Project: "web", Status: "running", StartedAt: 1000})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	got, err := GetRun(d, id)
	if err != nil || got.Status != "running" || got.FinishedAt != 0 {
		t.Fatalf("unexpected run: %+v err=%v", got, err)
	}
	got.Status = "ok"
	got.FinishedAt = 2000
	got.Stages = []StageResult{{Type: "deploy", Env: "dev", Status: "ok"}}
	if err := UpdateRun(d, *got); err != nil {
		t.Fatalf("update run: %v", err)
	}
	runs, _ := ListRuns(d, 1, 10)
	if len(runs) != 1 || runs[0].Status != "ok" || runs[0].FinishedAt != 2000 || len(runs[0].Stages) != 1 {
		t.Fatalf("unexpected runs: %+v", runs)
	}
}
