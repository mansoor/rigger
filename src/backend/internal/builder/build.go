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
	ifChanged := false // --if-changed: skip the build when the source is unchanged
	noCache := false   // --no-cache: rebuild every layer (the "force" mode)
	for _, arg := range o.Extra {
		switch arg {
		case "--push":
			push = true
		case "--bump":
			bump = true
		case "--if-changed":
			ifChanged = true
		case "--no-cache":
			noCache = true
		case "major", "minor", "patch", "build":
			bumpPart = arg
		default:
			target = arg // "all" or a specific build-service name
		}
	}

	// Load config BEFORE any bump: needed for the source info AND the pre-bump
	// version (advancePointers must know which env pointers tracked the old version
	// to advance them vs leave pins). Tags are re-read after the bump below.
	cfg, err := o.loadConfig()
	if err != nil {
		return err
	}
	prevVer := cfg.VersionString()

	// Materialize the project's source into this env's _src once, then each build
	// service builds from a subdir of it. Source comes from a git clone OR an
	// uploaded archive; both wipe + repopulate _src (idempotent). A pure greenfield
	// project (no source) leaves srcDir empty and builds from its scaffolded context.
	// Done before the bump so the change-detection ref reflects the real source.
	srcDir := ""
	envDir := wspath.EnvDir(o.WorkspacesDir, o.Workspace, o.Project, o.Env)
	if repo := cfg.SourceRepo(); repo != "" {
		var serr error
		if srcDir, serr = gitsync.Sync(envDir, repo, cfg.Branch(o.Env), o.GitAuth, o.Stdout); serr != nil {
			return serr
		}
	} else if cfg.SourceKind() == "upload" {
		var serr error
		if srcDir, serr = srcarchive.ExtractToSrc(envDir, wspath.SourceArchive(o.WorkspacesDir, o.Workspace, o.Project), o.Stdout); serr != nil {
			return serr
		}
	}

	// "If changed" mode (whole-project builds only): skip the build — and the version
	// bump + pointer advance — when the source (+ build-service set) matches the last
	// successful build. A specific-service target always builds. NOTE: only the SOURCE
	// is tracked; a changed build-arg or Dockerfile is NOT detected — use Always/Force.
	ref := ""
	if ifChanged && target == "all" {
		ref = o.buildRef(cfg, srcDir)
		if ref != "" && readBuildRef(envDir) == ref {
			o.success("Source unchanged since last build — skipping (build mode: if changed)")
			return nil
		}
	}

	if bump {
		o.info("Bumping version (%s)...", bumpPart)
		if _, err := version.Bump(o.configPath(), bumpPart); err != nil {
			return err
		}
		// Reload so tags reflect the new version.
		if cfg, err = o.loadConfig(); err != nil {
			return err
		}
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

	for _, svc := range builds {
		if err := o.buildService(cfg, svc, srcDir, push, noCache); err != nil {
			return err
		}
	}

	// Advance the env's image pointers so the next deploy runs what we just built
	// (tracking services only; pins are left + reported). Best-effort: a failure
	// here shouldn't fail the build itself — the images are already built.
	if err := o.advancePointers(cfg, builds, prevVer); err != nil {
		o.info("⚠ built ok, but could not advance image pointers: %v", err)
	}

	// Record the source ref so the next "if changed" run can skip an unchanged
	// rebuild. Full builds only — a single-service target leaves the ref untouched
	// (it doesn't reflect the whole service set).
	if target == "all" {
		if ref == "" {
			ref = o.buildRef(cfg, srcDir)
		}
		if ref != "" {
			writeBuildRef(envDir, ref)
		}
	}
	return nil
}

// buildRef returns a stable signature of the CURRENT source plus the build-service
// set, used by the "if changed" build mode to skip a rebuild when nothing changed
// since the last successful build. Empty for greenfield / undetectable sources (⇒
// the caller never skips). Only the SOURCE is tracked — a changed build-arg or
// Dockerfile is NOT reflected, so those need Always or Force.
func (o Options) buildRef(cfg *wsconfig.Config, srcDir string) string {
	var sig string
	switch {
	case cfg.SourceRepo() != "" && srcDir != "":
		sig, _ = gitsync.HeadSHA(srcDir)
	case cfg.SourceKind() == "upload":
		sig = srcarchive.Stamp(wspath.SourceArchive(o.WorkspacesDir, o.Workspace, o.Project))
	}
	if sig == "" {
		return ""
	}
	names := make([]string, 0)
	for _, svc := range cfg.BuildServices() {
		names = append(names, svc.Name)
	}
	sort.Strings(names)
	return sig + "|" + strings.Join(names, ",")
}

func buildRefPath(envDir string) string { return filepath.Join(envDir, ".build-ref") }

func readBuildRef(envDir string) string {
	b, _ := os.ReadFile(buildRefPath(envDir))
	return strings.TrimSpace(string(b))
}

func writeBuildRef(envDir, ref string) { _ = os.WriteFile(buildRefPath(envDir), []byte(ref), 0o644) }

// buildService builds (and optionally pushes) one build service. When srcDir is
// set (the project has a source repo) the context is a subdir of the checkout and
// the repo's own Dockerfile is used; otherwise the scaffolded context dir is used.
func (o Options) buildService(cfg *wsconfig.Config, svc wsconfig.Service, srcDir string, push, noCache bool) error {
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
	ver := cfg.VersionString()
	imgTag := cfg.ImageTag(svc.Name, o.Env)

	if svc.Build.IsNixpacks() {
		// Nixpacks auto-detects the stack and builds an OCI image with NO Dockerfile —
		// so skip Dockerfile scaffolding + validation entirely. Build-time env is passed
		// via --env (Nixpacks' analog of --build-arg). It shells out to `docker build`,
		// so the image lands in the same daemon (local, or the env's remote build host)
		// and pushes/pointer-advances identically. Run in ctxDir (path arg ".") so the
		// remote executor translates the dir and builds the pushed context.
		o.info("Building %s image with Nixpacks (auto-detect): %s", svc.Name, imgTag)
		o.info("  ↳ Nixpacks pulls a Nix base image and runs a package-install layer — the FIRST build can take several minutes with little log output while that step runs (it's not stuck). Later builds are cached and fast. If it never progresses, check this host's connectivity to ghcr.io.")
		nargs := []string{"build", ".", "--name", imgTag,
			"--env", "BUILD_ENV=" + o.Env,
			"--env", "VERSION=" + ver,
		}
		for _, kv := range o.serviceBuildArgs(svc, ver) {
			nargs = append(nargs, "--env", kv)
		}
		if err := o.nixpacksRunInDir(ctxDir, nargs...); err != nil {
			return err
		}
	} else {
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

		o.info("Building %s image: %s", svc.Name, imgTag)

		// Run with the build context as the working dir and relative paths, so the
		// remote executor can translate the dir to the host and build against the
		// pushed context on the remote daemon (local behaviour is identical).
		//
		// Use BuildKit via `docker buildx build` (not the legacy builder) so modern
		// Dockerfiles work — RUN --mount=type=cache/secret/bind, heredocs, etc. The
		// legacy builder rejects `--mount` ("requires BuildKit"). buildx is an argv change
		// (no env var), so it applies identically to local and remote build hosts (the
		// remote executor only forwards args, not env). --load puts the result in the
		// target daemon's image store so `compose up` finds it — matching the classic
		// builder's behaviour, including the no-registry pull_policy:never local fallback.
		args := []string{"buildx", "build", "--load"}
		if noCache {
			args = append(args, "--no-cache") // force mode: rebuild every layer
		}
		args = append(args,
			"--build-arg", "BUILD_ENV="+o.Env,
			"--build-arg", "VERSION="+ver,
		)
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
				if url, ok := composegen.EnvRouteURL(data, o.Env, o.BaseDomain, o.AutoURLMode, o.AutoURLHost); ok {
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
		val := expand(svc.Build.Args[k])
		// Warn on a leftover ${...} after substitution: Rigger only expands ${ENV},
		// ${VERSION}, ${ROUTE_URL}. Shell-style defaults (${VAR:-default}) and arbitrary
		// ${OTHER} are NOT interpreted — docker build receives the literal string, which then
		// gets baked into the image (e.g. Next.js NEXT_PUBLIC_* in the browser bundle). This
		// silently ships a broken value, so surface it loudly in the build log.
		if o.Stdout != nil && strings.Contains(val, "${") {
			fmt.Fprintf(o.Stdout, "\033[33m⚠ build-arg %s contains an unresolved token and is passed to docker build literally: %q\n"+
				"   Rigger expands only ${ENV}, ${VERSION}, ${ROUTE_URL}; shell-style ${VAR:-default} is not supported. "+
				"Note ${ROUTE_URL} already includes the scheme (http[s]://), so don't prefix it.\033[0m\n", k, val)
		}
		out = append(out, k+"="+val)
	}
	return out
}
