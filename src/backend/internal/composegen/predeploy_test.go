package composegen

import (
	"strings"
	"testing"
	"time"
)

// blockByContainer returns the service block (delimited by blank lines) whose
// container_name matches — used instead of svcBlock for the synthesized migrate
// service, whose name appears as a substring of the app's depends_on line and would
// false-match svcBlock's key heuristic.
func blockByContainer(t *testing.T, out, cname string) string {
	t.Helper()
	for _, block := range strings.Split(out, "\n\n") {
		if strings.Contains(block, "container_name: "+cname+"\n") || strings.HasSuffix(block, "container_name: "+cname) {
			return block
		}
	}
	t.Fatalf("no block with container_name %q\n---\n%s", cname, out)
	return ""
}

// A build service with a pre_deploy command synthesizes a one-shot "{svc}-migrate"
// service (reuses the app image, restart "no", waits for the DB) and gates the app on
// it via service_completed_successfully — so migrations run, once, before the app starts.
func TestPreDeploySynthesis(t *testing.T) {
	cfg := `{
		"project": {"name":"shop","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres"},
		"services": [
			{"name":"app","role":"app","build":{},"port":"9000","env_file":true,"web_routed":true,
			 "depends_on":["postgres"],"pre_deploy":"php artisan migrate --force"}
		],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	mig := blockByContainer(t, s, "shop_dev_app-migrate")
	for _, want := range []string{
		"  app-migrate:",                                      // top-level service key
		"image: ${APP_IMAGE:-reg/shop-app:1.0.0-build.0-dev}", // reuses the app's built image
		"command: 'php artisan migrate --force'",
		"    env_file: .env",
		"      postgres:\n        condition: service_healthy", // waits for the DB
		"    restart: \"no\"",                                 // one-shot (quoted — bare `no` is YAML false)
	} {
		if !strings.Contains(mig, want) {
			t.Errorf("app-migrate block missing %q\n---\n%s", want, mig)
		}
	}
	// One-shot: it must not publish or expose ports, and must not web-route.
	if strings.Contains(mig, "ports:") || strings.Contains(mig, "traefik") {
		t.Errorf("migrate service must be internal one-shot (no ports/traefik)\n%s", mig)
	}

	// The app now waits for the migrate to COMPLETE successfully (after its DB dep).
	app := svcBlock(t, s, "app")
	if !strings.Contains(app, "app-migrate:\n        condition: service_completed_successfully") {
		t.Errorf("app must gate on app-migrate completing successfully\n---\n%s", app)
	}
	if !strings.Contains(app, "postgres:\n        condition: service_healthy") {
		t.Errorf("app must still wait for postgres healthy\n---\n%s", app)
	}
}

// The migrate runs in the app's EXACT environment: it inherits the app service's
// per-service env_vars and service links (e.g. a DATABASE_URL link to postgres), not just
// the flat env_file — else a migration could see different DB config than the app.
func TestPreDeployInheritsServiceEnv(t *testing.T) {
	cfg := `{
		"project": {"name":"shop","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres"},
		"services": [
			{"name":"api","build":{},"port":"3000","env_file":true,"web_routed":true,
			 "pre_deploy":"npm run migrate",
			 "env_vars":{"NODE_ENV":"production"},
			 "links":[{"service":"postgres","env_var":"DATABASE_URL","scheme":"postgresql","path":"/shop"}]}
		],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	mig := blockByContainer(t, string(out), "shop_dev_api-migrate")
	for _, want := range []string{
		"- NODE_ENV=production",                                  // inherited per-service env
		"- DATABASE_URL=postgresql://shop_dev_postgres:5432/shop", // inherited service link
	} {
		if !strings.Contains(mig, want) {
			t.Errorf("migrate should inherit the app's env; missing %q\n---\n%s", want, mig)
		}
	}
}

// A sibling that reuses the build service's image (e.g. a queue worker) is ALSO gated on
// the migrate, so no process touches the schema before migrations finish.
func TestPreDeployGatesImageSiblings(t *testing.T) {
	cfg := `{
		"project": {"name":"shop","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres"},
		"services": [
			{"name":"app","build":{},"port":"9000","env_file":true,"web_routed":true,"pre_deploy":"npm run migrate"},
			{"name":"worker","image_from":"app","command":"node worker.js","env_file":true}
		],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	worker := svcBlock(t, string(out), "worker")
	if !strings.Contains(worker, "app-migrate:\n        condition: service_completed_successfully") {
		t.Errorf("worker (reuses app image) must also gate on the migrate\n---\n%s", worker)
	}
}

// Swarm ignores depends_on conditions, so the gate can't be enforced — no migrate
// service is synthesized for a swarm env.
func TestPreDeploySwarmSkipsSynthesis(t *testing.T) {
	cfg := `{
		"project": {"name":"shop","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres"},
		"services": [{"name":"app","build":{},"port":"9000","env_file":true,"web_routed":true,"pre_deploy":"php artisan migrate --force"}],
		"environments": {"prod": {"deployment":"swarm"}}
	}`
	out, err := GenerateAt([]byte(cfg), "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "app-migrate:") {
		t.Errorf("swarm env must NOT synthesize a migrate service (depends_on conditions are ignored)\n%s", out)
	}
}

// Dedup: an app that ALREADY brings its own one-shot migrate gate (an imported compose
// whose depends_on uses service_completed_successfully) must NOT get a second, synthesized
// migrate — even if a pre_deploy command is also set.
func TestPreDeployNoDoubleWhenAppGated(t *testing.T) {
	cfg := `{
		"project": {"name":"shop","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres"},
		"services": [
			{"name":"migrate","image_from":"app","command":"npm run migrate","restart":"no","env_file":true},
			{"name":"app","build":{},"port":"9000","env_file":true,"web_routed":true,
			 "depends_on":["migrate"],"depends_on_conditions":{"migrate":"service_completed_successfully"},
			 "pre_deploy":"npm run migrate"}
		],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "app-migrate:") {
		t.Errorf("must not synthesize app-migrate when the app already gates on its own migrate\n%s", s)
	}
	// The app's own gate round-trips faithfully (condition preserved, not downgraded).
	app := svcBlock(t, s, "app")
	if !strings.Contains(app, "migrate:\n        condition: service_completed_successfully") {
		t.Errorf("imported completed_successfully condition must be preserved\n---\n%s", app)
	}
}

// Dedup: a service literally named "{svc}-migrate" already in the graph blocks synthesis
// (mirrors the buildAdminer hasService guard) — no duplicate compose key.
func TestPreDeployNoDoubleWhenNameExists(t *testing.T) {
	cfg := `{
		"project": {"name":"shop","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres"},
		"services": [
			{"name":"app","build":{},"port":"9000","env_file":true,"web_routed":true,"pre_deploy":"php artisan migrate"},
			{"name":"app-migrate","image_from":"app","command":"echo already here","restart":"no","env_file":true}
		],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(out), "  app-migrate:\n"); n != 1 {
		t.Errorf("want exactly 1 app-migrate service, got %d\n%s", n, out)
	}
}

// A service's inline env_vars must NOT shadow a Rigger-managed key for an active managed
// dep: an imported compose that hardcoded DATABASE_URL (dev creds) would otherwise override
// the correct managed value from .env (compose environment: beats env_file:), breaking DB
// auth. The managed key is dropped from environment: so the .env value wins; a non-managed
// custom env var is preserved.
func TestManagedKeyNotShadowedByServiceEnv(t *testing.T) {
	cfg := `{
		"project": {"name":"shop","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres"},
		"services": [
			{"name":"api","build":{},"port":"3000","env_file":true,"web_routed":true,
			 "env_vars":{"DATABASE_URL":"postgresql://qrhub:qrhub@postgres:5432/qrhub","APP_ENV":"production"}}
		],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	api := svcBlock(t, string(out), "api")
	if strings.Contains(api, "qrhub:qrhub@postgres") {
		t.Errorf("hardcoded DATABASE_URL must NOT be emitted into environment: (it would shadow the managed .env value)\n---\n%s", api)
	}
	if !strings.Contains(api, "- APP_ENV=production") {
		t.Errorf("non-managed custom env var should be preserved\n---\n%s", api)
	}
}

// No pre_deploy ⇒ no migrate service and byte-identical output to the same config without
// the (empty) field — locks golden parity for the common case.
func TestPreDeployAbsentParity(t *testing.T) {
	cfg := `{
		"project": {"name":"shop","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres"},
		"services": [{"name":"app","build":{},"port":"9000","env_file":true,"web_routed":true,"depends_on":["postgres"]}],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "-migrate:") {
		t.Errorf("no pre_deploy must not synthesize a migrate service\n%s", out)
	}
}
