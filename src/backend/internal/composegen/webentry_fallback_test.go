package composegen

import (
	"strings"
	"testing"
	"time"
)

// A multi-service image stack (app + db) that marks NO web entry — the exact shape of
// the bundled Gitea/Ghost/WordPress templates — must still route to the application
// service under Traefik. Before the fix the generator left every service unrouted
// (the "multi-service stacks pick a web entry explicitly" bail), so Traefik emitted no
// router and the env 404'd on its domain while the containers looked healthy.
// The datastore-aware fallback promotes the first non-datastore service (here `app`),
// never the database.
func TestWebEntryFallbackMultiServicePicksApp(t *testing.T) {
	// `db` (postgres) is declared FIRST to prove the pick is datastore-aware, not just
	// "service[0]".
	cfg := `{
		"project":{"name":"git","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services":[
			{"name":"db","image":"postgres","tag":"15-alpine","port":"5432"},
			{"name":"app","image":"gitea/gitea","tag":"1.21","port":"3000","host_port":"3000"}
		],
		"environments":{"dev":{"deployment":"compose","domain":"git.example.com","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	app := svcBlock(t, s, "app")
	if !strings.Contains(app, "traefik.http.routers.") {
		t.Errorf("app service must get a Traefik router via the web-entry fallback\n---\n%s", app)
	}
	if !strings.Contains(app, "Host(`git.example.com`)") {
		t.Errorf("app router must match the env domain\n---\n%s", app)
	}
	if !strings.Contains(app, "loadbalancer.server.port=3000") {
		t.Errorf("app router must target the app's container port 3000\n---\n%s", app)
	}
	if db := svcBlock(t, s, "db"); strings.Contains(db, "traefik.http.routers.") {
		t.Errorf("datastore service must NOT be promoted to the web entry\n---\n%s", db)
	}
}

// When every service looks like a datastore there is no sensible web entry to guess, so
// the generator bails (parity with the old behavior of never inventing a router) rather
// than routing the domain to a database.
func TestWebEntryFallbackAllDatastoresBails(t *testing.T) {
	cfg := `{
		"project":{"name":"data","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services":[
			{"name":"pg","image":"postgres","tag":"15-alpine","port":"5432"},
			{"name":"cache","image":"redis","tag":"7-alpine","port":"6379"}
		],
		"environments":{"dev":{"deployment":"compose","domain":"data.example.com","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "traefik.http.routers.") {
		t.Errorf("an all-datastore stack must not auto-route any service\n---\n%s", out)
	}
}

// A single unmarked service is still unambiguously the web entry (behavior preserved),
// and its missing container port defaults to 80.
func TestWebEntryFallbackSingleServicePromoted(t *testing.T) {
	cfg := `{
		"project":{"name":"site","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services":[{"name":"web","image":"nginx","tag":"alpine"}],
		"environments":{"prod":{"deployment":"compose","domain":"site.example.com","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`
	out, err := GenerateAt([]byte(cfg), "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	web := svcBlock(t, string(out), "web")
	if !strings.Contains(web, "traefik.http.routers.") {
		t.Errorf("sole service must be promoted to the web entry\n---\n%s", web)
	}
	if !strings.Contains(web, "loadbalancer.server.port=80") {
		t.Errorf("missing container port must default to 80\n---\n%s", web)
	}
}

// The Grafana + Prometheus template shape, post-fix: no unsded config-file bind mount
// (which Docker would auto-create as a directory → OCI "not a directory" mount failure),
// and `extra_compose: user "0:0"` so each non-root image (Prometheus runs as nobody,
// Grafana as 472) can write its host-owned bind-mounted data dir. Locks in both template
// fixes + verifies extra_compose rendering, which had no prior test coverage.
func TestGrafanaTemplateShapeRenders(t *testing.T) {
	cfg := `{
		"project":{"name":"gra","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services":[
			{"name":"prometheus","image":"prom/prometheus","tag":"v2.48.0","port":"9090",
			 "host_port":"9090","extra_compose":"user: \"0:0\"",
			 "volumes":["./volumes/prometheus_data:/prometheus"]},
			{"name":"grafana","image":"grafana/grafana","tag":"10.2.0","port":"3000","web_routed":true,
			 "host_port":"3001","extra_compose":"user: \"0:0\"",
			 "volumes":["./volumes/grafana_data:/var/lib/grafana"]}
		],
		"environments":{"dev":{"deployment":"compose","domain":"gra.example.com","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "/etc/prometheus/prometheus.yml") {
		t.Errorf("must not bind an unsded prometheus.yml (auto-created as a dir → mount failure)\n%s", s)
	}
	// extra_compose lands as a 4-space-indented `user: "0:0"` line inside each service.
	if n := strings.Count(s, `    user: "0:0"`); n != 2 {
		t.Errorf("want user:\"0:0\" on both services, found %d\n%s", n, s)
	}
	if prom := svcBlock(t, s, "prometheus"); !strings.Contains(prom, `    user: "0:0"`) {
		t.Errorf("prometheus must run as root to write its bind-mounted data dir\n%s", prom)
	}
	if graf := svcBlock(t, s, "grafana"); !strings.Contains(graf, "traefik.http.routers.") {
		t.Errorf("grafana must be the web entry\n%s", graf)
	}
}

func TestIsDatastoreImage(t *testing.T) {
	cases := map[string]bool{
		"postgres":                              true,
		"postgres:15-alpine":                    true,
		"bitnami/postgresql":                    true,
		"docker.io/library/mysql":               true,
		"redis":                                 true,
		"clickhouse/clickhouse-server":          true, // fork variant (substring)
		"tensorchord/pgvecto-rs":                true, // Postgres+pgvector build (Immich)
		"pgvector/pgvector":                     true,
		"postgis/postgis":                       true,
		"gitea/gitea":                           false,
		"nginx":                                 false,
		"grafana/grafana":                       false,
		"prom/prometheus":                       false,
		"ghcr.io/immich-app/immich-server":      false,
		"ghcr.io/plausible/community-edition":   false,
		"":                                      false,
	}
	for img, want := range cases {
		if got := isDatastoreImage(img); got != want {
			t.Errorf("isDatastoreImage(%q) = %v, want %v", img, got, want)
		}
	}
}
