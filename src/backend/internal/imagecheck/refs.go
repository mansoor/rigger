package imagecheck

// Deciding which images an environment should be checked for.
//
// This used to read the project's own services[] list. That is the wrong source,
// because it describes what the OPERATOR declared, not what is running: managed
// dependencies and sidecars — a database, redis, object storage, a DB console,
// a mail catcher — are synthesised by composegen and never appear in it. For an
// app-plus-managed-deps project that meant one declared service, of which the
// only entry was the built app, and therefore nothing checkable at all. A project
// running eight images reported none.
//
// The generated compose file is the authority on what is actually deployed, so
// that is what gets read, through the same helper the update ACTION uses to
// decide what to pull. Sharing it is the point: an up-arrow that appears for a
// service the action then refuses to pull would be worse than no arrow.
//
// The project config remains a fallback for an environment whose compose has not
// been generated yet — a project created but never deployed still shows its
// declared images rather than a blank.

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/mansoor/rigger/ui/internal/composeimg"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// checkableRefs returns the images to check for one environment. ok is false only
// when nothing could be read at all — distinct from an environment that is
// readable and simply has nothing pullable, which is a real, cacheable answer.
func checkableRefs(workspacesDir, wsName, project, env string) (refs []composeimg.Ref, ok bool) {
	composePath := filepath.Join(wspath.EnvDir(workspacesDir, wsName, project, env), "docker-compose.yml")
	if data, err := os.ReadFile(composePath); err == nil {
		if refs, err := composeimg.Pullable(data); err == nil {
			return refs, true
		}
		// Compose exists but won't parse. Fall through to config rather than
		// reporting nothing: a stale or hand-edited compose shouldn't silently
		// disable update checking for the environment.
	}
	return configRefs(workspacesDir, wsName, project)
}

// configRefs reads the project's declared images — the fallback for an
// environment with no generated compose.
func configRefs(workspacesDir, wsName, project string) (refs []composeimg.Ref, ok bool) {
	data, err := os.ReadFile(wspath.ConfigPath(workspacesDir, wsName, project))
	if err != nil {
		return nil, false
	}
	type imgRef struct {
		Name  string `json:"name"`
		Image string `json:"image"`
		Tag   string `json:"tag"`
	}
	var cfg struct {
		Images   []imgRef `json:"images"`   // legacy image-stack model
		Services []imgRef `json:"services"` // unified service graph (current)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, false
	}
	// Image/prebuilt projects store their images under `services`; older configs
	// used `images`. Prefer services, fall back to images.
	declared := cfg.Services
	if len(declared) == 0 {
		declared = cfg.Images
	}
	out := []composeimg.Ref{}
	for _, d := range declared {
		// Reuse the same acceptance rule as compose so the two sources can't
		// disagree about what is checkable: no image, or a build pointer, means
		// the answer is Build rather than pull.
		ref := d.Image
		if d.Tag != "" {
			ref += ":" + d.Tag
		}
		image, tag, valid := composeimg.ParseRef(ref)
		if !valid {
			continue
		}
		out = append(out, composeimg.Ref{Service: d.Name, Image: image, Tag: tag})
	}
	return out, true
}
