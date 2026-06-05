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
	"sort"
	"strings"

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
	if cfg.ProjectType() == "image" {
		return generateImage(cfg, env, e, existing, r)
	}
	return generateCustom(cfg, env, e, existing, r)
}

func generateImage(cfg *wsconfig.Config, env string, e wsconfig.Env, existing map[string]string, r Rand) (string, string, error) {
	var b, ex strings.Builder
	header := fmt.Sprintf(""+
		"# ============================================================\n"+
		"# Auto-generated by Rigger — environment: %s | project: %s (type: image)\n"+
		"# Edit this file to update secrets. DO NOT COMMIT.\n"+
		"# Regenerate (resets values): re-bootstrap with regen-env\n"+
		"# ============================================================\n\n", env, cfg.Project.Name)
	b.WriteString(header)
	ex.WriteString(header)

	keys := make([]string, 0, len(e.EnvVars))
	for k := range e.EnvVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		raw := e.EnvVars[k].String()
		final := ResolveImageValue(k, raw, existing, r)
		fmt.Fprintf(&b, "%s=%s\n", k, final)

		// .env.example masks generated secret values.
		exVal := final
		if isSecretKey(k) && !IsPlaceholder(final) {
			exVal = "CHANGE_ME"
		}
		fmt.Fprintf(&ex, "%s=%s\n", k, exVal)
	}
	return b.String(), ex.String(), nil
}

func generateCustom(cfg *wsconfig.Config, env string, e wsconfig.Env, existing map[string]string, r Rand) (string, string, error) {
	project := cfg.Project.Name
	prefix := project + "_" + env
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
	p("PROJECT_NAME=%s\n", project)
	p("ENV=%s\n\n", env)

	p("# ── Image tags ─────────────────────────────────────────────\n")
	p("REGISTRY=%s\n", registry)
	p("IMAGE_TAG=%s\n", tag)
	p("BACKEND_IMAGE=%s/%s-backend:%s\n", registry, project, tag)
	if e.FrontendEnabled {
		p("FRONTEND_IMAGE=%s/%s-frontend:%s\n", registry, project, tag)
	}
	p("\n")

	p("# ── Domain & ports ─────────────────────────────────────────\n")
	p("DOMAIN=%s\n", e.Domain)
	p("HTTP_PORT=%s\n", e.HTTPPort)
	p("HTTPS_PORT=%s\n\n", e.HTTPSPort)

	p("# ── Stack config ────────────────────────────────────────────\n")
	p("BACKEND=%s\n", e.Backend)
	p("FRONTEND=%s\n", e.Frontend)
	p("FRONTEND_ENABLED=%t\n", e.FrontendEnabled)
	p("DEPLOYMENT=%s\n", e.Deployment)
	p("TRAEFIK_ENABLED=%t\n", e.TraefikEnabled)
	p("BACKEND_REPLICAS=%s\n", e.Replicas.Backend)
	p("FRONTEND_REPLICAS=%s\n\n", e.Replicas.Frontend)

	p("# ── Database ───────────────────────────────────────────────\n")
	p("DATABASE=%s\n", e.Database)
	switch e.Database {
	case "postgres":
		p("POSTGRES_HOST=%s_postgres\n", prefix)
		p("POSTGRES_PORT=5432\n")
		p("POSTGRES_DB=%s_%s\n", project, env)
		p("POSTGRES_USER=%s_user\n", project)
		p("POSTGRES_PASSWORD=%s\n", dbPassword)
	case "mysql":
		p("MYSQL_HOST=%s_mysql\n", prefix)
		p("MYSQL_PORT=3306\n")
		p("MYSQL_DATABASE=%s_%s\n", project, env)
		p("MYSQL_USER=%s_user\n", project)
		p("MYSQL_PASSWORD=%s\n", dbPassword)
		p("MYSQL_ROOT_PASSWORD=%s\n", dbRootPassword)
	}
	p("\n")

	p("# ── Application ────────────────────────────────────────────\n")
	p("APP_ENV=%s\n", env)
	if env == "dev" {
		p("APP_DEBUG=true\n")
	} else {
		p("APP_DEBUG=false\n")
	}
	p("APP_URL=http://%s\n", e.Domain)
	p("APP_KEY=%s\n\n", appKey)

	p("# ── Redis ──────────────────────────────────────────────────\n")
	p("REDIS_ENABLED=%t\n", e.RedisEnabled)
	if e.RedisEnabled {
		p("REDIS_HOST=%s_redis\n", prefix)
		p("REDIS_PORT=6379\n")
		p("REDIS_PASSWORD=\n")
	}
	p("\n")

	p("# ── Garage (S3-compatible storage) ─────────────────────────\n")
	p("GARAGE_ENABLED=%t\n", e.GarageEnabled)
	if e.GarageEnabled {
		p("GARAGE_HOST=%s_garage\n", prefix)
		p("GARAGE_API_PORT=3900\n")
		p("GARAGE_S3_PORT=3901\n")
		p("GARAGE_WEB_PORT=3903\n")
		p("GARAGE_ADMIN_TOKEN=%s\n", garageAdminToken)
		p("GARAGE_KEY_ID=%s\n", garageKeyID)
		p("GARAGE_SECRET_KEY=%s\n", garageSecretKey)
		p("GARAGE_BUCKET=%s-%s\n", project, env)
		p("GARAGE_ENDPOINT=http://%s_garage:3901\n", prefix)
	}
	p("\n")

	p("# ── Mail (fill in per-environment) ─────────────────────────\n")
	p("MAIL_DRIVER=smtp\n")
	p("MAIL_HOST=mailhog\n")
	p("MAIL_PORT=1025\n")
	p("MAIL_USERNAME=\n")
	p("MAIL_PASSWORD=\n")
	p("MAIL_FROM_ADDRESS=noreply@%s\n", e.Domain)
	p("MAIL_FROM_NAME=\"%s\"\n\n", project)

	p("# ── Node.js specific ───────────────────────────────────────\n")
	if env == "prod" {
		p("NODE_ENV=production\n")
	} else {
		p("NODE_ENV=development\n")
	}
	p("PORT=3000\n")

	// Append extra env_vars from config (wizard-collected).
	if len(e.EnvVars) > 0 {
		keys := make([]string, 0, len(e.EnvVars))
		for k := range e.EnvVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		p("\n# ── Extra variables (from config.json env_vars) ───────────────────\n")
		for _, k := range keys {
			p("%s=%s\n", k, e.EnvVars[k])
		}
	}

	envOut := b.String()

	// .env.example: mask real secrets.
	replacer := strings.NewReplacer(
		dbPassword, "CHANGE_ME_DB_PASSWORD",
		dbRootPassword, "CHANGE_ME_ROOT_PASSWORD",
		appKey, "base64:CHANGE_ME",
		garageAdminToken, "CHANGE_ME_GARAGE_TOKEN",
		garageKeyID, "CHANGE_ME_KEY_ID",
		garageSecretKey, "CHANGE_ME_SECRET_KEY",
	)
	exampleOut := replacer.Replace(envOut)
	return envOut, exampleOut, nil
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
