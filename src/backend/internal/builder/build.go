package builder

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mansoor/rigger/ui/internal/version"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
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
		case "backend", "frontend", "all":
			target = arg
		}
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

	buildImage := func(service string) error {
		return o.buildImage(cfg, service, push)
	}

	switch target {
	case "backend":
		return buildImage("backend")
	case "frontend":
		if !cfg.Environments[o.Env].FrontendEnabled {
			return fmt.Errorf("frontend is disabled for %q in config.json", o.Env)
		}
		return buildImage("frontend")
	case "all":
		if err := buildImage("backend"); err != nil {
			return err
		}
		if cfg.Environments[o.Env].FrontendEnabled {
			return buildImage("frontend")
		}
		o.info("Frontend disabled for %q — skipping", o.Env)
		return nil
	default:
		return fmt.Errorf("unknown build target %q (use: backend | frontend | all)", target)
	}
}

func (o Options) buildImage(cfg *wsconfig.Config, service string, push bool) error {
	ctxDir := filepath.Join(o.WorkspacesDir, o.Workspace, "envs", o.Env, service)
	if fi, err := os.Stat(ctxDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("build context not found: %s (run init %s first)", ctxDir, o.Env)
	}
	if _, err := os.Stat(filepath.Join(ctxDir, "Dockerfile")); err != nil {
		return fmt.Errorf("Dockerfile not found: %s/Dockerfile", ctxDir)
	}

	ver := cfg.VersionString()
	imgTag := cfg.ImageTag(service, o.Env)
	o.info("Building %s image: %s", service, imgTag)

	if err := o.dockerRun(
		"build",
		"--build-arg", "BUILD_ENV="+o.Env,
		"--build-arg", "VERSION="+ver,
		"--label", "project="+cfg.Project.Name,
		"--label", "environment="+o.Env,
		"--label", "version="+ver,
		"--label", "service="+service,
		"-t", imgTag,
		"-f", filepath.Join(ctxDir, "Dockerfile"),
		ctxDir,
	); err != nil {
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
