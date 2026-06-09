package composegen

import (
	"strings"
	"testing"
	"time"
)

// procBlock returns the service block (delimited by blank lines) that contains
// the given service key, so per-service assertions don't bleed across services.
func procBlock(t *testing.T, out, svcKey string) string {
	t.Helper()
	for _, block := range strings.Split(out, "\n\n") {
		if strings.Contains(block, "  "+svcKey+":\n") || strings.HasSuffix(block, "  "+svcKey+":") {
			return block
		}
	}
	t.Fatalf("service %q not found in output\n---\n%s", svcKey, out)
	return ""
}

const procCustomCfg = `{
	"project": {"name":"q","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
	"environments": {"dev": {
		"deployment":"compose",
		"backend":"laravel",
		"database":"postgres",
		"redis_enabled":true,
		"processes":[
			{"name":"queue","command":"php artisan queue:work --tries=3"},
			{"name":"scheduler","command":"php artisan schedule:work"}
		]
	}}
}`

// TestProcessesCompose verifies worker/scheduler services on a compose custom app:
// they reuse the backend image with a custom command, depend on the db+redis,
// and carry NO container_name (so a pool can scale) and NO published ports.
func TestProcessesCompose(t *testing.T) {
	out, err := GenerateAt([]byte(procCustomCfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	block := procBlock(t, s, "q_dev_queue")
	for _, want := range []string{
		"image: ${BACKEND_IMAGE:-reg/q-backend:",
		"command: 'php artisan queue:work --tries=3'",
		"env_file: .env",
		"- q_dev_net",
		"    depends_on:\n      q_dev_postgres:\n        condition: service_healthy\n      q_dev_redis:\n        condition: service_healthy",
		"restart: unless-stopped",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("queue block missing %q\n---\n%s", want, block)
		}
	}
	if strings.Contains(block, "container_name:") {
		t.Errorf("process must not set container_name (blocks scaling)\n%s", block)
	}
	if strings.Contains(block, "ports:") {
		t.Errorf("process must not publish ports\n%s", block)
	}
	if !strings.Contains(s, "  q_dev_scheduler:") {
		t.Errorf("scheduler service missing\n%s", s)
	}
	if !strings.Contains(s, "# ── Process: queue ") {
		t.Errorf("process section comment missing\n%s", s)
	}
}

// TestProcessesSwarm verifies the swarm depends_on list form and per-process
// replicas via the deploy block.
func TestProcessesSwarm(t *testing.T) {
	cfg := strings.Replace(procCustomCfg, `"deployment":"compose"`, `"deployment":"swarm"`, 1)
	cfg = strings.Replace(cfg,
		`{"name":"queue","command":"php artisan queue:work --tries=3"}`,
		`{"name":"queue","command":"php artisan queue:work --tries=3","replicas":2}`, 1)
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	block := procBlock(t, string(out), "q_dev_queue")
	for _, want := range []string{
		"    depends_on:\n      - q_dev_postgres\n      - q_dev_redis",
		"    deploy:\n      replicas: 2",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("swarm queue block missing %q\n---\n%s", want, block)
		}
	}
}

// TestProcessesFrontendSource verifies a process can reuse the frontend image.
func TestProcessesFrontendSource(t *testing.T) {
	cfg := `{
		"project": {"name":"q","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"environments": {"dev": {
			"deployment":"compose","backend":"nodejs","frontend_enabled":true,"frontend":"nextjs",
			"processes":[{"name":"ssr","command":"node worker.js","source":"frontend"}]
		}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	block := procBlock(t, string(out), "q_dev_ssr")
	if !strings.Contains(block, "image: ${FRONTEND_IMAGE:-reg/q-frontend:") {
		t.Errorf("frontend-sourced process should reuse FRONTEND_IMAGE\n%s", block)
	}
}

// TestProcessesAbsentUnchanged guards the safety net: with no processes, nothing
// extra is emitted.
func TestProcessesAbsentUnchanged(t *testing.T) {
	cfg := `{
		"project": {"name":"q","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"environments": {"dev": {"deployment":"compose","backend":"laravel","database":"postgres"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "# ── Process:") {
		t.Errorf("no processes configured but a Process section was emitted\n%s", out)
	}
}
