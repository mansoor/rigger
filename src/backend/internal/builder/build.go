package builder

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/gitsync"
	"github.com/mansoor/rigger/ui/internal/srcarchive"
	"github.com/mansoor/rigger/ui/internal/version"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// build ports scripts/build.sh: build (and optionally push) backend/frontend
// images for an environment, with an optional version bump first.
func (o Options) build() error {
	target := "all"
	push := false
	bump := false
	bumpPart := "build"
	for _, arg := range o.Extra {
		switch arg {
		case "--push":
			push = true
		case "--bump":
			bump = true
		case "major", "minor", "patch", "build":
			bumpPart = arg
		default:
			target = arg // "all" or a specific build-service name
		}
	}

	// Capture the version BEFORE any bump so advancePointers can tell which env
	// image pointers were tracking the old version (advance them) vs pinned to
	// something else (leave them).
	prevVer := ""
	if pc, perr := o.loadConfig(); perr == nil {
		prevVer = pc.VersionString()
	}

	if bump {
		o.info("Bumping version (%s)...", bumpPart)
		if _, err := version.Bump(o.configPath(), bumpPart); err != nil {
			return err
		}
	}

	// Load config AFTER the bump so tags reflect the new version.
	cfg, err := o.loadConfig()
	if err != nil {
		return err
	}
	if err := cfg.ValidateEnv(o.Env); err != nil {
		return err
	}

	builds := cfg.BuildServices()
	if target != "all" {
		var only []wsconfig.Service
		for _, svc := range builds {
			if svc.Name == target {
				only = append(only, svc)
			}
		}
		if len(only) == 0 {
			names := make([]string, 0, len(builds))
			for _, svc := range builds {
				names = append(names, svc.Name)
			}
			return fmt.Errorf("unknown build service %q (build services: %v)", target, names)
		}
		builds = only
	}
	if len(builds) == 0 {
		o.info("No build services for %q — nothing to build", o.Env)
		return nil
	}

	// Materialize the project's source into this env's _src once, then each build
	// service builds from a subdir of it. Source comes from a git clone OR an
	// uploaded archive; both wipe + repopulate _src (idempotent). A pure greenfield
	// project (no source) leaves srcDir empty and builds from its scaffolded context.
	srcDir := ""
	envDir := wspath.EnvDir(o.WorkspacesDir, o.Workspace, o.Project, o.Env)
	if repo := cfg.SourceRepo(); repo != "" {
		var serr error
		if srcDir, serr = gitsync.Sync(envDir, repo, cfg.Branch(o.Env), o.Stdout); serr != nil {
			return serr
		}
	} else if cfg.SourceKind() == "upload" {
		var serr error
		if srcDir, serr = srcarchive.ExtractToSrc(envDir, wspath.SourceArchive(o.WorkspacesDir, o.Workspace, o.Project), o.Stdout); serr != nil {
			return serr
		}
	}

	for _, svc := range builds {
		if err := o.buildService(cfg, svc, srcDir, push); err != nil {
			return err
		}
	}

	// Advance the env's image pointers so the next deploy runs what we just built
	// (tracking services only; pins are left + reported). Best-effort: a failure
	// here shouldn't fail the build itself — the images are already built.
	if err := o.advancePointers(cfg, builds, prevVer); err != nil {
		o.info("⚠ built ok, but could not advance image pointers: %v", err)
	}
	return nil
}

// buildService builds (and optionally pushes) one build service. When srcDir is
// set (the project has a source repo) the context is a subdir of the checkout and
// the repo's own Dockerfile is used; otherwise the scaffolded context dir is used.
func (o Options) buildService(cfg *wsconfig.Config, svc wsconfig.Service, srcDir string, push bool) error {
	dockerfile := "Dockerfile"
	if svc.Build != nil && svc.Build.Dockerfile != "" {
		dockerfile = svc.Build.Dockerfile
	}
	var ctxDir string
	if srcDir != "" {
		sub := "."
		if svc.Build != nil && svc.Build.Context != "" {
			sub = svc.Build.Context
		}
		ctxDir = filepath.Join(srcDir, sub)
	} else {
		ctxDir = filepath.Join(wspath.EnvDir(o.WorkspacesDir, o.Workspace, o.Project, o.Env), svc.ContextDir())
	}
	if fi, err := os.Stat(ctxDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("build context not found: %s (configure a source repo or run init)", ctxDir)
	}
	// Source-backed projects (git or upload) may ship source but NO Dockerfile (e.g. a
	// CodeCanyon Laravel app) — scaffold the service's blueprint Dockerfile into the
	// context. Re-scaffold (overwrite) a previously Rigger-generated one (marked by
	// .rigger-scaffolded) so template fixes apply even though _src isn't re-extracted
	// every build; an app's OWN Dockerfile (no marker) is never touched.
	dfPath := filepath.Join(ctxDir, dockerfile)
	_, dfErr := os.Stat(dfPath)
	_, markerErr := os.Stat(filepath.Join(ctxDir, ".rigger-scaffolded"))
	if (dfErr != nil || markerErr == nil) && srcDir != "" && svc.Build != nil && svc.Build.Template != "" && o.TemplatesDir != "" {
		o.info("Scaffolding the %s Dockerfile for %s", svc.Build.Template, svc.Name)
		if serr := workspace.ScaffoldDockerfile(o.TemplatesDir, svc.Build.Template, ctxDir, o.Env); serr != nil {
			return serr
		}
	}
	if _, err := os.Stat(dfPath); err != nil {
		return fmt.Errorf("%s not found in build context %s", dockerfile, ctxDir)
	}

	ver := cfg.VersionString()
	imgTag := cfg.ImageTag(svc.Name, o.Env)
	o.info("Building %s image: %s", svc.Name, imgTag)

	// Run with the build context as the working dir and relative paths, so the
	// remote executor can translate the dir to the host and build against the
	// pushed context on the remote daemon (local behaviour is identical).
	args := []string{
		"build",
		"--build-arg", "BUILD_ENV=" + o.Env,
		"--build-arg", "VERSION=" + ver,
	}
	for _, kv := range o.serviceBuildArgs(svc, ver) {
		args = append(args, "--build-arg", kv)
	}
	args = append(args,
		"--label", "project="+cfg.Project.Name,
		"--label", "environment="+o.Env,
		"--label", "version="+ver,
		"--label", "service="+svc.Name,
		"-t", imgTag,
		"-f", dockerfile,
		".",
	)
	if err := o.dockerRunInDir(ctxDir, args...); err != nil {
		return err
	}
	o.success("Built: %s", imgTag)

	if push {
		o.info("Pushing %s...", imgTag)
		if err := o.dockerRun("push", imgTag); err != nil {
			return err
		}
		o.success("Pushed: %s", imgTag)
	}
	return nil
}

// serviceBuildArgs resolves a build service's custom --build-arg values into
// sorted "KEY=VALUE" strings (sorted for deterministic argv). Values may embed
// build-time tokens, substituted here:
//
//	${ENV}       → the target environment name
//	${VERSION}   → the full version string (e.g. 1.2.3-build.4)
//	${ROUTE_URL} → the env's public Traefik route (e.g. https://app.example.com);
//	               empty when the env isn't web-routed
//
// ${ROUTE_URL} is the one that needs the config + base domain, so it's resolved
// lazily (only when actually referenced) to avoid an unnecessary file read.
func (o Options) serviceBuildArgs(svc wsconfig.Service, ver string) []string {
	if svc.Build == nil || len(svc.Build.Args) == 0 {
		return nil
	}
	keys := make([]string, 0, len(svc.Build.Args))
	for k := range svc.Build.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	routeURL, routeResolved := "", false
	resolveRoute := func() string {
		if !routeResolved {
			routeResolved = true
			if data, err := os.ReadFile(o.configPath()); err == nil {
				if url, ok := composegen.EnvRouteURL(data, o.Env, o.BaseDomain); ok {
					routeURL = url
				}
			}
		}
		return routeURL
	}

	expand := func(v string) string {
		v = strings.ReplaceAll(v, "${ENV}", o.Env)
		v = strings.ReplaceAll(v, "${VERSION}", ver)
		if strings.Contains(v, "${ROUTE_URL}") {
			v = strings.ReplaceAll(v, "${ROUTE_URL}", resolveRoute())
		}
		return v
	}

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+expand(svc.Build.Args[k]))
	}
	return out
}
