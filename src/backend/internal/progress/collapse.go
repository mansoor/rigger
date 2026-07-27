// Package progress collapses Docker's per-layer pull chatter into a single
// updating line.
//
// With stdout piped rather than attached to a TTY, `docker compose` prints one
// line per layer per progress tick:
//
//	a Pulling
//	619be1103602 Already exists
//	e88463a31d6d Pulling fs layer
//	e88463a31d6d Downloading [==>        ]  16.38kB/360.6kB
//	e88463a31d6d Downloading [========>  ]  114.7kB/360.6kB
//	…
//	e88463a31d6d Pull complete
//	a Pulled
//
// A single small image costs ~40 lines; a multi-service stack runs into the
// hundreds, which buries the deploy output that actually matters and bloats the
// recorded run stored for history.
//
// # Wire protocol
//
// A line whose first byte is '\r' is TRANSIENT: the UI replaces the previous
// transient line rather than appending, and the run recorder drops it entirely.
// Every other line is permanent and behaves exactly as it always did. That keeps
// this readable in any consumer that doesn't know the convention — a transient
// line is still just a line of text.
package progress

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Transient marks a line the UI should overwrite and the recorder should drop.
const Transient = '\r'

// IsTransient reports whether a line is a progress update rather than output to
// keep. Safe on an empty line.
func IsTransient(line string) bool {
	return len(line) > 0 && line[0] == Transient
}

// Layer ids are 12 hex characters; service names are arbitrary. That's the only
// reliable way to tell ` e88463a31d6d Downloading …` from ` app Pulling `.
var layerID = regexp.MustCompile(`^[0-9a-f]{12}$`)

// ` <id> Downloading [===>   ]  16.38kB/360.6kB`  →  current / total
var sizePair = regexp.MustCompile(`([0-9.]+\s*[kKMGT]?B)\s*/\s*([0-9.]+\s*[kKMGT]?B)`)

type layer struct {
	phase string
	done  bool
	got   int64 // bytes downloaded
	size  int64 // layer size, when known
}

// Collapser wraps the writer a command streams to. Feed it the raw stream; it
// forwards everything except pull chatter, which it replaces with one transient
// line and a permanent line per image.
type Collapser struct {
	w io.Writer

	mu       sync.Mutex
	buf      []byte
	layers   map[string]*layer
	order    []string
	lastEmit time.Time
	dirty    bool // state changed since the last transient emit

	// Injectable so tests don't depend on wall-clock timing.
	now      func() time.Time
	interval time.Duration
}

// New returns a Collapser writing to w.
func New(w io.Writer) *Collapser {
	return &Collapser{
		w:        w,
		layers:   map[string]*layer{},
		now:      time.Now,
		interval: 150 * time.Millisecond,
	}
}

func (c *Collapser) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.buf = append(c.buf, p...)
	for {
		i := bytes.IndexByte(c.buf, '\n')
		if i < 0 {
			break
		}
		line := string(c.buf[:i])
		c.buf = c.buf[i+1:]
		if err := c.line(strings.TrimSuffix(line, "\r")); err != nil {
			return len(p), err
		}
	}
	// Report the whole slice consumed: a partial line is buffered, not dropped.
	return len(p), nil
}

// Flush writes any buffered partial line. Call it once the command has exited.
func (c *Collapser) Flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.buf) == 0 {
		return nil
	}
	line := string(c.buf)
	c.buf = nil
	return c.line(strings.TrimSuffix(line, "\r"))
}

func (c *Collapser) line(line string) error {
	id, rest, ok := split(line)
	if !ok {
		return c.pass(line)
	}

	if layerID.MatchString(id) {
		c.layer(id, rest)
		return c.maybeEmit(false)
	}

	// Service-level lines. `Pulling` is redundant once the transient line is
	// showing, but `Pulled` is the one thing worth keeping in the record.
	switch {
	case rest == "Pulling":
		return c.maybeEmit(false)
	case rest == "Pulled":
		if err := c.maybeEmit(true); err != nil {
			return err
		}
		return c.pass("✓ Pulled " + id)
	}
	return c.pass(line)
}

// split takes ` <token> <rest>` or `<token>: <rest>` and returns the token and
// the trimmed remainder.
func split(line string) (string, string, bool) {
	s := strings.TrimSpace(line)
	if s == "" {
		return "", "", false
	}
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSuffix(s[:i], ":"), strings.TrimSpace(s[i:]), true
}

func (c *Collapser) layer(id, rest string) {
	l := c.layers[id]
	if l == nil {
		l = &layer{}
		c.layers[id] = l
		c.order = append(c.order, id)
	}
	c.dirty = true

	phase := rest
	if i := strings.IndexAny(rest, "["); i > 0 {
		phase = strings.TrimSpace(rest[:i])
	}
	l.phase = phase

	if got, size, ok := parsePair(rest); ok {
		// Extracting reports the same byte range again; only downloads count
		// toward the transfer total, or the figure would read double.
		if phase == "Downloading" {
			l.got, l.size = got, size
		} else if l.size == 0 {
			l.size = size
		}
	}

	switch phase {
	case "Download complete", "Verifying Checksum":
		if l.size > 0 {
			l.got = l.size
		}
	case "Pull complete", "Already exists":
		l.done = true
		if l.size > 0 {
			l.got = l.size
		}
	}
}

func parsePair(s string) (int64, int64, bool) {
	m := sizePair.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	a, ok1 := parseSize(m[1])
	b, ok2 := parseSize(m[2])
	return a, b, ok1 && ok2
}

// parseSize reads Docker's SI-style sizes ("32B", "16.38kB", "14.71MB").
func parseSize(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool { return r != '.' && (r < '0' || r > '9') })
	if i <= 0 {
		return 0, false
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, false
	}
	mult := float64(1)
	switch strings.ToLower(strings.TrimSpace(s[i:])) {
	case "b":
		mult = 1
	case "kb":
		mult = 1e3
	case "mb":
		mult = 1e6
	case "gb":
		mult = 1e9
	case "tb":
		mult = 1e12
	default:
		return 0, false
	}
	// Round rather than truncate: 16.38 * 1e3 is 16379.999… in binary floating
	// point, and truncating would report a byte short on every parse.
	return int64(math.Round(n * mult)), true
}

// maybeEmit writes the aggregate transient line, rate-limited unless forced.
func (c *Collapser) maybeEmit(force bool) error {
	if !c.dirty || len(c.layers) == 0 {
		return nil
	}
	now := c.now()
	if !force && now.Sub(c.lastEmit) < c.interval {
		return nil
	}
	c.lastEmit = now
	c.dirty = false

	var done int
	var got, size int64
	downloading, extracting := false, false
	for _, id := range c.order {
		l := c.layers[id]
		if l.done {
			done++
		}
		switch l.phase {
		case "Downloading":
			downloading = true
		case "Extracting":
			extracting = true
		}
		got += l.got
		size += l.size
	}

	// Report the slowest thing still happening; downloading outranks extracting
	// because that's what the user is waiting on. No "done" state here — the
	// permanent "✓ Pulled x" line says that. An earlier version derived it from
	// done == total, which read "Pulled" for a split second at the start, when
	// the only layer discovered so far was a cached one.
	verb := "Pulling"
	switch {
	case downloading:
		verb = "Downloading"
	case extracting:
		verb = "Extracting"
	}

	line := fmt.Sprintf("⏳ %s · %d/%d layers", verb, done, len(c.layers))
	if size > 0 {
		line += " · " + humanize(got) + "/" + humanize(size)
	}
	_, err := io.WriteString(c.w, string(Transient)+line+"\n")
	return err
}

func humanize(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.0f kB", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func (c *Collapser) pass(line string) error {
	_, err := io.WriteString(c.w, line+"\n")
	return err
}

// ─────────────────────────────────────────────────────────────────────────────

// Filter drops transient lines, for the writer that records a run's output to
// history. Line-buffered, because a Write may split a line anywhere.
type Filter struct {
	w   io.Writer
	buf []byte
}

func NewFilter(w io.Writer) *Filter { return &Filter{w: w} }

func (f *Filter) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	for {
		i := bytes.IndexByte(f.buf, '\n')
		if i < 0 {
			break
		}
		line := string(f.buf[:i+1])
		f.buf = f.buf[i+1:]
		if IsTransient(line) {
			continue
		}
		if _, err := io.WriteString(f.w, line); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// Flush writes any trailing partial line, unless it is transient.
func (f *Filter) Flush() error {
	if len(f.buf) == 0 || IsTransient(string(f.buf)) {
		f.buf = nil
		return nil
	}
	line := string(f.buf)
	f.buf = nil
	_, err := io.WriteString(f.w, line)
	return err
}
