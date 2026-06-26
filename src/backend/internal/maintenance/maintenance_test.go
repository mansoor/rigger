package maintenance

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestApplyAndClear(t *testing.T) {
	dir := t.TempDir()
	hosts := []string{"mcl-qrh-dev.10.10.10.111.nip.io", "qrhub.example.com"}
	if err := Apply(dir, "mcl", "qrh", "dev", hosts, false); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir, "mcl", "qrh", "dev") {
		t.Fatal("fragment not written")
	}
	b, err := os.ReadFile(dir + "/maint-mcl-qrh-dev.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"Host(`mcl-qrh-dev.10.10.10.111.nip.io`) || Host(`qrhub.example.com`)",
		"priority: 100000",
		"service: rigger-maint@file",
		`entryPoints: ["web"]`,
		"replacement: \"/maintenance/mcl/qrh/dev\"",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	// non-SSL ⇒ no websecure router / tls.
	if strings.Contains(s, "websecure") || strings.Contains(s, "tls:") {
		t.Errorf("unexpected SSL router in non-SSL fragment:\n%s", s)
	}
	if err := Clear(dir, "mcl", "qrh", "dev"); err != nil {
		t.Fatal(err)
	}
	if Exists(dir, "mcl", "qrh", "dev") {
		t.Fatal("fragment not removed")
	}
	// Clear is idempotent.
	if err := Clear(dir, "mcl", "qrh", "dev"); err != nil {
		t.Errorf("second Clear errored: %v", err)
	}
}

func TestApplySSLAddsSecureRouter(t *testing.T) {
	dir := t.TempDir()
	if err := Apply(dir, "ws", "proj", "prod", []string{"app.example.com"}, true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dir + "/maint-ws-proj-prod.yml")
	s := string(b)
	for _, want := range []string{`entryPoints: ["websecure"]`, "tls: {}"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in SSL fragment:\n%s", want, s)
		}
	}
}

func TestApplyNoHostsErrors(t *testing.T) {
	if err := Apply(t.TempDir(), "ws", "proj", "dev", nil, false); err == nil {
		t.Fatal("expected error when no hosts")
	}
}

func TestActiveAt(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cases := []struct {
		name string
		r    Record
		want bool
	}{
		{"manual enabled", Record{Enabled: true}, true},
		{"in window", Record{WindowStart: 999_000, WindowEnd: 1_001_000}, true},
		{"before window", Record{WindowStart: 1_001_000, WindowEnd: 1_002_000}, false},
		{"after window", Record{WindowStart: 998_000, WindowEnd: 999_000}, false},
		{"no state", Record{}, false},
	}
	for _, c := range cases {
		if got := c.r.ActiveAt(now); got != c.want {
			t.Errorf("%s: ActiveAt=%v want %v", c.name, got, c.want)
		}
	}
}
