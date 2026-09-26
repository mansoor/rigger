package api

// Keeping a WebSocket alive through a reverse proxy.
//
// Rigger's SSE endpoint already sends a heartbeat comment every 25s "to keep the
// connection alive through proxies". The WebSockets never got the same
// treatment, and they need it more: a log stream following a quiet container can
// go minutes without a byte, and a terminal left open sends nothing at all.
//
// nginx's proxy_read_timeout defaults to 60s and Nginx Proxy Manager inherits
// it — its websocket toggle only adds the Upgrade/Connection headers, it does not
// change timeouts. So an idle stream is closed by the proxy, the browser reports
// close code 1006, and the UI shows a connection error that looks like a Rigger
// fault. Traefik and Cloudflare have their own idle limits; a ping is the fix for
// all of them and needs no configuration from the operator.
//
// Ping frames only — deliberately no read deadline. Requiring a pong within a
// window would mean treating a silent-but-healthy connection as dead, which is
// the exact bug being fixed here. Peer death is already noticed by the read loops
// and by write failures.
//
// gorilla/websocket panics on concurrent writes, and a panic in a spawned output
// goroutine is not recovered by net/http — it would take the process down. Since
// the ping goroutine is a second writer, every write goes through one mutex.

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsPingInterval matches the SSE heartbeat: comfortably inside a 60s proxy
// timeout, with room for one ping to be lost.
const wsPingInterval = 25 * time.Second

// wsPingTimeout bounds a single ping write so a wedged connection can't block
// the keepalive goroutine forever.
const wsPingTimeout = 10 * time.Second

// wsWriter serializes writes to one connection and can keep it warm.
type wsWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func newWSWriter(c *websocket.Conn) *wsWriter { return &wsWriter{conn: c} }

// Text sends a text frame. Errors are returned so a caller streaming output can
// notice the peer is gone; callers writing one-off messages ignore them.
func (w *wsWriter) Text(b []byte) error {
	return w.write(websocket.TextMessage, b)
}

// Binary sends a binary frame — terminal output, which is not valid UTF-8.
func (w *wsWriter) Binary(b []byte) error {
	return w.write(websocket.BinaryMessage, b)
}

func (w *wsWriter) write(messageType int, b []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteMessage(messageType, b)
}

// ping sends a ping control frame.
func (w *wsWriter) ping() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsPingTimeout))
}

// Keepalive starts pinging until the returned stop is called. Callers defer stop
// so the goroutine cannot outlive the handler.
//
// A failed ping stops the pinging and nothing else: the connection is already
// broken, and the read loops own deciding what that means. Tearing down a stream
// from here would risk killing a live command over one dropped frame.
func (w *wsWriter) Keepalive() (stop func()) {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(wsPingInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := w.ping(); err != nil {
					return
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}
