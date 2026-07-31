package dockerops

// Which services an update can actually pull.
//
// `update` used to make this decision per STACK, from whether a registry was
// configured:
//
//	registry set    → pull everything
//	no registry     → pull nothing, just recreate
//
// The second case exists for a good reason. An image Rigger built locally has no
// registry prefix and lives only on the build daemon, so `compose pull` resolves
// it against Docker Hub and fails on a name that was never pushed anywhere.
//
// But a project that builds its own app usually runs things it did NOT build
// alongside it — mongo, rabbitmq, a proxy, a managed database — and those come
// from a registry like any other image. Skipping the pull for the whole stack
// meant Update on those quietly did nothing but recreate the same image, which
// is worse than refusing: it reports success and changes nothing.
//
// So the decision is per service, and it is read from the generated compose file
// rather than from project config. Compose is the authority on what is actually
// being run, and it covers services Rigger synthesises — managed dependencies and
// sidecars never appear in the project's own services[] list.

import (
	"os"

	yaml "go.yaml.in/yaml/v3"
)

// pullDoc is the minimal view needed to tell a pulled service from a built one.
type pullDoc struct {
	Services map[string]struct {
		Image string `yaml:"image"`
		// Decoded as `any` because compose accepts both the short string form
		// ("build: .") and the long mapping form. Its presence is all that
		// matters here, not its shape.
		Build any `yaml:"build"`
	} `yaml:"services"`
}

// pullableServices returns the compose services that can be pulled: those with an
// image and no build section.
//
// A service with BOTH is one Rigger builds and tags itself; the local build is
// the newer artefact, so pulling would replace it with whatever the registry has
// — which for a no-registry project is nothing at all.
func pullableServices(composeYAML []byte) (map[string]bool, error) {
	var doc pullDoc
	if err := yaml.Unmarshal(composeYAML, &doc); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(doc.Services))
	for name, svc := range doc.Services {
		if svc.Image != "" && svc.Build == nil {
			out[name] = true
		}
	}
	return out, nil
}

// pullTargets narrows an update's target list to what is worth pulling.
//
// target is the compose service names the update is acting on; empty means the
// whole stack. The result is the subset that comes from a registry, in the order
// given (or sorted, for a whole-stack update) so the emitted command is stable.
func pullTargets(target []string, pullable map[string]bool) []string {
	if len(target) > 0 {
		out := make([]string, 0, len(target))
		for _, name := range target {
			if pullable[name] {
				out = append(out, name)
			}
		}
		return out
	}
	out := make([]string, 0, len(pullable))
	for name := range pullable {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

// pullableTargets reads the env's generated compose file and returns which of the
// update's targets should be pulled. On any error it returns nothing, which falls
// back to the old behaviour of skipping the pull — a recreate that changes no
// image is a poor outcome, but a pull that fails on an unresolvable name aborts
// the whole update.
func (r *runner) pullableTargets(target []string) []string {
	data, err := os.ReadFile(r.composePath)
	if err != nil {
		return nil
	}
	pullable, err := pullableServices(data)
	if err != nil {
		return nil
	}
	return pullTargets(target, pullable)
}

// sortStrings is a tiny insertion sort — the lists here are a handful of service
// names, and this keeps the package free of another import for it.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
