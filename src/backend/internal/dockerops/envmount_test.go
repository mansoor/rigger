package dockerops

import (
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/wsconfig"
)

func nixSvc(name, mount string) wsconfig.Service {
	return wsconfig.Service{Name: name, EnvFileMount: mount, Build: &wsconfig.Build{Method: "nixpacks"}}
}

func TestMisplacedEnvMount(t *testing.T) {
	cases := []struct {
		name string
		svc  wsconfig.Service
		warn bool
	}{
		{
			// The exact shape that bit mcl_sdm: a Laravel project switched to Nixpacks
			// keeps the Dockerfile template's webroot mount.
			name: "nixpacks with the dockerfile webroot mount",
			svc:  nixSvc("app", "/var/www/html/.env"),
			warn: true,
		},
		{name: "nixpacks with the app root", svc: nixSvc("app", "/app/.env"), warn: false},
		{name: "nixpacks with a nested app path", svc: nixSvc("app", "/app/config/.env"), warn: false},
		{name: "nixpacks with no mount at all", svc: nixSvc("app", ""), warn: false},
		{name: "nixpacks with whitespace mount", svc: nixSvc("app", "   "), warn: false},
		{
			// A Dockerfile build owns its own layout — /var/www/html is correct there.
			name: "dockerfile build is never flagged",
			svc:  wsconfig.Service{Name: "app", EnvFileMount: "/var/www/html/.env", Build: &wsconfig.Build{}},
			warn: false,
		},
		{
			name: "image service with no build block",
			svc:  wsconfig.Service{Name: "db", EnvFileMount: "/var/www/html/.env"},
			warn: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := misplacedEnvMount(tc.svc)
			if (msg != "") != tc.warn {
				t.Fatalf("misplacedEnvMount = %q, want warn=%v", msg, tc.warn)
			}
			if tc.warn {
				// The message has to carry both the problem and the fix, or it just
				// adds noise to a deploy log.
				for _, want := range []string{tc.svc.Name, tc.svc.EnvFileMount, "/app/.env"} {
					if !strings.Contains(msg, want) {
						t.Errorf("message should mention %q, got: %s", want, msg)
					}
				}
			}
		})
	}
}

func TestWarnMisplacedEnvMountsFromConfig(t *testing.T) {
	const cfg = `{
	  "services": [
	    {"name":"app","env_file_mount":"/var/www/html/.env","build":{"method":"nixpacks"}},
	    {"name":"worker","env_file_mount":"/app/.env","build":{"method":"nixpacks"}},
	    {"name":"legacy","env_file_mount":"/var/www/html/.env","build":{"template":"laravel"}}
	  ]
	}`
	var got []string
	warnMisplacedEnvMounts([]byte(cfg), func(f string, a ...any) { got = append(got, f) })
	if len(got) != 1 {
		t.Fatalf("expected exactly one warning (app), got %d: %v", len(got), got)
	}
}

func TestWarnMisplacedEnvMountsIgnoresBadConfig(t *testing.T) {
	var got []string
	warnMisplacedEnvMounts([]byte("{not json"), func(f string, a ...any) { got = append(got, f) })
	if len(got) != 0 {
		t.Fatalf("unparseable config should stay silent, got %v", got)
	}
}
