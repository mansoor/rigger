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

// wsCloseWait bounds how long Finish waits for the peer to answer the close
// frame. Long enough for a browser on a slow link, short enough that a peer which
// never answers cannot hold the handler open.
const wsCloseWait = 2 * time.Second

// wsWriter serializes writes to one connection, keeps it warm, and ends it
// properly. A handler's lifecycle is:
//
//	ww := newWSWriter(conn)
//	ww.Keepalive()
//	defer ww.Finish()
//	… read the opening message …
//	ww.Drain(onGone)      // or SetReader, if the handler reads for itself
//	… stream output with Text/Binary …
type wsWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex

	stopPing func()          // set by Keepalive
	readDone <-chan struct{} // closed when the connection's reader exits
	finished bool            // close frame already sent
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

// Keepalive starts pinging until the returned stop is called, or until Finish.
// Every ping is answered by a pong, so a handler that pings must also read — see
// Drain.
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
	stop = func() { once.Do(func() { close(done) }) }
	w.stopPing = stop
	return stop
}

// Drain becomes the connection's only reader, discarding incoming messages until
// the connection fails, then calls onGone (nil to ignore). Start it after the
// handler has read its opening message — gorilla allows one reader at a time.
//
// Not optional once pings are sent. gorilla processes inbound frames only inside
// a read call, so a handler that stops reading after its opening message leaves
// every pong the browser sends in the socket buffer. Closing a socket that still
// holds unread data makes the kernel send a reset instead of a clean close, and a
// reset can destroy the last lines of output in flight — exactly the line that
// says whether a deploy succeeded.
func (w *wsWriter) Drain(onGone func()) {
	done := make(chan struct{})
	w.readDone = done
	go func() {
		defer close(done)
		for {
			if _, _, err := w.conn.ReadMessage(); err != nil {
				if onGone != nil {
					onGone()
				}
				return
			}
		}
	}()
}

// SetReader registers a reader the handler runs itself (the terminal forwards
// what it reads to a shell), so Finish can wait for the peer's close reply.
func (w *wsWriter) SetReader(done <-chan struct{}) { w.readDone = done }

// Finish ends the stream the way the protocol intends: stop pinging, send a close
// frame with "normal closure", and give the peer a moment to answer.
//
// Without it the handler simply returned and gorilla dropped the TCP connection,
// so the browser reported close code 1006 — "abnormal closure" — at the end of
// every stream, successful or not. With close codes now shown to the user, that
// would put a "connection dropped, check your proxy" warning under every
// completed deploy. Safe to call more than once.
func (w *wsWriter) Finish() {
	if w.stopPing != nil {
		w.stopPing()
	}
	w.mu.Lock()
	if w.finished {
		w.mu.Unlock()
		return
	}
	w.finished = true
	msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	err := w.conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(wsCloseWait))
	w.mu.Unlock()

	// Wait for the peer's close reply, which ends the reader. Skip the wait when
	// the close frame could not be sent (the peer is already gone) or when no
	// reader is running (the handler failed before reading its opening message).
	if err != nil || w.readDone == nil {
		return
	}
	select {
	case <-w.readDone:
	case <-time.After(wsCloseWait):
	}
}
