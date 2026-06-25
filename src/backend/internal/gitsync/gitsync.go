// Package gitsync checks out a project's single source repository into an
// environment's source directory (envs/{env}/_src) so build services can build
// from the user's own repo. One repo per project; each build service's
// build.context is a subdir within it. git runs in the Rigger container; the
// build context is later file-synced to remote hosts by the deploy layer.
package gitsync

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// timeout bounds each git operation so a hung clone/fetch can't block forever.
const timeout = 3 * time.Minute

// Auth carries per-clone credentials for a private repository, applied via the git
// process ENVIRONMENT only — never via argv or the remote URL — so the secret can't
// leak into `ps` output or the streamed build log. internal/gitproviders builds it
// (a temp gitconfig with an http extraheader for tokens, or GIT_SSH_COMMAND pointing
// at a temp deploy-key file for SSH). Cleanup removes any temp files and must be
// called once after the git operations complete. A nil *Auth means no credentials
// (today's public-repo behavior — byte-identical).
type Auth struct {
	Env     []string // extra environment entries for the git child process
	Cleanup func()   // remove temp files; may be nil; safe to call once
}

// SrcDir returns the source checkout directory for an environment.
func SrcDir(envDir string) string { return filepath.Join(envDir, "_src") }

// Sync ensures repo@branch is checked out at SrcDir(envDir): a shallow clone on
// first run, else fetch + hard-reset to origin/branch. Returns the source dir.
// auth (may be nil) supplies private-repo credentials via the git child's env.
func Sync(envDir, repo, branch string, auth *Auth, out io.Writer) (string, error) {
	if out == nil {
		out = io.Discard
	}
	if repo == "" {
		return "", fmt.Errorf("no source repository configured")
	}
	if branch == "" {
		branch = "main"
	}
	src := SrcDir(envDir)
	run := func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Stdout, cmd.Stderr = out, out
		if auth != nil && len(auth.Env) > 0 {
			cmd.Env = append(os.Environ(), auth.Env...)
		}
		return cmd.Run()
	}
	if _, err := os.Stat(filepath.Join(src, ".git")); err == nil {
		fmt.Fprintf(out, "⟳ Updating source (%s @ %s)\n", repo, branch)
		if err := run("-C", src, "fetch", "--depth", "1", "origin", branch); err != nil {
			return "", fmt.Errorf("git fetch: %w", err)
		}
		if err := run("-C", src, "reset", "--hard", "FETCH_HEAD"); err != nil {
			return "", fmt.Errorf("git reset: %w", err)
		}
		return src, nil
	}
	fmt.Fprintf(out, "⟳ Cloning source (%s @ %s)\n", repo, branch)
	_ = os.RemoveAll(src)
	if err := run("clone", "--depth", "1", "--branch", branch, repo, src); err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}
	return src, nil
}

// HeadSHA returns the commit SHA currently checked out at src ("" + error if src
// isn't a git checkout). Used by the build "if changed" mode to detect whether the
// source moved since the last successful build.
func HeadSHA(src string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", src, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
