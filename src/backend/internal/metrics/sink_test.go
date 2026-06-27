package metrics

import (
	"strings"
	"testing"
	"time"
)

// The TSDB exposition payload must carry one line per metric with {project,env}
// labels and a shared millisecond timestamp, in Prometheus text format VictoriaMetrics
// ingests at /api/v1/import/prometheus.
func TestExposition(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_000)
	out := exposition([]Sample{{
		Project: "mcl_qrh", Env: "dev",
		CPUPct: 12.5, MemoryBytes: 1048576, DiskBytes: 2048, NetRxBytes: 10, NetTxBytes: 20,
		At: at,
	}})
	for _, want := range []string{
		`rigger_cpu_pct{project="mcl_qrh",env="dev"} 12.5 1700000000000`,
		`rigger_memory_bytes{project="mcl_qrh",env="dev"} 1048576 1700000000000`,
		`rigger_disk_bytes{project="mcl_qrh",env="dev"} 2048 1700000000000`,
		`rigger_net_rx_bytes{project="mcl_qrh",env="dev"} 10 1700000000000`,
		`rigger_net_tx_bytes{project="mcl_qrh",env="dev"} 20 1700000000000`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing line %q in:\n%s", want, out)
		}
	}
	// Exactly 5 metric lines for one sample.
	if n := strings.Count(strings.TrimSpace(out), "\n"); n != 4 {
		t.Errorf("expected 5 lines (4 newlines) for one sample, got %d newlines:\n%s", n, out)
	}
}

// Label values are escaped so a stray quote/backslash can't break the line format.
func TestExpositionEscapesLabels(t *testing.T) {
	out := exposition([]Sample{{Project: `a"b\c`, Env: "dev", At: time.UnixMilli(1)}})
	if !strings.Contains(out, `project="a\"b\\c"`) {
		t.Errorf("label not escaped:\n%s", out)
	}
}
