// Package envorder resolves the deployment-tier ordering of a project's
// environments (dev → staging → prod). Environments are stored as an unordered
// map, but the release pipeline and the project UI need a stable promotion
// order. The effective order is:
//
//  1. an explicit per-project list (project.env_order), when set;
//  2. else an auto-guess from common tier names found in each env name;
//  3. else alphabetical.
//
// The package is pure (stdlib only) so the resolver stays trivially testable and
// importable from anywhere without risking a dependency cycle.
package envorder

import (
	"sort"
	"strings"
)

// DefaultTiers lists common environment tier substrings from lowest to highest.
// Used to auto-guess ordering when a project has no explicit order. Workspace
// admins can override the list via the env_tier_names setting (see SplitTierNames).
var DefaultTiers = []string{
	"local", "dev", "develop", "development", "sandbox", "test", "testing",
	"integration", "qa", "stage", "staging", "uat", "preprod", "pre-prod",
	"prod", "production", "live",
}

// SplitTierNames parses a workspace env_tier_names setting (comma- or
// newline-separated, lowest→highest) into a normalised slice. Empty/blank input
// returns DefaultTiers.
func SplitTierNames(s string) []string {
	if strings.TrimSpace(s) == "" {
		return DefaultTiers
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if t := strings.ToLower(strings.TrimSpace(f)); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return DefaultTiers
	}
	return out
}

// rank returns the index of the earliest tier substring contained in env (lower
// = earlier in the deploy chain), or len(tiers) when none match so unknown envs
// sort last. Because DefaultTiers lists more-specific tiers before their
// substrings (preprod before prod), an env like "preprod" ranks ahead of "prod".
func rank(env string, tiers []string) int {
	e := strings.ToLower(env)
	best := len(tiers)
	for i, t := range tiers {
		if t != "" && strings.Contains(e, t) && i < best {
			best = i
		}
	}
	return best
}

// Resolve returns env names in effective deploy order.
//
//   - explicit (non-empty): that order, filtered to names that still exist, with
//     any env missing from the list appended alphabetically so a newly-added env
//     is never dropped from the chain;
//   - otherwise: auto-guess by tier rank (tierNames, or DefaultTiers when empty),
//     ties broken alphabetically — which degrades to plain alphabetical when no
//     env name matches a known tier.
func Resolve(envs, explicit, tierNames []string) []string {
	exists := make(map[string]bool, len(envs))
	for _, e := range envs {
		exists[e] = true
	}

	if len(explicit) > 0 {
		out := make([]string, 0, len(envs))
		seen := make(map[string]bool, len(envs))
		for _, e := range explicit {
			if exists[e] && !seen[e] {
				out = append(out, e)
				seen[e] = true
			}
		}
		var rest []string
		for _, e := range envs {
			if !seen[e] {
				rest = append(rest, e)
			}
		}
		sort.Strings(rest)
		return append(out, rest...)
	}

	tiers := tierNames
	if len(tiers) == 0 {
		tiers = DefaultTiers
	}
	out := append([]string(nil), envs...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i], tiers), rank(out[j], tiers)
		if ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}
