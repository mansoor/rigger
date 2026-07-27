package progress

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// TestLiveStdin feeds real command output through the Collapser and reports what
// came out. Opt-in via RIGGER_LIVE_STDIN=1, so the normal gate skips it:
//
//	docker compose pull 2>&1 | RIGGER_LIVE_STDIN=1 ./progress.test -test.run TestLiveStdin -test.v
//
// It exists because the parsing is only as good as the real output it was
// written against — the fixture in collapse_test.go is a capture, not a promise.
func TestLiveStdin(t *testing.T) {
	if os.Getenv("RIGGER_LIVE_STDIN") != "1" {
		t.Skip("set RIGGER_LIVE_STDIN=1 and pipe command output in to run this")
	}

	var raw bytes.Buffer
	in := io.TeeReader(os.Stdin, &raw)

	var collapsed bytes.Buffer
	c := New(&collapsed)
	if _, err := io.Copy(c, in); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	var recorded bytes.Buffer
	f := NewFilter(&recorded)
	f.Write(collapsed.Bytes()) //nolint:errcheck
	f.Flush()                  //nolint:errcheck

	count := func(s string) int {
		n := 0
		sc := bufio.NewScanner(strings.NewReader(s))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) != "" {
				n++
			}
		}
		return n
	}

	inLines, outLines, recLines := count(raw.String()), count(collapsed.String()), count(recorded.String())

	// Every transient line overwrites the one before it, so the viewer only ever
	// shows one of them at a time — that is the number worth reporting.
	var transient, permanent []string
	for _, l := range strings.Split(collapsed.String(), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if IsTransient(l) {
			transient = append(transient, strings.TrimPrefix(l, "\r"))
		} else {
			permanent = append(permanent, l)
		}
	}
	t.Logf("input lines:      %d", inLines)
	t.Logf("streamed lines:   %d  (%d transient → 1 visible row, %d permanent)", outLines, len(transient), len(permanent))
	t.Logf("RECORDED lines:   %d", recLines)
	if n := len(transient); n > 0 {
		t.Logf("--- the single row, as it updates ---")
		for _, i := range []int{0, n / 3, 2 * n / 3, n - 1} {
			t.Logf("    %s", transient[i])
		}
	}
	t.Logf("--- what the run history would store ---\n%s", recorded.String())

	// Whatever the input, the stored record must not be per-layer chatter.
	for _, l := range strings.Split(recorded.String(), "\n") {
		for _, chatter := range []string{"Pulling fs layer", "Download complete", "Verifying Checksum", "Extracting [", "Downloading ["} {
			if strings.Contains(l, chatter) {
				t.Errorf("layer chatter reached the record: %q", l)
			}
		}
	}
	if inLines > 20 && recLines >= inLines {
		t.Errorf("no collapsing happened: %d in, %d recorded", inLines, recLines)
	}
}
