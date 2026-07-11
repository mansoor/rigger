package edge

import (
	"strings"
	"testing"
)

func TestEdgeDir(t *testing.T) {
	cases := map[string]string{
		"/opt/rigger/workspaces":  "/opt/rigger/edge",
		"/opt/rigger/workspaces/": "/opt/rigger/edge",
		"/toolkit/workspaces":     "/toolkit/edge",
		"":                        "/opt/rigger/edge",
	}
	for in, want := range cases {
		if got := EdgeDir(in); got != want {
			t.Errorf("EdgeDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComposeStandalone(t *testing.T) {
	yml := composeYAML("/opt/rigger/edge", Options{Swarm: false})
	must := []string{
		"container_name: rigger-edge-traefik",
		"external: true",
		"/opt/rigger/edge/traefik.yml:/etc/traefik/traefik.yml:ro",
		"/var/run/docker.sock:/var/run/docker.sock:ro",
		"traefik.http.routers.rigger-fallback.priority=1",
	}
	for _, s := range must {
		if !strings.Contains(yml, s) {
			t.Errorf("standalone compose missing %q", s)
		}
	}
	// Standalone must NOT use swarm-only constructs.
	for _, bad := range []string{"node.role == manager", "deploy:"} {
		if strings.Contains(yml, bad) {
			t.Errorf("standalone compose unexpectedly contains %q", bad)
		}
	}
}

func TestComposeSwarm(t *testing.T) {
	yml := composeYAML("/opt/rigger/edge", Options{Swarm: true})
	must := []string{
		"node.role == manager",
		"deploy:",
		"labels:",
		"traefik.http.services.rigger-fallback.loadbalancer.server.port=80",
	}
	for _, s := range must {
		if !strings.Contains(yml, s) {
			t.Errorf("swarm compose missing %q", s)
		}
	}
	// Swarm forbids container_name.
	if strings.Contains(yml, "container_name") {
		t.Error("swarm compose must not set container_name")
	}
}

func TestLoadingMiddlewareProvider(t *testing.T) {
	if !strings.Contains(loadingMiddleware(false), "rigger-fallback@docker") {
		t.Error("standalone loading middleware should reference rigger-fallback@docker")
	}
	if !strings.Contains(loadingMiddleware(true), "rigger-fallback@swarm") {
		t.Error("swarm loading middleware should reference rigger-fallback@swarm")
	}
}

func TestAssetsEmbedded(t *testing.T) {
	for _, name := range []string{"docker-proxy.nginx.conf", "fallback.default.conf", "fallback.html"} {
		if len(asset(name)) == 0 {
			t.Errorf("embedded asset %q is empty/missing", name)
		}
	}
}
