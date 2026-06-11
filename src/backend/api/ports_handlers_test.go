package api

import (
	"encoding/json"
	"sort"
	"testing"
)

// TestWsPortConfigHostPorts verifies host ports are read from the unified
// services[] graph (not the removed images[] field) and that host_port tolerates
// both string and numeric JSON, while ${VAR}/ranges/blank are ignored.
func TestWsPortConfigHostPorts(t *testing.T) {
	raw := `{
      "environments": { "dev": {} },
      "images": [ { "host_port": "9999" } ],
      "services": [
        { "name": "web",     "host_port": "3000", "extra_ports": ["8443:443"] },
        { "name": "api",     "host_port": 8000 },
        { "name": "worker" },
        { "name": "tmpl",    "host_port": "${PORT}" },
        { "name": "managed", "host_port": "" }
      ]
    }`
	var c wsPortConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	got := c.hostPorts()
	sort.Ints(got)
	want := []int{3000, 8000, 8443}
	if len(got) != len(want) {
		t.Fatalf("hostPorts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hostPorts = %v, want %v", got, want)
		}
	}
	// The legacy images[] field must NOT contribute (9999 absent).
	for _, p := range got {
		if p == 9999 {
			t.Error("legacy images[] host_port leaked into hostPorts")
		}
	}
}
