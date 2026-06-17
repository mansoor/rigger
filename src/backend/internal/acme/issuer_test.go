package acme

import (
	"strings"
	"testing"
)

func testIssuer() *Issuer {
	return &Issuer{DynDir: "/dynamic", Image: "goacme/lego:test", Token: "tok", Volume: "rigger-dynamic"}
}

func TestLegoArgsRun(t *testing.T) {
	i := testIssuer()
	got := strings.Join(i.legoArgs("app.example.com", "ops@example.com", "run"), " ")
	for _, want := range []string{
		"run --rm",
		"-e CF_DNS_API_TOKEN=tok",
		"-v rigger-dynamic:/data",
		"goacme/lego:test",
		"--accept-tos",
		"--email ops@example.com",
		"--dns cloudflare",
		"--domains app.example.com",
		"--path /data",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("run args missing %q:\n%s", want, got)
		}
	}
	// The lego action subcommand must come LAST (after the global flags).
	if !strings.HasSuffix(got, " run") {
		t.Errorf("expected action 'run' last, got: %s", got)
	}
	if strings.Contains(got, "--days") {
		t.Errorf("run must not pass --days: %s", got)
	}
}

func TestLegoArgsRenew(t *testing.T) {
	i := testIssuer()
	got := strings.Join(i.legoArgs("app.example.com", "ops@example.com", "renew"), " ")
	if !strings.HasSuffix(got, "renew --days 30") {
		t.Errorf("expected 'renew --days 30' at the end, got: %s", got)
	}
}

func TestDynamicConfigYAML(t *testing.T) {
	i := testIssuer()
	got := i.dynamicConfigYAML("app.example.com")
	for _, want := range []string{
		"tls:",
		"certificates:",
		"certFile: /dynamic/certificates/app.example.com.crt",
		"keyFile: /dynamic/certificates/app.example.com.key",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("yaml missing %q:\n%s", want, got)
		}
	}
}

func TestCertBaseSanitizesWildcard(t *testing.T) {
	if got := certBase("*.example.com"); got != "_.example.com" {
		t.Errorf("wildcard sanitize: got %q want _.example.com", got)
	}
	if got := certBase("App.Example.COM "); got != "app.example.com" {
		t.Errorf("lowercase/trim: got %q", got)
	}
}

func TestEnabled(t *testing.T) {
	if (&Issuer{Token: ""}).Enabled() {
		t.Error("empty token should be disabled")
	}
	if !(&Issuer{Token: "x"}).Enabled() {
		t.Error("token present should be enabled")
	}
}
