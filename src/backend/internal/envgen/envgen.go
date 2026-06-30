// Package envgen ports scripts/env-gen.sh: it generates a workspace
// environment's .env (and a secrets-masked .env.example) from config.json.
//
// Two paths mirror the bash script:
//   - image stacks: write the per-environment env_vars, auto-generating secrets
//     for placeholder secret-keys and preserving any already-set values.
//   - custom stacks: emit the fixed structured .env (project, image tags, DB,
//     app, redis, garage, mail, node), preserving existing secrets, then append
//     any extra env_vars from config.
//
// Secret generation is injectable (Rand) so tests are deterministic; production
// uses crypto/rand. Parity with the bash script is semantic (same keys/values),
// not byte-for-byte.
package envgen

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/mansoor/rigger/ui/internal/blueprints"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
)

// Rand returns n cryptographically-random bytes. CryptoRand is the production
// implementation; tests inject a deterministic stub.
type Rand func(n int) []byte

// CryptoRand reads n bytes from crypto/rand. Panics only on catastrophic OS
// entropy failure.
func CryptoRand(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return b
}

// identSafe lowercases a name and collapses every run of non-alphanumeric
// characters into a single underscore, trimming leading/trailing underscores —
// turning a free-form value (e.g. "weather dashboard app") into one safe to use
// as a SQL database/user identifier ("weather_dashboard_app"). Already-safe
// values like a resource prefix ("mcl_wda") pass through unchanged.
func identSafe(s string) string {
	var b strings.Builder
	pendingSep := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingSep && b.Len() > 0 {
				b.WriteByte('_')
			}
			pendingSep = false
			b.WriteRune(r)
		} else {
			pendingSep = true
		}
	}
	return b.String()
}

// envQuote double-quotes v when it contains characters a strict dotenv parser
// treats as significant — whitespace, '#', or quotes. Docker's env_file is
// lenient (the whole value after '=' is taken literally), but a physical .env
// mounted via env_file_mount is parsed strictly (e.g. Laravel's phpdotenv:
// "Encountered unexpected whitespace"), so a free-form value like a project
// display name ("weather dashboard app") must be quoted. Plain values (the
// common KEY=value case) pass through unchanged, so existing output is stable.
// Both docker compose and phpdotenv strip the surrounding quotes on read.
func envQuote(v string) string {
	if v == "" || !strings.ContainsAny(v, " \t\r\n\"'#") {
		return v
	}
	esc := strings.ReplaceAll(v, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"`, `\"`)
	return `"` + esc + `"`
}

func hexN(r Rand, n int) string    { return hex.EncodeToString(r(n)) }
func base64N(r Rand, n int) string { return base64.StdEncoding.EncodeToString(r(n)) }

// strongPw returns a 16-char password guaranteed to contain an upper, a lower, a
// digit and a special char (a single '-', which is both URL- and dotenv-safe so it
// can be embedded verbatim in a connection URL). Required by engines like OpenSearch
// 2.12+ that reject weak admin passwords. Derived from the injected Rand so tests
// stay deterministic; ambiguous glyphs (0/O/1/l/I) are excluded.
func strongPw(r Rand) string {
	const lower = "abcdefghijkmnopqrstuvwxyz"
	const upper = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	const digit = "23456789"
	const alnum = lower + upper + digit
	b := r(16)
	out := make([]byte, 16)
	out[0] = lower[int(b[0])%len(lower)]
	out[1] = upper[int(b[1])%len(upper)]
	out[2] = digit[int(b[2])%len(digit)]
	out[3] = '-'
	for i := 4; i < 16; i++ {
		out[i] = alnum[int(b[i])%len(alnum)]
	}
	return string(out)
}

// ── Secret-key rules (shared with workspace.GenerateSmartDefaults) ──────────────

// IsPlaceholder reports whether a value is a stand-in that was never meant to be
// used as-is (env-gen.sh _is_placeholder).
func IsPlaceholder(v string) bool {
	v = strings.TrimSpace(v)
	vu := strings.ToUpper(v)
	return v == "" ||
		strings.Contains(vu, "CHANGE_ME") ||
		strings.Contains(vu, "CHANGEME") ||
		strings.Contains(vu, "CHANGE-ME") ||
		strings.Contains(vu, "YOUR_") ||
		strings.Contains(vu, "REPLACE_ME") ||
		strings.Contains(vu, "REPLACE-ME") ||
		strings.HasPrefix(vu, "CHANGE") ||
		strings.EqualFold(v, "secret") ||
		strings.EqualFold(v, "password") ||
		strings.EqualFold(v, "changeme") ||
		strings.EqualFold(v, "todo") ||
		strings.EqualFold(v, "fixme")
}

// isSkipKey reports keys that need human input and must keep their placeholder
// even when it looks like a stand-in (env-gen.sh _is_skip_key).
func isSkipKey(key string) bool {
	ku := strings.ToUpper(key)
	for _, s := range []string{"PORT", "HOST", "URL", "DOMAIN", "PATH", "DIR", "MODE", "ENABLED", "DB_NAME", "DATABASE", "DB_USER", "USERNAME"} {
		if strings.Contains(ku, s) {
			return true
		}
	}
	return false
}

// managedContractKeys returns the env keys Rigger owns authoritatively for the env's
// active managed dependencies, so scanned/extra seed values can't silently override
// them (see the Extra-variables loop in Generate). Covers the framework DB contract
// (fe), the raw managed-DB keys, redis, garage, and the Adminer secret — each gated
// on the dependency actually being active for this env.
func managedContractKeys(cfg *wsconfig.Config, e wsconfig.Env, fe map[string]string) map[string]bool {
	out := make(map[string]bool, len(fe)+24)
	for k := range fe {
		out[k] = true
	}
	if eng := cfg.EffDatabase(e); eng != "" && eng != "none" {
		for _, k := range []string{
			"DATABASE", "DATABASE_URL", "DB_EXTERNAL_PORT",
			"MYSQL_HOST", "MYSQL_PORT", "MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD", "MYSQL_ROOT_PASSWORD",
			"POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD",
			"MONGO_HOST", "MONGO_PORT", "MONGO_DB", "MONGO_USER", "MONGO_PASSWORD", "MONGO_URI",
			"OPENSEARCH_HOST", "OPENSEARCH_PORT", "OPENSEARCH_USER", "OPENSEARCH_PASSWORD", "OPENSEARCH_URL",
			"VICTORIA_HOST", "VICTORIA_PORT", "VICTORIA_URL",
		} {
			out[k] = true
		}
	}
	if cfg.EffRedis(e) {
		for _, k := range []string{"REDIS_ENABLED", "REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD", "REDIS_URL"} {
			out[k] = true
		}
	}
	// Object storage (local and/or minio — independent). FILESYSTEM_DISK + OBJECT_STORAGE
	// are owned whenever either backend is on; MinIO additionally owns the MINIO_*/AWS_* keys.
	if cfg.MinIOOn(e) || cfg.LocalStorageOn(e) {
		out["OBJECT_STORAGE"] = true
		out["FILESYSTEM_DISK"] = true
	}
	if cfg.MinIOOn(e) {
		for _, k := range []string{
			"MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD", "MINIO_BUCKET",
			"MINIO_ENDPOINT", "MINIO_REGION", "MINIO_CONSOLE_PASSPHRASE", "MINIO_CONSOLE_SALT",
			// The framework S3 contract (e.g. Laravel) maps MinIO onto AWS_* keys — reserve
			// them so a repo's .env.example AWS_* defaults can't shadow the managed values.
			"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_DEFAULT_REGION", "AWS_BUCKET",
			"AWS_ENDPOINT", "AWS_USE_PATH_STYLE_ENDPOINT",
		} {
			out[k] = true
		}
	}
	// When Mailpit is on for this env, Rigger owns the SMTP wiring so a repo's
	// .env.example MAIL_HOST/PORT can't redirect mail away from the catch-all.
	if cfg.EffMailpit(e) {
		for _, k := range []string{"MAIL_MAILER", "MAIL_DRIVER", "MAIL_HOST", "MAIL_PORT", "MAIL_ENCRYPTION"} {
			out[k] = true
		}
	}
	if cfg.HasAdminerEnv(e) {
		out["ADMINER_LOGIN_SECRET"] = true
	}
	if e.ProtectAdminUIs {
		for _, k := range []string{"ADMIN_UI_USER", "ADMIN_UI_PASSWORD", "ADMIN_UI_USERS"} {
			out[k] = true
		}
	}
	return out
}

// databaseURL composes the standard connection URL Rigger emits as the baseline
// DATABASE_URL, from the managed-DB facts envgen already wrote (host/user/db/password).
// Mirrors the POSTGRES_*/MYSQL_*/MONGO_* values produced in Generate's DB block: user
// "{dbBase}_user", db "{dbBase}_{env}", host "{prefix}_{engine}". The password is encoded
// via url.UserPassword so a special character can't break the URL. "" for engines without
// a URL form (none).
func databaseURL(engine, prefix, dbBase, env, password string) string {
	user := dbBase + "_user"
	dbName := dbBase + "_" + env
	mk := func(scheme, host, port, query string) string {
		u := url.URL{
			Scheme: scheme,
			User:   url.UserPassword(user, password),
			Host:   host + ":" + port,
			Path:   "/" + dbName,
		}
		if query != "" {
			u.RawQuery = query
		}
		return u.String()
	}
	switch engine {
	case "postgres":
		return mk("postgresql", prefix+"_postgres", "5432", "")
	case "mysql", "mariadb":
		return mk("mysql", prefix+"_"+engine, "3306", "")
	case "mongodb":
		// Mongo's root user authenticates against the admin DB (matches MONGO_URI).
		return mk("mongodb", prefix+"_mongodb", "27017", "authSource=admin")
	}
	return ""
}

// frameworkEnv unions the blueprint-declared env contracts of every build
// service in the config, resolved against the active env's managed-dep facts
// (db engine/host/credentials, redis). Build services carry their framework via
// build.template (set by the detector or the blueprint picker); services without
// a recognised blueprint contribute nothing. On a key clash between two
// frameworks the first service's value wins.
func frameworkEnv(cfg *wsconfig.Config, e wsconfig.Env, prefix, dbBase, env, dbPassword, minioUser, minioPassword, minioBucket string) map[string]string {
	// User must match the account the managed-db container provisions, which
	// envgen writes as MYSQL_USER/POSTGRES_USER = "<dbBase>_user".
	dbUser := dbBase + "_user"
	engine := cfg.EffDatabase(e)
	var db *blueprints.DBFacts
	switch engine {
	case "mysql", "mariadb":
		// MariaDB is wire-compatible with MySQL — frameworks use the mysql driver;
		// only the host (container/alias) differs ({prefix}_mysql vs {prefix}_mariadb).
		db = &blueprints.DBFacts{Engine: "mysql", Host: prefix + "_" + engine, Port: "3306", Name: dbBase + "_" + env, User: dbUser, Password: dbPassword}
	case "postgres":
		db = &blueprints.DBFacts{Engine: "postgres", Host: prefix + "_postgres", Port: "5432", Name: dbBase + "_" + env, User: dbUser, Password: dbPassword}
	}
	var redis *blueprints.RedisFacts
	if cfg.EffRedis(e) {
		redis = &blueprints.RedisFacts{Host: prefix + "_redis", Port: "6379"}
	}
	// Storage facts drive the framework's file/object-storage wiring (local and/or minio,
	// independent): S3 → the AWS_* contract (access key/secret ARE the MinIO root creds;
	// endpoint is the in-network MinIO S3 API; region is a placeholder SDKs require but
	// MinIO ignores); DefaultDisk → FILESYSTEM_DISK (s3 when minio is on, else local).
	var storage *blueprints.StorageFacts
	if cfg.MinIOOn(e) || cfg.LocalStorageOn(e) {
		storage = &blueprints.StorageFacts{
			Local:       cfg.LocalStorageOn(e),
			S3:          cfg.MinIOOn(e),
			DefaultDisk: cfg.StorageDefaultDisk(e),
			KeyID:       minioUser,
			SecretKey:   minioPassword,
			Bucket:      minioBucket,
			Endpoint:    "http://minio:9000", // bare host — AWS SDK rejects underscores
			Region:      "us-east-1",
		}
	}

	out := map[string]string{}
	seen := map[string]bool{}
	for _, svc := range cfg.Services {
		if svc.Build == nil || svc.Build.Template == "" || seen[svc.Build.Template] {
			continue
		}
		seen[svc.Build.Template] = true
		bp, ok := blueprints.Get(svc.Build.Template)
		if !ok || bp.EnvVars == nil {
			continue
		}
		for k, v := range bp.EnvVars(db, redis, storage) {
			if _, exists := out[k]; !exists {
				out[k] = v
			}
		}
	}
	return out
}

// isSecretKey reports keys whose placeholder should be replaced with a generated
// secret (env-gen.sh _is_secret_key).
func isSecretKey(key string) bool {
	ku := strings.ToUpper(key)
	if strings.Contains(ku, "PASSWORD") || strings.Contains(ku, "PASSWD") ||
		strings.Contains(ku, "SECRET") || strings.Contains(ku, "TOKEN") ||
		strings.Contains(ku, "SALT") {
		return true
	}
	return strings.Contains(ku, "KEY") && !strings.Contains(ku, "_ID")
}

// genSecret generates a secret sized by key type (env-gen.sh _gen_secret).
func genSecret(key string, r Rand) string {
	ku := strings.ToUpper(key)
	switch {
	case strings.Contains(ku, "ROOT_PASSWORD") || strings.Contains(ku, "MASTER_PASSWORD"):
		return "rigger-" + hexN(r, 20)
	case strings.Contains(ku, "PASSWORD") || strings.Contains(ku, "PASSWD"):
		return "rigger-" + hexN(r, 12)
	case strings.Contains(ku, "ADMIN_TOKEN") || strings.Contains(ku, "ADMIN_SECRET"):
		return hexN(r, 32)
	case strings.Contains(ku, "TOKEN"):
		return hexN(r, 24)
	case strings.Contains(ku, "SECRET"):
		return hexN(r, 20)
	case strings.Contains(ku, "SALT") || strings.Contains(ku, "_KEY"):
		return hexN(r, 16)
	default:
		return hexN(r, 12)
	}
}

// ResolveImageValue applies the env-gen.sh image-stack rules to one env var:
// keep non-placeholders and skip-keys as-is; for placeholder secret-keys, reuse
// an existing value if present else generate one; otherwise keep the placeholder.
func ResolveImageValue(key, value string, existing map[string]string, r Rand) string {
	if !IsPlaceholder(value) {
		return value
	}
	if isSkipKey(key) {
		return value
	}
	if isSecretKey(key) {
		if existing != nil {
			if ev, ok := existing[key]; ok && ev != "" {
				return ev
			}
		}
		return genSecret(key, r)
	}
	return value
}

// ── Generation ──────────────────────────────────────────────────────────────────

// imageEnvVar maps a build service name to its image-override env var
// (api → API_IMAGE). Mirrors composegen's imageEnvVar so the .env value and the
// compose `${NAME_IMAGE:-…}` default line up.
func imageEnvVar(name string) string {
	return strings.ReplaceAll(strings.ToUpper(name), "-", "_") + "_IMAGE"
}

// ManagedSecretKeys are the auto-generated secrets envgen preserves across regen (the
// getOut keys). Bootstrap pins their resolved values into config.json the first time they
// appear, so a later regen with a missing/empty .env reads them back instead of rerolling
// — which would break against an already-initialized DB / data volume.
var ManagedSecretKeys = []string{
	"MYSQL_PASSWORD", "MYSQL_ROOT_PASSWORD", "POSTGRES_PASSWORD", "MONGO_PASSWORD", "DB_PASSWORD",
	"OPENSEARCH_PASSWORD",
	"APP_KEY", "MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD",
	"MINIO_CONSOLE_PASSPHRASE", "MINIO_CONSOLE_SALT",
}

// Generate produces the .env and .env.example contents for one environment.
// existing is the parsed current .env (may be nil) used to preserve secrets.
func Generate(cfg *wsconfig.Config, env string, existing map[string]string, r Rand) (envOut, exampleOut string, err error) {
	if r == nil {
		r = CryptoRand
	}
	e, ok := cfg.Environments[env]
	if !ok {
		return "", "", fmt.Errorf("unknown environment %q", env)
	}
	return generate(cfg, env, e, existing, r)
}

func generate(cfg *wsconfig.Config, env string, e wsconfig.Env, existing map[string]string, r Rand) (string, string, error) {
	project := cfg.Project.Name
	// imgBase is the immutable Docker resource prefix (matches composegen container
	// names and builder image tags); project (display name) stays for DB
	// names/users/buckets so it can repeat across workspaces.
	imgBase := cfg.Project.Prefix()
	prefix := imgBase + "_" + env
	registry := cfg.Project.Registry
	tag := cfg.VersionString() + "-" + env

	get := func(key, def string) string {
		if existing != nil {
			if v, ok := existing[key]; ok && v != "" {
				return v
			}
		}
		return def
	}
	// getOut preserves a managed secret across regen by reading the OUTPUT key(s)
	// actually written to .env (first match wins). The earlier `{prefix}_X` lookups
	// were NEVER written, so a regen rerolled every secret — which silently broke an
	// already-initialized managed-DB volume (its root/user password is fixed at first
	// init; a new .env password then gets "Access denied"). At create time existing is
	// nil, so the generated default is used (unchanged behavior).
	getOut := func(def string, keys ...string) string {
		// A secret pinned in config.json's `secrets` map is AUTHORITATIVE — it matches the
		// password the managed DB / data volume was initialized with on first deploy, and is
		// never user-editable. It MUST win even over the live .env: if the .env ever drifts
		// (a rerolled or hand-edited value), preserving the .env would lock the app out of an
		// already-initialized volume forever, with no self-healing. Checking the pin first
		// makes a regen heal a drifted .env back to the value the volume actually uses.
		// (A SEPARATE channel from env_vars on purpose: a repo's .env.example values must NOT
		// override Rigger-managed secrets. Bootstrap pins these on first generation via
		// pinManagedSecrets.)
		for _, k := range keys {
			if s, ok := e.Secrets[k]; ok && s != "" {
				return s
			}
		}
		// Not pinned (e.g. a project predating the pinning mechanism): preserve the live .env
		// value across regen so an already-initialized volume keeps working.
		if existing != nil {
			for _, k := range keys {
				if v, ok := existing[k]; ok && v != "" {
					return v
				}
			}
		}
		return def
	}
	dbPassword := getOut("changeme_"+hexN(r, 8), "MYSQL_PASSWORD", "POSTGRES_PASSWORD", "MONGO_PASSWORD", "DB_PASSWORD")
	dbRootPassword := getOut("changeme_"+hexN(r, 8), "MYSQL_ROOT_PASSWORD")
	// OpenSearch needs a complexity-meeting admin password (its own default — the shared
	// "changeme_…" wouldn't pass), preserved across regen like every managed-DB secret.
	osPassword := getOut(strongPw(r), "OPENSEARCH_PASSWORD")
	appKey := getOut("base64:"+base64N(r, 32), "APP_KEY")
	// MinIO root credentials double as the app's S3 access key/secret (AWS_*). They
	// must be preserved across regen — the bucket/data volume is provisioned with them.
	minioUser := getOut("rigger", "MINIO_ROOT_USER")
	minioPassword := getOut("rigger-"+hexN(r, 16), "MINIO_ROOT_PASSWORD") // MinIO requires ≥8 chars
	// opens3/console session crypto (CONSOLE_PBKDF_*). Stable per env so sessions survive regen.
	minioConsolePass := getOut(hexN(r, 16), "MINIO_CONSOLE_PASSPHRASE")
	minioConsoleSalt := getOut(hexN(r, 16), "MINIO_CONSOLE_SALT")

	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	p("# ============================================================\n")
	p("# Auto-generated by Rigger — environment: %s | project: %s\n", env, project)
	p("# DO NOT COMMIT — contains secrets\n")
	p("# ============================================================\n\n")

	p("# ── Project ────────────────────────────────────────────────\n")
	p("COMPOSE_PROJECT_NAME=%s\n", prefix)
	p("PROJECT_NAME=%s\n", envQuote(project))
	p("ENV=%s\n\n", env)

	p("# ── Image tags (one per build service) ─────────────────────\n")
	p("REGISTRY=%s\n", registry)
	p("IMAGE_TAG=%s\n", tag)
	for _, svc := range cfg.BuildServices() {
		// Omit the "{registry}/" prefix when there's no registry (local-only build) —
		// a leading slash is an invalid image reference. Must match wsconfig.ImageTag
		// and composegen.serviceImageRef so the built tag and the .env pointer agree.
		img := fmt.Sprintf("%s-%s:%s", imgBase, svc.Name, tag)
		if registry != "" {
			img = registry + "/" + img
		}
		p("%s=%s\n", imageEnvVar(svc.Name), img)
	}
	p("\n")

	p("# ── Domain ─────────────────────────────────────────────────\n")
	p("DOMAIN=%s\n", e.Domain)
	// HTTP_PORT/HTTPS_PORT are deliberately NOT emitted: they were never used for compose
	// interpolation (host ports are written by composegen straight from config.json), yet
	// `env_file: .env` injected them into EVERY container — and an app that reads a bare
	// HTTP_PORT (e.g. Gitea) then bound to Rigger's host port instead of its own and became
	// unreachable. The authoritative host port lives in config.json (e.HTTPPort).
	p("\n")

	p("# ── Stack config ────────────────────────────────────────────\n")
	p("DEPLOYMENT=%s\n", e.Deployment)
	p("TRAEFIK_ENABLED=%t\n\n", e.TraefikEnabled)

	p("# ── Database ───────────────────────────────────────────────\n")
	// Managed deps are project-level (Eff* falls back to the legacy per-env value);
	// only DBExternal (host-port exposure) stays per-env.
	engine := cfg.EffDatabase(e)
	p("DATABASE=%s\n", engine)
	// DB identifiers must be valid (no spaces/punctuation), so derive them from
	// the dns-safe resource prefix — NOT cfg.Project.Name, which is a free-form
	// display name that can contain spaces (e.g. "weather dashboard app" would
	// yield the invalid identifier "weather dashboard app_dev").
	dbBase := identSafe(imgBase)
	switch engine {
	case "postgres":
		p("POSTGRES_HOST=%s_postgres\n", prefix)
		p("POSTGRES_PORT=5432\n")
		p("POSTGRES_DB=%s_%s\n", dbBase, env)
		p("POSTGRES_USER=%s_user\n", dbBase)
		p("POSTGRES_PASSWORD=%s\n", dbPassword)
	case "mysql", "mariadb":
		// MariaDB reuses the MYSQL_* contract; only the host (container) differs.
		p("MYSQL_HOST=%s_%s\n", prefix, engine)
		p("MYSQL_PORT=3306\n")
		p("MYSQL_DATABASE=%s_%s\n", dbBase, env)
		p("MYSQL_USER=%s_user\n", dbBase)
		p("MYSQL_PASSWORD=%s\n", dbPassword)
		p("MYSQL_ROOT_PASSWORD=%s\n", dbRootPassword)
	case "mongodb":
		// The root user IS the app connection identity in this first cut (no separate
		// app-user provisioning); it authenticates against the admin database, so the
		// ready-to-use URI carries authSource=admin.
		host := prefix + "_mongodb"
		mongoUser := dbBase + "_user"
		mongoDB := dbBase + "_" + env
		p("MONGO_HOST=%s\n", host)
		p("MONGO_PORT=27017\n")
		p("MONGO_DB=%s\n", mongoDB)
		p("MONGO_USER=%s\n", mongoUser)
		p("MONGO_PASSWORD=%s\n", dbPassword)
		p("MONGO_URI=mongodb://%s:%s@%s:27017/%s?authSource=admin\n", mongoUser, dbPassword, host, mongoDB)
	case "opensearch":
		// Security plugin ON → HTTPS + the fixed bootstrap `admin` user (OpenSearch
		// doesn't provision a custom user in this cut). Apps connect over https with
		// cert verification disabled (self-signed demo cert, in-network only). No
		// "database" concept — OpenSearch uses indices, so there's no OPENSEARCH_DB.
		host := prefix + "_opensearch"
		p("OPENSEARCH_HOST=%s\n", host)
		p("OPENSEARCH_PORT=9200\n")
		p("OPENSEARCH_USER=admin\n")
		p("OPENSEARCH_PASSWORD=%s\n", osPassword)
		p("OPENSEARCH_URL=https://admin:%s@%s:9200\n", osPassword, host)
	case "victoriametrics":
		// Single-node TSDB, no auth on :8428 — connection is just the base URL. Apps
		// push via remote-write (/api/v1/write) and query with PromQL (/api/v1/query).
		host := prefix + "_victoriametrics"
		p("VICTORIA_HOST=%s\n", host)
		p("VICTORIA_PORT=8428\n")
		p("VICTORIA_URL=http://%s:8428\n", host)
	}
	// When the DB is published externally, expose the host port (overridable) so the
	// generated compose's ${DB_EXTERNAL_PORT} resolves and the info tab can show it.
	// DBExternal is per-environment (expose on dev, keep prod private).
	if engine != "" && engine != "none" && e.DBExternal {
		port := "3306"
		switch engine {
		case "postgres":
			port = "5432"
		case "opensearch":
			port = "9200"
		case "victoriametrics":
			port = "8428"
		}
		p("DB_EXTERNAL_PORT=%s\n", port)
	}
	// Adminer auto-login HMAC secret — shared between the bind-mounted Adminer plugin
	// (which reads it via env_file) and Rigger's adminer-login endpoint (which signs
	// links). Emitted when Adminer is present via the project web_sql flag or a legacy
	// literal "adminer" service.
	if cfg.HasAdminerEnv(e) {
		p("ADMINER_LOGIN_SECRET=%s\n", get("ADMINER_LOGIN_SECRET", hexN(r, 32)))
	}
	// Admin-UI basic-auth credential (per env, when protection is enabled and the
	// project has an admin sidecar). ADMIN_UI_USERS is the htpasswd line Traefik's
	// basicauth middleware reads; single-quoted at emit time so the dotenv parser
	// doesn't try to expand the bcrypt hash's '$' segments. The plaintext password is
	// preserved across regen (read from the existing .env) so a regen doesn't lock the
	// user out — only the hash re-derives.
	if e.ProtectAdminUIs && (cfg.HasAdminerEnv(e) || cfg.EffStorageUI(e)) {
		adminPass := ""
		if existing != nil {
			adminPass = existing["ADMIN_UI_PASSWORD"]
		}
		if adminPass == "" {
			adminPass = "rigger-" + hexN(r, 9)
		}
		hash, herr := bcrypt.GenerateFromPassword([]byte(adminPass), bcrypt.DefaultCost)
		if herr == nil {
			p("ADMIN_UI_USER=admin\n")
			p("ADMIN_UI_PASSWORD=%s\n", adminPass)
			p("ADMIN_UI_USERS='admin:%s'\n", string(hash))
		}
	}
	// App basic auth-gate credential (per env, when auth_gate=basic). Same htpasswd
	// shape as ADMIN_UI_USERS; APP_AUTH_USERS is the line Traefik's basicauth middleware
	// reads for the app's web router. Plaintext preserved across regen (so a regen
	// doesn't lock the user out — only the bcrypt hash re-derives).
	if cfg.EffAuthGate(e) == "basic" {
		appPass := ""
		if existing != nil {
			appPass = existing["APP_AUTH_PASSWORD"]
		}
		if appPass == "" {
			appPass = "rigger-" + hexN(r, 9)
		}
		if hash, herr := bcrypt.GenerateFromPassword([]byte(appPass), bcrypt.DefaultCost); herr == nil {
			p("APP_AUTH_USER=admin\n")
			p("APP_AUTH_PASSWORD=%s\n", appPass)
			p("APP_AUTH_USERS='admin:%s'\n", string(hash))
		}
	}
	p("\n")

	p("# ── Application ────────────────────────────────────────────\n")
	p("APP_ENV=%s\n", env)
	if env == "dev" {
		p("APP_DEBUG=true\n")
	} else {
		p("APP_DEBUG=false\n")
	}
	// APP_URL must be a valid absolute URI; an empty domain would yield the
	// malformed "http://" which crashes framework consoles (e.g. Laravel's
	// artisan throws "Invalid URI"). Fall back to localhost (+ the published
	// HTTP port when it isn't the default 80) so dev/local envs boot.
	if e.Domain != "" {
		p("APP_URL=http://%s\n", e.Domain)
	} else if hp := string(e.HTTPPort); hp != "" && hp != "80" {
		p("APP_URL=http://localhost:%s\n", hp)
	} else {
		p("APP_URL=http://localhost\n")
	}
	p("APP_KEY=%s\n\n", appKey)

	redisOn := cfg.EffRedis(e)
	p("# ── Redis ──────────────────────────────────────────────────\n")
	p("REDIS_ENABLED=%t\n", redisOn)
	if redisOn {
		p("REDIS_HOST=%s_redis\n", prefix)
		p("REDIS_PORT=6379\n")
		p("REDIS_PASSWORD=\n")
	}
	p("\n")

	// ── Object / file storage (local and/or minio — independent) ────────────────
	// local → FILESYSTEM_DISK=local (via the framework contract) + a persistent volume;
	// minio → managed MinIO S3 (the S3 access key/secret ARE the MinIO root creds). Both
	// may be on. Bucket name is lowercase+hyphens only — derive from the safe prefix (or
	// the override), env-suffixed. OBJECT_STORAGE is a human-readable summary of what's on.
	minioOn := cfg.MinIOOn(e)
	localOn := cfg.LocalStorageOn(e)
	bucketBase := strings.ReplaceAll(dbBase, "_", "-")
	if b := cfg.Project.StorageBucket; b != "" {
		bucketBase = strings.ReplaceAll(strings.ToLower(b), "_", "-")
	}
	minioBucket := bucketBase + "-" + env
	summary := "none"
	switch {
	case minioOn && localOn:
		summary = "local+minio"
	case minioOn:
		summary = "minio"
	case localOn:
		summary = "local"
	}
	p("# ── Object storage ─────────────────────────────────────────\n")
	p("OBJECT_STORAGE=%s\n", summary)
	if minioOn {
		p("MINIO_ROOT_USER=%s\n", minioUser)
		p("MINIO_ROOT_PASSWORD=%s\n", minioPassword)
		p("MINIO_BUCKET=%s\n", minioBucket)
		// Bare service name "minio" — mc/S3 SDKs reject underscore hostnames like
		// {prefix}_minio ("Invalid Request (invalid hostname)"). Unique per project net.
		p("MINIO_ENDPOINT=http://minio:9000\n")
		p("MINIO_REGION=us-east-1\n")
		if cfg.EffStorageUI(e) {
			p("MINIO_CONSOLE_PASSPHRASE=%s\n", minioConsolePass)
			p("MINIO_CONSOLE_SALT=%s\n", minioConsoleSalt)
		}
	}
	p("\n")

	// ── Framework env contract (blueprint-declared) ──────────────────────────
	// Each build service's blueprint declares the env keys its framework reads
	// (Laravel → DB_*, Rails → DATABASE_URL, Spring → SPRING_DATASOURCE_*, …).
	// Emit them from the active managed-dep facts so an app built from an
	// arbitrary scanned repo wires up to the db/redis without the user hand-
	// mapping Rigger's MYSQL_*/POSTGRES_* onto the framework's keys. The keys
	// stay language-specific in the blueprint; envgen stays generic.
	fe := frameworkEnv(cfg, e, prefix, dbBase, env, dbPassword, minioUser, minioPassword, minioBucket)
	if len(fe) > 0 {
		p("# ── Framework env contract (blueprint-declared) ────────────\n")
		keys := make([]string, 0, len(fe))
		for k := range fe {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// envQuote so a value with whitespace/# (e.g. a connection string) stays valid
			// when the .env is parsed strictly (phpdotenv via env_file_mount).
			p("%s=%s\n", k, envQuote(fe[k]))
		}
		p("\n")
	}

	// Baseline DATABASE_URL — a near-universal convention (Prisma, Rails, SQLAlchemy,
	// many Node/Go ORMs). Rigger claims DATABASE_URL as a managed key (so a repo's own
	// value is stripped), so it MUST also provide one or the key goes missing — e.g.
	// `prisma migrate deploy` then fails with "Environment variable not found:
	// DATABASE_URL". Compose it from the managed-DB creds, UNLESS a blueprint framework
	// already emitted its own (fe wins — its scheme/format may be framework-specific).
	if engine != "" && engine != "none" {
		if _, ok := fe["DATABASE_URL"]; !ok {
			if url := databaseURL(engine, prefix, dbBase, env, dbPassword); url != "" {
				p("# ── Database URL (baseline; %s) ─────────────────────────────\n", engine)
				p("DATABASE_URL=%s\n\n", envQuote(url))
			}
		}
	}

	// Mail. When the Mailpit test-SMTP sidecar is on for THIS env, point the app at it
	// (bare host "mailpit" — SMTP rejects underscore hostnames; reachable in-network on
	// :1025). Mailpit is a catch-all, so no auth/encryption. When off, leave MAIL_HOST
	// blank for the user to fill a real SMTP (and MAIL_* are NOT reserved, so the app's
	// own .env.example values win). Replaces the old hardcoded MAIL_HOST=mailhog.
	mailpitOn := cfg.EffMailpit(e)
	p("# ── Mail ───────────────────────────────────────────────────\n")
	p("MAIL_MAILER=smtp\n")
	p("MAIL_DRIVER=smtp\n") // legacy alias (Laravel <7)
	if mailpitOn {
		p("MAIL_HOST=mailpit\n")
		p("MAIL_PORT=1025\n")
		p("MAIL_ENCRYPTION=null\n")
	} else {
		p("MAIL_HOST=\n")
		p("MAIL_PORT=587\n")
	}
	p("MAIL_USERNAME=\n")
	p("MAIL_PASSWORD=\n")
	mailDomain := e.Domain
	if mailDomain == "" {
		mailDomain = "localhost"
	}
	p("MAIL_FROM_ADDRESS=noreply@%s\n", mailDomain)
	p("MAIL_FROM_NAME=%s\n\n", envQuote(project))

	p("# ── App listen port ────────────────────────────────────────\n")
	if env == "prod" {
		p("NODE_ENV=production\n")
	} else {
		p("NODE_ENV=development\n")
	}
	// PORT must match the port Traefik routes to (the web-routed service's port);
	// otherwise an app that honors $PORT listens somewhere Traefik can't reach
	// (502). Fall back to 3000 (the common Node default) when no web port is set.
	webPort := 3000
	if wp := cfg.WebPort(); wp > 0 {
		webPort = wp
	}
	p("PORT=%d\n", webPort)

	// Append extra env_vars from config, auto-resolving placeholder secrets (the
	// former image-stack secret generation) and preserving any existing values.
	//
	// CRITICAL: skip keys that duplicate Rigger's managed-infrastructure contract
	// (managed DB creds/host, redis, garage, the framework DB contract). A duplicate
	// key later in .env silently overrides the earlier one (last wins) — and a scanned
	// repo's .env.example routinely ships MYSQL_*/DB_* defaults (e.g. weather/weatherpass).
	// If those won, the managed DB container would initialize with the repo's creds while
	// the app connects with Rigger's → "Access denied". The managed contract MUST be
	// authoritative; app-level keys (MAIL_*, APP_*, feature flags) stay user-overridable.
	if len(e.EnvVars) > 0 {
		reserved := managedContractKeys(cfg, e, fe)
		keys := make([]string, 0, len(e.EnvVars))
		var skipped []string
		for k := range e.EnvVars {
			if reserved[k] {
				skipped = append(skipped, k)
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sort.Strings(skipped)
		p("\n# ── Extra variables (from config.json env_vars) ───────────────────\n")
		if len(skipped) > 0 {
			p("# (skipped %d key(s) owned by Rigger's managed services: %s)\n", len(skipped), strings.Join(skipped, ", "))
		}
		// In writable-.env mode the app owns its .env at runtime (e.g. an installer
		// writing INSTALLED=true), so a regen must PRESERVE the app's current value
		// for app-level keys rather than reset them to the config.json default.
		// Managed-infra keys are reserved above (skipped here) and re-asserted from
		// Rigger's values, so they stay authoritative even in this mode.
		writableEnv := cfg.HasWritableEnvFile()
		for _, k := range keys {
			// envQuote so a free-form value with whitespace/# (e.g. APP_NAME=Document
			// Management seeded from the app's .env.example) stays valid when the .env is
			// parsed strictly (phpdotenv via env_file_mount). docker compose & phpdotenv
			// both strip the surrounding quotes; plain values pass through unquoted.
			if writableEnv && existing != nil {
				if ev, ok := existing[k]; ok {
					p("%s=%s\n", k, envQuote(ev))
					continue
				}
			}
			p("%s=%s\n", k, envQuote(ResolveImageValue(k, e.EnvVars[k].String(), existing, r)))
		}
	}

	envOut := b.String()
	return envOut, maskExample(envOut), nil
}

// examplePlaceholder is the per-key placeholder used in .env.example for the
// structured secret keys; other secret keys mask to a generic CHANGE_ME.
var examplePlaceholder = map[string]string{
	"POSTGRES_PASSWORD":          "CHANGE_ME_DB_PASSWORD",
	"MYSQL_PASSWORD":             "CHANGE_ME_DB_PASSWORD",
	"MYSQL_ROOT_PASSWORD":        "CHANGE_ME_ROOT_PASSWORD",
	"DB_PASSWORD":                "CHANGE_ME_DB_PASSWORD",
	"SPRING_DATASOURCE_PASSWORD": "CHANGE_ME_DB_PASSWORD",
	"APP_KEY":                  "base64:CHANGE_ME",
	"MINIO_ROOT_PASSWORD":      "CHANGE_ME_MINIO_PASSWORD",
	"MINIO_CONSOLE_PASSPHRASE": "CHANGE_ME",
}

// urlCredRE matches the password in a URL userinfo (scheme://user:PASS@host).
var urlCredRE = regexp.MustCompile(`(://[^:/@\s]+:)[^@/\s]+(@)`)

// connStrPassRE matches a Password=… field in an ADO.NET connection string.
var connStrPassRE = regexp.MustCompile(`(?i)(Password=)[^;]+`)

// redactInlineSecrets replaces credentials embedded inside a value (URL userinfo
// passwords, connection-string passwords) with CHANGE_ME so the masked
// .env.example never leaks a real secret carried by a non-secret-named key.
func redactInlineSecrets(v string) string {
	v = urlCredRE.ReplaceAllString(v, "${1}CHANGE_ME${2}")
	v = connStrPassRE.ReplaceAllString(v, "${1}CHANGE_ME")
	return v
}

// maskExample produces the .env.example by masking secret VALUES key-by-key. This
// is collision-free, unlike value replacement (distinct keys can share a value
// when secrets are generated from a non-random source, e.g. in tests).
func maskExample(env string) string {
	var b strings.Builder
	for _, line := range strings.Split(env, "\n") {
		k, v, ok := strings.Cut(line, "=")
		key := strings.TrimSpace(k)
		switch {
		case !ok || strings.HasPrefix(strings.TrimSpace(line), "#"):
			b.WriteString(line)
		case examplePlaceholder[key] != "":
			b.WriteString(key + "=" + examplePlaceholder[key])
		case isSecretKey(key) && !IsPlaceholder(v):
			b.WriteString(key + "=CHANGE_ME")
		default:
			// Framework env values can embed credentials inline (e.g.
			// DATABASE_URL=mysql://user:pass@host, ConnectionStrings=…;Password=…;).
			// Redact those so .env.example never carries a real secret.
			b.WriteString(key + "=" + redactInlineSecrets(v))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// ParseEnv parses .env content into a key→value map, ignoring comments and blank
// lines. Used to load an existing .env so regeneration preserves secrets.
func ParseEnv(content []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = unquoteEnvValue(strings.TrimSpace(v))
	}
	return out
}

// unquoteEnvValue reverses envQuote: it strips ONE layer of surrounding double or
// single quotes, and for double quotes unescapes \" and \\ (in the reverse order
// envQuote applied them). This keeps preserved values stable across regen — without
// it, a value envgen wrote quoted (e.g. APP_NAME="Document Management") would be read
// back WITH its quotes and then re-quoted on the next regen ("\"Document Management\"").
// Plain unquoted values (the common case — secrets, hosts, ports) pass through unchanged.
func unquoteEnvValue(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		inner := v[1 : len(v)-1]
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		return inner
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1]
	}
	return v
}

// RebaseImageRegistry rewrites a .env's REGISTRY line and the "{registry}/" prefix
// of every "{SVC}_IMAGE" pointer to match `registry` (the project's configured
// registry; "" = local-only). Only the registry prefix changes — the local image
// portion ("{prefix}-{svc}:{tag}", including any pinned version) is preserved.
//
// The image pointers are DERIVED from the registry at generation time, but nothing
// else re-derives them when the project's registry config later changes. Without
// this, changing (or clearing) the registry was silently ignored: stale
// "{SVC}_IMAGE=oldregistry/…" values kept compose pulling/denying the wrong image.
// Called on every config save so a registry change always propagates. Returns the
// (possibly unchanged) content and whether anything changed.
func RebaseImageRegistry(content []byte, registry, prefix string) ([]byte, bool) {
	if len(content) == 0 || prefix == "" {
		return content, false
	}
	marker := prefix + "-"
	lines := strings.Split(string(content), "\n")
	changed := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		rawKey, val := line[:eq], line[eq+1:]
		key := strings.TrimSpace(rawKey)

		if key == "REGISTRY" {
			if nl := rawKey + "=" + registry; nl != line {
				lines[i] = nl
				changed = true
			}
			continue
		}
		if !strings.HasSuffix(key, "_IMAGE") {
			continue
		}
		idx := strings.Index(val, marker)
		if idx < 0 {
			continue // not one of our prefixed build-image pointers — leave it
		}
		local := val[idx:] // "{prefix}-{svc}:{tag}"
		nv := local
		if registry != "" {
			nv = registry + "/" + local
		}
		if nl := rawKey + "=" + nv; nl != line {
			lines[i] = nl
			changed = true
		}
	}
	return []byte(strings.Join(lines, "\n")), changed
}
