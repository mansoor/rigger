package progress

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// Verbatim `docker compose pull` output (stdout piped, not a TTY).
const composePull = ` a Pulling
 619be1103602 Already exists
 e88463a31d6d Pulling fs layer
 de93e10dd670 Pulling fs layer
 c9ae96b49a2d Pulling fs layer
 4f4fb700ef54 Pulling fs layer
 4f4fb700ef54 Waiting
 e88463a31d6d Downloading [==>                                                ]  16.38kB/360.6kB
 de93e10dd670 Downloading [==================================================>]  7.452kB/7.452kB
 de93e10dd670 Verifying Checksum
 de93e10dd670 Download complete
 e88463a31d6d Downloading [===============>                                   ]  114.7kB/360.6kB
 c9ae96b49a2d Downloading [>                                                  ]  162.8kB/14.71MB
 4f4fb700ef54 Downloading [==================================================>]      32B/32B
 4f4fb700ef54 Download complete
 e88463a31d6d Verifying Checksum
 e88463a31d6d Download complete
 e88463a31d6d Extracting [====>                                              ]  32.77kB/360.6kB
 c9ae96b49a2d Downloading [=====>                                             ]  1.678MB/14.71MB
 e88463a31d6d Extracting [==================================================>]  360.6kB/360.6kB
 e88463a31d6d Pull complete
 de93e10dd670 Extracting [==================================================>]  7.452kB/7.452kB
 de93e10dd670 Pull complete
 c9ae96b49a2d Downloading [==================================================>]  14.71MB/14.71MB
 c9ae96b49a2d Download complete
 c9ae96b49a2d Extracting [==================================================>]  14.71MB/14.71MB
 c9ae96b49a2d Pull complete
 4f4fb700ef54 Pull complete
 a Pulled
`

// newTest builds a Collapser with a controllable clock and no rate limiting, so
// every state change produces a line and assertions are deterministic.
func newTest(w *bytes.Buffer) *Collapser {
	c := New(w)
	c.now = func() time.Time { return time.Unix(0, 0) }
	c.interval = 0
	return c
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestComposePullCollapses(t *testing.T) {
	var out bytes.Buffer
	c := newTest(&out)
	if _, err := c.Write([]byte(composePull)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got := lines(out.String())
	var permanent, transient []string
	for _, l := range got {
		if IsTransient(l) {
			transient = append(transient, l)
		} else {
			permanent = append(permanent, l)
		}
	}

	// The whole pull reduces to exactly one line worth recording.
	if len(permanent) != 1 || permanent[0] != "✓ Pulled a" {
		t.Fatalf("permanent output = %q, want [\"✓ Pulled a\"]", permanent)
	}
	if len(transient) == 0 {
		t.Fatal("expected transient progress lines")
	}

	// 30 lines of chatter in, 1 line of record out.
	if in := len(lines(composePull)); in <= len(permanent)*5 {
		t.Fatalf("sanity: input was only %d lines", in)
	}

	// The last transient reports every layer complete, with a total that counts
	// each layer once (Extracting must not double-count the download).
	last := transient[len(transient)-1]
	if !strings.Contains(last, "5/5 layers") {
		t.Fatalf("final transient = %q, want 5/5 layers", last)
	}
	if !strings.Contains(last, "15.1 MB/15.1 MB") {
		t.Fatalf("final transient = %q, want a 15.1 MB total", last)
	}
}

func TestRecorderKeepsOnlyTheSummary(t *testing.T) {
	var stream bytes.Buffer
	c := newTest(&stream)
	c.Write([]byte(composePull)) //nolint:errcheck
	c.Flush()                    //nolint:errcheck

	var recorded bytes.Buffer
	f := NewFilter(&recorded)
	// Feed it in awkward chunks — a Write can split a line anywhere.
	b := stream.Bytes()
	for i := 0; i < len(b); i += 7 {
		end := i + 7
		if end > len(b) {
			end = len(b)
		}
		if _, err := f.Write(b[i:end]); err != nil {
			t.Fatalf("filter write: %v", err)
		}
	}
	if err := f.Flush(); err != nil {
		t.Fatalf("filter flush: %v", err)
	}
	if got := recorded.String(); got != "✓ Pulled a\n" {
		t.Fatalf("recorded = %q, want %q", got, "✓ Pulled a\n")
	}
}

// `docker pull` (not compose) uses `<id>: <phase>`; both must collapse.
func TestDockerPullFormat(t *testing.T) {
	const raw = `2.8-alpine: Pulling from library/caddy
63b69af3dc55: Pulling fs layer
60177795cab3: Pulling fs layer
63b69af3dc55: Downloading [====>                                              ]  1.2MB/12MB
63b69af3dc55: Download complete
63b69af3dc55: Pull complete
60177795cab3: Pull complete
Digest: sha256:af32e97399feb
Status: Downloaded newer image for caddy:2.8-alpine
`
	var out bytes.Buffer
	c := newTest(&out)
	c.Write([]byte(raw)) //nolint:errcheck
	c.Flush()            //nolint:errcheck

	for _, l := range lines(out.String()) {
		if IsTransient(l) {
			continue
		}
		// Layer chatter must be gone; the human-meaningful lines must survive.
		if strings.Contains(l, "Pulling fs layer") || strings.Contains(l, "Download complete") {
			t.Fatalf("layer chatter survived: %q", l)
		}
	}
	perm := out.String()
	for _, want := range []string{"Pulling from library/caddy", "Digest: sha256", "Status: Downloaded newer image"} {
		if !strings.Contains(perm, want) {
			t.Fatalf("expected %q to survive, got:\n%s", want, perm)
		}
	}
}

// Ordinary deploy output must pass through untouched — this filter sits in the
// path of every command, not just pulls.
func TestNonPullOutputUntouched(t *testing.T) {
	const raw = ` Container mcl_sdm_dev_db  Creating
 Container mcl_sdm_dev_db  Created
 Container mcl_sdm_dev_db  Starting
 Container mcl_sdm_dev_db  Healthy
 Network mcl_sdm_dev_net  Created
⚑ Deploying 'mcl_sdm_dev' (compose)
✓ Stack 'mcl_sdm_dev' is up
`
	var out bytes.Buffer
	c := newTest(&out)
	c.Write([]byte(raw)) //nolint:errcheck
	c.Flush()            //nolint:errcheck
	if out.String() != raw {
		t.Fatalf("non-pull output was altered:\n--- got ---\n%s\n--- want ---\n%s", out.String(), raw)
	}
}

func TestRateLimitCollapsesBurst(t *testing.T) {
	var out bytes.Buffer
	c := New(&out)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now } // frozen: only the first emit passes
	c.interval = 150 * time.Millisecond

	c.Write([]byte(" aaaaaaaaaaaa Pulling fs layer \n")) //nolint:errcheck
	for i := 0; i < 200; i++ {
		c.Write([]byte(" aaaaaaaaaaaa Downloading [=>  ]  1.0MB/10MB\n")) //nolint:errcheck
	}
	if n := len(lines(out.String())); n > 2 {
		t.Fatalf("burst produced %d lines, want the rate limit to hold it to 1", n)
	}
}

func TestPartialLineBuffered(t *testing.T) {
	var out bytes.Buffer
	c := newTest(&out)
	c.Write([]byte(" Container app  Star")) //nolint:errcheck
	if out.Len() != 0 {
		t.Fatalf("partial line emitted early: %q", out.String())
	}
	c.Write([]byte("ted\n")) //nolint:errcheck
	if got := out.String(); got != " Container app  Started\n" {
		t.Fatalf("reassembled = %q", got)
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"32B": 32, "16.38kB": 16380, "360.6kB": 360600,
		"14.71MB": 14710000, "1.5GB": 1500000000,
	}
	for in, want := range cases {
		got, ok := parseSize(in)
		if !ok || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	if _, ok := parseSize("banana"); ok {
		t.Error("parseSize accepted nonsense")
	}
}
