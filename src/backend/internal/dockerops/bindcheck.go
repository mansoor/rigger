package dockerops

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// envRelBind matches an env-dir-relative bind mount in the generated compose —
// "${RIGGER_BIND_ROOT:-.}/<relpath>:<container>" (composegen's form) or a bare
// "./<relpath>:<container>" — capturing the source relpath and the container target.
// Named volumes (prefix_data:/path) and absolute host paths don't match.
var envRelBind = regexp.MustCompile(`(?:\$\{RIGGER_BIND_ROOT:-\.\}|\.)/([^:\n]+):(/[^:\n]+)`)

// fileLike reports whether a path's final segment looks like a file (ends with a
// short dotted extension) — e.g. prometheus.yml, hwaccel.transcoding.yml, app.conf.
// Data directories (volumes/db_data, ml_cache) have no extension and are skipped.
var fileLike = regexp.MustCompile(`\.[A-Za-z0-9]{1,6}$`)

type missingBind struct {
	Source string // env-dir-relative source path (e.g. "prometheus.yml")
	Target string // container mount target (e.g. "/etc/prometheus/prometheus.yml")
}

// missingBindFiles scans the env's generated docker-compose.yml for bind mounts whose
// host source is absent AND looks like a file. Docker silently creates a missing bind
// source as a *directory*, so a container expecting a file there fails to start with a
// cryptic "not a directory" OCI error (and the app is left mounting an empty dir). This
// surfaces those up front so the deploy can stop with an actionable message instead.
//
// It checks the (local) env dir — the control plane generates and stages every file
// here before any local run or remote sync, so an absent required config file is absent
// at the daemon too. Returns nil when the compose file is unreadable (nothing to guard).
func missingBindFiles(envDir string) []missingBind {
	data, err := os.ReadFile(filepath.Join(envDir, "docker-compose.yml"))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []missingBind
	for _, m := range envRelBind.FindAllStringSubmatch(string(data), -1) {
		src, target := m[1], m[2]
		if !fileLike.MatchString(filepath.Base(src)) || seen[src] {
			continue
		}
		seen[src] = true
		if _, err := os.Stat(filepath.Join(envDir, filepath.FromSlash(src))); os.IsNotExist(err) {
			out = append(out, missingBind{Source: src, Target: target})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// checkBindFiles fails a deploy when the compose references config files that the app
// expects to be provided but don't exist yet (see missingBindFiles). The message lists
// each one so the user can create it before retrying.
func (r *runner) checkBindFiles() error {
	missing := missingBindFiles(r.envDir)
	if len(missing) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("cannot deploy: the compose file bind-mounts config files that don't exist yet —\n")
	b.WriteString("create them first (the app expects you to provide them), then deploy again:\n")
	for _, m := range missing {
		fmt.Fprintf(&b, "  • %s  →  %s\n", filepath.Join(filepath.Base(r.envDir), m.Source), m.Target)
	}
	b.WriteString("(a missing bind source is silently created by Docker as a directory, which breaks a file mount)")
	return fmt.Errorf("%s", b.String())
}
