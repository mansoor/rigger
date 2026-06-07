// Package version ports scripts/version.sh: semantic version management stored
// in a workspace's config.json under .project.version {major,minor,patch,build}.
// It replaces the bash version command in the runtime path (Phase 6.5 finish).
package version

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"

	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Handles reports whether this package owns the given run command.
func Handles(cmd string) bool { return cmd == "version" || cmd == "ver" }

// Options mirrors the data the shell bridge passes through. For version, the
// bridge maps run.sh's ENV slot to Subcommand and the first Extra arg to Arg
// (run.sh.template: `version.sh "${ENV:-current}" "${EXTRA[@]}"`).
type Options struct {
	WorkspacesDir string
	Workspace     string // parent tier
	Project       string // project name
	Subcommand    string // current | bump | set  (default current)
	Arg           string // bump part, or the version string for set
	Stdout        io.Writer
}

var setRe = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)-build\.([0-9]+)$`)

// Run dispatches a version subcommand. It returns handled=true whenever the
// command is a version command (so the bridge never falls back to bash), with
// err carrying any failure.
func Run(opts Options) (bool, error) {
	cfgPath := wspath.ConfigPath(opts.WorkspacesDir, opts.Workspace, opts.Project)
	out := opts.Stdout
	if out == nil {
		out = io.Discard
	}

	sub := opts.Subcommand
	if sub == "" {
		sub = "current"
	}

	switch sub {
	case "current":
		v, err := Current(cfgPath)
		if err != nil {
			return true, err
		}
		fmt.Fprintln(out, v)
		return true, nil

	case "bump":
		part := opts.Arg
		if part == "" {
			part = "build"
		}
		v, err := Bump(cfgPath, part)
		if err != nil {
			return true, err
		}
		fmt.Fprintf(out, "Version bumped (%s): %s\n", part, v)
		return true, nil

	case "set":
		if opts.Arg == "" {
			return true, fmt.Errorf("usage: version set M.m.p-build.N")
		}
		v, err := Set(cfgPath, opts.Arg)
		if err != nil {
			return true, err
		}
		fmt.Fprintf(out, "Version set to: %s\n", v)
		return true, nil

	default:
		return true, fmt.Errorf("unknown version command %q (use: current | bump | set)", sub)
	}
}

// Current returns the version string from config.json.
func Current(configPath string) (string, error) {
	cfg, err := wsconfig.Load(configPath)
	if err != nil {
		return "", err
	}
	return cfg.VersionString(), nil
}

// Bump increments the given part (major|minor|patch|build), resetting lower
// parts to 0, and writes config.json. Returns the new version string.
func Bump(configPath, part string) (string, error) {
	cfg, err := wsconfig.Load(configPath)
	if err != nil {
		return "", err
	}
	v := cfg.Project.Version
	switch part {
	case "major":
		v.Major++
		v.Minor, v.Patch, v.Build = 0, 0, 0
	case "minor":
		v.Minor++
		v.Patch, v.Build = 0, 0
	case "patch":
		v.Patch++
		v.Build = 0
	case "build":
		v.Build++
	default:
		return "", fmt.Errorf("unknown version part %q (use: major | minor | patch | build)", part)
	}
	return writeVersion(configPath, v)
}

// Set parses an explicit "M.m.p-build.N" version and writes config.json.
func Set(configPath, ver string) (string, error) {
	m := setRe.FindStringSubmatch(ver)
	if m == nil {
		return "", fmt.Errorf("invalid version format %q (expected M.m.p-build.N)", ver)
	}
	v := wsconfig.Version{
		Major: mustAtoi(m[1]), Minor: mustAtoi(m[2]),
		Patch: mustAtoi(m[3]), Build: mustAtoi(m[4]),
	}
	return writeVersion(configPath, v)
}

// writeVersion sets .project.version on the on-disk config.json and rewrites it,
// preserving all other data. Keys are re-serialized via a generic map (semantic
// equivalence — values/behavior preserved; key order may be normalized).
func writeVersion(configPath string, v wsconfig.Version) (string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", err
	}
	proj, ok := doc["project"].(map[string]any)
	if !ok {
		proj = map[string]any{}
		doc["project"] = proj
	}
	proj["version"] = map[string]any{
		"major": v.Major, "minor": v.Minor, "patch": v.Patch, "build": v.Build,
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	out = append(out, '\n')
	if err := os.WriteFile(configPath, out, 0644); err != nil {
		return "", err
	}
	verStr := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." +
		strconv.Itoa(v.Patch) + "-build." + strconv.Itoa(v.Build)
	return verStr, nil
}

func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s) // setRe guarantees digits
	return n
}
