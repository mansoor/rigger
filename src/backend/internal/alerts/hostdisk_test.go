package alerts

import (
	"testing"
	"time"
)

func hostsIn(ts []target) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.host)
	}
	return out
}

// A disk rule must produce one target per reachable machine. Before this it
// produced exactly one target ever, so a fleet of ten reported on one.
func TestDiskTargetsPerHost(t *testing.T) {
	e := &Evaluator{hostDisk: func() []HostDisk {
		return []HostDisk{
			{Host: "", UsedPct: 41, Reachable: true},
			{Host: "web-1", UsedPct: 92, Reachable: true},
			{Host: "db-1", UsedPct: 77, Reachable: true},
		}
	}}
	got := e.diskTargets(Rule{ConditionType: CondDiskAbovePct})
	if len(got) != 3 {
		t.Fatalf("expected 3 targets, got %d (%v)", len(got), hostsIn(got))
	}
	for i, want := range []struct {
		host string
		pct  float64
	}{{"", 41}, {"web-1", 92}, {"db-1", 77}} {
		if got[i].host != want.host || got[i].diskPct != want.pct {
			t.Errorf("target %d = %q/%.0f, want %q/%.0f", i, got[i].host, got[i].diskPct, want.host, want.pct)
		}
	}
}

// The important one. A host whose disk was full and has since gone unreachable
// must NOT become a target: a target that isn't met resolves the open event, so
// including it at 0% would turn "I can't see the machine" into "the machine is
// fine" at the exact moment that is least likely to be true.
func TestDiskTargetsSkipUnreachable(t *testing.T) {
	e := &Evaluator{hostDisk: func() []HostDisk {
		return []HostDisk{
			{Host: "", UsedPct: 30, Reachable: true},
			{Host: "web-1", Reachable: false}, // was at 98% before it went dark
		}
	}}
	got := e.diskTargets(Rule{ConditionType: CondDiskAbovePct})
	if len(got) != 1 || got[0].host != "" {
		t.Fatalf("unreachable host must be skipped entirely, got %v", hostsIn(got))
	}
}

// With no sampler the evaluator must still watch the control plane — an install
// that never registers a host keeps the behaviour it always had.
func TestDiskTargetsFallsBackToControlPlane(t *testing.T) {
	got := (&Evaluator{}).diskTargets(Rule{ConditionType: CondDiskAbovePct})
	if len(got) != 1 || got[0].host != "" {
		t.Fatalf("expected a single control-plane target, got %v", hostsIn(got))
	}
}

// Sampling a fleet means one SSH connection per host. The evaluator ticks every
// 60s, so without a TTL a ten-host fleet would be dialled 14,400 times a day.
func TestHostDiskSampleIsCached(t *testing.T) {
	calls := 0
	now := time.Now()
	e := &Evaluator{
		nowFn:    func() time.Time { return now },
		hostDisk: func() []HostDisk { calls++; return []HostDisk{{Host: "web-1", Reachable: true}} },
	}

	for i := 0; i < 5; i++ {
		e.sampleHostDisk()
	}
	if calls != 1 {
		t.Errorf("expected 1 sample within the TTL, got %d", calls)
	}

	// Just inside the window: still cached.
	now = now.Add(hostDiskTTL - time.Second)
	e.sampleHostDisk()
	if calls != 1 {
		t.Errorf("expected no resample just inside the TTL, got %d calls", calls)
	}

	// Past it: refreshed.
	now = now.Add(2 * time.Second)
	e.sampleHostDisk()
	if calls != 2 {
		t.Errorf("expected a resample past the TTL, got %d calls", calls)
	}
}

func TestHostLabel(t *testing.T) {
	// The empty host is the control plane — stored empty so events predating
	// multi-host evaluation keep their meaning without a backfill.
	if got := hostLabel(""); got != "Control plane" {
		t.Errorf("hostLabel(\"\") = %q", got)
	}
	if got := hostLabel("web-1"); got != "web-1" {
		t.Errorf("hostLabel(\"web-1\") = %q", got)
	}
}

// The message must name the machine, or an inbox with three hosts over
// threshold reads as three identical alerts.
func TestDiskMessageNamesTheHost(t *testing.T) {
	e := &Evaluator{}
	rule := Rule{ConditionType: CondDiskAbovePct, Threshold: 85}

	met, val, msg := e.check(rule, target{host: "web-1", diskPct: 92.4}, nil)
	if !met || val != 92.4 {
		t.Fatalf("expected fire at 92.4%% over 85%%, got met=%v val=%v", met, val)
	}
	if want := "web-1 disk usage 92.4% (threshold 85%)"; msg != want {
		t.Errorf("msg = %q, want %q", msg, want)
	}

	if met, _, _ := e.check(rule, target{host: "web-1", diskPct: 84.9}, nil); met {
		t.Error("84.9% must not fire against an 85% threshold")
	}
}
