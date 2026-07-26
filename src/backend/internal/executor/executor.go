// Package executor abstracts how a `docker` command is run so the same call
// sites work either against the local daemon (today) or, in Phase 7, on a remote
// host over SSH. Every docker invocation in dockerops/backup/builder builds a
// Spec and runs it through an Executor; Local reproduces the previous
// exec.Command behavior exactly, and remotehost.Remote runs it over SSH.
package executor

import (
	"context"
	"io"
	"os/exec"
	"time"
)

// Spec describes one command invocation. Args are the arguments after the binary.
type Spec struct {
	Args []string
	// Bin is the executable to run; empty ⇒ "docker" (the historical default, so every
	// existing docker call site is unchanged). Set to e.g. "nixpacks" to run an
	// alternative build backend through the same local/remote abstraction.
	Bin     string
	Dir     string // working directory (e.g. the env dir so `compose` reads ./.env)
	Env     []string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Timeout time.Duration // when >0, the command is killed if it runs longer (e.g. a hung `docker stats`)
	// Context, when set, ties the command's lifetime to the caller's context:
	// cancelling it SIGKILLs the docker process. Pipelines stamp this (via
	// WithContext) so a Cancel request kills a hung build mid-flight. nil ⇒ the
	// command runs uncancellable (existing behavior).
	Context context.Context
}

// waitDelay bounds how long Wait lingers after a cancelled command's process
// group has been killed, waiting on the stdout/stderr copy goroutines.
const waitDelay = 5 * time.Second

// newCmd builds the exec.Cmd, applying Spec.Context and/or Spec.Timeout. A
// cancellable command is created whenever either is set; the returned cancel must
// be called by the caller (deferred) to release resources.
//
// Cancellable commands also get their own process group (see setProcGroup) so
// cancellation reaches everything the command spawned — `docker compose` runs the
// compose plugin as a subprocess, and killing only the parent orphans it.
func newCmd(s Spec) (*exec.Cmd, context.CancelFunc) {
	base := s.Context
	if base == nil {
		base = context.Background()
	}
	bin := BinOr(s.Bin)
	if s.Timeout > 0 {
		ctx, cancel := context.WithTimeout(base, s.Timeout)
		cmd := exec.CommandContext(ctx, bin, s.Args...) //nolint:gosec
		setProcGroup(cmd)
		return cmd, cancel
	}
	if s.Context != nil {
		cmd := exec.CommandContext(base, bin, s.Args...) //nolint:gosec
		setProcGroup(cmd)
		return cmd, func() {}
	}
	return exec.Command(bin, s.Args...), func() {} //nolint:gosec
}

// BinOr returns the Spec's binary, defaulting to "docker" when unset. Shared by the
// local and remote executors so both honor Spec.Bin identically.
func BinOr(bin string) string {
	if bin == "" {
		return "docker"
	}
	return bin
}

// Executor runs docker commands. Docker streams via Stdin/Stdout/Stderr;
// DockerOutput captures and returns stdout (do not set Spec.Stdout with it).
type Executor interface {
	Docker(Spec) error
	DockerOutput(Spec) ([]byte, error)
}

// Local runs docker against the local daemon — identical to the prior
// exec.Command("docker", …) usage across the codebase.
type Local struct{}

func (Local) Docker(s Spec) error {
	cmd, cancel := newCmd(s)
	defer cancel()
	cmd.Dir = s.Dir
	if s.Env != nil {
		cmd.Env = s.Env
	}
	cmd.Stdin = s.Stdin
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	return cmd.Run()
}

func (Local) DockerOutput(s Spec) ([]byte, error) {
	cmd, cancel := newCmd(s)
	defer cancel()
	cmd.Dir = s.Dir
	if s.Env != nil {
		cmd.Env = s.Env
	}
	cmd.Stdin = s.Stdin
	return cmd.Output()
}

// Default returns e, or Local{} when e is nil — so existing callers/tests that
// leave the executor unset keep running locally.
func Default(e Executor) Executor {
	if e == nil {
		return Local{}
	}
	return e
}

// ctxExecutor wraps an Executor so every command it runs carries ctx, unless the
// caller already set a per-Spec context. Because builder/dockerops/backup funnel
// all docker calls through Default(opts.Exec), injecting one of these as the
// Options.Exec propagates cancellation to every command of a pipeline stage
// without touching individual call sites.
type ctxExecutor struct {
	inner Executor
	ctx   context.Context
}

// WithContext binds e (defaulting to Local) to ctx so its commands are cancelled
// when ctx is. A nil ctx returns the executor unchanged.
func WithContext(e Executor, ctx context.Context) Executor {
	if ctx == nil {
		return Default(e)
	}
	return ctxExecutor{inner: Default(e), ctx: ctx}
}

func (c ctxExecutor) Docker(s Spec) error {
	if s.Context == nil {
		s.Context = c.ctx
	}
	return c.inner.Docker(s)
}

func (c ctxExecutor) DockerOutput(s Spec) ([]byte, error) {
	if s.Context == nil {
		s.Context = c.ctx
	}
	return c.inner.DockerOutput(s)
}

// RemoteDir forwards path translation when the wrapped executor is a remote one,
// so callers that type-assert for it (e.g. build-context sync) still work through
// the wrapper.
func (c ctxExecutor) RemoteDir(localDir string) string {
	if rd, ok := c.inner.(interface{ RemoteDir(string) string }); ok {
		return rd.RemoteDir(localDir)
	}
	return localDir
}
