package composegen

import (
	"strings"
	"testing"
	"time"
)

// svcBlock returns the service block (delimited by blank lines) containing the
// given service key, so per-service assertions don't bleed across services.
func svcBlock(t *testing.T, out, svcKey string) string {
	t.Helper()
	for _, block := range strings.Split(out, "\n\n") {
		if strings.Contains(block, "  "+svcKey+":\n") || strings.HasSuffix(block, "  "+svcKey+":") {
			return block
		}
	}
	t.Fatalf("service %q not found in output\n---\n%s", svcKey, out)
	return ""
}

// A Laravel-shaped custom app expressed in the unified model: a build `app`
// service (php-fpm, internal), an `nginx` pull service that web-routes, and a
// `worker` that reuses the app image with a custom command. Plus postgres+redis
// managed deps. Exercises every source type and the depends_on resolution.
const shopCfg = `{
	"project": {"name":"shop","registry":"reg","version":{"major":2,"minor":1,"patch":0,"build":3}},
	"services": [
		{"name":"app","role":"app","build":{},"port":"9000","env_file":true,
		 "volumes":["uploads:/app/storage/uploads"],"depends_on":["postgres","redis"],
		 "healthcheck":"php -r 'exit(0);'"},
		{"name":"nginx","image":"nginx","tag":"1.25-alpine","web_routed":true,"port":"80",
		 "depends_on":["app"],"volumes":["./nginx.conf:/etc/nginx/conf.d/default.conf:ro"]},
		{"name":"worker","role":"worker","image_from":"app","command":"php artisan queue:work",
		 "env_file":true,"depends_on":["postgres","redis"]}
	],
	"environments": {"dev": {"deployment":"compose","http_port":"8080","database":"postgres","redis_enabled":true}}
}`

// A scanned project (git_repo set) re-roots repo-relative bind sources at the
// env's _src checkout AND wraps them in ${RIGGER_BIND_ROOT} so the host Docker
// daemon can resolve them when Rigger runs containerised.
func TestBindSourceScannedRootedAtSrc(t *testing.T) {
	cfg := `{
		"project": {"name":"kyt","git_repo":"https://example.com/kyt.git","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [
			{"name":"proxy","image":"caddy:2-alpine","port":"80","web_routed":true,
			 "volumes":["./Caddyfile:/etc/caddy/Caddyfile:ro","data:/data"]}
		],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	proxy := svcBlock(t, string(out), "proxy")
	if want := "      - ${RIGGER_BIND_ROOT:-.}/_src/Caddyfile:/etc/caddy/Caddyfile:ro"; !strings.Contains(proxy, want) {
		t.Errorf("scanned bind not rooted at _src; want %q\n---\n%s", want, proxy)
	}
	// Named volumes are still prefixed (not rewritten as binds).
	if want := "      - kyt_dev_data:/data"; !strings.Contains(proxy, want) {
		t.Errorf("named volume mishandled; want %q\n---\n%s", want, proxy)
	}
}

func TestServicesCustomShape(t *testing.T) {
	out, err := GenerateAt([]byte(shopCfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	app := svcBlock(t, s, "app")
	for _, want := range []string{
		"image: ${APP_IMAGE:-reg/shop-app:2.1.0-build.3-dev}",
		"env_file: .env",
		"          - app",
		"    depends_on:\n      postgres:\n        condition: service_healthy\n      redis:\n        condition: service_healthy",
		"      - shop_dev_uploads:/app/storage/uploads",
		"php -r 'exit(0);'",
		"    expose:\n      - \"9000\"",
		"restart: unless-stopped",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("app block missing %q\n---\n%s", want, app)
		}
	}
	if strings.Contains(app, "ports:") {
		t.Errorf("internal app service must not publish ports\n%s", app)
	}

	nginx := svcBlock(t, s, "nginx")
	for _, want := range []string{
		"image: nginx:1.25-alpine",
		"    ports:\n      - \"8080:80\"",                                          // web-routed, no traefik → bind env HTTP port
		"    depends_on:\n      app:\n        condition: service_healthy", // app has a healthcheck
		// Relative bind sources are rooted at ${RIGGER_BIND_ROOT} so the host daemon
		// can resolve them when Rigger runs containerised (no git_repo here → no _src).
		"      - ${RIGGER_BIND_ROOT:-.}/nginx.conf:/etc/nginx/conf.d/default.conf:ro",
	} {
		if !strings.Contains(nginx, want) {
			t.Errorf("nginx block missing %q\n---\n%s", want, nginx)
		}
	}

	worker := svcBlock(t, s, "worker")
	for _, want := range []string{
		"image: ${APP_IMAGE:-reg/shop-app:2.1.0-build.3-dev}", // reuses app's image
		"command: 'php artisan queue:work'",
	} {
		if !strings.Contains(worker, want) {
			t.Errorf("worker block missing %q\n---\n%s", want, worker)
		}
	}
	if strings.Contains(worker, "ports:") || strings.Contains(worker, "expose:") {
		t.Errorf("worker must not publish/expose ports\n%s", worker)
	}

	// Managed deps + volumes block.
	for _, want := range []string{
		"  postgres:", "  redis:", // service keys are short
		"  shop_dev_uploads:", "  shop_dev_pg_data:", "  shop_dev_redis_data:", // volume names stay prefixed
	} {
		if !strings.Contains(s, want) {
			t.Errorf("expected %q in output\n%s", want, s)
		}
	}
}

// Catalog-driven managed DB: MariaDB engine + chosen version + external publish.
// MariaDB reuses the MYSQL_* contract but a distinct image/container/volume, omits
// the MySQL-only native-password command, and (external) publishes its port.
func TestManagedDBMariaDBVersionExternal(t *testing.T) {
	cfg := `{
		"project": {"name":"shop","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"app","role":"app","build":{},"port":"9000","env_file":true,"depends_on":["mariadb"]}],
		"environments": {"dev": {"deployment":"compose","http_port":"8080","database":"mariadb","db_version":"10.11","db_external":true}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// Managed-dep blocks are asserted against the full output (svcBlock would false-
	// match the app's depends_on line for the same name).
	for _, want := range []string{
		"  mariadb:",                            // service key is short
		"image: mariadb:10.11",                  // engine image + chosen version
		"container_name: shop_dev_mariadb",      // container_name stays prefixed (stable identity)
		"    ports:\n      - \"${DB_EXTERNAL_PORT:-3306}:3306\"", // external publish
		"MYSQL_ROOT_PASSWORD",                    // reuses MYSQL_* contract
		"      - shop_dev_mariadb_data:/var/lib/mysql",
		"mariadb-admin ping",                     // mariadb healthcheck
		"  shop_dev_mariadb_data:",               // named volume declared
		"          - shop_dev_mariadb",           // prefixed network alias → POSTGRES_HOST/DB_HOST style host resolves
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q\n---\n%s", want, s)
		}
	}
	// The app (build) service also advertises both its short name and prefixed alias.
	app0 := svcBlock(t, s, "app")
	for _, want := range []string{"          - app", "          - shop_dev_app"} {
		if !strings.Contains(app0, want) {
			t.Errorf("app network aliases missing %q\n%s", want, app0)
		}
	}
	if strings.Contains(s, "--default-authentication-plugin") {
		t.Errorf("mariadb must NOT carry the MySQL-only native-password command\n%s", s)
	}
	// app depends_on resolves the mariadb managed dep (healthcheck → service_healthy).
	app := svcBlock(t, s, "app")
	if !strings.Contains(app, "mariadb:\n        condition: service_healthy") {
		t.Errorf("app depends_on should resolve mariadb as healthy\n%s", app)
	}
}

// Managed deps moved from per-env to project-level (consistent across envs); only
// DBExternal stays per-env. The generator reads an effective value (project-level
// else legacy per-env), so a project-level engine/version/redis must produce the
// SAME compose as the old per-env form. This locks the fallback parity.
func TestManagedDepProjectLevelParity(t *testing.T) {
	legacy := `{
		"project": {"name":"shop","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"app","role":"app","build":{},"port":"9000","env_file":true,"depends_on":["postgres"]}],
		"environments": {"dev": {"deployment":"compose","http_port":"8080","database":"postgres","db_version":"15-alpine","redis_enabled":true}}
	}`
	project := `{
		"project": {"name":"shop","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres","db_version":"15-alpine","redis_enabled":true},
		"services": [{"name":"app","role":"app","build":{},"port":"9000","env_file":true,"depends_on":["postgres"]}],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	a, err := GenerateAt([]byte(legacy), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateAt([]byte(project), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("project-level deps must generate identical compose to legacy per-env\n--- legacy ---\n%s\n--- project ---\n%s", a, b)
	}
	if !strings.Contains(string(b), "  postgres:") || !strings.Contains(string(b), "  redis:") {
		t.Errorf("project-level deps should still emit postgres + redis blocks\n%s", b)
	}
}

// A web-routed service that also sets its own host_port must emit exactly ONE
// `ports:` block publishing that host_port (not also the env HTTP port) — two
// `ports:` keys are invalid YAML. Regression for the CloudBeaver duplicate-ports bug.
func TestWebRoutedHostPortSinglePortsBlock(t *testing.T) {
	cfg := `{
		"project": {"name":"db","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"cloudbeaver","image":"dbeaver/cloudbeaver","tag":"latest","port":"8978","web_routed":true,"host_port":"9000"}],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if n := strings.Count(s, "    ports:"); n != 1 {
		t.Fatalf("expected exactly one ports block, got %d\n%s", n, s)
	}
	if !strings.Contains(s, `      - "9000:8978"`) {
		t.Errorf("expected host_port mapping 9000:8978\n%s", s)
	}
	if strings.Contains(s, "8080:8978") {
		t.Errorf("must not also bind the env HTTP port when host_port is set\n%s", s)
	}
}

// The Adminer web-SQL service (database-hosting web entry): publishes its host port
// once (8978:8080), injects the env's .env (env_file), and bind-mounts the Rigger
// auto-login plugin into Adminer's plugins-enabled dir via ${RIGGER_BIND_ROOT}.
func TestAdminerWebSQLService(t *testing.T) {
	cfg := `{
		"project": {"name":"db","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"adminer","image":"adminer","tag":"4.8.1","port":"8080","web_routed":true,
			"host_port":"8978","env_file":true,
			"volumes":["${RIGGER_BIND_ROOT:-.}/adminer-login.php:/var/www/html/plugins-enabled/01-rigger-autologin.php:ro"]}],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	a := svcBlock(t, string(out), "adminer")
	for _, want := range []string{
		"image: adminer:4.8.1",
		"    env_file: .env",
		"      - \"8978:8080\"",
		"      - ${RIGGER_BIND_ROOT:-.}/adminer-login.php:/var/www/html/plugins-enabled/01-rigger-autologin.php:ro",
	} {
		if !strings.Contains(a, want) {
			t.Errorf("adminer block missing %q\n---\n%s", want, a)
		}
	}
	// Exactly one ports: block (the host_port), not also the env HTTP port.
	if strings.Count(a, "ports:") != 1 {
		t.Errorf("adminer must have a single ports: block\n%s", a)
	}
}

// A self-serving app (Spring Boot / Go / .NET shape): one build service routed by
// Traefik on its own port, no nginx. Verifies traefik labels + traefik network join.
func TestServicesSelfServingTraefik(t *testing.T) {
	cfg := `{
		"project": {"name":"api","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"app","role":"app","build":{},"web_routed":true,"port":"8080","env_file":true,
			"healthcheck":"curl -sf http://localhost:8080/actuator/health || exit 1"}],
		"environments": {"prod": {"deployment":"compose","domain":"api.example.com","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`
	out, err := GenerateAt([]byte(cfg), "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	app := svcBlock(t, string(out), "app")
	for _, want := range []string{
		"image: ${APP_IMAGE:-reg/api-app:1.0.0-build.0-prod}",
		"      traefik_net: {}",
		"traefik.http.routers.api_prod_app.rule=Host(`api.example.com`)",
		"loadbalancer.server.port=8080",
		"actuator/health",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("self-serving app missing %q\n---\n%s", want, app)
		}
	}
	if strings.Contains(string(out), "  nginx:") {
		t.Errorf("self-serving app should not emit an nginx service\n%s", out)
	}
}

// Subdomain routing: a service routed at app.{domain}.
func TestServicesSubdomainRoute(t *testing.T) {
	cfg := `{
		"project": {"name":"q","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"web","build":{},"web_routed":true,"subdomain":"app","port":"3000","env_file":true}],
		"environments": {"prod": {"deployment":"compose","domain":"example.com","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`
	out, err := GenerateAt([]byte(cfg), "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "Host(`app.example.com`)") {
		t.Errorf("expected subdomain route app.example.com\n%s", out)
	}
}

// Traefik routing modes: HTTP-only, HTTPS+Let's Encrypt, HTTPS+self-signed.
func TestServicesTraefikModes(t *testing.T) {
	mk := func(ssl, selfSigned bool) string {
		return `{
			"project": {"name":"api","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
			"services": [{"name":"app","build":{},"web_routed":true,"port":"8080","env_file":true}],
			"environments": {"prod": {"deployment":"compose","domain":"api.example.com",
				"traefik_enabled":true,"traefik_network":"traefik_net",
				"ssl_enabled":` + boolStr(ssl) + `,"ssl_self_signed":` + boolStr(selfSigned) + `}}
		}`
	}

	// HTTP-only: web entrypoint, no TLS, no redirect.
	httpOut, err := GenerateAt([]byte(mk(false, false)), "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	app := svcBlock(t, string(httpOut), "app")
	mustContain(t, app, "traefik.http.routers.api_prod_app.entrypoints=web")
	mustNotContain(t, app, "websecure")
	mustNotContain(t, app, "redirectscheme")
	mustNotContain(t, app, "certresolver")

	// HTTPS + Let's Encrypt: websecure + letsencrypt + companion http→https redirect.
	leOut, err := GenerateAt([]byte(mk(true, false)), "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	app = svcBlock(t, string(leOut), "app")
	mustContain(t, app, "traefik.http.routers.api_prod_app.entrypoints=websecure")
	mustContain(t, app, "traefik.http.routers.api_prod_app.tls.certresolver=letsencrypt")
	mustContain(t, app, "traefik.http.routers.api_prod_app_web.entrypoints=web")
	mustContain(t, app, "traefik.http.middlewares.api_prod_app_redirect.redirectscheme.scheme=https")

	// HTTPS + self-signed: websecure + tls=true but NO certresolver (default cert).
	ssOut, err := GenerateAt([]byte(mk(true, true)), "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	app = svcBlock(t, string(ssOut), "app")
	mustContain(t, app, "traefik.http.routers.api_prod_app.entrypoints=websecure")
	mustContain(t, app, "traefik.http.routers.api_prod_app.tls=true")
	mustNotContain(t, app, "certresolver")
	mustContain(t, app, "redirectscheme.scheme=https") // still redirects http→https
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func mustContain(t *testing.T, s, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Errorf("missing %q\n---\n%s", want, s)
	}
}

func mustNotContain(t *testing.T, s, bad string) {
	t.Helper()
	if strings.Contains(s, bad) {
		t.Errorf("unexpected %q\n---\n%s", bad, s)
	}
}

// Auto-derived route: Traefik on + blank domain → {prefix}-{env}.localhost (HTTP),
// and with a base domain → {prefix}-{env}.{base} over Let's Encrypt.
func TestServicesAutoRoute(t *testing.T) {
	cfg := []byte(`{
		"project": {"name":"weather app","resource_prefix":"mcl_wda","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"nginx","image":"nginx","tag":"alpine","web_routed":true,"port":"80"}],
		"environments": {"dev": {"deployment":"compose","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`)

	// Local default: *.localhost over HTTP, no host-port binding.
	local, err := GenerateAt(cfg, "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	app := svcBlock(t, string(local), "nginx")
	mustContain(t, app, "Host(`mcl-wda-dev.localhost`)") // underscores → hyphens
	mustContain(t, app, "entrypoints=web")
	mustNotContain(t, app, "ports:") // routed, not host-bound

	// With a workspace base domain: real host + Let's Encrypt.
	prod, err := GenerateRouted(cfg, "dev", RouteOpts{BaseDomain: "apps.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	app = svcBlock(t, string(prod), "nginx")
	mustContain(t, app, "Host(`mcl-wda-dev.apps.example.com`)")
	mustContain(t, app, "entrypoints=websecure")
	mustContain(t, app, "certresolver=letsencrypt")
}

// project.local_tls makes an auto-routed *.localhost env use self-signed HTTPS.
func TestServicesLocalTLS(t *testing.T) {
	cfg := []byte(`{
		"project": {"name":"vault","resource_prefix":"ws_vault","registry":"reg","local_tls":true,
			"version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"app","build":{},"web_routed":true,"port":"80"}],
		"environments": {"dev": {"deployment":"compose","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`)
	out, err := GenerateAt(cfg, "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	app := svcBlock(t, string(out), "app")
	mustContain(t, app, "Host(`ws-vault-dev.localhost`)")
	mustContain(t, app, "entrypoints=websecure")
	mustContain(t, app, "tls=true")
	mustNotContain(t, app, "certresolver") // self-signed, not Let's Encrypt
}

// EnvRouteURL resolves the display URL the same way deploy routing does.
func TestEnvRouteURL(t *testing.T) {
	mk := func(traefik bool, domain, ssl string) []byte {
		return []byte(`{
			"project": {"name":"x","resource_prefix":"ws_app"},
			"services": [{"name":"app","build":{},"web_routed":true,"port":"80"}],
			"environments": {"dev": {"deployment":"compose","traefik_enabled":` + boolStr(traefik) +
			`,"traefik_network":"traefik_net","domain":"` + domain + `","ssl_enabled":` + ssl + `}}
		}`)
	}
	// Traefik off → not routed.
	if u, routed := EnvRouteURL(mk(false, "", "false"), "dev", ""); routed {
		t.Errorf("traefik off should not route, got %q", u)
	}
	// Auto localhost.
	if u, routed := EnvRouteURL(mk(true, "", "false"), "dev", ""); !routed || u != "http://ws-app-dev.localhost" {
		t.Errorf("auto localhost = %q,%v; want http://ws-app-dev.localhost,true", u, routed)
	}
	// Auto base domain → HTTPS.
	if u, routed := EnvRouteURL(mk(true, "", "false"), "dev", "apps.example.com"); !routed || u != "https://ws-app-dev.apps.example.com" {
		t.Errorf("auto base = %q,%v; want https://ws-app-dev.apps.example.com,true", u, routed)
	}
	// Explicit domain wins.
	if u, routed := EnvRouteURL(mk(true, "my.host", "true"), "dev", "apps.example.com"); !routed || u != "https://my.host" {
		t.Errorf("explicit = %q,%v; want https://my.host,true", u, routed)
	}
}

// Swarm secrets still wire through deployBlock for a build service.
func TestServicesSwarmSecrets(t *testing.T) {
	cfg := `{
		"project": {"name":"q","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"app","build":{},"web_routed":true,"port":"8080","env_file":true}],
		"environments": {"prod": {"deployment":"swarm","database":"postgres",
			"secret_keys":["POSTGRES_PASSWORD"],"secret_versions":{"POSTGRES_PASSWORD":2}}}
	}`
	out, err := GenerateAt([]byte(cfg), "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "secrets:") {
		t.Errorf("expected top-level secrets block\n%s", s)
	}
	// postgres managed dep reads the secret via _FILE convention.
	if !strings.Contains(s, "POSTGRES_PASSWORD_FILE: /run/secrets/POSTGRES_PASSWORD") {
		t.Errorf("expected POSTGRES_PASSWORD_FILE wiring\n%s", s)
	}
}

// No services + no managed deps ⇒ a valid (if minimal) compose with no services emitted.
func TestServicesEmpty(t *testing.T) {
	cfg := `{
		"project": {"name":"q","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [],
		"environments": {"dev": {"deployment":"compose"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "services:") {
		t.Errorf("expected a services: header even when empty\n%s", out)
	}
}

// EnvFileMount delivers the env's generated .env as a physical file in the app
// workdir for frameworks that re-read .env from disk (e.g. Laravel `php artisan
// serve`). It's emitted as an inline compose `config` (content:), NOT a host
// bind — Rigger runs in a container and the host daemon can't resolve a
// Rigger-side bind path. Absent mount OR absent content ⇒ nothing emitted.
func TestServicesEnvFileMount(t *testing.T) {
	cfg := `{
		"project": {"name":"app","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"backend","build":{},"env_file":true,"env_file_mount":"/var/www/html/.env"}],
		"environments": {"dev": {"deployment":"compose"}}
	}`
	out, err := GenerateRouted([]byte(cfg), "dev", RouteOpts{EnvFile: "DB_HOST=mysql\nDB_PORT=3306\n"})
	if err != nil {
		t.Fatal(err)
	}
	// Service references the config at its target path.
	app := svcBlock(t, string(out), "backend")
	for _, want := range []string{"configs:", "- source: app_dev_dotenv", "target: /var/www/html/.env"} {
		if !strings.Contains(app, want) {
			t.Errorf("service block missing %q\n---\n%s", want, app)
		}
	}
	// Top-level config carries the .env content inline (indented under content:).
	for _, want := range []string{"configs:\n  app_dev_dotenv:\n    content: |", "      DB_HOST=mysql", "      DB_PORT=3306"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("top-level config missing %q\n---\n%s", want, out)
		}
	}
	// No host bind path (the broken approach) anywhere.
	if strings.Contains(string(out), "./.env:") {
		t.Errorf("must not emit a host bind for .env\n%s", out)
	}

	// env_file_mount unset ⇒ no configs at all.
	cfg2 := strings.Replace(cfg, `,"env_file_mount":"/var/www/html/.env"`, "", 1)
	out2, err := GenerateRouted([]byte(cfg2), "dev", RouteOpts{EnvFile: "DB_HOST=mysql\n"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out2), "configs:") {
		t.Errorf("no configs expected when env_file_mount unset\n%s", out2)
	}

	// mount set but no content supplied ⇒ gracefully skipped.
	out3, err := GenerateRouted([]byte(cfg), "dev", RouteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out3), "configs:") {
		t.Errorf("no configs expected when EnvFile content is empty\n%s", out3)
	}
}
