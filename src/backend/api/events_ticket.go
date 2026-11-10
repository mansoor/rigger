package api

// Opening the live-events stream without a token in the URL.
//
// EventSource, the browser API behind GET /api/events, can't send an
// Authorization header, so the stream used to take the access token as
// ?token=. URLs end up in reverse-proxy access logs and browser history, and a
// token there was a working credential. Now the page first asks for a ticket over
// the normal authenticated API, then opens the stream with it. A ticket is
// random, works once, expires after a minute, and opens nothing but this stream,
// so one found in a log is already spent.

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/mansoor/rigger/ui/internal/auth"
)

const (
	eventTicketTTL = time.Minute
	// maxEventTickets caps unredeemed tickets, so a signed-in client can't grow
	// the store without bound. Far above what open tabs need.
	maxEventTickets = 10000
)

var errTooManyTickets = errors.New("too many unredeemed event tickets; try again in a minute")

type eventTicket struct {
	claims  *auth.Claims
	expires time.Time
}

type ticketStore struct {
	mu      sync.Mutex
	tickets map[string]eventTicket
	now     func() time.Time
}

func newTicketStore() *ticketStore {
	return &ticketStore{tickets: map[string]eventTicket{}, now: time.Now}
}

// eventTickets is process-wide: the server that issues a ticket is the one that
// redeems it, within a minute.
var eventTickets = newTicketStore()

// issue returns a new ticket for the session in c.
func (s *ticketStore) issue(c *auth.Claims) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	t := base64.RawURLEncoding.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, e := range s.tickets {
		if !now.Before(e.expires) {
			delete(s.tickets, k)
		}
	}
	if len(s.tickets) >= maxEventTickets {
		return "", errTooManyTickets
	}
	s.tickets[t] = eventTicket{claims: c, expires: now.Add(eventTicketTTL)}
	return t, nil
}

// redeem spends a ticket and returns the session it was issued for. Each ticket
// works once, and only before it expires.
func (s *ticketStore) redeem(t string) (*auth.Claims, bool) {
	if t == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tickets[t]
	if !ok {
		return nil, false
	}
	delete(s.tickets, t)
	if !s.now().Before(e.expires) {
		return nil, false
	}
	return e.claims, true
}

// POST /api/events/ticket — a single-use ticket for opening GET /api/events.
func (h *Handler) EventsTicket(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	t, err := eventTickets.issue(claims)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ticket": t})
}
