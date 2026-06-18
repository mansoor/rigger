package builder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/deployhistory"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// advancePointers moves each just-built service's .env image pointer
// ({SVC}_IMAGE) to the new version tag so the next deploy runs what was built —
// but ONLY for services that were TRACKING (pointer empty or equal to the
// previous version's tag). A pointer set to anything else is a deliberate PIN
// (rollback or hand-edit) and is left untouched + reported, so a held-back
// version is never silently clobbered by a build. It then regenerates
// docker-compose.yml so the baked default agrees (compose reads the .env override
// live; swarm injects .env into the deploy env — both want the new tag). Only the
// services actually built (the filtered `builds` slice) are advanced.
func (o Options) advancePointers(cfg *wsconfig.Config, builds []wsconfig.Service, prevVer string) error {
	envDir := wspath.EnvDir(o.WorkspacesDir, o.Workspace, o.Project, o.Env)
	envPath := filepath.Join(envDir, ".env")
	dotenv := parseDotenv(envPath)

	reg, prefix := cfg.Project.Registry, cfg.Project.Prefix()
	updates := map[string]string{}
	var pinned []string
	for _, svc := range builds {
		key := deployhistory.OverrideKey(svc.Name)
		newTag := cfg.ImageTag(svc.Name, o.Env)
		// Must match wsconfig.ImageTag: omit the "{registry}/" prefix when there's no
		// registry (local-only build), else the prev pointer never matches and a
		// tracking pointer is wrongly treated as pinned.
		prevTag := fmt.Sprintf("%s-%s:%s-%s", prefix, svc.Name, prevVer, o.Env)
		if reg != "" {
			prevTag = reg + "/" + prevTag
		}
		switch cur := dotenv[key]; {
		case cur == "" || cur == prevTag:
			if cur != newTag {
				updates[key] = newTag // tracking → advance
			}
		case cur == newTag:
			// already current — nothing to do
		default:
			pinned = append(pinned, fmt.Sprintf("%s(%s)", svc.Name, cur)) // pinned → leave
		}
	}

	if len(updates) > 0 {
		if err := workspace.UpdateEnvVars(o.WorkspacesDir, o.Workspace, o.Project, o.Env, updates, nil, nil); err != nil {
			return fmt.Errorf("update .env image pointers: %w", err)
		}
	}

	// Regenerate compose from the (now-updated) .env so the baked default matches.
	cfgBytes, err := os.ReadFile(o.configPath())
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	envContent, _ := os.ReadFile(envPath)
	// Pass the FULL routing inputs (not just BaseDomain) so the regenerated compose
	// carries the same Traefik labels deploy/refresh would — otherwise a magic-DNS or
	// override-cert env gets a label-incomplete compose and 404s until a manual Refresh.
	content, err := composegen.GenerateRouted(cfgBytes, o.Env, composegen.RouteOpts{
		BaseDomain:   o.BaseDomain,
		AutoURLMode:  o.AutoURLMode,
		AutoURLHost:  o.AutoURLHost,
		DNSProvider:  o.DNSProvider,
		OverrideCert: o.OverrideCert,
		EnvFile:      string(envContent),
	})
	if err != nil {
		return fmt.Errorf("generate compose: %w", err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "docker-compose.yml"), content, 0o644); err != nil {
		return fmt.Errorf("write compose: %w", err)
	}

	if len(updates) > 0 {
		o.success("Advanced %d image pointer(s) → %s — Deploy/Update %q to roll out", len(updates), cfg.VersionString(), o.Env)
	} else {
		o.info("Image pointers already current for %q", o.Env)
	}
	if len(pinned) > 0 {
		o.info("Left pinned (not advanced): %s", strings.Join(pinned, ", "))
	}
	return nil
}

// parseDotenv reads a KEY=VALUE .env into a map (missing file → empty map). The
// image-tag values we compare are written unquoted by envgen/UpdateEnvVars, so no
// quote handling is needed.
func parseDotenv(path string) map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.IndexByte(line, '='); i > 0 {
			m[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
		}
	}
	return m
}
