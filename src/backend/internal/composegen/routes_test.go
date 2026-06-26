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

// With routes, the api service gets ONE router whose rule ORs both path prefixes, plus a
// priority so it outranks the web catch-all; the web service keeps a plain Host() rule.
func TestRoutesPathGrouping(t *testing.T) {
	s := genDev(t, twoSvcConfig(`[
	  {"service":"web","type":"path","match":"/"},
	  {"service":"api","type":"path","match":"/api"},
	  {"service":"api","type":"path","match":"/r"}
	]`))
	host := "mcl-qrh-dev.10.10.10.111.nip.io"
	apiRule := "traefik.http.routers.mcl_qrh_dev_api.rule=Host(" + bt + host + bt + ") && (PathPrefix(" + bt + "/api" + bt + ") || PathPrefix(" + bt + "/r" + bt + "))"
	webRule := "traefik.http.routers.mcl_qrh_dev_web.rule=Host(" + bt + host + bt + ")\""
	for _, want := range []string{
		apiRule,
		webRule,
		"traefik.http.routers.mcl_qrh_dev_api.priority=",
		"traefik.http.services.mcl_qrh_dev_api.loadbalancer.server.port=4000",
		"traefik.http.services.mcl_qrh_dev_web.loadbalancer.server.port=3000",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	// The web catch-all must NOT carry a PathPrefix (it's the default route).
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "routers.mcl_qrh_dev_web.rule=") && strings.Contains(line, "PathPrefix") {
			t.Errorf("web catch-all router unexpectedly has a PathPrefix: %s", line)
		}
	}
}

// strip_prefix on a path route emits a stripprefix middleware with the matched prefix(es).
func TestRoutesStripPrefix(t *testing.T) {
	s := genDev(t, twoSvcConfig(`[
	  {"service":"web","type":"path","match":"/"},
	  {"service":"api","type":"path","match":"/api","strip_prefix":true}
	]`))
	if !strings.Contains(s, "traefik.http.middlewares.mcl_qrh_dev_api_strip.stripprefix.prefixes=/api") {
		t.Errorf("missing stripprefix middleware in:\n%s", s)
	}
	if !strings.Contains(s, "_strip,rigger-loading@file") {
		t.Errorf("strip middleware not wired into the router chain in:\n%s", s)
	}
}

// A subdomain route gets its own Host(sub.domain) router, named {cname}_sd{i}.
func TestRoutesSubdomain(t *testing.T) {
	s := genDev(t, twoSvcConfig(`[
	  {"service":"web","type":"path","match":"/"},
	  {"service":"api","type":"subdomain","match":"admin"}
	]`))
	want := "traefik.http.routers.mcl_qrh_dev_api_sd0.rule=Host(" + bt + "admin.mcl-qrh-dev.10.10.10.111.nip.io" + bt + ")"
	if !strings.Contains(s, want) {
		t.Errorf("missing subdomain router %q in:\n%s", want, s)
	}
}

// With NO routes the generator is unchanged: the web_routed service gets the plain legacy
// Host() rule, the api stays unrouted (in-network expose), and no route-mode extras appear.
func TestRoutesEmptyLegacyParity(t *testing.T) {
	s := genDev(t, twoSvcConfig(""))
	host := "mcl-qrh-dev.10.10.10.111.nip.io"
	if !strings.Contains(s, "traefik.http.routers.mcl_qrh_dev_web.rule=Host("+bt+host+bt+")") {
		t.Errorf("legacy web Host rule missing in:\n%s", s)
	}
	if strings.Contains(s, ".priority=") || strings.Contains(s, ".stripprefix.") {
		t.Errorf("route-mode extras leaked into a no-routes config:\n%s", s)
	}
	// api is not web_routed and has no routes → it must NOT get a Traefik router.
	if strings.Contains(s, "routers.mcl_qrh_dev_api.rule=") {
		t.Errorf("api unexpectedly routed without any route:\n%s", s)
	}
}
