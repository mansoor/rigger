package api

import "testing"

func TestPreviewEnvKey(t *testing.T) {
	if got := previewEnvKey(42); got != "pr42" {
		t.Errorf("previewEnvKey(42) = %q, want pr42 (hyphen-free)", got)
	}
}

func TestPreviewExpiry(t *testing.T) {
	if got := previewExpiry(0, 1000); got != 0 {
		t.Errorf("previewExpiry(0) = %d, want 0 (no TTL)", got)
	}
	if got := previewExpiry(-5, 1000); got != 0 {
		t.Errorf("previewExpiry(negative) = %d, want 0", got)
	}
	if got := previewExpiry(2, 1000); got != 1000+2*3600 {
		t.Errorf("previewExpiry(2h, now=1000) = %d, want %d", got, 1000+2*3600)
	}
}

func TestPreviewBranchAllowed(t *testing.T) {
	cases := []struct {
		filter, branch string
		want           bool
	}{
		{"", "anything", true},                    // no filter → allow all
		{"  ", "anything", true},                   // blank-ish → allow all
		{"feature/*", "feature/login", true},       // glob match
		{"feature/*", "hotfix/bug", false},         // glob miss
		{"main", "main", true},                     // exact
		{"main", "develop", false},                 // exact miss
		{"release-[0-9]", "release-3", true},       // char class
		{"[", "whatever", false},                   // invalid pattern → not allowed
	}
	for _, c := range cases {
		if got := previewBranchAllowed(c.filter, c.branch); got != c.want {
			t.Errorf("previewBranchAllowed(%q, %q) = %v, want %v", c.filter, c.branch, got, c.want)
		}
	}
}

func TestPreviewForkAllowed(t *testing.T) {
	cases := []struct {
		policy string
		isFork bool
		want   bool
	}{
		{"off", false, true},     // same-repo branch always allowed
		{"", false, true},        // (default policy) same-repo allowed
		{"off", true, false},     // fork blocked by default
		{"", true, false},        // empty policy = off for forks
		{"approved", true, false}, // approval gate ⇒ not auto-deployed
		{"on", true, true},       // explicitly allow forks
		{"on", false, true},
	}
	for _, c := range cases {
		if got := previewForkAllowed(c.policy, c.isFork); got != c.want {
			t.Errorf("previewForkAllowed(%q, fork=%v) = %v, want %v", c.policy, c.isFork, got, c.want)
		}
	}
}

func TestTailStr(t *testing.T) {
	if got := tailStr("short", 100); got != "short" {
		t.Errorf("tailStr(short) = %q, want short", got)
	}
	if got := tailStr("abcdef", 3); got != "…def" {
		t.Errorf("tailStr(abcdef, 3) = %q, want …def", got)
	}
}
