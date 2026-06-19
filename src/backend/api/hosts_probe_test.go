package api

import "testing"

func TestParseSwarmInfo(t *testing.T) {
	cases := []struct {
		name        string
		out         string
		wantState   string
		wantManager bool
	}{
		{"manager", "active|true", "active", true},
		{"worker", "active|false", "active", false},
		{"inactive", "inactive|false", "inactive", false},
		{"whitespace", "  active | true \n", "active", true},
		{"missing manager field", "inactive", "inactive", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, manager := parseSwarmInfo(tc.out)
			if state != tc.wantState || manager != tc.wantManager {
				t.Errorf("parseSwarmInfo(%q) = (%q, %v), want (%q, %v)", tc.out, state, manager, tc.wantState, tc.wantManager)
			}
		})
	}
}
