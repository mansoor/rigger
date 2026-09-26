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
func TestWriterSerializesPingsAndOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		ww := newWSWriter(c)

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

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v — a panic or interleaved frame would surface here", err)
		}
		if string(msg) == "done" {
			return
		}
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
