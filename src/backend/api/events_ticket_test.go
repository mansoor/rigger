package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/db"
)

func TestEventTicketWorksOnce(t *testing.T) {
	s := newTicketStore()
	c := &auth.Claims{UserID: 7}
	tk, err := s.issue(c)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s.redeem(tk); !ok || got != c {
		t.Fatalf("redeem = %v, %v; want the issuing session", got, ok)
	}
	if _, ok := s.redeem(tk); ok {
		t.Error("a ticket worked twice")
	}
}

func TestEventTicketExpires(t *testing.T) {
	s := newTicketStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	tk, err := s.issue(&auth.Claims{UserID: 7})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(eventTicketTTL)
	if _, ok := s.redeem(tk); ok {
		t.Error("an expired ticket worked")
	}
}

func TestEventTicketsAreRandomAndBounded(t *testing.T) {
	s := newTicketStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	c := &auth.Claims{UserID: 7}

	a, _ := s.issue(c)
	b, _ := s.issue(c)
	if a == b || len(a) < 43 {
		t.Errorf("tickets %q and %q: want distinct values of 256 bits", a, b)
	}
	for _, guess := range []string{"", "made-up"} {
		if _, ok := s.redeem(guess); ok {
			t.Errorf("redeem(%q) worked", guess)
		}
	}

	for i := len(s.tickets); i < maxEventTickets; i++ {
		s.tickets[fmt.Sprint("filler-", i)] = eventTicket{claims: c, expires: now.Add(eventTicketTTL)}
	}
	if _, err := s.issue(c); !errors.Is(err, errTooManyTickets) {
		t.Fatalf("issue past the cap: err = %v, want errTooManyTickets", err)
	}
	// Expired tickets are swept on the next issue, making room again.
	now = now.Add(eventTicketTTL)
	if _, err := s.issue(c); err != nil {
		t.Errorf("issue after the backlog expired: %v", err)
	}
	if len(s.tickets) != 1 {
		t.Errorf("%d tickets kept after the sweep, want 1", len(s.tickets))
	}
}

// The events stream opens only with a ticket. An access token in the URL — the
// old way, and the thing proxy logs captured — gets nothing.
func TestEventStreamWantsATicketNotAToken(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	svc := auth.NewService(d, "test-secret-not-real", 15)
	if _, err := svc.SetupAdmin("admin@example.com", "Admin", "Original-Passw0rd!"); err != nil {
		t.Fatal(err)
	}
	access, _, err := svc.Login2("admin@example.com", "Original-Passw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{auth: svc}

	for _, query := range []string{"", "?token=" + access, "?ticket=made-up"} {
		rec := httptest.NewRecorder()
		h.StreamEvents(rec, httptest.NewRequest(http.MethodGet, "/api/events"+query, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET /api/events%.20s…: status %d, want 401", query, rec.Code)
		}
	}

	// The ticket endpoint sits behind the normal bearer-token check.
	ticketHandler := svc.Middleware(http.HandlerFunc(h.EventsTicket))
	rec := httptest.NewRecorder()
	ticketHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/events/ticket", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("ticket without a session: status %d, want 401", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/events/ticket", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	rec = httptest.NewRecorder()
	ticketHandler.ServeHTTP(rec, req)
	var body struct{ Ticket string }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Ticket == "" {
		t.Fatalf("ticket with a session: status %d, body %s", rec.Code, rec.Body)
	}
	if c, ok := eventTickets.redeem(body.Ticket); !ok || c.UserID == 0 {
		t.Error("the issued ticket was not redeemable for the signed-in account")
	}
}
