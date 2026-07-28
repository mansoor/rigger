package dockerops

import (
	"os"
	"testing"
)

// TestLiveComposeAnalysis runs the placement analyser over a REAL generated
// compose file, so the parsing is checked against what composegen actually emits
// rather than only against a hand-written fixture.
//
//	RIGGER_LIVE_COMPOSE=/path/to/docker-compose.yml ./dockerops.test -test.run TestLiveCompose -test.v
func TestLiveComposeAnalysis(t *testing.T) {
	path := os.Getenv("RIGGER_LIVE_COMPOSE")
	if path == "" {
		t.Skip("set RIGGER_LIVE_COMPOSE to a generated docker-compose.yml to run this")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	risks, err := unpinnedStatefulServices(content)
	if err != nil {
		t.Fatalf("a real generated compose failed to parse: %v", err)
	}
	t.Logf("%s → %d service(s) would be flagged on a multi-node cluster:", path, len(risks))
	for _, r := range risks {
		t.Logf("    • %s — %s", r.Service, r.Reason)
	}
}
