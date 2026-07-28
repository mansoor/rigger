package api

import "testing"

func TestParseSwarmInfo(t *testing.T) {
	cases := []struct {
		name        string
		out         string
		wantState   string
		wantManager bool
		wantNodeID  string
	}{
		// Verbatim from `docker info --format
		// '{{.Swarm.LocalNodeState}}|{{.Swarm.ControlAvailable}}|{{.Swarm.NodeID}}'`.
		{"manager with node id", "active|true|l61fwvuvm7f23btlnxm9y1stv", "active", true, "l61fwvuvm7f23btlnxm9y1stv"},
		{"manager", "active|true", "active", true, ""},
		{"worker", "active|false", "active", false, ""},
		{"inactive", "inactive|false", "inactive", false, ""},
		{"whitespace", "  active | true | abc123 \n", "active", true, "abc123"},
		{"missing manager field", "inactive", "inactive", false, ""},
		{"empty", "", "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, manager, nodeID := parseSwarmInfo(tc.out)
			if state != tc.wantState || manager != tc.wantManager || nodeID != tc.wantNodeID {
				t.Errorf("parseSwarmInfo(%q) = (%q, %v, %q), want (%q, %v, %q)",
					tc.out, state, manager, nodeID, tc.wantState, tc.wantManager, tc.wantNodeID)
			}
		})
	}
}
