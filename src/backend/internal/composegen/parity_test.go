package composegen

import (
	"strings"
	"testing"
	"time"
)

// TestHealthcheckQuoteEscaping guards the historical YAML bug: literal double
// quotes in a healthcheck command must be escaped so the flow sequence stays valid.
func TestHealthcheckQuoteEscaping(t *testing.T) {
	cfg := []byte(`{
		"project": {"name":"q","registry":"reg","version":{"major":1,"minor":0,"patch":0,"build":0}},
		"services": [{"name":"web","image":"nginx","tag":"alpine","web_routed":true,"port":"80",
			"healthcheck":"sh -c \"curl -sf http://localhost/ || exit 1\""}],
		"environments": {"dev": {"deployment":"compose","http_port":"8080"}}
	}`)
	out, err := GenerateAt(cfg, "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	want := `      test: ["CMD-SHELL", "sh -c \"curl -sf http://localhost/ || exit 1\""]`
	if !strings.Contains(string(out), want) {
		t.Errorf("healthcheck quotes not escaped.\nwant line: %s\ngot:\n%s", want, out)
	}
}
