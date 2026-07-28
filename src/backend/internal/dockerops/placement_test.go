package dockerops

import (
	"strings"
	"testing"
)

// Shaped after a real generated stack: an app bind-mounting its .env, a database
// on a named volume, a pinned cache, and a stateless worker.
const placementCompose = `
volumes:
  mcl_sdm_dev_mysql_data:
  mcl_sdm_dev_redis_data:

services:

  app:
    image: registry.example.com/app:1.0.0
    volumes:
      - ${RIGGER_BIND_ROOT:-.}/.env:/var/www/html/.env
    deploy:
      replicas: 2

  mysql:
    image: mysql:8.0
    volumes:
      - mcl_sdm_dev_mysql_data:/var/lib/mysql
    deploy:
      replicas: 1

  redis:
    image: redis:7-alpine
    volumes:
      - mcl_sdm_dev_redis_data:/data
    deploy:
      replicas: 1
      placement:
        constraints:
          - node.hostname==worker-1

  worker:
    image: registry.example.com/app:1.0.0
    command: ["php", "artisan", "queue:work"]
    deploy:
      replicas: 3
`

func TestUnpinnedStatefulServices(t *testing.T) {
	risks, err := unpinnedStatefulServices([]byte(placementCompose))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got := map[string]string{}
	for _, r := range risks {
		got[r.Service] = r.Reason
	}

	// app: a bind mount only exists on the node the env dir was synced to.
	if !strings.HasPrefix(got["app"], "bind mount") {
		t.Errorf("app should be flagged for its bind mount, got %q", got["app"])
	}
	// mysql: a local named volume doesn't follow a rescheduled task.
	if !strings.HasPrefix(got["mysql"], "named volume") {
		t.Errorf("mysql should be flagged for its named volume, got %q", got["mysql"])
	}
	// redis is stateful too, but pinned — the operator has already handled it.
	if _, flagged := got["redis"]; flagged {
		t.Errorf("redis is pinned by a placement constraint and must not be flagged")
	}
	// worker holds nothing; Swarm can move it wherever it likes.
	if _, flagged := got["worker"]; flagged {
		t.Errorf("stateless worker must not be flagged")
	}
	if len(risks) != 2 {
		t.Fatalf("expected exactly app and mysql, got %+v", risks)
	}
}

// Deterministic ordering, so a deploy log doesn't churn between runs.
func TestRiskOrderIsStable(t *testing.T) {
	first, err := unpinnedStatefulServices([]byte(placementCompose))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := unpinnedStatefulServices([]byte(placementCompose))
		if err != nil {
			t.Fatal(err)
		}
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("ordering changed between runs: %+v vs %+v", first, again)
			}
		}
	}
}

func TestNoVolumesNoRisk(t *testing.T) {
	const stateless = `
services:
  api:
    image: example/api:1
    deploy:
      replicas: 4
  web:
    image: example/web:1
`
	risks, err := unpinnedStatefulServices([]byte(stateless))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(risks) != 0 {
		t.Fatalf("a stateless stack must produce no warning, got %+v", risks)
	}
}

// A mount whose source isn't a declared volume and isn't a host path (e.g. a
// tmpfs target, or a volume declared elsewhere) is not guessed at.
func TestUnknownSourceIgnored(t *testing.T) {
	const odd = `
volumes:
  known_data:
services:
  a:
    image: x
    volumes:
      - known_data:/data
  b:
    image: y
    volumes:
      - some_external_volume:/data
`
	risks, err := unpinnedStatefulServices([]byte(odd))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(risks) != 1 || risks[0].Service != "a" {
		t.Fatalf("only the declared named volume should be flagged, got %+v", risks)
	}
}

// Rigger emits bind sources like `${RIGGER_BIND_ROOT:-.}/.env`, whose colon
// belongs to the ${…} default — splitting on the first colon truncates the path
// mid-variable. Caught by running the analyser over a real generated compose.
func TestMountSourceHandlesVariableDefaults(t *testing.T) {
	cases := map[string]string{
		"${RIGGER_BIND_ROOT:-.}/.env:/var/www/html/.env":     "${RIGGER_BIND_ROOT:-.}/.env",
		"${RIGGER_BIND_ROOT:-.}/conf.d:/etc/nginx/conf.d:ro": "${RIGGER_BIND_ROOT:-.}/conf.d",
		"mysql_data:/var/lib/mysql":                          "mysql_data",
		"./volumes/data:/data":                               "./volumes/data",
		"/etc/localtime:/etc/localtime:ro":                   "/etc/localtime",
		"no_target_half":                                     "no_target_half",
	}
	for in, want := range cases {
		if got := mountSource(in); got != want {
			t.Errorf("mountSource(%q) = %q, want %q", in, got, want)
		}
	}
}

// The whole source must survive into the message, or the warning names a path
// the user can't find.
func TestBindReasonKeepsFullSource(t *testing.T) {
	const c = `
services:
  app:
    image: x
    volumes:
      - ${RIGGER_BIND_ROOT:-.}/.env:/var/www/html/.env
`
	risks, err := unpinnedStatefulServices([]byte(c))
	if err != nil {
		t.Fatal(err)
	}
	if len(risks) != 1 || risks[0].Reason != "bind mount ${RIGGER_BIND_ROOT:-.}/.env" {
		t.Fatalf("reason = %q, want the untruncated source", risks[0].Reason)
	}
}

func TestMalformedComposeIsAnError(t *testing.T) {
	if _, err := unpinnedStatefulServices([]byte("services: [this is not\n  valid: yaml")); err == nil {
		t.Fatal("expected a parse error rather than a silent empty result")
	}
}

// The long mapping form is valid compose that Rigger doesn't emit; it must be
// skipped rather than misread as a bind mount.
func TestLongFormVolumeSkipped(t *testing.T) {
	const long = `
services:
  a:
    image: x
    volumes:
      - type: tmpfs
        target: /tmp
`
	risks, err := unpinnedStatefulServices([]byte(long))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(risks) != 0 {
		t.Fatalf("long-form mount should be skipped, got %+v", risks)
	}
}
