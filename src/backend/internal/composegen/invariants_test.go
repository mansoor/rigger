package composegen

import (
	"regexp"
	"strings"
	"testing"
)

// Cross-cutting invariants for the compose generator.
//
// Most tests here assert one SHAPE (this config → these labels). These assert
// RELATIONSHIPS that must hold for EVERY config, so they catch the "two code paths
// encode the same decision and drift apart" bug class — where a fix updates one path
// and silently breaks another. The motivating case: a route-table backend got Traefik
// router labels (emitServicePorts) but was left off traefik_net (network attachment),
// so Traefik routed to a container it couldn't reach. See traefikRouted().
//
// To guard a new invariant: add a check to checkInvariants; every corpus config is run
// through it. To widen coverage: add a config to invariantCorpus.

// invariantCorpus is a spread of real-world-shaped configs exercising the label/network/
// port paths: route-table multi-service (the bug), legacy single web entry, subdomain
// sidecars, subdomain multi-service, a no-Traefik host-port stack, and a Swarm deployment.
var invariantCorpus = []struct {
	name string
	cfg  []byte
}{
	{"qrhub-route-table", twoSvcConfig(`[
	  {"service":"web","type":"path","match":"/"},
	  {"service":"api","type":"path","match":"/api"},
	  {"service":"api","type":"path","match":"/r"}
	]`)},
	{"single-web-entry-no-routes", twoSvcConfig("")},
	{"route-table-plus-subdomain-sidecar", []byte(`{
	  "project":{"name":"qrh","resource_prefix":"mcl_qrh","type":"custom"},
	  "services":[
	    {"name":"web","image":"web","tag":"latest","port":3000,"web_routed":true,"env_file":true},
	    {"name":"api","image":"api","tag":"latest","port":4000,"env_file":true},
	    {"name":"adminer","image":"adminer","tag":"latest","port":8080,"web_routed":true,"subdomain":"adminer","auth_protect":true}
	  ],
	  "routes":[
	    {"service":"web","type":"path","match":"/"},
	    {"service":"api","type":"path","match":"/api"}
	  ],
	  "environments":{"dev":{"deployment":"compose","traefik_enabled":true,"traefik_network":"traefik_net","domain":""}}
	}`)},
	{"subdomain-multiservice-no-routes", []byte(`{
	  "project":{"name":"qsd","resource_prefix":"mcl_qsd","type":"custom"},
	  "services":[
	    {"name":"web","image":"web","tag":"latest","port":3000,"web_routed":true,"env_file":true},
	    {"name":"admin","image":"admin","tag":"latest","port":8080,"web_routed":true,"subdomain":"admin","env_file":true}
	  ],
	  "environments":{"dev":{"deployment":"compose","traefik_enabled":true,"traefik_network":"traefik_net","domain":""}}
	}`)},
	{"laravel-managed-deps-host-port", []byte(shopCfg)},
	{"swarm-route-table", []byte(`{
	  "project":{"name":"qsw","resource_prefix":"mcl_qsw","type":"custom"},
	  "services":[
	    {"name":"web","image":"web","tag":"latest","port":3000,"web_routed":true,"env_file":true},
	    {"name":"api","image":"api","tag":"latest","port":4000,"env_file":true}
	  ],
	  "routes":[
	    {"service":"web","type":"path","match":"/"},
	    {"service":"api","type":"path","match":"/api"}
	  ],
	  "environments":{"dev":{"deployment":"swarm","traefik_enabled":true,"traefik_network":"traefik_net","domain":""}}
	}`)},
}

func TestComposeInvariants(t *testing.T) {
	for _, c := range invariantCorpus {
		t.Run(c.name, func(t *testing.T) {
			out := genDev(t, c.cfg)
			checkInvariants(t, out)
		})
	}
}

var (
	svcHeaderRe   = regexp.MustCompile(`^  ([A-Za-z0-9_-]+):[ \t]*$`)
	onTraefikNet  = regexp.MustCompile(`(?m)^\s+traefik_net:`)                                        // network attachment (map or `: {}`)
	routerSvcRe   = regexp.MustCompile(`traefik\.http\.routers\.[A-Za-z0-9_-]+\.service=([A-Za-z0-9_-]+)`)
	lbServiceRe   = regexp.MustCompile(`traefik\.http\.services\.([A-Za-z0-9_-]+)\.loadbalancer\.server\.port=`)
	publishedPort = regexp.MustCompile(`(?m)^\s+- "?(\d{2,5}):\d{2,5}"?(/(?:tcp|udp))?\s*$`)          // host:container[/proto]
)

type svcView struct {
	name string
	body string
}

// parseServiceBlocks splits the top-level `services:` map into per-service bodies so
// per-service assertions don't bleed across services.
func parseServiceBlocks(out string) []svcView {
	var svcs []svcView
	inServices, cur := false, -1
	for _, ln := range strings.Split(out, "\n") {
		if ln != "" && ln[0] != ' ' { // a column-0 line
			if strings.HasPrefix(ln, "#") {
				continue // top-of-file comment banner
			}
			inServices = strings.TrimSpace(ln) == "services:"
			cur = -1
			continue
		}
		if !inServices {
			continue
		}
		if m := svcHeaderRe.FindStringSubmatch(ln); m != nil {
			svcs = append(svcs, svcView{name: m[1]})
			cur = len(svcs) - 1
			continue
		}
		if cur >= 0 {
			svcs[cur].body += ln + "\n"
		}
	}
	return svcs
}

func checkInvariants(t *testing.T, out string) {
	t.Helper()
	blocks := parseServiceBlocks(out)
	if len(blocks) == 0 {
		t.Fatalf("no service blocks parsed from:\n%s", out)
	}

	lbDefs := map[string]bool{}
	for _, m := range lbServiceRe.FindAllStringSubmatch(out, -1) {
		lbDefs[m[1]] = true
	}

	for _, s := range blocks {
		enabled := strings.Contains(s.body, "traefik.enable=true")

		// INV1 — a service Traefik is told to route MUST be on traefik_net, or the router
		// points at a backend Traefik can't reach (502/404). This is the QRHub bug.
		if enabled && !onTraefikNet.MatchString(s.body) {
			t.Errorf("[%s] has traefik.enable=true but is not attached to traefik_net:\n%s", s.name, s.body)
		}
		// INV1b — the inverse: don't attach a service to the proxy network unless it's
		// actually routed there (dead network membership hides real drift).
		if !enabled && onTraefikNet.MatchString(s.body) {
			t.Errorf("[%s] is on traefik_net but has no Traefik router (traefik.enable):\n%s", s.name, s.body)
		}
		// INV2 — a loadbalancer service definition is only meaningful with an enabled router
		// in the same block; the two are emitted as one coherent set.
		if strings.Contains(s.body, "loadbalancer.server.port=") && !enabled {
			t.Errorf("[%s] defines a loadbalancer service but has no traefik.enable=true:\n%s", s.name, s.body)
		}
		// INV3 — at most one `ports:` block per service (a second is invalid YAML — the
		// duplicate-key trap emitServicePorts exists to avoid).
		if strings.Count(s.body, "    ports:\n") > 1 {
			t.Errorf("[%s] emits more than one ports: block (invalid YAML):\n%s", s.name, s.body)
		}
	}

	// INV4 — every router's `.service=X` resolves to a defined loadbalancer service X.
	for _, m := range routerSvcRe.FindAllStringSubmatch(out, -1) {
		if !lbDefs[m[1]] {
			t.Errorf("router references service %q with no matching loadbalancer.server.port definition", m[1])
		}
	}

	// INV5 — published host ports are unique across the compose (a host port can bind once).
	// Keyed by host-port + protocol so 53:53/tcp and 53:53/udp don't false-positive.
	seen := map[string]bool{}
	for _, m := range publishedPort.FindAllStringSubmatch(out, -1) {
		key := m[1] + m[2]
		if seen[key] {
			t.Errorf("host port %s is published by more than one service (port conflict)", key)
		}
		seen[key] = true
	}
}
