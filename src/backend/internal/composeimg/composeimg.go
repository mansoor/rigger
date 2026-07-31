// Package composeimg reads image references out of a generated compose file and
// says which ones come from a registry.
//
// Shared because two subsystems need the same answer and must not disagree: the
// update CHECK (does this service have a newer digest?) and the update ACTION
// (which services should `compose pull` be given?). A button that appears for a
// service the action then refuses to pull is worse than no button.
//
// The distinction that matters is Rigger-built vs. pulled, and in a generated
// compose file it looks like this:
//
//	app:      image: ${APP_IMAGE:-localhost:5001/mcl_sdm-app:1.0.0-build.7-dev}
//	mysql:    image: mysql:8.0
//
// Rigger does not build through compose. It builds out of band and records the
// result in the env's {SVC}_IMAGE pointer, so a built service always reaches
// compose as a variable reference and anything pulled is a literal one. Testing
// for a `build:` section — the obvious rule — matches nothing at all, because no
// generated service has one.
package composeimg

import (
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// Ref is one pullable service: the compose service name and its image split into
// repository and tag.
type Ref struct {
	Service string
	Image   string
	Tag     string
}

type composeDoc struct {
	Services map[string]struct {
		Image string `yaml:"image"`
		// Present for completeness: compose accepts a build section and a future
		// generated file might use one. Its shape varies (string or mapping), so
		// only its presence is read.
		Build any `yaml:"build"`
	} `yaml:"services"`
}

// Pullable returns the services in a generated compose file that can be pulled
// from a registry, sorted by service name so callers emit stable commands.
func Pullable(composeYAML []byte) ([]Ref, error) {
	var doc composeDoc
	if err := yaml.Unmarshal(composeYAML, &doc); err != nil {
		return nil, err
	}
	var out []Ref
	for name, svc := range doc.Services {
		if svc.Build != nil {
			continue
		}
		image, tag, ok := ParseRef(svc.Image)
		if !ok {
			continue
		}
		out = append(out, Ref{Service: name, Image: image, Tag: tag})
	}
	sortRefs(out)
	return out, nil
}

// ParseRef splits an image reference into repository and tag, reporting false for
// anything that can't be checked or pulled as a moving tag.
//
// Rejected:
//   - "" — no image at all
//   - a reference containing ${…} — a Rigger-built pointer, whose newer version
//     comes from Build, not from a registry
//   - a digest pin (…@sha256:…) — pinned by definition; it cannot drift, so
//     "update available" would never be true and pulling changes nothing
func ParseRef(ref string) (image, tag string, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.Contains(ref, "${") || strings.Contains(ref, "@") {
		return "", "", false
	}
	// Split on the LAST colon, and only when what follows is a tag rather than a
	// port: "localhost:5001/foo" has a colon in the registry host, not a tag.
	if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i+1:], "/") {
		image, tag = ref[:i], ref[i+1:]
	} else {
		image, tag = ref, ""
	}
	if image == "" {
		return "", "", false
	}
	if tag == "" {
		// Compose's own default, and what `docker pull` would resolve to.
		tag = "latest"
	}
	return image, tag, true
}

// sortRefs orders by service name — a handful of entries, so an insertion sort
// keeps the package free of another import.
func sortRefs(r []Ref) {
	for i := 1; i < len(r); i++ {
		for j := i; j > 0 && r[j].Service < r[j-1].Service; j-- {
			r[j], r[j-1] = r[j-1], r[j]
		}
	}
}
