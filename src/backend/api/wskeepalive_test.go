package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// End-to-end over a real connection: the server must send ping frames on an
// otherwise silent stream. Without them nginx (60s default read timeout) closes
// the connection and the browser reports close 1006 — the bug this fixes.
func TestKeepalivePingsAnIdleConnection(t *testing.T) {
	// Shorter than wsPingInterval would make the test slow, so drive ping()
	// directly at a fast cadence and assert the frames arrive.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		ww := newWSWriter(c)
		for i := 0; i < 3; i++ {
			if err := ww.ping(); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		ww.Text([]byte("done")) //nolint:errcheck
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var mu sync.Mutex
	pings := 0
	conn.SetPingHandler(func(string) error {
		mu.Lock()
		pings++
		mu.Unlock()
		return nil
	})

	// Reading is what dispatches control frames; the text frame ends the stream.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if string(msg) == "done" {
			break
		}
	}

	mu.Lock()
	got := pings
	mu.Unlock()
	if got != 3 {
		t.Errorf("received %d pings, want 3 — an idle stream with no pings is what a proxy kills", got)
	}
}

// The ping goroutine is a second writer, and gorilla/websocket PANICS on
// concurrent writes — in a spawned goroutine that panic is not recovered by
// net/http and would take the process down. Run writes and pings together with
// -race to prove they serialize.
//
// The server drains and finishes exactly as the real handlers do. The first
// version of this test did neither, and failed 8 runs in 20: the client answered
// 200 pings with 200 pongs, nothing read them, and closing a socket with unread
// data made the kernel reset the connection before "done" arrived. That was not
// a test bug — the real handlers did the same thing.
func TestWriterSerializesPingsAndOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		ww := newWSWriter(c)
		defer ww.Finish()
		ww.Drain(nil)

		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					ww.Text([]byte("output line")) //nolint:errcheck
					ww.ping()                      //nolint:errcheck
				}
			}()
		}
		wg.Wait()
		ww.Text([]byte("done")) //nolint:errcheck
	}))
	defer srv.Close()

	conn := dialTest(t, srv)
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v — a panic, interleaved frame or reset would surface here", err)
		}
		if string(msg) == "done" {
			return
		}
	}
}

func dialTest(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetReadDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
	return conn
}

// readToClose reads until the connection ends and returns every text message and
// the close code (-1 when the connection ended without a close frame at all).
func readToClose(conn *websocket.Conn) (lines []string, code int) {
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ce, ok := err.(*websocket.CloseError); ok {
				return lines, ce.Code
			}
			return lines, -1
		}
		lines = append(lines, string(msg))
	}
}

// Every stream used to end with close code 1006 — gorilla's Close() drops the TCP
// connection without a close frame. Now that the UI explains close codes, that
// would print "connection dropped, check your proxy" under every completed
// deploy. Finish sends a normal closure instead.
func TestFinishSendsNormalClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		ww := newWSWriter(c)
		ww.Keepalive()
		defer ww.Finish()
		ww.Drain(nil)
		ww.Text([]byte("✓ deploy completed")) //nolint:errcheck
	}))
	defer srv.Close()

	lines, code := readToClose(dialTest(t, srv))
	if len(lines) != 1 || lines[0] != "✓ deploy completed" {
		t.Errorf("lines = %q, want the completion line", lines)
	}
	if code != websocket.CloseNormalClosure {
		t.Errorf("close code = %d, want %d (normal closure) — 1006 is what the UI reports as a dropped connection", code, websocket.CloseNormalClosure)
	}
}

// The exact failure mode: a long operation receives pongs it must not leave
// unread. With pongs outstanding, the last line and a clean close must still
// arrive — every time, so the loop catches a timing-dependent reset.
func TestPongsDoNotCostTheLastLine(t *testing.T) {
	for i := 0; i < 25; i++ {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			ww := newWSWriter(c)
			defer ww.Finish()
			ww.Drain(nil)
			for p := 0; p < 100; p++ { // a long deploy's worth of keepalives
				ww.ping() //nolint:errcheck
			}
			time.Sleep(20 * time.Millisecond) // let the pongs land before closing
			ww.Text([]byte("last line"))      //nolint:errcheck
		}))
		lines, code := readToClose(dialTest(t, srv))
		srv.Close()
		if len(lines) == 0 || lines[len(lines)-1] != "last line" {
			t.Fatalf("run %d: last line lost (got %q, close %d)", i, lines, code)
		}
		if code != websocket.CloseNormalClosure {
			t.Fatalf("run %d: close code %d, want %d", i, code, websocket.CloseNormalClosure)
		}
	}
}

// Drain's onGone is how a following log stream learns the browser left.
func TestDrainReportsThePeerLeaving(t *testing.T) {
	gone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		ww := newWSWriter(c)
		defer ww.Finish()
		ww.Drain(func() { close(gone) })
		<-gone
	}))
	defer srv.Close()

	conn := dialTest(t, srv)
	conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "")) //nolint:errcheck
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("onGone never ran — a following stream would never be cancelled")
	}
}

// Finish runs from a defer and may also be reached on an error path.
func TestFinishIsSafeTwice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		ww := newWSWriter(c)
		ww.Keepalive()
		ww.Drain(nil)
		ww.Finish()
		ww.Finish()
	}))
	defer srv.Close()

	if _, code := readToClose(dialTest(t, srv)); code != websocket.CloseNormalClosure {
		t.Errorf("close code = %d, want %d", code, websocket.CloseNormalClosure)
	}
}

// Keepalive must not outlive its handler: stop has to end the goroutine, and be
// safe to call more than once (handlers defer it).
func TestKeepaliveStopIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		ww := newWSWriter(c)
		stop := ww.Keepalive()
		stop()
		stop()                  // a second close of the same channel would panic
		ww.Text([]byte("done")) //nolint:errcheck
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	_, msg, err := conn.ReadMessage()
	if err != nil || string(msg) != "done" {
		t.Fatalf("msg=%q err=%v", msg, err)
	}
}

// The interval has to sit inside the proxy timeout it exists to defeat.
func TestPingIntervalBeatsProxyTimeout(t *testing.T) {
	// nginx's proxy_read_timeout default, which Nginx Proxy Manager inherits.
	const nginxDefault = 60 * time.Second
	if wsPingInterval >= nginxDefault {
		t.Fatalf("wsPingInterval %v does not beat nginx's %v", wsPingInterval, nginxDefault)
	}
	// Room for one lost ping before the timeout is reached.
	if 2*wsPingInterval >= nginxDefault {
		t.Errorf("wsPingInterval %v leaves no margin for a dropped ping", wsPingInterval)
	}
}

// A stream is authorized once, when it opens. When its session ends (the account
// is disabled, its password changes) the next keepalive tick closes it with 1008
// "session ended", so a terminal can't outlive the access that opened it.
func TestEndWhenClosesTheStreamOnceTheSessionEnds(t *testing.T) {
	var mu sync.Mutex
	sessionOK := true
	results := make(chan [2]bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ww := newWSWriter(c)
		defer ww.Finish()
		gone := make(chan struct{})
		ww.Drain(func() { close(gone) })
		ww.EndWhen(func() bool { mu.Lock(); defer mu.Unlock(); return sessionOK })

		ww.Text([]byte("before")) //nolint:errcheck
		whileValid := ww.tick()
		mu.Lock()
		sessionOK = false
		mu.Unlock()
		afterEnd := ww.tick()
		results <- [2]bool{whileValid, afterEnd}
		<-gone // the handler's reader sees the connection end, so a shell would exit
	}))
	defer srv.Close()

	conn := dialTest(t, srv)
	var lines []string
	var closeErr *websocket.CloseError
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			closeErr, _ = err.(*websocket.CloseError)
			break
		}
		lines = append(lines, string(msg))
	}
	r := <-results
	if !r[0] {
		t.Error("a tick ended a stream whose session was still valid")
	}
	if r[1] {
		t.Error("a tick kept a stream going after its session ended")
	}
	if closeErr == nil || closeErr.Code != websocket.ClosePolicyViolation || closeErr.Text != "session ended" {
		t.Errorf("close = %v, want 1008 \"session ended\"", closeErr)
	}
	if len(lines) != 1 || lines[0] != "before" {
		t.Errorf("lines = %q, want just the output sent before the session ended", lines)
	}
}
