package proxyroutes

import (
	"strings"
	"testing"
)

func render(t *testing.T, r Route) string {
	t.Helper()
	y, err := routeYAML(r, t.TempDir(), nil, false, false, false, nil)
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

func TestAccessListAppliedToRoute(t *testing.T) {
	lists := map[int64]AccessList{
		9: {
			ID: 9, Name: "office", PassAuth: false,
			Users: []BasicUser{{User: "bob", Hash: "$2y$zz"}},
			Rules: []AccessRule{{Action: "allow", Address: "10.0.0.0/8"}, {Action: "deny", Address: "1.2.3.4"}},
		},
	}
	y, err := routeYAML(Route{
		ID: 4, Name: "app", Enabled: true, Type: "proxy", Host: "app.example.com",
		Upstreams: []Upstream{{Scheme: "http", Host: "10.0.0.2", Port: 3000}},
		TLSMode:   "none", AccessListID: 9,
	}, t.TempDir(), nil, false, false, false, lists)
	if err != nil {
		t.Fatalf("routeYAML: %v", err)
	}
	mustContain(t, y,
		"proxy-4-auth:", "basicAuth:", "removeHeader: true", `"bob:$2y$zz"`,
		"proxy-4-ipallow:", `"10.0.0.0/8"`,
	)
	if strings.Contains(y, "1.2.3.4") {
		t.Errorf("deny address must not appear in the Traefik allow-list:\n%s", y)
	}
}

func TestAccessListGeoBlock(t *testing.T) {
	lists := map[int64]AccessList{
		2: {ID: 2, Name: "geo", GeoMode: "block", Countries: []string{"RU", "CN"}},
		3: {ID: 3, Name: "geoallow", GeoMode: "allow", Countries: []string{"US"}},
	}
	r := func(id, alID int64) Route {
		return Route{ID: id, Name: "g", Enabled: true, Type: "proxy", Host: "g.example.com",
			Upstreams: []Upstream{{Scheme: "http", Host: "10.0.0.9", Port: 80}}, TLSMode: "none", AccessListID: alID}
	}
	// geo plugin disabled instance-wide → no geo middleware even with a policy.
	off, _ := routeYAML(r(6, 2), t.TempDir(), nil, false, false, false, lists)
	if strings.Contains(off, "-geo:") {
		t.Errorf("geo middleware must not render when the plugin is disabled:\n%s", off)
	}
	// block mode enabled.
	y, _ := routeYAML(r(6, 2), t.TempDir(), nil, false, false, true, lists)
	mustContain(t, y, "proxy-6-geo:", "geoblock:", "blockedCountries:", `"RU"`, `"CN"`, "defaultAllow: true")
	// allow mode enabled.
	ya, _ := routeYAML(r(7, 3), t.TempDir(), nil, false, false, true, lists)
	mustContain(t, ya, "proxy-7-geo:", "allowedCountries:", `"US"`, "defaultAllow: false")
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
