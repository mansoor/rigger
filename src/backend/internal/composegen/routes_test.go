package composegen

import (
	"strings"
	"testing"
)

const bt = "`"

// twoSvc is a web (:3000, web entry) + api (:4000) custom stack on an auto-URL nip.io domain.
func twoSvcConfig(routesJSON string) []byte {
	routes := ""
	if routesJSON != "" {
		routes = `"routes":` + routesJSON + `,`
	}
	return []byte(`{
	  "project":{"name":"qrh","resource_prefix":"mcl_qrh","type":"custom"},
	  "services":[
	    {"name":"web","image":"web","tag":"latest","port":3000,"web_routed":true,"env_file":true},
	    {"name":"api","image":"api","tag":"latest","port":4000,"env_file":true}
	  ],
	  ` + routes + `
	  "environments":{"dev":{"deployment":"compose","traefik_enabled":true,"traefik_network":"traefik_net","domain":""}}
	}`)
}

func genDev(t *testing.T, cfg []byte) string {
	t.Helper()
	out, err := GenerateRouted(cfg, "dev", RouteOpts{AutoURLMode: "nip", AutoURLHost: "10.10.10.111"})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

const host = "mcl-qrh-dev.10.10.10.111.nip.io"

// Each route becomes its own router; path routes carry a PathPrefix + priority; the catch-all is
// a plain Host(); all of a service's routers share ONE loadbalancer service.
func TestRoutesPerRoute(t *testing.T) {
	s := genDev(t, twoSvcConfig(`[
	  {"service":"web","type":"path","match":"/"},
	  {"service":"api","type":"path","match":"/api"},
	  {"service":"api","type":"path","match":"/r"}
	]`))
	for _, want := range []string{
		"traefik.http.routers.mcl_qrh_dev_api_r0.rule=Host(" + bt + host + bt + ") && PathPrefix(" + bt + "/api" + bt + ")",
		"traefik.http.routers.mcl_qrh_dev_api_r1.rule=Host(" + bt + host + bt + ") && PathPrefix(" + bt + "/r" + bt + ")",
		"traefik.http.routers.mcl_qrh_dev_web_r0.rule=Host(" + bt + host + bt + ")\"",
		"traefik.http.routers.mcl_qrh_dev_api_r0.priority=",
		"traefik.http.routers.mcl_qrh_dev_api_r0.service=mcl_qrh_dev_api",
		"traefik.http.services.mcl_qrh_dev_api.loadbalancer.server.port=4000",
		"traefik.http.services.mcl_qrh_dev_web.loadbalancer.server.port=3000",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	// The web catch-all must NOT carry a PathPrefix.
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "routers.mcl_qrh_dev_web_r0.rule=") && strings.Contains(line, "PathPrefix") {
			t.Errorf("web catch-all router unexpectedly has a PathPrefix: %s", line)
		}
	}
}

// Target == "/" strips the prefix (stripprefix, no addprefix).
func TestRoutesTargetStrip(t *testing.T) {
	s := genDev(t, twoSvcConfig(`[
	  {"service":"web","type":"path","match":"/"},
	  {"service":"api","type":"path","match":"/api","target":"/"}
	]`))
	if !strings.Contains(s, "traefik.http.middlewares.mcl_qrh_dev_api_r0_strip.stripprefix.prefixes=/api") {
		t.Errorf("missing stripprefix middleware in:\n%s", s)
	}
	if strings.Contains(s, "mcl_qrh_dev_api_r0_addprefix") {
		t.Errorf("strip-only route should not emit an addprefix middleware:\n%s", s)
	}
}

// Target == "/api/v1" rewrites: strip /api then add /api/v1.
func TestRoutesTargetRewrite(t *testing.T) {
	s := genDev(t, twoSvcConfig(`[
	  {"service":"web","type":"path","match":"/"},
	  {"service":"api","type":"path","match":"/api","target":"/api/v1"}
	]`))
	for _, want := range []string{
		"traefik.http.middlewares.mcl_qrh_dev_api_r0_strip.stripprefix.prefixes=/api",
		"traefik.http.middlewares.mcl_qrh_dev_api_r0_addprefix.addprefix.prefix=/api/v1",
		"_strip,mcl_qrh_dev_api_r0_addprefix,rigger-loading@file",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

// Blank target (default) = passthrough: no strip / addprefix middlewares.
func TestRoutesPassthrough(t *testing.T) {
	s := genDev(t, twoSvcConfig(`[
	  {"service":"web","type":"path","match":"/"},
	  {"service":"api","type":"path","match":"/api"}
	]`))
	if strings.Contains(s, "mcl_qrh_dev_api_r0_strip") || strings.Contains(s, "mcl_qrh_dev_api_r0_addprefix") {
		t.Errorf("passthrough route should emit no rewrite middleware:\n%s", s)
	}
}

// A subdomain route gets its own Host(sub.domain) router.
func TestRoutesSubdomain(t *testing.T) {
	s := genDev(t, twoSvcConfig(`[
	  {"service":"web","type":"path","match":"/"},
	  {"service":"api","type":"subdomain","match":"admin"}
	]`))
	want := "traefik.http.routers.mcl_qrh_dev_api_r0.rule=Host(" + bt + "admin." + host + bt + ")"
	if !strings.Contains(s, want) {
		t.Errorf("missing subdomain router %q in:\n%s", want, s)
	}
}

// Regression: when a project defines a routing table (web/api), synthesized admin sidecars
// (Adminer / MinIO console / Mailpit) route on their OWN subdomain and are never listed in the
// path-routing table. They must keep their subdomain router instead of being dropped — otherwise
// adminer.{host} / storage.{host} fall through to the "not ready" fallback. (See qrhub.)
func TestRoutesSubdomainSidecarKeepsRouter(t *testing.T) {
	cfg := []byte(`{
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
	}`)
	s := genDev(t, cfg)
	// The sidecar (not in the route table) still gets a legacy subdomain router.
	want := "traefik.http.routers.mcl_qrh_dev_adminer.rule=Host(" + bt + "adminer." + host + bt + ")"
	if !strings.Contains(s, want) {
		t.Errorf("subdomain sidecar lost its router in route mode; missing %q in:\n%s", want, s)
	}
	// The route-table services still route by path.
	if !strings.Contains(s, "traefik.http.routers.mcl_qrh_dev_api_r0.rule=Host("+bt+host+bt+") && PathPrefix("+bt+"/api"+bt+")") {
		t.Errorf("api path route missing in:\n%s", s)
	}
}

// With NO routes the generator is unchanged: the web_routed service gets the plain legacy
// Host() rule, the api stays unrouted (in-network expose), and no route-mode extras appear.
func TestRoutesEmptyLegacyParity(t *testing.T) {
	s := genDev(t, twoSvcConfig(""))
	if !strings.Contains(s, "traefik.http.routers.mcl_qrh_dev_web.rule=Host("+bt+host+bt+")") {
		t.Errorf("legacy web Host rule missing in:\n%s", s)
	}
	if strings.Contains(s, ".priority=") || strings.Contains(s, ".stripprefix.") || strings.Contains(s, ".addprefix.") {
		t.Errorf("route-mode extras leaked into a no-routes config:\n%s", s)
	}
	if strings.Contains(s, "routers.mcl_qrh_dev_api.rule=") || strings.Contains(s, "routers.mcl_qrh_dev_api_r0.rule=") {
		t.Errorf("api unexpectedly routed without any route:\n%s", s)
	}
}

// A service routed ONLY via the route table (web_routed=false) must also JOIN traefik_net —
// otherwise Traefik emits a router whose backend it can't reach (502/404). Regression for the
// QRHub "api off traefik_net after refresh" bug: label emission used the route table but network
// attachment used the legacy web_routed flag, so the two disagreed for a path-routed backend.
func TestRoutesTableServiceJoinsTraefikNet(t *testing.T) {
	s := genDev(t, twoSvcConfig(`[
	  {"service":"web","type":"path","match":"/"},
	  {"service":"api","type":"path","match":"/api"}
	]`))
	api := svcBlock(t, s, "api")
	if !strings.Contains(api, "traefik_net: {}") {
		t.Errorf("route-table api service is not attached to traefik_net (Traefik can't reach it):\n%s", api)
	}
	// Sanity: the web entry is still attached too.
	if web := svcBlock(t, s, "web"); !strings.Contains(web, "traefik_net: {}") {
		t.Errorf("web entry lost traefik_net:\n%s", web)
	}
}

// Inverse guard: with NO routes, a non-web_routed service must NOT join traefik_net (it's
// in-network only) — the legacy behavior stays byte-identical.
func TestRoutesNoTableUnroutedServiceStaysOffTraefikNet(t *testing.T) {
	s := genDev(t, twoSvcConfig(""))
	if api := svcBlock(t, s, "api"); strings.Contains(api, "traefik_net") {
		t.Errorf("unrouted api joined traefik_net without any route:\n%s", api)
	}
}
