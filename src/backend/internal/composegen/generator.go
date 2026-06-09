package composegen

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Generate produces docker-compose.yml content for one environment from
// config.json bytes. Byte-for-byte replacement for scripts/compose-gen.sh.
func Generate(configJSON []byte, env string) ([]byte, error) {
	return GenerateAt(configJSON, env, time.Now().UTC())
}

// GenerateAt is Generate with an injectable timestamp (for tests / determinism).
func GenerateAt(configJSON []byte, env string, now time.Time) ([]byte, error) {
	cfg, err := parseConfig(configJSON)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	e, ok := cfg.Environments[env]
	if !ok {
		return nil, fmt.Errorf("unknown environment %q", env)
	}
	g := &gen{cfg: cfg, env: env, e: e, now: now}
	g.build()
	return []byte(g.b.String()), nil
}

type gen struct {
	cfg *Config
	env string
	e   Env
	now time.Time
	b   strings.Builder
}

// line appends s followed by a newline (echo "s").
func (g *gen) line(s string) { g.b.WriteString(s); g.b.WriteByte('\n') }

// raw appends s verbatim (already contains its own newlines).
func (g *gen) raw(s string) { g.b.WriteString(s) }

func (g *gen) build() {
	c := g.cfg
	e := g.e
	registry := c.Project.Registry
	ver := c.versionString()
	tag := ver + "-" + g.env
	// All Docker resource names derive from the immutable resource prefix
	// ({workspace}_{project}), NOT the editable display name, so names stay
	// globally unique when project display names repeat across workspaces.
	rp := c.resourcePrefix()
	prefix := rp + "_" + g.env
	isSwarm := e.Deployment == "swarm"

	// ── Header ──
	sep := "# " + strings.Repeat("=", 60)
	g.line(sep)
	g.line("# docker-compose.yml — " + g.env + " environment")
	g.line("# Project : " + c.Project.Name)
	g.line("# Version : " + ver)
	g.line("# Generated: " + g.now.Format("2006-01-02 15:04:05") + " UTC")
	g.line("# Regenerate: ./run.sh refresh " + g.env)
	g.line(sep)
	g.line("")

	// ── Networks ──
	// Swarm services require a swarm-scoped network; bridge is local-only and is
	// rejected by `docker stack deploy`.
	g.line("networks:")
	g.line("  " + prefix + "_net:")
	if isSwarm {
		g.line("    driver: overlay")
	} else {
		g.line("    driver: bridge")
	}
	if e.TraefikEnabled {
		g.line("  " + e.TraefikNetwork + ":")
		g.line("    external: true")
	}
	g.line("")

	// ── Secrets (swarm only) ──
	g.emitTopLevelSecrets()

	// ── Services (unified graph) + managed dependencies ──
	// rp is the image-name base so pushed tags (registry/<prefix>-<service>) stay
	// globally unique across workspaces.
	g.buildStack(prefix, rp, registry, tag, isSwarm)
}

// ── Shared emit helpers (mirror lib.sh / compose-gen.sh helpers) ─────────────────

// deployBlock emits the per-service deploy section. For compose it's a single
// `restart:`. For swarm it emits replicas + (optional) placement + restart_policy +
// update_config + (optional) rollback_config, sourced from the env's SwarmConfig
// (g.e.Swarm) with a per-service override for replicas/placement. When no SwarmConfig
// is set the output is byte-identical to the historical hardcoded block.
func (g *gen) deployBlock(isSwarm bool, svc, replicas, restart string) {
	if replicas == "" {
		replicas = "1"
	}
	if restart == "" {
		restart = "unless-stopped"
	}
	if !isSwarm {
		g.line("    restart: " + restart)
		g.emitServiceSecrets()
		return
	}

	sw := g.e.Swarm
	var placement []string
	if so, ok := sw.Services[svc]; ok {
		if r := string(so.Replicas); r != "" {
			replicas = r
		}
		placement = so.Placement
	}

	g.raw("    deploy:\n      replicas: " + replicas + "\n")
	if len(placement) > 0 {
		g.raw("      placement:\n        constraints:\n")
		for _, c := range placement {
			g.raw("          - " + c + "\n")
		}
	}

	// restart_policy (defaults match the historical block).
	cond, delay, maxAtt, window := "on-failure", "5s", "3", ""
	if rp := sw.RestartPolicy; rp != nil {
		if rp.Condition != "" {
			cond = rp.Condition
		}
		if rp.Delay != "" {
			delay = rp.Delay
		}
		if r := string(rp.MaxAttempts); r != "" {
			maxAtt = r
		}
		window = rp.Window
	}
	g.raw("      restart_policy:\n        condition: " + cond + "\n        delay: " + delay + "\n        max_attempts: " + maxAtt + "\n")
	if window != "" {
		g.raw("        window: " + window + "\n")
	}

	// update_config (defaults match the historical block).
	par, udelay, order, fail := "1", "10s", "", "rollback"
	if uc := sw.UpdateConfig; uc != nil {
		if r := string(uc.Parallelism); r != "" {
			par = r
		}
		if uc.Delay != "" {
			udelay = uc.Delay
		}
		order = uc.Order
		if uc.FailureAction != "" {
			fail = uc.FailureAction
		}
	}
	g.raw("      update_config:\n        parallelism: " + par + "\n        delay: " + udelay + "\n")
	if order != "" {
		g.raw("        order: " + order + "\n")
	}
	g.raw("        failure_action: " + fail + "\n")

	// rollback_config — only when explicitly configured (omitted by default).
	if rc := sw.RollbackConfig; rc != nil {
		rpar, rdelay := "1", "0s"
		if r := string(rc.Parallelism); r != "" {
			rpar = r
		}
		if rc.Delay != "" {
			rdelay = rc.Delay
		}
		g.raw("      rollback_config:\n        parallelism: " + rpar + "\n        delay: " + rdelay + "\n")
		if rc.Order != "" {
			g.raw("        order: " + rc.Order + "\n")
		}
	}

	g.emitServiceSecrets()
}

func (g *gen) traefikLabels(router, host, port string) {
	if !g.e.TraefikEnabled {
		return
	}
	if port == "" {
		port = "80"
	}
	g.line("    labels:")
	g.line("      - \"traefik.enable=true\"")
	g.line("      - \"traefik.http.routers." + router + ".rule=Host(`" + host + "`)\"")
	if g.e.SSLEnabled {
		g.line("      - \"traefik.http.routers." + router + ".entrypoints=websecure\"")
		g.line("      - \"traefik.http.routers." + router + ".tls=true\"")
		g.line("      - \"traefik.http.routers." + router + ".tls.certresolver=letsencrypt\"")
		g.line("      - \"traefik.http.services." + router + ".loadbalancer.server.port=" + port + "\"")
	} else {
		g.line("      - \"traefik.http.routers." + router + ".entrypoints=web\"")
		g.line("      - \"traefik.http.services." + router + ".loadbalancer.server.port=" + port + "\"")
	}
}

func (g *gen) portMapping(hostPort, containerPort string) {
	g.line("    ports:")
	g.line("      - \"" + hostPort + ":" + containerPort + "\"")
}

// healthcheck mirrors healthcheck_block: defaults interval 30s, timeout 10s,
// retries 3, start_period 30s; start_interval emitted only when non-empty.
func (g *gen) healthcheck(cmd, interval, timeout, retries, startPeriod, startInterval string) {
	if interval == "" {
		interval = "30s"
	}
	if timeout == "" {
		timeout = "10s"
	}
	if retries == "" {
		retries = "3"
	}
	if startPeriod == "" {
		startPeriod = "30s"
	}
	safe := strings.ReplaceAll(cmd, "\"", "\\\"")
	g.raw("    healthcheck:\n" +
		"      test: [\"CMD-SHELL\", \"" + safe + "\"]\n" +
		"      interval: " + interval + "\n" +
		"      timeout: " + timeout + "\n" +
		"      retries: " + retries + "\n" +
		"      start_period: " + startPeriod + "\n")
	if startInterval != "" {
		g.line("      start_interval: " + startInterval)
	}
}

// sectionComment emits a "  # ── <label> <N dashes>" service separator. The
// trailing dash counts are fixed per service type to match compose-gen.sh's
// hand-written heredocs exactly (they are NOT padded to a constant width).
func sectionComment(label string, dashes int) string {
	return "  # ── " + label + " " + strings.Repeat("─", dashes)
}

// emitExtraCompose appends raw YAML, indenting every line by 4 spaces (sed 's/^/    /').
func (g *gen) emitExtraCompose(raw string) {
	if raw == "" || raw == "null" || raw == "empty" {
		return
	}
	for _, ln := range strings.Split(raw, "\n") {
		g.line("    " + ln)
	}
}

// sortedKeys returns map keys sorted ascending (matches jq `keys[]`).
func sortedKeys(m map[string]flexStr) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
