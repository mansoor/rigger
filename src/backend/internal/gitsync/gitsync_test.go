package gitsync

import (
	"strings"
	"testing"
)

func TestClassifyError(t *testing.T) {
	cases := []struct{ name, output, want string }{
		{"terminal prompts disabled",
			"fatal: could not read Username for 'https://github.com': terminal prompts disabled",
			"authentication failed"},
		{"auth failed",
			"remote: Support for password authentication was removed.\nfatal: Authentication failed for 'https://github.com/org/private.git/'",
			"authentication failed"},
		{"403",
			"fatal: unable to access 'https://gitea.example.com/org/repo.git/': The requested URL returned error: 403 Forbidden",
			"authentication failed"},
		{"repo not found",
			"remote: Repository not found.\nfatal: repository 'https://github.com/org/nope.git/' not found",
			"repository not found"},
		{"branch not found",
			"fatal: Remote branch nosuch not found in upstream origin",
			"branch not found"},
		{"host unreachable",
			"fatal: unable to access 'https://nope.invalid/x.git/': Could not resolve host: nope.invalid",
			"could not reach the git host"},
		{"fallback to last fatal line",
			"Cloning into 'x'...\nfatal: some other git problem",
			"some other git problem"},
	}
	for _, c := range cases {
		got := ClassifyError(c.output).Error()
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: ClassifyError = %q, want to contain %q", c.name, got, c.want)
		}
	}
	// Branch-not-found must win over the generic "not found" repository case.
	if got := ClassifyError("fatal: Remote branch x not found in upstream origin").Error(); strings.Contains(got, "repository not found") {
		t.Errorf("branch-not-found misclassified as repository not found: %q", got)
	}
	// No recognizable output ⇒ generic, never empty.
	if got := ClassifyError("").Error(); got == "" {
		t.Error("empty output should still yield a non-empty error")
	}
}
