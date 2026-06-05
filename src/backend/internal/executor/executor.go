// Package executor abstracts how a `docker` command is run so the same call
// sites work either against the local daemon (today) or, in Phase 7, on a remote
// host over SSH. Every docker invocation in dockerops/backup/builder builds a
// Spec and runs it through an Executor; Local reproduces the previous
// exec.Command behavior exactly, and remotehost.Remote runs it over SSH.
package executor

import (
	"io"
	"os/exec"
)

// Spec describes one docker invocation. Args are the arguments after "docker".
type Spec struct {
	Args   []string
	Dir    string // working directory (e.g. the env dir so `compose` reads ./.env)
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
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
	cmd := exec.Command("docker", s.Args...) //nolint:gosec // args built from allowlisted commands
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
	cmd := exec.Command("docker", s.Args...) //nolint:gosec
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
