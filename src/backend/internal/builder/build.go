package builder

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mansoor/rigger/ui/internal/version"
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
	if target == "all" {
		if len(builds) == 0 {
			o.info("No build services for %q — nothing to build", o.Env)
			return nil
		}
		for _, svc := range builds {
			if err := o.buildImage(cfg, svc.Name, push); err != nil {
				return err
			}
		}
		return nil
	}
	for _, svc := range builds {
		if svc.Name == target {
			return o.buildImage(cfg, svc.Name, push)
		}
	}
	names := make([]string, 0, len(builds))
	for _, svc := range builds {
		names = append(names, svc.Name)
	}
	return fmt.Errorf("unknown build service %q (build services: %v)", target, names)
}

func (o Options) buildImage(cfg *wsconfig.Config, service string, push bool) error {
	ctxDir := filepath.Join(wspath.EnvDir(o.WorkspacesDir, o.Workspace, o.Project, o.Env), service)
	if fi, err := os.Stat(ctxDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("build context not found: %s (run init %s first)", ctxDir, o.Env)
	}
	if _, err := os.Stat(filepath.Join(ctxDir, "Dockerfile")); err != nil {
		return fmt.Errorf("Dockerfile not found: %s/Dockerfile", ctxDir)
	}

	ver := cfg.VersionString()
	imgTag := cfg.ImageTag(service, o.Env)
	o.info("Building %s image: %s", service, imgTag)

	// Run with the build context as the working dir and relative paths, so the
	// remote executor can translate the dir to the host and build against the
	// pushed context on the remote daemon (local behaviour is identical).
	if err := o.dockerRunInDir(ctxDir,
		"build",
		"--build-arg", "BUILD_ENV="+o.Env,
		"--build-arg", "VERSION="+ver,
		"--label", "project="+cfg.Project.Name,
		"--label", "environment="+o.Env,
		"--label", "version="+ver,
		"--label", "service="+service,
		"-t", imgTag,
		"-f", "Dockerfile",
		".",
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
