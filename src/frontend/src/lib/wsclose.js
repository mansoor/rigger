// Why a stream ended, in words.
//
// A WebSocket `error` event carries no detail — the spec deliberately withholds
// it so a page can't probe the network. The close code is the only signal there
// is, and every one of these streams was throwing it away: an idle log stream
// killed by a proxy and a rejected connection both printed
// "--- connection error ---", which tells the operator nothing and sends them
// looking in the wrong place.
//
// 1006 is the one worth naming. It means the connection vanished without a close
// frame, which in practice is almost always a reverse proxy timing out an idle
// upstream connection. Rigger now pings to prevent that (see wskeepalive.go), so
// seeing 1006 after this means something else is closing the connection — and the
// message should say where to look.

const REASONS = {
  1000: 'stream closed',
  1001: 'stream closed — page navigated away',
  1005: 'stream closed',
  1011: 'stream closed — server error',
  1012: 'stream closed — server restarting',
  1013: 'stream closed — server busy, try again',
}

// closeReason returns a plain-text explanation for a CloseEvent.
export function closeReason(e) {
  const code = e?.code
  if (code === 1006) {
    return 'connection dropped without a close frame (1006) — if you reach Rigger ' +
      'through a reverse proxy, check its read timeout; nginx defaults to 60s'
  }
  if (REASONS[code]) return REASONS[code]
  // A server-sent reason is more specific than anything guessable from the code.
  const detail = (e?.reason || '').trim()
  if (detail) return `stream closed (${code}): ${detail}`
  return code ? `stream closed (code ${code})` : 'stream closed'
}

// A clean close needs no colour; anything else is worth the operator's eye.
const isClean = (e) => e?.code === 1000 || e?.code === 1001 || e?.code === 1005 || !e?.code

const DIM = '\x1b[2m'
const RED = '\x1b[31m'
const OFF = '\x1b[0m'

// closeLine formats a close for a viewer that holds an array of lines.
export function closeLine(e) {
  const colour = isClean(e) ? DIM : RED
  return `${colour}--- ${closeReason(e)} ---${OFF}`
}

// closeTermLine formats a close for an xterm instance, which needs CRLF and
// wants blank space around the notice.
export function closeTermLine(e) {
  return `\r\n${closeLine(e)}\r\n`
}
