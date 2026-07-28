package dockerops

// Placement safety for multi-node Swarm clusters.
//
// Swarm schedules a service's tasks onto any eligible node, and moves them when a
// node drains or fails. That is exactly what you want for stateless services and
// exactly what silently destroys data for everything else, because:
//
//   - named volumes use Docker's `local` driver, so they exist only on the node
//     that first created them. A task rescheduled elsewhere comes up against a
//     brand-new empty volume — the database looks wiped, with no error anywhere.
//   - bind mounts point at the env directory, which Rigger syncs over SSH to the
//     one host bound to the environment. On any other node the path does not
//     exist, so Docker creates an empty directory and the mount is wrong.
//
// Neither is a Rigger limitation — it is how `local` volumes and bind mounts work
// — and neither can be fixed by deploying differently. The fix is a placement
// constraint pinning the service to the node holding its data, or a cluster-aware
// volume driver.
//
// So: before a Swarm deploy to a cluster with more than one node, look at what is
// about to be deployed and name anything that keeps state without being pinned.
// This warns; it never blocks. A single-node Swarm — by far the common case —
// says nothing at all, because nothing can be rescheduled anywhere.

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// placementRisk is one service that keeps state but isn't pinned to a node.
type placementRisk struct {
	Service string
	Reason  string // human-readable: what keeps it tied to a node
}

// composeDoc is the minimal view of a generated compose file this needs. Volumes
// are decoded as `any` because compose accepts both the short string form and the
// long mapping form; Rigger emits strings, and anything else is skipped rather
// than guessed at.
type composeDoc struct {
	Volumes  map[string]any `yaml:"volumes"`
	Services map[string]struct {
		Volumes []any `yaml:"volumes"`
		Deploy  struct {
			Placement struct {
				Constraints []string `yaml:"constraints"`
			} `yaml:"placement"`
		} `yaml:"deploy"`
	} `yaml:"services"`
}

// unpinnedStatefulServices returns the services in a generated compose file that
// would lose track of their data if Swarm moved them to another node.
func unpinnedStatefulServices(composeYAML []byte) ([]placementRisk, error) {
	var doc composeDoc
	if err := yaml.Unmarshal(composeYAML, &doc); err != nil {
		return nil, err
	}

	var risks []placementRisk
	for _, name := range sortedKeys(doc.Services) {
		svc := doc.Services[name]
		if len(svc.Deploy.Placement.Constraints) > 0 {
			continue // pinned — the operator has told Swarm where this belongs
		}
		for _, raw := range svc.Volumes {
			mount, ok := raw.(string)
			if !ok {
				continue // long-form mapping; not emitted by Rigger
			}
			source := mountSource(mount)
			if reason := volumeReason(source, doc.Volumes); reason != "" {
				risks = append(risks, placementRisk{Service: name, Reason: reason})
				break // one reason per service is enough to make the point
			}
		}
	}
	return risks, nil
}

// mountSource takes the source half of a `source:target[:opts]` mount.
//
// It can't just cut at the first colon: Rigger emits bind sources like
// `${RIGGER_BIND_ROOT:-.}/.env`, where the colon belongs to the shell-style
// default inside ${…}. Cutting there truncates the path mid-variable.
func mountSource(mount string) string {
	depth := 0
	for i := 0; i < len(mount); i++ {
		switch {
		case strings.HasPrefix(mount[i:], "${"):
			depth++
			i++ // skip the '{' too
		case mount[i] == '}' && depth > 0:
			depth--
		case mount[i] == ':' && depth == 0:
			if i == 0 {
				return mount // leading colon: malformed, hand it back whole
			}
			return mount[:i]
		}
	}
	return mount // no target half (or all inside ${…})
}

// volumeReason classifies a mount source, returning why it ties a service to one
// node — or "" when it doesn't (a tmpfs, or something unrecognised).
func volumeReason(source string, declared map[string]any) string {
	switch {
	case strings.HasPrefix(source, "/"), strings.HasPrefix(source, "."), strings.HasPrefix(source, "${"):
		// A host path. It only exists on the node Rigger synced the env dir to.
		return "bind mount " + source
	case declared != nil:
		if _, ok := declared[source]; ok {
			// A named volume, which uses the local driver unless told otherwise.
			return "named volume " + source
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Sort so the warning reads the same on every deploy.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// swarmNodeCount asks the target daemon how many nodes are in its cluster. It
// reads `docker info` rather than `docker node ls` because info works on any
// node, costs one call, and a count is all this needs.
func (s *swarmRunner) swarmNodeCount() (int, error) {
	out, err := s.dockerOutput("info", "--format", "{{.Swarm.Nodes}}")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// warnUnpinnedState prints a warning when this stack keeps data on nodes it has
// not been pinned to, and the cluster has somewhere else to put it. Entirely
// best-effort: anything that can't be determined is passed over in silence
// rather than blocking or guessing.
func (s *swarmRunner) warnUnpinnedState() {
	nodes, err := s.swarmNodeCount()
	if err != nil || nodes < 2 {
		return // single-node cluster (or unknown): nothing can be rescheduled away
	}
	// The local copy: compose is always generated here, then synced to the host.
	content, err := os.ReadFile(s.composePath)
	if err != nil {
		return
	}
	risks, err := unpinnedStatefulServices(content)
	if err != nil || len(risks) == 0 {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "⚠ This Swarm has %d nodes, and these services keep data on whichever node runs them:\n", nodes)
	for _, r := range risks {
		fmt.Fprintf(&b, "    • %s — %s\n", r.Service, r.Reason)
	}
	b.WriteString("  If Swarm reschedules one, it starts against an empty volume on the new node.\n")
	b.WriteString("  Pin them to a node with a placement constraint (Edit Project → Environments →\n")
	b.WriteString("  Swarm settings), e.g. node.hostname==<node>, or move the data onto a\n")
	b.WriteString("  cluster-aware volume driver.")
	s.info("%s", b.String())
}
