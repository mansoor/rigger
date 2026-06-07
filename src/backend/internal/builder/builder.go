// Package builder ports scripts/build.sh and scripts/promote.sh: building and
// pushing a workspace's images, and promoting (retag + redeploy) an image
// between environments. It replaces those bash scripts in the runtime path
// (Phase 6.5 finish).
package builder

import (
	"fmt"
	"io"

	"github.com/mansoor/rigger/ui/internal/dockerops"
	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Handles reports whether this package owns the given run command.
func Handles(cmd string) bool { return cmd == "build" || cmd == "promote" }

// Options mirrors the data the shell bridge passes through.
//
//	build:   Env = target environment, Extra = [backend|frontend|all] [--push] [--bump [part]]
//	promote: Env = source environment, Extra = [dst_env] [--dry-run]
type Options struct {
	WorkspacesDir string
	Workspace     string // parent tier
	Project       string // project name
	Command       string
	Env           string
	Extra         []string
	EnvVars       []string // child-process environment
	Stdout        io.Writer
	Stderr        io.Writer

	// Exec runs the docker commands. nil → local daemon (Phase 7: remote over SSH).
	// Also the test seam (a fake executor records argv).
	Exec executor.Executor
	// RemoteWorkspacesDir is the build-context base on the remote host (Phase 7).
	RemoteWorkspacesDir string
	// deploy seam (nil → real dockerops); set by tests.
	deploy func(env string) error
}

// Run dispatches build/promote. The bool reports whether builder handled the
// command (always true for build/promote; false otherwise).
func (o Options) Run() (bool, error) {
	switch o.Command {
	case "build":
		return true, o.build()
	case "promote":
		return true, o.promote()
	}
	return false, nil
}

// Run is the package entrypoint used by the shell bridge.
func Run(o Options) (bool, error) { return o.Run() }

func (o Options) out() io.Writer {
	if o.Stdout != nil {
		return o.Stdout
	}
	return io.Discard
}

func (o Options) configPath() string {
	return wspath.ConfigPath(o.WorkspacesDir, o.Workspace, o.Project)
}

// dockerRun runs a docker command, streaming to the configured writers.
func (o Options) dockerRun(args ...string) error {
	return executor.Default(o.Exec).Docker(executor.Spec{
		Args: args, Env: o.EnvVars, Stdout: o.Stdout, Stderr: o.Stderr,
	})
}

// dockerRunInDir runs a docker command with the working directory set to dir, so
// relative paths (e.g. a build context) resolve there. For a remote executor the
// dir is translated to the host's path, so the build runs against the pushed
// context on the remote daemon.
func (o Options) dockerRunInDir(dir string, args ...string) error {
	return executor.Default(o.Exec).Docker(executor.Spec{
		Args: args, Dir: dir, Env: o.EnvVars, Stdout: o.Stdout, Stderr: o.Stderr,
	})
}

// SetDeploy overrides the deploy step used by promote. The shell bridge sets it
// to route the post-promote deploy through remote-aware execution; it is also a
// test seam.
func (o *Options) SetDeploy(fn func(env string) error) { o.deploy = fn }

// runDeploy brings up the stack for env (used by promote).
func (o Options) runDeploy(env string) error {
	if o.deploy != nil {
		return o.deploy(env)
	}
	_, err := dockerops.Run(dockerops.Options{
		WorkspacesDir: o.WorkspacesDir,
		Workspace:     o.Workspace,
		Project:       o.Project,
		Command:       "start",
		Env:           env,
		EnvVars:       o.EnvVars,
		Stdout:        o.Stdout,
		Stderr:        o.Stderr,
	})
	return err
}

func (o Options) info(format string, a ...any)    { fmt.Fprintf(o.out(), "⚑ "+format+"\n", a...) }
func (o Options) success(format string, a ...any) { fmt.Fprintf(o.out(), "✓ "+format+"\n", a...) }

func (o Options) loadConfig() (*wsconfig.Config, error) {
	return wsconfig.Load(o.configPath())
}
