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
	"regexp"
	"sort"
	"strings"

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

// frameworkEnv unions the blueprint-declared env contracts of every build
// service in the config, resolved against the active env's managed-dep facts
// (db engine/host/credentials, redis). Build services carry their framework via
// build.template (set by the detector or the blueprint picker); services without
// a recognised blueprint contribute nothing. On a key clash between two
// frameworks the first service's value wins.
func frameworkEnv(cfg *wsconfig.Config, e wsconfig.Env, prefix, dbBase, env, dbPassword string) map[string]string {
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
		for k, v := range bp.EnvVars(db, redis) {
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
	prefixUpper := strings.ToUpper(prefix)
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
	dbPassword := get(prefixUpper+"_DB_PASSWORD", "changeme_"+hexN(r, 8))
	dbRootPassword := get(prefixUpper+"_DB_ROOT_PASSWORD", "changeme_"+hexN(r, 8))
	appKey := get(prefixUpper+"_APP_KEY", "base64:"+base64N(r, 32))
	garageAdminToken := get(prefixUpper+"_GARAGE_ADMIN_TOKEN", hexN(r, 16))
	garageKeyID := get(prefixUpper+"_GARAGE_KEY_ID", hexN(r, 8))
	garageSecretKey := get(prefixUpper+"_GARAGE_SECRET_KEY", hexN(r, 32))

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

	p("# ── Domain & ports ─────────────────────────────────────────\n")
	p("DOMAIN=%s\n", e.Domain)
	p("HTTP_PORT=%s\n", e.HTTPPort)
	p("HTTPS_PORT=%s\n\n", e.HTTPSPort)

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
	}
	// When the DB is published externally, expose the host port (overridable) so the
	// generated compose's ${DB_EXTERNAL_PORT} resolves and the info tab can show it.
	// DBExternal is per-environment (expose on dev, keep prod private).
	if engine != "" && engine != "none" && e.DBExternal {
		port := "5432"
		if engine != "postgres" {
			port = "3306"
		}
		p("DB_EXTERNAL_PORT=%s\n", port)
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

	garageOn := cfg.EffGarage(e)
	p("# ── Garage (S3-compatible storage) ─────────────────────────\n")
	p("GARAGE_ENABLED=%t\n", garageOn)
	if garageOn {
		p("GARAGE_HOST=%s_garage\n", prefix)
		p("GARAGE_API_PORT=3900\n")
		p("GARAGE_S3_PORT=3901\n")
		p("GARAGE_WEB_PORT=3903\n")
		p("GARAGE_ADMIN_TOKEN=%s\n", garageAdminToken)
		p("GARAGE_KEY_ID=%s\n", garageKeyID)
		p("GARAGE_SECRET_KEY=%s\n", garageSecretKey)
		// S3 bucket names allow lowercase + hyphens only — derive from the safe
		// prefix, not the free-form display name.
		p("GARAGE_BUCKET=%s-%s\n", strings.ReplaceAll(dbBase, "_", "-"), env)
		p("GARAGE_ENDPOINT=http://%s_garage:3901\n", prefix)
	}
	p("\n")

	// ── Framework env contract (blueprint-declared) ──────────────────────────
	// Each build service's blueprint declares the env keys its framework reads
	// (Laravel → DB_*, Rails → DATABASE_URL, Spring → SPRING_DATASOURCE_*, …).
	// Emit them from the active managed-dep facts so an app built from an
	// arbitrary scanned repo wires up to the db/redis without the user hand-
	// mapping Rigger's MYSQL_*/POSTGRES_* onto the framework's keys. The keys
	// stay language-specific in the blueprint; envgen stays generic.
	if fe := frameworkEnv(cfg, e, prefix, dbBase, env, dbPassword); len(fe) > 0 {
		p("# ── Framework env contract (blueprint-declared) ────────────\n")
		keys := make([]string, 0, len(fe))
		for k := range fe {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p("%s=%s\n", k, fe[k])
		}
		p("\n")
	}

	p("# ── Mail (fill in per-environment) ─────────────────────────\n")
	p("MAIL_DRIVER=smtp\n")
	p("MAIL_HOST=mailhog\n")
	p("MAIL_PORT=1025\n")
	p("MAIL_USERNAME=\n")
	p("MAIL_PASSWORD=\n")
	mailDomain := e.Domain
	if mailDomain == "" {
		mailDomain = "localhost"
	}
	p("MAIL_FROM_ADDRESS=noreply@%s\n", mailDomain)
	p("MAIL_FROM_NAME=\"%s\"\n\n", project)

	p("# ── Node.js specific ───────────────────────────────────────\n")
	if env == "prod" {
		p("NODE_ENV=production\n")
	} else {
		p("NODE_ENV=development\n")
	}
	p("PORT=3000\n")

	// Append extra env_vars from config, auto-resolving placeholder secrets (the
	// former image-stack secret generation) and preserving any existing values.
	if len(e.EnvVars) > 0 {
		keys := make([]string, 0, len(e.EnvVars))
		for k := range e.EnvVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		p("\n# ── Extra variables (from config.json env_vars) ───────────────────\n")
		for _, k := range keys {
			p("%s=%s\n", k, ResolveImageValue(k, e.EnvVars[k].String(), existing, r))
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
	"APP_KEY":             "base64:CHANGE_ME",
	"GARAGE_ADMIN_TOKEN":  "CHANGE_ME_GARAGE_TOKEN",
	"GARAGE_KEY_ID":       "CHANGE_ME_KEY_ID",
	"GARAGE_SECRET_KEY":   "CHANGE_ME_SECRET_KEY",
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
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
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
