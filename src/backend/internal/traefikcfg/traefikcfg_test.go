package traefikcfg

import (
	"strings"
	"testing"
)

func TestGenerate(t *testing.T) {
	// Stock (no plugins): full static config, but no experimental.plugins block.
	stock := Generate(Options{ACMEEmail: "ops@acme.io"})
	for _, sub := range []string{
		"entryPoints:", "address: \":80\"", "address: \":443\"",
		"providers:", "endpoint: \"tcp://socket-proxy:2375\"", "exposedByDefault: false",
		"directory: \"/dynamic\"", "watch: true",
		"certificatesResolvers:", "email: \"ops@acme.io\"", "storage: \"/certs/acme.json\"",
		"dnsChallenge:", "provider: cloudflare", "api:", "dashboard: false",
	} {
		if !strings.Contains(stock, sub) {
			t.Errorf("stock config missing %q\n%s", sub, stock)
		}
	}
	if strings.Contains(stock, "experimental:") {
		t.Errorf("stock config must not declare plugins:\n%s", stock)
	}
	// No swarm provider by default (single-node compose install).
	if strings.Contains(stock, "swarm:") {
		t.Errorf("stock config must not emit providers.swarm:\n%s", stock)
	}

	// Swarm manager: providers.swarm appears alongside providers.docker.
	sw := Generate(Options{ACMEEmail: "ops@acme.io", Swarm: true})
	if !strings.Contains(sw, "  swarm:\n    endpoint: \"tcp://socket-proxy:2375\"") {
		t.Errorf("swarm-manager config must emit providers.swarm:\n%s", sw)
	}
	if !strings.Contains(sw, "  docker:\n") {
		t.Errorf("providers.docker must remain for compose stacks:\n%s", sw)
	}

	// WAF + GeoIP enabled (cache off): only those two plugins, with their versions.
	on := Generate(Options{ACMEEmail: "x@y.z", WAF: true, GeoIP: true,
		CorazaVer: "v0.2.1", SouinVer: "v1.7.8", GeoblockVer: "v0.14.0"})
	for _, sub := range []string{
		"experimental:\n  plugins:",
		"coraza:\n      moduleName: \"github.com/jcchavezs/coraza-http-wasm-traefik\"\n      version: \"v0.2.1\"",
		"geoblock:\n      moduleName: \"github.com/nscuro/traefik-plugin-geoblock\"\n      version: \"v0.14.0\"",
	} {
		if !strings.Contains(on, sub) {
			t.Errorf("enabled config missing %q\n%s", sub, on)
		}
	}
	if strings.Contains(on, "souin:") {
		t.Errorf("cache disabled — souin must not be declared:\n%s", on)
	}
}
