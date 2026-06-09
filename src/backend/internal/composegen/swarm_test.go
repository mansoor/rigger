package composegen

import (
	"strings"
	"testing"
	"time"
)

// TestSwarmDeployBlockDefaults verifies that a swarm env WITHOUT a swarm config
// emits the historical default deploy block (also covered byte-for-byte by the
// fullcustom/swarmsecret goldens; asserted here for clarity).
func TestSwarmDeployBlockDefaults(t *testing.T) {
	cfg := []byte(`{
		"project": {"name":"q","type":"image","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"images": [{"name":"app","image":"nginx","tag":"alpine","port":80}],
		"environments": {"prod": {"deployment":"swarm"}}
	}`)
	out, err := GenerateAt(cfg, "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"    deploy:",
		"      replicas: 1",
		"      restart_policy:\n        condition: on-failure\n        delay: 5s\n        max_attempts: 3",
		"      update_config:\n        parallelism: 1\n        delay: 10s\n        failure_action: rollback",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("default swarm block missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "placement:") || strings.Contains(string(out), "rollback_config:") {
		t.Errorf("default swarm block should not emit placement/rollback_config\n%s", out)
	}
}

// TestSwarmDeployBlockTuned verifies the configurable swarm fields: per-service
// replicas + placement, a custom restart_policy/update_config, and rollback_config.
func TestSwarmDeployBlockTuned(t *testing.T) {
	cfg := []byte(`{
		"project": {"name":"q","type":"image","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"images": [
			{"name":"app","image":"nginx","tag":"alpine","port":80},
			{"name":"db","image":"mariadb","tag":"11"}
		],
		"environments": {"prod": {
			"deployment":"swarm",
			"swarm": {
				"restart_policy": {"condition":"any","delay":"3s","max_attempts":5,"window":"20s"},
				"update_config": {"parallelism":2,"delay":"5s","order":"start-first","failure_action":"continue"},
				"rollback_config": {"parallelism":1,"delay":"0s","order":"stop-first"},
				"services": {
					"app": {"replicas":3,"placement":["node.role==worker"]},
					"db":  {"replicas":1,"placement":["node.labels.storage==ssd","node.role==manager"]}
				}
			}
		}}
	}`)
	out, err := GenerateAt(cfg, "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	// app service: replicas 3 + one placement constraint.
	for _, want := range []string{
		"  q_prod_app:",
		"      replicas: 3",
		"      placement:\n        constraints:\n          - node.role==worker",
		"        condition: any",
		"        delay: 3s",
		"        max_attempts: 5",
		"        window: 20s",
		"      update_config:\n        parallelism: 2\n        delay: 5s\n        order: start-first\n        failure_action: continue",
		"      rollback_config:\n        parallelism: 1\n        delay: 0s\n        order: stop-first",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("tuned swarm block missing %q\n---\n%s", want, s)
		}
	}

	// db service: replicas 1 + two placement constraints.
	if !strings.Contains(s, "      replicas: 1") {
		t.Errorf("db replicas missing\n%s", s)
	}
	if !strings.Contains(s, "          - node.labels.storage==ssd\n          - node.role==manager") {
		t.Errorf("db placement constraints missing\n%s", s)
	}
}
