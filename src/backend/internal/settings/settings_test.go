package settings

import "testing"

// TestEffectiveRegistry covers the Phase-0 resolver behavior: the project's own
// registry wins (trimmed); with none set, the workspace/global system lookups are
// stubs that return "" (Phase 1 implements them), so the result is "". A nil DB is
// used to confirm the resolver tolerates it (CLI / no-DB paths).
func TestEffectiveRegistry(t *testing.T) {
	cases := []struct {
		name    string
		projReg string
		want    string
	}{
		{"project registry wins", "ghcr.io/acme", "ghcr.io/acme"},
		{"project registry trimmed", "  registry.example.com  ", "registry.example.com"},
		{"no project registry → stubs return empty", "", ""},
		{"whitespace-only → empty", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveRegistry(nil, "ws", tc.projReg); got != tc.want {
				t.Errorf("EffectiveRegistry(nil, ws, %q) = %q, want %q", tc.projReg, got, tc.want)
			}
		})
	}

	// Phase-0 stubs are intentionally empty until the system designation exists.
	if got := WorkspaceSystemRegistry(nil, "ws"); got != "" {
		t.Errorf("WorkspaceSystemRegistry stub = %q, want empty", got)
	}
	if got := GlobalSystemRegistry(nil); got != "" {
		t.Errorf("GlobalSystemRegistry stub = %q, want empty", got)
	}
}
