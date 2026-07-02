package composegen

import (
	"strings"
	"testing"
	"time"
)

// TestAuxSearchTSDBAlongsidePrimaryDB: OpenSearch (search) and VictoriaMetrics (tsdb) are
// auxiliary engines that emit ALONGSIDE the primary Postgres DB + Redis — not in the DB
// slot. They are internal-only in v1, so neither publishes a host port.
func TestAuxSearchTSDBAlongsidePrimaryDB(t *testing.T) {
	cfg := []byte(`{
		"project":{"name":"aux","resource_prefix":"mcl_aux","type":"custom",
			"database":"postgres","search":"opensearch","tsdb":"victoriametrics","redis_enabled":true},
		"services":[{"name":"web","image":"web","tag":"latest","port":3000,"web_routed":true}],
		"environments":{"dev":{"deployment":"compose"}}
	}`)
	out, err := GenerateAt(cfg, "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"  postgres:", "  opensearch:", "  victoriametrics:", "  redis:"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing managed service %q in:\n%s", want, s)
		}
	}
	// Aux services are internal-only (v1) — no published host ports.
	if os := svcBlock(t, s, "opensearch"); strings.Contains(os, "ports:") {
		t.Errorf("opensearch should not publish host ports:\n%s", os)
	}
	if vm := svcBlock(t, s, "victoriametrics"); strings.Contains(vm, "ports:") {
		t.Errorf("victoriametrics should not publish host ports:\n%s", vm)
	}
	// Their data volumes are declared.
	for _, v := range []string{"mcl_aux_dev_opensearch_data:", "mcl_aux_dev_victoriametrics_data:"} {
		if !strings.Contains(s, v) {
			t.Errorf("missing named volume %q", v)
		}
	}
}

// TestAuxEngineNotPrimaryDB: an aux engine parked in the legacy Database slot self-heals to
// its own slot (composegen.normalizeAux), emitting as the aux service, not a phantom DB.
func TestAuxEngineLegacyDBSlotRelocates(t *testing.T) {
	cfg := []byte(`{
		"project":{"name":"leg","resource_prefix":"mcl_leg","type":"custom","database":"opensearch"},
		"services":[{"name":"web","image":"web","tag":"latest","port":3000,"web_routed":true}],
		"environments":{"dev":{"deployment":"compose"}}
	}`)
	out, err := GenerateAt(cfg, "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if s := string(out); !strings.Contains(s, "  opensearch:") {
		t.Errorf("legacy Database=opensearch should still emit the opensearch service:\n%s", s)
	}
}

// TestMongoExpressParity: a MongoDB project with web_sql synthesizes mongo-express (not
// Adminer, which is SQL-only); a Postgres project with web_sql synthesizes Adminer (not
// mongo-express). The web_sql toggle drives the right web console per engine.
func TestMongoExpressParity(t *testing.T) {
	mongo := []byte(`{
		"project":{"name":"m","resource_prefix":"mcl_m","type":"custom","database":"mongodb","web_sql":true},
		"services":[{"name":"web","image":"web","tag":"latest","port":3000,"web_routed":true}],
		"environments":{"dev":{"deployment":"compose"}}
	}`)
	out, err := GenerateAt(mongo, "dev", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "  mongo-express:") {
		t.Errorf("mongodb + web_sql should synthesize mongo-express:\n%s", s)
	}
	if strings.Contains(s, "  adminer:") {
		t.Errorf("mongodb must NOT get Adminer (SQL-only):\n%s", s)
	}
	if !strings.Contains(s, "ME_CONFIG_MONGODB_URL") {
		t.Errorf("mongo-express missing ME_CONFIG_MONGODB_URL:\n%s", s)
	}

	pg := []byte(`{
		"project":{"name":"p","resource_prefix":"mcl_p","type":"custom","database":"postgres","web_sql":true},
		"services":[{"name":"web","image":"web","tag":"latest","port":3000,"web_routed":true}],
		"environments":{"dev":{"deployment":"compose"}}
	}`)
	out2, _ := GenerateAt(pg, "dev", time.Unix(0, 0).UTC())
	s2 := string(out2)
	if !strings.Contains(s2, "  adminer:") {
		t.Errorf("postgres + web_sql should synthesize Adminer:\n%s", s2)
	}
	if strings.Contains(s2, "  mongo-express:") {
		t.Errorf("postgres must NOT get mongo-express:\n%s", s2)
	}
}
