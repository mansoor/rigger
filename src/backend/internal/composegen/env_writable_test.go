package composegen

import (
	"strings"
	"testing"
)

// Writable .env mode: bind the env's real .env rw (rooted at RIGGER_BIND_ROOT),
// suppress process-env (env_file:), and skip the read-only config.
func TestServicesEnvFileWritable(t *testing.T) {
	cfg := []byte(`{
	  "project":{"name":"dms","resource_prefix":"mcl_dms","type":"custom"},
	  "services":[{"name":"app","build":{"context":"."},"env_file":true,"env_file_mount":"/var/www/html/.env","env_file_writable":true,"web_routed":true,"port":"80"}],
	  "environments":{"dev":{"deployment":"compose","traefik_enabled":false}}
	}`)
	out, err := GenerateRouted(cfg, "dev", RouteOpts{EnvFile: "DB_HOST=mcl_dms_dev_mysql\nINSTALLED=0\n"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "${RIGGER_BIND_ROOT:-.}/.env:/var/www/html/.env") {
		t.Errorf("expected writable .env bind in:\n%s", s)
	}
	if strings.Contains(s, "env_file: .env") {
		t.Errorf("env_file: must be suppressed in writable mode:\n%s", s)
	}
	if strings.Contains(s, "configs:") {
		t.Errorf("read-only config must not be emitted in writable mode:\n%s", s)
	}
}
