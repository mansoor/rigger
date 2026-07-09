// Package gitsync checks out a project's single source repository into an
// environment's source directory (envs/{env}/_src) so build services can build
// from the user's own repo. One repo per project; each build service's
// build.context is a subdir within it. git runs in the Rigger container; the
// build context is later file-synced to remote hosts by the deploy layer.
package gitsync

import (
	"bytes"
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
	// A blank branch means "the repository's default" — resolve it from the remote's HEAD
	// symref rather than assuming "main" (repos also use master / develop / trunk). If it
	// can't be resolved (auth/network), branch stays "" and git picks the default at clone.
	if branch == "" {
		branch = resolveDefaultBranch(repo, auth)
	}
	src := SrcDir(envDir)
	run := func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		// Stream to the caller's log AND capture the output so a failure can say WHY.
		var captured bytes.Buffer
		w := io.MultiWriter(out, &captured)
		cmd.Stdout, cmd.Stderr = w, w
		// Run git non-interactively: without GIT_TERMINAL_PROMPT=0 a private repo with
		// no/bad credentials makes git block on a username/password prompt until the
		// timeout (the request then dies with no useful message). With it, git fails
		// immediately with a readable "could not read Username …" we can classify.
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if auth != nil && len(auth.Env) > 0 {
			cmd.Env = append(cmd.Env, auth.Env...)
		}
		if err := cmd.Run(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("timed out after %s (the repository may be very large or the host unreachable)", timeout)
			}
			return ClassifyError(captured.String())
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(src, ".git")); err == nil {
		fmt.Fprintf(out, "⟳ Updating source (%s @ %s)\n", repo, branchLabel(branch))
		fetchArgs := []string{"-C", src, "fetch", "--depth", "1", "origin"}
		if branch != "" {
			fetchArgs = append(fetchArgs, branch)
		}
		if err := run(fetchArgs...); err != nil {
			return "", err
		}
		if err := run("-C", src, "reset", "--hard", "FETCH_HEAD"); err != nil {
			return "", err
		}
		return src, nil
	}
	fmt.Fprintf(out, "⟳ Cloning source (%s @ %s)\n", repo, branchLabel(branch))
	_ = os.RemoveAll(src)
	cloneArgs := []string{"clone", "--depth", "1"}
	if branch != "" {
		cloneArgs = append(cloneArgs, "--branch", branch)
	}
	cloneArgs = append(cloneArgs, repo, src)
	if err := run(cloneArgs...); err != nil {
		return "", err
	}
	return src, nil
}

// Push initializes a fresh git repository in srcDir, commits everything, and pushes
// it to repo@branch. Used to seed a "start from a stack template" project's empty
// remote with generated starter code. Like Sync, git runs in the Rigger container
// and credentials are applied via the child ENVIRONMENT only (never argv/URL) so the
// secret can't leak into `ps` or the streamed log. auth may be nil for a public repo
// with an embedded token, but a real push almost always needs one. A blank branch
// defaults to "main". The committer identity is set per-invocation via -c flags so we
// don't depend on (or mutate) any global git config in the container.
func Push(srcDir, repo, branch string, auth *Auth, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if repo == "" {
		return fmt.Errorf("no target repository configured")
	}
	if branch == "" {
		branch = "main"
	}
	run := func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		var captured bytes.Buffer
		w := io.MultiWriter(out, &captured)
		cmd.Stdout, cmd.Stderr = w, w
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if auth != nil && len(auth.Env) > 0 {
			cmd.Env = append(cmd.Env, auth.Env...)
		}
		if err := cmd.Run(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("timed out after %s (the host may be unreachable)", timeout)
			}
			return ClassifyError(captured.String())
		}
		return nil
	}
	// -C runs git in srcDir without a chdir. Identity via -c so no global config needed.
	ident := []string{"-c", "user.email=rigger@localhost", "-c", "user.name=Rigger"}
	fmt.Fprintf(out, "⟳ Pushing scaffold to %s @ %s\n", repo, branchLabel(branch))
	if err := run("-C", srcDir, "init", "-q"); err != nil {
		return err
	}
	if err := run("-C", srcDir, "add", "-A"); err != nil {
		return err
	}
	commitArgs := append([]string{"-C", srcDir}, ident...)
	commitArgs = append(commitArgs, "commit", "-q", "-m", "Initial scaffold from Rigger")
	if err := run(commitArgs...); err != nil {
		return err
	}
	if err := run("-C", srcDir, "branch", "-M", branch); err != nil {
		return err
	}
	if err := run("-C", srcDir, "remote", "add", "origin", repo); err != nil {
		return err
	}
	if err := run("-C", srcDir, "push", "-u", "origin", branch); err != nil {
		return err
	}
	return nil
}

// branchLabel renders the branch for progress output, naming the empty (default) case.
func branchLabel(branch string) string {
	if branch == "" {
		return "default branch"
	}
	return branch
}

// resolveDefaultBranch returns the remote repository's default branch (its HEAD symref),
// so a blank branch clones whatever the repo actually defaults to — main, master, develop,
// … — rather than assuming "main". Returns "" when it can't be determined (auth/network);
// the caller then omits --branch and lets git pick the default at clone time.
func resolveDefaultBranch(repo string, auth *Auth) string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--symref", repo, "HEAD")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if auth != nil && len(auth.Env) > 0 {
		cmd.Env = append(cmd.Env, auth.Env...)
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	// e.g. "ref: refs/heads/develop\tHEAD"
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ref: refs/heads/"); ok {
			if i := strings.IndexAny(rest, " \t"); i > 0 {
				return rest[:i]
			}
		}
	}
	return ""
}

// ClassifyError turns git's captured output into a specific, user-facing reason for a
// clone/fetch/ls-remote failure — so the UI can show "authentication failed …" instead of
// a bare "exit status 128". Falls back to git's own last output line, then a generic message.
func ClassifyError(output string) error {
	lo := strings.ToLower(output)
	switch {
	case strings.Contains(lo, "could not read username"),
		strings.Contains(lo, "terminal prompts disabled"),
		strings.Contains(lo, "authentication failed"),
		strings.Contains(lo, "invalid username or password"),
		strings.Contains(lo, "permission denied"),
		strings.Contains(lo, "403 forbidden"):
		return fmt.Errorf("authentication failed — the repository is private or the credentials are wrong; add or select a Git provider with access to it")
	case strings.Contains(lo, "remote branch") && strings.Contains(lo, "not found"):
		return fmt.Errorf("branch not found in the repository — check the branch name")
	case strings.Contains(lo, "repository not found"),
		strings.Contains(lo, "not found"),
		strings.Contains(lo, "does not exist"),
		strings.Contains(lo, "404"):
		return fmt.Errorf("repository not found — check the URL (and, if it's private, that the selected Git provider has access)")
	case strings.Contains(lo, "could not resolve host"),
		strings.Contains(lo, "couldn't connect"),
		strings.Contains(lo, "connection refused"),
		strings.Contains(lo, "connection timed out"),
		strings.Contains(lo, "network is unreachable"),
		strings.Contains(lo, "ssl certificate problem"):
		return fmt.Errorf("could not reach the git host — check the URL and that the server is reachable from Rigger")
	}
	if line := lastNonEmptyLine(output); line != "" {
		return fmt.Errorf("%s", strings.TrimPrefix(line, "fatal: "))
	}
	return fmt.Errorf("git command failed")
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
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
