package dockerops

// Which services an update can actually pull.
//
// `update` used to make this decision per STACK, from whether a registry was
// configured:
//
//	registry set    → pull everything
//	no registry     → pull nothing, just recreate
//
// The second case exists for a good reason. An image Rigger built has no registry
// prefix when there is nowhere to push it, and lives only on the build daemon, so
// `compose pull` resolves it against Docker Hub and fails on a name that was
// never pushed anywhere.
//
// But a project that builds its own app usually runs things it did NOT build
// alongside it — mysql, redis, minio, adminer, mailpit — and those come from a
// registry like any other image. Skipping the pull for the whole stack meant
// Update on those quietly did nothing but recreate the same image, which is worse
// than refusing: it reports success and changes nothing.
//
// So the decision is per service, read from the generated compose file rather
// than project config. Compose is the authority on what is actually being run,
// and it covers the services Rigger synthesises — managed dependencies and
// sidecars never appear in the project's own services[] list. See
// internal/composeimg for how a built service is told from a pulled one.

import (
	"os"

	"github.com/mansoor/rigger/ui/internal/composeimg"
)

// pullTargets narrows an update's target list to what is worth pulling.
//
// target is the compose service names the update is acting on; empty means the
// whole stack. The result preserves the caller's order (or compose's sorted
// order for a whole-stack update) so the emitted command is stable.
func pullTargets(target []string, pullable []composeimg.Ref) []string {
	set := make(map[string]bool, len(pullable))
	for _, r := range pullable {
		set[r.Service] = true
	}
	if len(target) > 0 {
		out := make([]string, 0, len(target))
		for _, name := range target {
			if set[name] {
				out = append(out, name)
			}
		}
		return out
	}
	out := make([]string, 0, len(pullable))
	for _, r := range pullable {
		out = append(out, r.Service)
	}
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
	pullable, err := composeimg.Pullable(data)
	if err != nil {
		return nil
	}
	return pullTargets(target, pullable)
}
