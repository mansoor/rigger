package composegen

import (
	"sort"
	"strconv"
)

// Docker Swarm secret wiring (Phase 8). Emitted only for swarm deployments that
// have at least one flagged secret key; compose deployments are unaffected and
// keep their existing `env_file: .env` / `${VAR}` output byte-for-byte.

type secretEntry struct {
	Key  string // env-var name, e.g. POSTGRES_PASSWORD
	Name string // swarm secret name, e.g. myapp_prod_POSTGRES_PASSWORD_v1
}

// swarmSecrets returns the ordered secret entries for this env, or nil unless it
// is a swarm deployment with at least one flagged secret key.
func (g *gen) swarmSecrets() []secretEntry {
	if g.e.Deployment != "swarm" || len(g.e.SecretKeys) == 0 {
		return nil
	}
	keys := append([]string(nil), g.e.SecretKeys...)
	sort.Strings(keys)
	out := make([]secretEntry, 0, len(keys))
	for _, k := range keys {
		v := 1
		if n, ok := g.e.SecretVersions[k]; ok && n > 1 {
			v = n
		}
		out = append(out, secretEntry{Key: k, Name: secretFullName(g.cfg.resourcePrefix(), g.env, k, v)})
	}
	return out
}

// secretFullName mirrors dockerops.SecretName, kept local to avoid an import
// cycle: "{project}_{env}_{KEY}_v{N}".
func secretFullName(project, env, key string, version int) string {
	if version < 1 {
		version = 1
	}
	return project + "_" + env + "_" + key + "_v" + strconv.Itoa(version)
}

// emitTopLevelSecrets writes the top-level `secrets:` block (external secrets,
// pre-created by Rigger via `docker secret create`). No-op without swarm secrets.
func (g *gen) emitTopLevelSecrets() {
	secs := g.swarmSecrets()
	if len(secs) == 0 {
		return
	}
	g.line("secrets:")
	for _, s := range secs {
		g.line("  " + s.Name + ":")
		g.line("    external: true")
	}
	g.line("")
}

// emitServiceSecrets writes a service-level `secrets:` block mounting every swarm
// secret at its bare key path (/run/secrets/<KEY>), independent of version so the
// container path is stable across rotations. No-op without swarm secrets.
func (g *gen) emitServiceSecrets() {
	secs := g.swarmSecrets()
	if len(secs) == 0 {
		return
	}
	g.line("    secrets:")
	for _, s := range secs {
		g.line("      - source: " + s.Name)
		g.line("        target: " + s.Key)
	}
}

// isSecretKey reports whether key is a swarm secret in this env.
func (g *gen) isSecretKey(key string) bool {
	for _, s := range g.swarmSecrets() {
		if s.Key == key {
			return true
		}
	}
	return false
}

// dbEnvLine emits a database `environment:` entry (map form). When the key is a
// swarm secret it uses the `<KEY>_FILE: /run/secrets/<KEY>` convention (read by
// the official postgres/mysql/mariadb images); otherwise the usual
// `<KEY>: ${KEY}` — identical to the pre-Phase-8 output.
func (g *gen) dbEnvLine(key string) string {
	if g.isSecretKey(key) {
		return "      " + key + "_FILE: /run/secrets/" + key
	}
	return "      " + key + ": ${" + key + "}"
}
