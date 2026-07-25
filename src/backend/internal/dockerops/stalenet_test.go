package dockerops

import (
	"bytes"
	"strings"
	"testing"
)

func TestNetworkAttachHint(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{
			// Standalone host.
			name:   "standalone attach failure",
			output: `Error response from daemon: failed to set up container networking: Could not attach to network abc123: network abc123 not found`,
			want:   true,
		},
		{
			// Swarm host: the lookup goes through the cluster store, so the
			// daemon wraps it as an rpc error.
			name:   "swarm attach failure",
			output: `Error response from daemon: failed to set up container networking: Could not attach to network d2a894082652: rpc error: code = NotFound desc = network d2a894082652 not found`,
			want:   true,
		},
		{
			name:   "unrelated failure is not claimed",
			output: `Error response from daemon: driver failed programming external connectivity on endpoint: port is already allocated`,
			want:   false,
		},
		{
			// "not found" alone is far too common to hint on.
			name:   "image pull failure is not claimed",
			output: `Error response from daemon: manifest for localhost:5001/app:1.0.0 not found: manifest unknown`,
			want:   false,
		},
		{name: "empty output", output: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := networkAttachHint(tc.output)
			if (got != "") != tc.want {
				t.Fatalf("networkAttachHint(%q) = %q, want hint=%v", tc.output, got, tc.want)
			}
			if tc.want && !strings.Contains(got, "Refresh") {
				t.Fatalf("hint should name Refresh as the fix, got %q", got)
			}
		})
	}
}

func TestTailWriterForwardsAndRetainsTail(t *testing.T) {
	var sink bytes.Buffer
	tw := newTailWriter(&sink, 10)

	for _, chunk := range []string{"abcde", "fghij", "klmno"} {
		if _, err := tw.Write([]byte(chunk)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// Everything still reaches the caller's writer...
	if got := sink.String(); got != "abcdefghijklmno" {
		t.Fatalf("forwarded = %q, want %q", got, "abcdefghijklmno")
	}
	// ...while only the last max bytes are retained for inspection.
	if got := tw.String(); got != "fghijklmno" {
		t.Fatalf("tail = %q, want %q", got, "fghijklmno")
	}
}

func TestTailWriterNilSink(t *testing.T) {
	// opts.Stderr can be nil; the tail must still work and not panic.
	tw := newTailWriter(nil, 4)
	if _, err := tw.Write([]byte("abcdef")); err != nil {
		t.Fatalf("write to nil sink: %v", err)
	}
	if got := tw.String(); got != "cdef" {
		t.Fatalf("tail = %q, want %q", got, "cdef")
	}
}
