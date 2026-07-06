package composegen

import (
	"fmt"
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

// An uploaded-source project (source_kind=upload, no git_repo) extracts into _src at
// build time, so its relative bind sources must re-root under _src just like a scanned
// git repo's do.
func TestBindSourceUploadRootedAtSrc(t *testing.T) {
	cfg := `{
		"project": {"name":"wea","source_kind":"upload","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [
			{"name":"proxy","image":"caddy:2-alpine","port":"80","web_routed":true,
			 "volumes":["./Caddyfile:/etc/caddy/Caddyfile:ro"]}
		],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if want := "      - ${RIGGER_BIND_ROOT:-.}/_src/Caddyfile:/etc/caddy/Caddyfile:ro"; !strings.Contains(string(out), want) {
		t.Errorf("upload-source bind not rooted at _src; want %q\n---\n%s", want, out)
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

// Managed MongoDB (minimal): emits a mongo service with the MONGO_INITDB_* init
// contract mapped from the .env MONGO_* keys, its own data volume + healthcheck,
// and — even with web_sql requested — NO Adminer (a SQL client can't talk Mongo).
func TestManagedDBMongo(t *testing.T) {
	cfg := `{
		"project": {"name":"docs","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"mongodb","web_sql":true},
		"services": [{"name":"app","role":"app","build":{},"port":"3000","env_file":true,"depends_on":["mongodb"]}],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"  mongodb:",
		"image: mongo:7",
		"container_name: docs_dev_mongodb",
		"MONGO_INITDB_ROOT_USERNAME: ${MONGO_USER}",
		"MONGO_INITDB_ROOT_PASSWORD: ${MONGO_PASSWORD}",
		"MONGO_INITDB_DATABASE: ${MONGO_DB}",
		"      - docs_dev_mongodb_data:/data/db",
		"  docs_dev_mongodb_data:",
		"          - docs_dev_mongodb",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("mongo output missing %q\n---\n%s", want, s)
		}
	}
	if strings.Contains(s, "  adminer:") || strings.Contains(s, "image: adminer") {
		t.Errorf("Adminer must NOT be synthesized for a non-SQL (mongo) engine\n%s", s)
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

// Adminer synthesized from the project web_sql flag (the unified path): for a pure
// database-hosting project (no app service) it claims the apex web entry; the block
// matches the legacy literal-adminer shape (image/host_port/env_file/volume).
func TestAdminerSynthApex(t *testing.T) {
	cfg := `{
		"project": {"name":"db1","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres","web_sql":true},
		"environments": {"dev": {"deployment":"compose","http_port":"8080","traefik_enabled":true,"traefik_network":"rigger-traefik","domain":"db1.example.com"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	a := svcBlock(t, string(out), "adminer")
	for _, want := range []string{
		"image: adminer:4.8.1",
		"container_name: db1_dev_adminer",
		"    env_file: .env",
		"      - ${RIGGER_BIND_ROOT:-.}/adminer-login.php:/var/www/html/plugins-enabled/01-rigger-autologin.php:ro",
		"    depends_on:\n      postgres:",                          // waits for the managed DB
		"routers.db1_dev_adminer.rule=Host(`db1.example.com`)", // apex (no subdomain)
	} {
		if !strings.Contains(a, want) {
			t.Errorf("synth adminer (apex) missing %q\n---\n%s", want, a)
		}
	}
	// web_sql with NO database → no adminer (nothing to connect to).
	noDB := strings.Replace(cfg, `"database":"postgres",`, "", 1)
	out2, _ := GenerateAt([]byte(noDB), "dev", time.Unix(0, 0).UTC())
	if strings.Contains(string(out2), "adminer:") {
		t.Errorf("adminer should not be synthesized without a database\n%s", out2)
	}
}

// On a stack that already has an app web entry, the synthesized Adminer routes on the
// "adminer" subdomain so it doesn't collide with the app at the apex.
func TestAdminerSynthSubdomain(t *testing.T) {
	cfg := `{
		"project": {"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres","web_sql":true},
		"services": [{"name":"web","build":{},"port":"3000","web_routed":true}],
		"environments": {"dev": {"deployment":"compose","traefik_enabled":true,"traefik_network":"rigger-traefik","domain":"app1.example.com"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	a := svcBlock(t, string(out), "adminer")
	if !strings.Contains(a, "routers.app1_dev_adminer.rule=Host(`adminer.app1.example.com`)") {
		t.Errorf("synth adminer should route on the adminer subdomain\n---\n%s", a)
	}
}

// Mailpit synthesizes as a Traefik-routed sidecar on the "mail" subdomain, gated on the
// per-env mailpit toggle (project default, env-overridable). Off in an env → no service.
func TestMailpitSidecar(t *testing.T) {
	cfg := `{
		"project": {"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0},"mailpit":true},
		"services": [{"name":"web","build":{},"port":"3000","web_routed":true}],
		"environments": {
			"dev": {"deployment":"compose","traefik_enabled":true,"traefik_network":"rigger-traefik","domain":"app1.example.com"},
			"prod": {"deployment":"compose","traefik_enabled":true,"traefik_network":"rigger-traefik","domain":"app1.example.com","mailpit":false}
		}
	}`
	dev, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	m := svcBlock(t, string(dev), "mailpit")
	for _, want := range []string{"image: axllent/mailpit", "routers.app1_dev_mailpit.rule=Host(`mail.app1.example.com`)"} {
		if !strings.Contains(m, want) {
			t.Errorf("mailpit block missing %q\n---\n%s", want, m)
		}
	}
	// prod overrides mailpit=false → no mailpit service.
	prod, _ := GenerateAt([]byte(cfg), "prod", time.Unix(0, 0).UTC())
	if strings.Contains(string(prod), "  mailpit:") {
		t.Errorf("prod override mailpit=false must drop the sidecar\n%s", prod)
	}
}

// Per-env tri-state override of the project sidecar defaults: project web_sql=true, but
// the env forces web_sql=false → no Adminer in that env (and vice-versa for storage_ui).
func TestPerEnvSidecarOverride(t *testing.T) {
	// Project default Adminer ON; prod env overrides it OFF → no adminer service in prod.
	cfg := `{
		"project": {"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres","web_sql":true},
		"services": [{"name":"web","build":{},"port":"3000","web_routed":true}],
		"environments": {"prod": {"deployment":"compose","traefik_enabled":true,"traefik_network":"rigger-traefik","domain":"app1.example.com","web_sql":false}}
	}`
	out, err := GenerateAt([]byte(cfg), "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "  adminer:") {
		t.Errorf("env override web_sql=false must drop Adminer in this env\n%s", out)
	}
	// A dev env with no override inherits the project default (Adminer ON).
	devCfg := strings.Replace(cfg, `"prod": {"deployment":"compose","traefik_enabled":true,"traefik_network":"rigger-traefik","domain":"app1.example.com","web_sql":false}`,
		`"dev": {"deployment":"compose","traefik_enabled":true,"traefik_network":"rigger-traefik","domain":"app1.example.com"}`, 1)
	out2, _ := GenerateAt([]byte(devCfg), "dev", time.Unix(0, 0).UTC())
	if !strings.Contains(string(out2), "  adminer:") {
		t.Errorf("env with no override must inherit the project default (Adminer ON)\n%s", out2)
	}
}

// object_storage=minio emits the MinIO server + a one-shot mc bucket-init; neither
// carries a Docker healthcheck (so Traefik won't drop them). object_storage=local emits
// no storage container but mounts a persistent volume over the app's storage dir.
func TestObjectStorageMinIOAndLocal(t *testing.T) {
	minioCfg := `{
		"project": {"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0},"object_storage":"minio"},
		"services": [{"name":"web","build":{},"port":"3000","web_routed":true}],
		"environments": {"dev": {"deployment":"compose"}}
	}`
	out, err := GenerateAt([]byte(minioCfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	m := svcBlock(t, s, "minio")
	for _, want := range []string{"image: minio/minio:", "server /data --console-address", "- app1_dev_minio_data:/data"} {
		if !strings.Contains(m, want) {
			t.Errorf("minio block missing %q\n---\n%s", want, m)
		}
	}
	if strings.Contains(m, "healthcheck:") {
		t.Errorf("minio must NOT carry a healthcheck (distroless → Traefik would drop it)\n%s", m)
	}
	init := svcBlock(t, s, "minio_init")
	for _, want := range []string{"image: minio/mc:", "mc mb --ignore-existing", "$$MINIO_BUCKET", "restart: \"no\"", "http://minio:9000"} {
		if !strings.Contains(init, want) {
			t.Errorf("minio_init block missing %q\n---\n%s", want, init)
		}
	}
	// mc/S3 reject underscore hostnames — must use the bare "minio" name, never {prefix}_minio.
	if strings.Contains(init, "app1_dev_minio:9000") {
		t.Errorf("minio_init must use bare host http://minio:9000, not the underscore host\n%s", init)
	}

	// local: no minio container, but the app service gets the storage volume at the default path.
	localCfg := `{
		"project": {"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0},"object_storage":"local"},
		"services": [{"name":"web","build":{},"port":"3000","web_routed":true}],
		"environments": {"dev": {"deployment":"compose"}}
	}`
	out2, err := GenerateAt([]byte(localCfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s2 := string(out2)
	if strings.Contains(s2, "  minio:") {
		t.Errorf("local storage must not emit a minio container\n%s", s2)
	}
	if !strings.Contains(svcBlock(t, s2, "web"), "- app1_dev_storage:/var/www/html/storage") {
		t.Errorf("local storage must mount the persistent volume on the app service\n%s", s2)
	}
}

// Both storage backends on at once (storage_local + storage_minio): the MinIO server
// runs AND the app service gets the local storage volume.
func TestObjectStorageBoth(t *testing.T) {
	cfg := `{
		"project": {"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0},"storage_local":true,"storage_minio":true},
		"services": [{"name":"web","build":{},"port":"3000","web_routed":true}],
		"environments": {"dev": {"deployment":"compose"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "  minio:") {
		t.Errorf("storage_minio should emit the minio container\n%s", s)
	}
	if !strings.Contains(svcBlock(t, s, "web"), "- app1_dev_storage:/var/www/html/storage") {
		t.Errorf("storage_local should mount the persistent volume on the app service\n%s", s)
	}
}

// The MinIO admin console (opens3/console) synthesizes as a Traefik-routed service on
// the "storage" subdomain (correct image, no host port under Traefik) — gated on the
// storage_ui flag + object_storage=minio.
func TestStorageConsoleSubdomain(t *testing.T) {
	cfg := `{
		"project": {"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0},"object_storage":"minio","storage_ui":true},
		"services": [{"name":"web","build":{},"port":"3000","web_routed":true}],
		"environments": {"dev": {"deployment":"compose","traefik_enabled":true,"traefik_network":"rigger-traefik","domain":"app1.example.com"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	g := svcBlock(t, string(out), "storage_console")
	for _, want := range []string{
		"image: opens3/console",
		"routers.app1_dev_storage_console.rule=Host(`storage.app1.example.com`)",
		"- CONSOLE_MINIO_SERVER=http://minio:9000", // bare host — S3/console reject underscores
	} {
		if !strings.Contains(g, want) {
			t.Errorf("storage_console block missing %q\n---\n%s", want, g)
		}
	}
	// Under Traefik it must NOT publish a host port (no multi-instance collision).
	if strings.Contains(g, "ports:") {
		t.Errorf("storage_console should not publish a host port under Traefik\n%s", g)
	}
	// With the flag off, no storage_console service.
	off := strings.Replace(cfg, `"storage_ui":true`, `"storage_ui":false`, 1)
	out2, _ := GenerateAt([]byte(off), "dev", time.Unix(0, 0).UTC())
	if strings.Contains(string(out2), "storage_console:") {
		t.Errorf("storage_console must not render when the flag is off\n%s", out2)
	}
}

// With protect_admin_uis on, the admin sidecars (Adminer + MinIO console) get a Traefik
// basic-auth middleware referencing ${ADMIN_UI_USERS}; the app's own web entry does NOT.
func TestProtectAdminUIs(t *testing.T) {
	cfg := `{
		"project": {"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres","web_sql":true,"object_storage":"minio","storage_ui":true},
		"services": [{"name":"web","build":{},"port":"3000","web_routed":true}],
		"environments": {"dev": {"deployment":"compose","traefik_enabled":true,"traefik_network":"rigger-traefik","domain":"app1.example.com","protect_admin_uis":true}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"adminer", "storage_console"} {
		b := svcBlock(t, string(out), name)
		mw := "middlewares.app1_dev_" + name + "_auth.basicauth.users=${ADMIN_UI_USERS}"
		if !strings.Contains(b, mw) {
			t.Errorf("%s should carry the basic-auth middleware\n---\n%s", name, b)
		}
		if !strings.Contains(b, "routers.app1_dev_"+name+".middlewares=app1_dev_"+name+"_auth") {
			t.Errorf("%s router should reference the auth middleware\n---\n%s", name, b)
		}
	}
	// The app's own web entry must never be auth-gated.
	if strings.Contains(svcBlock(t, string(out), "web"), "basicauth") {
		t.Errorf("app service must NOT get basic-auth")
	}
	// Flag off → no basicauth anywhere.
	off := strings.Replace(cfg, `,"protect_admin_uis":true`, "", 1)
	out2, _ := GenerateAt([]byte(off), "dev", time.Unix(0, 0).UTC())
	if strings.Contains(string(out2), "basicauth") {
		t.Errorf("no basic-auth expected when protect_admin_uis is off\n%s", out2)
	}
}

// Cloudflare Tunnel exposure: the app gets NO Traefik labels and NO host port (just
// `expose`d in-network), no traefik network join, and a cloudflared connector is
// synthesized with the tunnel token. See docs/EXPOSURE_AND_REMOTE_ACCESS.md.
func TestExposeCloudflareTunnel(t *testing.T) {
	cfg := `{
		"project": {"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"web","image":"nginx","tag":"alpine","port":"80","web_routed":true}],
		"environments": {"dev": {"deployment":"compose","traefik_enabled":true,"traefik_network":"rigger-traefik","domain":"app1.example.com","expose_mode":"cloudflare_tunnel"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	web := svcBlock(t, string(out), "web")
	if strings.Contains(web, "traefik.enable") {
		t.Errorf("tunnel app must NOT emit Traefik labels\n---\n%s", web)
	}
	if strings.Contains(web, "rigger-traefik") {
		t.Errorf("tunnel app must NOT join the Traefik network\n---\n%s", web)
	}
	if !strings.Contains(web, "expose:") {
		t.Errorf("tunnel app must expose its port for the connector\n---\n%s", web)
	}
	cf := svcBlock(t, string(out), "cloudflared")
	if !strings.Contains(cf, "cloudflare/cloudflared") || !strings.Contains(cf, "TUNNEL_TOKEN=${CF_TUNNEL_TOKEN}") {
		t.Errorf("cloudflared connector missing/incomplete\n---\n%s", cf)
	}
	// Default (no expose_mode) must NOT synthesize a connector.
	def := strings.Replace(cfg, `,"expose_mode":"cloudflare_tunnel"`, "", 1)
	out2, _ := GenerateAt([]byte(def), "dev", time.Unix(0, 0).UTC())
	if strings.Contains(string(out2), "cloudflared:") {
		t.Errorf("no cloudflared expected when expose_mode is unset\n%s", out2)
	}
}

// auth_gate=basic puts the existing basic-auth middleware (via ${APP_AUTH_USERS}) on a
// real app's web router — not just admin sidecars.
func TestAuthGateBasic(t *testing.T) {
	cfg := `{
		"project": {"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"web","image":"nginx","tag":"alpine","port":"80","web_routed":true}],
		"environments": {"dev": {"deployment":"compose","traefik_enabled":true,"traefik_network":"rigger-traefik","domain":"app1.example.com","auth_gate":"basic"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	web := svcBlock(t, string(out), "web")
	if !strings.Contains(web, "basicauth.users=${APP_AUTH_USERS}") {
		t.Errorf("auth_gate=basic must gate the app web router with APP_AUTH_USERS\n---\n%s", web)
	}
}

// Internal-only "attach to network": the web service joins a user-named external
// Docker network (declared external) so an outside proxy / stack can reach it.
func TestExposeAttachNetwork(t *testing.T) {
	cfg := `{
		"project": {"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"web","image":"nginx","tag":"alpine","port":"80","web_routed":true}],
		"environments": {"dev": {"deployment":"compose","expose_mode":"none","attach_network":"my-proxy-net"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "  my-proxy-net:\n    external: true") {
		t.Errorf("attach network must be declared external at top level\n%s", s)
	}
	if web := svcBlock(t, s, "web"); !strings.Contains(web, "my-proxy-net: {}") {
		t.Errorf("web service must join the attach network\n---\n%s", web)
	}
}

// A Traefik-routed web service with an EXPLICIT host_port publishes that host port AND
// keeps its Traefik router — so a user-run reverse proxy can target host:port directly
// (e.g. when Rigger isn't public-facing) while Traefik still routes the domain.
// Under Traefik a web-routed service's primary host port is STRIPPED by default (the app is
// reached by domain; the host port is redundant and conflict-prone) and RE-PUBLISHED only when
// the env/workspace opts to "keep" it. The Traefik router is emitted in both cases.
func TestWebRoutedHostPortStrippedUnderTraefikByDefault(t *testing.T) {
	base := `{
		"project":{"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services":[{"name":"frontend","image":"nginx","tag":"alpine","port":"3000","web_routed":true,"host_port":"3456"}],
		"environments":{"dev":{"deployment":"compose","traefik_enabled":true%s}}
	}`
	// Default: no host port published, router present.
	out, err := GenerateAt([]byte(fmt.Sprintf(base, "")), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	web := svcBlock(t, string(out), "frontend")
	if strings.Contains(web, `- "3456:3000"`) {
		t.Errorf("primary host_port must be stripped under Traefik by default\n---\n%s", web)
	}
	if !strings.Contains(web, "traefik.http.routers.") {
		t.Errorf("Traefik router must still be emitted\n---\n%s", web)
	}
	// Per-env override "keep": host port re-published, router still present.
	out2, err := GenerateAt([]byte(fmt.Sprintf(base, `,"traefik_host_ports":"keep"`)), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	web2 := svcBlock(t, string(out2), "frontend")
	if !strings.Contains(web2, `- "3456:3000"`) {
		t.Errorf("host_port must be published when the env keeps it\n---\n%s", web2)
	}
	if !strings.Contains(web2, "traefik.http.routers.") {
		t.Errorf("Traefik router must still be emitted alongside the kept host port\n---\n%s", web2)
	}
}

// The workspace default (RouteOpts.KeepHostPortsUnderTraefik=true) re-publishes host ports for
// every env that doesn't override it; extra_ports and non-web-routed services are unaffected.
func TestKeepHostPortsWorkspaceDefault(t *testing.T) {
	cfg := `{
		"project":{"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services":[{"name":"app","image":"gitea/gitea","tag":"1.21","port":"3000","web_routed":true,"host_port":"3000","extra_ports":["2222:22"]}],
		"environments":{"dev":{"deployment":"compose","traefik_enabled":true}}
	}`
	out, err := GenerateRouted([]byte(cfg), "dev", RouteOpts{KeepHostPortsUnderTraefik: true})
	if err != nil {
		t.Fatal(err)
	}
	app := svcBlock(t, string(out), "app")
	if !strings.Contains(app, `- "3000:3000"`) {
		t.Errorf("workspace keep-default must publish the host port\n---\n%s", app)
	}
	// extra_ports always publish regardless of the strip decision.
	if !strings.Contains(app, `- "2222:22"`) {
		t.Errorf("extra_ports must always publish\n---\n%s", app)
	}
}

// A verified custom domain emits an extra HTTPS router (per-host Let's Encrypt) on the
// apex web service, plus a shared http→https redirect middleware. Empty list ⇒ no extra
// routers (golden parity, covered by every other test).
func TestCustomDomainRouting(t *testing.T) {
	cfg := `{
		"project":{"name":"app1","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services":[{"name":"web","image":"nginx","tag":"alpine","port":"80","web_routed":true}],
		"environments":{"prod":{"deployment":"compose","traefik_enabled":true}}
	}`
	out, err := GenerateRouted([]byte(cfg), "prod", RouteOpts{BaseDomain: "example.com", CustomDomains: []string{"app.acme.com"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"Host(`app.acme.com`)",
		"_cd0.tls.certresolver=letsencrypt",
		"_cdredirect.redirectscheme.scheme=https",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("custom-domain output missing %q\n%s", want, s)
		}
	}
	// No custom domains ⇒ no _cd routers (parity).
	out2, _ := GenerateRouted([]byte(cfg), "prod", RouteOpts{BaseDomain: "example.com"})
	if strings.Contains(string(out2), "_cd0") {
		t.Errorf("empty CustomDomains must not emit _cd routers\n%s", out2)
	}
}

// A legacy project that carries a literal "adminer" service AND the web_sql flag must
// render exactly ONE adminer service (synth skipped — no duplicate, invalid key).
func TestAdminerNoDoubleEmit(t *testing.T) {
	cfg := `{
		"project": {"name":"db2","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"postgres","web_sql":true},
		"services": [{"name":"adminer","image":"adminer","tag":"4.8.1","port":"8080","web_routed":true,"host_port":"8978","env_file":true}],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(out), "  adminer:\n"); n != 1 {
		t.Errorf("want exactly 1 adminer service, got %d\n%s", n, out)
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

// OverrideCert: an SSL env using a per-email file-provider cert emits tls=true but NO
// certresolver (Traefik serves the out-of-band cert by SNI), while still redirecting
// http→https. Default (OverrideCert=false) keeps the letsencrypt resolver.
func TestServicesOverrideCert(t *testing.T) {
	cfg := []byte(`{
		"project": {"name":"api","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"app","build":{},"web_routed":true,"port":"8080","env_file":true}],
		"environments": {"prod": {"deployment":"compose","domain":"api.example.com",
			"traefik_enabled":true,"traefik_network":"traefik_net","ssl_enabled":true,"acme_email":"team@example.com"}}
	}`)

	// Without OverrideCert → Traefik's own resolver.
	def, err := GenerateRouted(cfg, "prod", RouteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, svcBlock(t, string(def), "app"), "tls.certresolver=letsencrypt")

	// With OverrideCert → tls=true, no certresolver, redirect intact.
	ov, err := GenerateRouted(cfg, "prod", RouteOpts{OverrideCert: true})
	if err != nil {
		t.Fatal(err)
	}
	app := svcBlock(t, string(ov), "app")
	mustContain(t, app, "traefik.http.routers.api_prod_app.tls=true")
	mustNotContain(t, app, "certresolver")
	mustContain(t, app, "redirectscheme.scheme=https")
}

// Managed OpenSearch emits a single-node service with the admin password wired from
// ${OPENSEARCH_PASSWORD}, a JVM heap floor, a data volume, and a TLS-aware healthcheck.
// Non-SQL → no Adminer service is synthesized.
func TestManagedOpenSearch(t *testing.T) {
	cfg := []byte(`{
		"project": {"name":"logs","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"opensearch"},
		"services": [{"name":"app","build":{},"web_routed":true,"port":"3000","env_file":true}],
		"environments": {"prod": {"deployment":"compose","domain":"example.com","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`)
	out, err := GenerateAt(cfg, "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"  opensearch:",
		"image: opensearchproject/opensearch:2",
		"discovery.type: single-node",
		"OPENSEARCH_INITIAL_ADMIN_PASSWORD: ${OPENSEARCH_PASSWORD}",
		"OPENSEARCH_JAVA_OPTS: -Xms512m -Xmx512m",
		"logs_prod_opensearch_data:/usr/share/opensearch/data",
		"_cluster/health",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "  adminer:") {
		t.Errorf("unexpected adminer service for a non-SQL engine:\n%s", s)
	}
}

// Managed VictoriaMetrics emits a single-node service with a storage path + data
// volume and — because the image is FROM scratch (no shell) — NO healthcheck (so
// dependents wait for service_started).
func TestManagedVictoriaMetrics(t *testing.T) {
	cfg := []byte(`{
		"project": {"name":"metrics","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0},"database":"victoriametrics"},
		"services": [{"name":"app","build":{},"web_routed":true,"port":"3000","env_file":true}],
		"environments": {"prod": {"deployment":"compose","domain":"example.com","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`)
	out, err := GenerateAt(cfg, "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"  victoriametrics:",
		"image: victoriametrics/victoria-metrics:v1.102.0",
		"command: -storageDataPath=/victoria-metrics-data -retentionPeriod=1",
		"metrics_prod_victoriametrics_data:/victoria-metrics-data",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	// No shell in the image → no healthcheck is emitted (app has none either).
	if strings.Contains(s, "healthcheck:") {
		t.Errorf("VictoriaMetrics must not carry a healthcheck:\n%s", s)
	}
}

func TestManagedRabbitMQ(t *testing.T) {
	cfg := []byte(`{
		"project": {"name":"broker","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0},"queue":"rabbitmq"},
		"services": [{"name":"app","build":{},"web_routed":true,"port":"3000","env_file":true,"depends_on":["rabbitmq"]}],
		"environments": {"prod": {"deployment":"compose","domain":"example.com","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`)
	out, err := GenerateAt(cfg, "prod", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"  rabbitmq:",
		"image: rabbitmq:3.13-management",
		"container_name: broker_prod_rabbitmq",
		"RABBITMQ_DEFAULT_USER: ${RABBITMQ_USER}",
		"RABBITMQ_DEFAULT_PASS: ${RABBITMQ_PASSWORD}",
		"RABBITMQ_DEFAULT_VHOST: ${RABBITMQ_VHOST}",
		"broker_prod_rabbitmq_data:/var/lib/rabbitmq",
		"rabbitmq-diagnostics -q ping",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	// Managed broker carries a healthcheck, so the app's depends_on can gate on healthy.
	if !strings.Contains(s, "healthcheck:") {
		t.Errorf("RabbitMQ must carry a healthcheck:\n%s", s)
	}
	if !strings.Contains(s, "condition: service_healthy") {
		t.Errorf("app depends_on rabbitmq should be service_healthy:\n%s", s)
	}
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

// Magic-DNS fallback: no base domain + auto_url_mode → a cross-machine {label}.{ip}.sslip.io
// host over HTTP. A base domain still wins over the auto-URL; empty host degrades to localhost.
func TestServicesMagicDNSRoute(t *testing.T) {
	cfg := []byte(`{
		"project": {"name":"weather app","resource_prefix":"mcl_wda","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"nginx","image":"nginx","tag":"alpine","web_routed":true,"port":"80"}],
		"environments": {"dev": {"deployment":"compose","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`)
	// sslip with a host → cross-machine host over HTTP.
	out, err := GenerateRouted(cfg, "dev", RouteOpts{AutoURLMode: "sslip", AutoURLHost: "10.10.10.111"})
	if err != nil {
		t.Fatal(err)
	}
	app := svcBlock(t, string(out), "nginx")
	mustContain(t, app, "Host(`mcl-wda-dev.10.10.10.111.sslip.io`)")
	mustContain(t, app, "entrypoints=web")
	mustNotContain(t, app, "certresolver") // HTTP in Phase 1

	// nip variant.
	out2, _ := GenerateRouted(cfg, "dev", RouteOpts{AutoURLMode: "nip", AutoURLHost: "10.10.10.111"})
	mustContain(t, svcBlock(t, string(out2), "nginx"), "Host(`mcl-wda-dev.10.10.10.111.nip.io`)")

	// A base domain wins over the auto-URL mode.
	out3, _ := GenerateRouted(cfg, "dev", RouteOpts{BaseDomain: "onrigger.com", AutoURLMode: "sslip", AutoURLHost: "10.10.10.111"})
	mustContain(t, svcBlock(t, string(out3), "nginx"), "Host(`mcl-wda-dev.onrigger.com`)")

	// Magic-DNS mode but no host → degrade to localhost (unchanged default).
	out4, _ := GenerateRouted(cfg, "dev", RouteOpts{AutoURLMode: "sslip"})
	mustContain(t, svcBlock(t, string(out4), "nginx"), "Host(`mcl-wda-dev.localhost`)")
}

// With a base domain + a DNS provider, the apex web router uses the DNS-01
// certresolver and requests ONE wildcard cert (*.{base}); without a provider it
// stays on per-host letsencrypt (HTTP-01) — golden behaviour.
func TestServicesWildcardCert(t *testing.T) {
	cfg := []byte(`{
		"project": {"name":"app","resource_prefix":"mcl_wda","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"nginx","image":"nginx","tag":"alpine","web_routed":true,"port":"80"}],
		"environments": {"dev": {"deployment":"compose","traefik_enabled":true,"traefik_network":"traefik_net"}}
	}`)
	// DNS provider set → dns resolver + wildcard SANs on the apex router.
	out, err := GenerateRouted(cfg, "dev", RouteOpts{BaseDomain: "onrigger.com", DNSProvider: "cloudflare"})
	if err != nil {
		t.Fatal(err)
	}
	app := svcBlock(t, string(out), "nginx")
	for _, want := range []string{
		"Host(`mcl-wda-dev.onrigger.com`)",
		"tls.certresolver=dns",
		"tls.domains[0].main=onrigger.com",
		"tls.domains[0].sans=*.onrigger.com",
	} {
		mustContain(t, app, want)
	}
	mustNotContain(t, app, "certresolver=letsencrypt")

	// No provider → unchanged per-host HTTP-01 (golden behaviour, no wildcard SANs).
	out2, _ := GenerateRouted(cfg, "dev", RouteOpts{BaseDomain: "onrigger.com"})
	app2 := svcBlock(t, string(out2), "nginx")
	mustContain(t, app2, "certresolver=letsencrypt")
	mustNotContain(t, app2, "tls.domains")
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
	if u, routed := EnvRouteURL(mk(false, "", "false"), "dev", "", "", ""); routed {
		t.Errorf("traefik off should not route, got %q", u)
	}
	// Auto localhost.
	if u, routed := EnvRouteURL(mk(true, "", "false"), "dev", "", "", ""); !routed || u != "http://ws-app-dev.localhost" {
		t.Errorf("auto localhost = %q,%v; want http://ws-app-dev.localhost,true", u, routed)
	}
	// Auto base domain → HTTPS.
	if u, routed := EnvRouteURL(mk(true, "", "false"), "dev", "apps.example.com", "", ""); !routed || u != "https://ws-app-dev.apps.example.com" {
		t.Errorf("auto base = %q,%v; want https://ws-app-dev.apps.example.com,true", u, routed)
	}
	// Explicit domain wins.
	if u, routed := EnvRouteURL(mk(true, "my.host", "true"), "dev", "apps.example.com", "", ""); !routed || u != "https://my.host" {
		t.Errorf("explicit = %q,%v; want https://my.host,true", u, routed)
	}
	// Preview env: the hyphen-free pr{n} suffix stays a single flat label under the
	// wildcard cert (ws-app-pr42, not ws-app-pr-42), so one *.base cert covers it.
	prCfg := []byte(`{
		"project": {"name":"x","resource_prefix":"ws_app"},
		"services": [{"name":"app","build":{},"web_routed":true,"port":"80"}],
		"environments": {"pr42": {"deployment":"compose","traefik_enabled":true,"traefik_network":"traefik_net","domain":"","ssl_enabled":false}}
	}`)
	if u, routed := EnvRouteURL(prCfg, "pr42", "apps.example.com", "", ""); !routed || u != "https://ws-app-pr42.apps.example.com" {
		t.Errorf("preview env = %q,%v; want https://ws-app-pr42.apps.example.com,true", u, routed)
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

// EnvFileMount under SWARM must NOT use inline `content:` — `docker stack deploy`
// rejects it ("Additional property content is not allowed"). It uses `file: ./.env`
// with a content-addressed config name (swarm configs are immutable).
func TestServicesEnvFileMountSwarm(t *testing.T) {
	cfg := `{
		"project": {"name":"app","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"backend","build":{},"env_file":true,"env_file_mount":"/var/www/html/.env"}],
		"environments": {"dev": {"deployment":"swarm"}}
	}`
	out, err := GenerateRouted([]byte(cfg), "dev", RouteOpts{EnvFile: "DB_HOST=mysql\nDB_PORT=3306\n"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "content: |") {
		t.Errorf("swarm config must not use inline content: (rejected by stack deploy)\n%s", s)
	}
	for _, want := range []string{"configs:\n  app_dev_dotenv:", "file: ./.env", "name: app_dev_dotenv_"} {
		if !strings.Contains(s, want) {
			t.Errorf("swarm top-level config missing %q\n---\n%s", want, s)
		}
	}
	// The service still references the stable compose key (not the hashed swarm name).
	if app := svcBlock(t, s, "backend"); !strings.Contains(app, "- source: app_dev_dotenv") {
		t.Errorf("service must reference the dotenv config by its compose key\n%s", app)
	}
}

// Under SWARM, Traefik router labels must sit under deploy.labels (where the swarm
// provider reads them) — never at the container level — and container_name is omitted.
func TestSwarmTraefikLabelsUnderDeploy(t *testing.T) {
	cfg := `{
		"project": {"name":"app","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"web","build":{},"web_routed":true,"port":"80"}],
		"environments": {"dev": {"deployment":"swarm","traefik_enabled":true,"domain":"app.example.com","http_port":8080}}
	}`
	out, err := GenerateRouted([]byte(cfg), "dev", RouteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "container_name:") {
		t.Errorf("swarm must not emit container_name (unsupported)\n%s", s)
	}
	// Labels nested under deploy: (6-space `labels:`, 8-space entries).
	if !strings.Contains(s, "      labels:\n        - \"traefik.enable=true\"") {
		t.Errorf("expected Traefik labels under deploy.labels\n---\n%s", s)
	}
	// NOT at the container level (4-space `labels:`, 6-space entries) — the swarm
	// provider ignores those, which was the routing bug.
	if strings.Contains(s, "    labels:\n      - \"traefik.enable=true\"") {
		t.Errorf("Traefik labels must not be at container level for swarm\n---\n%s", s)
	}
}

// Swarm Traefik labels must be scoped to the routed service only — a managed dep
// (mysql/redis/…) built after it must NOT inherit them, else Traefik adds the DB's
// internal-network IP to the app's server pool → intermittent 502/timeouts.
func TestSwarmLabelsDoNotLeakToManagedDeps(t *testing.T) {
	cfg := `{
		"project": {"name":"app","registry":"reg","database":"mysql","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"web","build":{},"web_routed":true,"port":"80"}],
		"environments": {"dev": {"deployment":"swarm","traefik_enabled":true,"domain":"app.example.com","http_port":8080}}
	}`
	out, err := GenerateRouted([]byte(cfg), "dev", RouteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// The web service carries the router labels…
	if web := svcBlock(t, s, "web"); !strings.Contains(web, "traefik.enable=true") {
		t.Errorf("web service should carry Traefik labels\n%s", web)
	}
	// …the managed mysql must NOT.
	if mysql := svcBlock(t, s, "mysql"); strings.Contains(mysql, "traefik") {
		t.Errorf("managed mysql must NOT inherit the app's Traefik labels\n---\n%s", mysql)
	}
}

// Service links emit {ENV_VAR}={scheme}://{prefix}_{target}:{port}{path} into the
// service's environment block: port defaults to the target service's own port (or a
// managed-dep default when the target has no service entry), and a link wins over an
// env_vars key of the same name (merged into one sorted keyspace, no dup key).
func TestServiceLinks(t *testing.T) {
	cfg := `{
		"project": {"name":"app","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [
			{"name":"backend","build":{},"port":"8000"},
			{"name":"frontend","build":{},"port":"3000","web_routed":true,
			 "env_vars":{"STATIC":"x","API_URL":"http://override-me"},
			 "links":[
				{"service":"backend","env_var":"API_URL","path":"/api"},
				{"service":"postgres","env_var":"PG_URL","scheme":"postgres","port":"","path":"/db"}
			 ]}
		],
		"environments": {"dev": {"deployment":"compose","http_port":"8080","database":"postgres"}}
	}`
	out, err := GenerateAt([]byte(cfg), "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	fe := svcBlock(t, string(out), "frontend")
	for _, want := range []string{
		"- API_URL=http://app_dev_backend:8000/api",   // target port (8000) + path; link beats override-me
		"- PG_URL=postgres://app_dev_postgres:5432/db", // managed-dep default port + custom scheme
		"- STATIC=x",                                   // static env preserved
	} {
		if !strings.Contains(fe, want) {
			t.Errorf("frontend env missing %q\n---\n%s", want, fe)
		}
	}
	// No duplicate/clobbered key: the override-me literal must not survive.
	if strings.Contains(fe, "http://override-me") {
		t.Errorf("link did not override the static env var\n---\n%s", fe)
	}
}
