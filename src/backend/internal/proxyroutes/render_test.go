package proxyroutes

import (
	"strings"
	"testing"
)

func render(t *testing.T, r Route) string {
	t.Helper()
	y, err := routeYAML(r, t.TempDir(), nil, false, false)
	if err != nil {
		t.Fatalf("routeYAML: %v", err)
	}
	return y
}

func mustContain(t *testing.T, y string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(y, s) {
			t.Errorf("rendered config missing %q\n---\n%s", s, y)
		}
	}
}

func TestProxyRouteWithTLSAuthAndForceHTTPS(t *testing.T) {
	y := render(t, Route{
		ID: 7, Name: "Jellyfin", Enabled: true, Type: "proxy",
		Host: "media.example.com", PassHostHeader: true,
		Upstreams: []Upstream{{Scheme: "http", Host: "192.168.1.50", Port: 8096}},
		TLSMode:   "le-http", ForceHTTPS: true,
		AuthMode: "basic", AuthUsers: []BasicUser{{User: "admin", Hash: "$2y$xx"}},
		HSTSSeconds: 31536000, SecurityHeaders: true,
	})
	mustContain(t, y,
		"proxy-7:",
		"Host(`media.example.com`)",
		`entryPoints: ["websecure"]`,
		"certResolver: letsencrypt",
		`url: "http://192.168.1.50:8096"`,
		"passHostHeader: true",
		"proxy-7-auth:",
		"basicAuth:",
		`"admin:$2y$xx"`,
		"stsSeconds: 31536000",
		"frameDeny: true",
		"proxy-7-http:", // the web->websecure redirect router
		"redirectScheme:",
	)
}

func TestRedirectRoute(t *testing.T) {
	y := render(t, Route{
		ID: 3, Name: "Old", Enabled: true, Type: "redirect",
		Host: "old.example.com", RedirectTo: "https://new.example.com", RedirectCode: 301,
		TLSMode: "le-http",
	})
	mustContain(t, y,
		"proxy-3-redirect:",
		"redirectRegex:",
		`replacement: "https://new.example.com"`,
		"permanent: true",
		"service: proxy-rigger@file",
	)
}

func TestDefaultRouteStatus404(t *testing.T) {
	y := render(t, Route{
		ID: 9, Name: "Default", Enabled: true, IsDefault: true, DefaultMode: "404",
	})
	mustContain(t, y,
		"PathPrefix(`/`)",
		"priority: 2",
		"replacePath:",
		`path: "/__proxydefault/404"`,
		"service: proxy-rigger@file",
	)
}

func TestDefaultRoutePageEmitsNothing(t *testing.T) {
	y := render(t, Route{ID: 1, Enabled: true, IsDefault: true, DefaultMode: "page"})
	if y != "" {
		t.Errorf("default page mode should emit nothing, got:\n%s", y)
	}
}

func TestIPAllowAndExistingCert(t *testing.T) {
	y := render(t, Route{
		ID: 5, Name: "wiki", Enabled: true, Type: "proxy", Host: "wiki.example.com",
		Upstreams: []Upstream{{Scheme: "https", Host: "10.0.0.8", Port: 8080}},
		InsecureSkipVerify: true, TLSMode: "existing", IPAllow: "192.168.0.0/16, 10.0.0.0/8",
	})
	mustContain(t, y,
		"proxy-5-ipallow:",
		"ipAllowList:",
		`"192.168.0.0/16"`,
		`"10.0.0.0/8"`,
		"serversTransport: proxy-5-transport",
		"insecureSkipVerify: true",
		"tls: {}", // existing cert → no resolver
	)
}
