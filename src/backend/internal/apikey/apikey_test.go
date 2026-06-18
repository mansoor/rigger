package apikey

import (
	"testing"
	"time"
)

func TestGenerateAndHash(t *testing.T) {
	raw, hash, prefix, err := Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(raw) != 4+64 { // "rgk_" + 32 bytes hex
		t.Errorf("raw key length = %d, want 68", len(raw))
	}
	if raw[:4] != "rgk_" {
		t.Errorf("raw key prefix = %q, want rgk_", raw[:4])
	}
	if Hash(raw) != hash {
		t.Error("Hash(raw) != returned hash")
	}
	if Hash("rgk_different") == hash {
		t.Error("different inputs hashed equal")
	}
	if len(prefix) == 0 || prefix == raw {
		t.Errorf("prefix should be a non-secret snippet, got %q", prefix)
	}
	// Two generations differ.
	raw2, _, _, _ := Generate()
	if raw2 == raw {
		t.Error("two generated keys collided")
	}
}

func TestAuthorize(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	base := func() *Key {
		return &Key{
			Enabled:       true,
			Scopes:        []string{OpProjectsList, OpEnvStart},
			ProjectAccess: "all",
		}
	}

	if err := base().Authorize(OpProjectsList, "mcl", "ahb", now); err != nil {
		t.Errorf("expected allow, got %v", err)
	}
	if err := base().Authorize(OpEnvStop, "mcl", "ahb", now); err != ErrScope {
		t.Errorf("missing scope: want ErrScope, got %v", err)
	}

	disabled := base()
	disabled.Enabled = false
	if err := disabled.Authorize(OpProjectsList, "mcl", "ahb", now); err != ErrDisabled {
		t.Errorf("want ErrDisabled, got %v", err)
	}

	expired := base()
	expired.ExpiresAt = now.Unix() - 1
	if err := expired.Authorize(OpProjectsList, "mcl", "ahb", now); err != ErrExpired {
		t.Errorf("want ErrExpired, got %v", err)
	}

	// Specific project access: only listed pairs pass.
	spec := base()
	spec.ProjectAccess = "specific"
	spec.Projects = []ProjectRef{{Workspace: "mcl", Project: "ahb"}}
	if err := spec.Authorize(OpProjectsList, "mcl", "ahb", now); err != nil {
		t.Errorf("listed project should pass, got %v", err)
	}
	if err := spec.Authorize(OpProjectsList, "mcl", "other", now); err != ErrProject {
		t.Errorf("unlisted project: want ErrProject, got %v", err)
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter()
	now := time.Unix(2_000_000, 0)

	// limit 0 = unlimited.
	for i := 0; i < 100; i++ {
		if !rl.Allow(1, "mcl/ahb", 0, now) {
			t.Fatal("limit 0 should never block")
		}
	}

	// limit 3: 3 allowed, 4th blocked within the window.
	for i := 0; i < 3; i++ {
		if !rl.Allow(2, "mcl/ahb", 3, now) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if rl.Allow(2, "mcl/ahb", 3, now) {
		t.Error("4th request should be blocked")
	}
	// A different project for the same key has its own bucket.
	if !rl.Allow(2, "mcl/other", 3, now) {
		t.Error("different project should have a fresh window")
	}
	// After the window passes, requests are allowed again.
	later := now.Add(61 * time.Second)
	if !rl.Allow(2, "mcl/ahb", 3, later) {
		t.Error("window should have reset after 60s")
	}
}
