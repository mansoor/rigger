package clouddns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockCF is a tiny in-memory Cloudflare API for the client's happy paths.
type mockCF struct {
	records map[string]record // id -> record
	nextID  int
	lastPUT string
}

func newMock() *mockCF { return &mockCF{records: map[string]record{}} }

func (m *mockCF) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, result any) {
		raw, _ := json.Marshal(result)
		json.NewEncoder(w).Encode(apiResp{Success: true, Result: raw}) //nolint:errcheck
	}
	mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		write(w, []zone{{ID: "zone1", Name: "example.com"}, {ID: "zone2", Name: "other.com"}})
	})
	mux.HandleFunc("/zones/zone1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			name := r.URL.Query().Get("name")
			var found []record
			for _, rec := range m.records {
				if rec.Name == name && rec.Type == "A" {
					found = append(found, rec)
				}
			}
			write(w, found)
		case http.MethodPost:
			var rec record
			json.NewDecoder(r.Body).Decode(&rec) //nolint:errcheck
			m.nextID++
			rec.ID = "rec" + string(rune('0'+m.nextID))
			m.records[rec.ID] = rec
			write(w, rec)
		}
	})
	mux.HandleFunc("/zones/zone1/dns_records/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/zones/zone1/dns_records/")
		switch r.Method {
		case http.MethodPut:
			var rec record
			json.NewDecoder(r.Body).Decode(&rec) //nolint:errcheck
			rec.ID = id
			m.records[id] = rec
			m.lastPUT = rec.Content
			write(w, rec)
		case http.MethodDelete:
			delete(m.records, id)
			write(w, map[string]string{"id": id})
		}
	})
	return mux
}

func newClient(t *testing.T) (*Cloudflare, *mockCF) {
	m := newMock()
	srv := httptest.NewServer(m.handler(t))
	t.Cleanup(srv.Close)
	c := NewCloudflare("test-token")
	c.api = srv.URL
	return c, m
}

func TestUpsertCreatesThenUpdates(t *testing.T) {
	c, m := newClient(t)
	ctx := context.Background()
	fqdn := "app.apps.example.com"

	if err := c.UpsertA(ctx, fqdn, "1.2.3.4"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(m.records) != 1 {
		t.Fatalf("want 1 record, got %d", len(m.records))
	}
	var got record
	for _, r := range m.records {
		got = r
	}
	if got.Content != "1.2.3.4" || got.Proxied {
		t.Errorf("record = %+v, want content 1.2.3.4 proxied=false", got)
	}

	// Second upsert with a new IP must UPDATE (not create a duplicate).
	if err := c.UpsertA(ctx, fqdn, "5.6.7.8"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(m.records) != 1 {
		t.Errorf("upsert should update in place, got %d records", len(m.records))
	}
	if m.lastPUT != "5.6.7.8" {
		t.Errorf("update content = %q, want 5.6.7.8", m.lastPUT)
	}
}

func TestDelete(t *testing.T) {
	c, m := newClient(t)
	ctx := context.Background()
	fqdn := "x.example.com"
	c.UpsertA(ctx, fqdn, "9.9.9.9") //nolint:errcheck
	if err := c.DeleteA(ctx, fqdn); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(m.records) != 0 {
		t.Errorf("record should be gone, got %d", len(m.records))
	}
	// Deleting a non-existent record is a no-op, not an error.
	if err := c.DeleteA(ctx, fqdn); err != nil {
		t.Errorf("delete of absent record should be nil, got %v", err)
	}
}

func TestZoneLongestSuffix(t *testing.T) {
	c, _ := newClient(t)
	id, err := c.zoneFor(context.Background(), "deep.sub.example.com")
	if err != nil || id != "zone1" {
		t.Fatalf("zoneFor = %q, %v; want zone1", id, err)
	}
	if _, err := c.zoneFor(context.Background(), "nope.invalid"); err == nil {
		t.Error("expected error for a domain with no accessible zone")
	}
}
