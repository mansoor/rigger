package proxyroutes

import (
	"testing"

	"github.com/mansoor/rigger/ui/internal/db"
)

// TestStoreCreateRoundTrip exercises the SQLite INSERT/SELECT path that the API
// uses on save — never covered before, while render_test only calls routeYAML.
func TestStoreCreateRoundTrip(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := NewStore(d)

	in := Route{
		Name: "Jellyfin", Enabled: true, Type: "proxy",
		Host: "media.example.com", PassHostHeader: true,
		Upstreams: []Upstream{{Scheme: "http", Host: "192.168.1.50", Port: 8096}},
		TLSMode:   "none",
		Locations: []Location{{Path: "/api", Upstreams: []Upstream{{Scheme: "http", Host: "10.0.0.5", Port: 80}}}},
	}
	id, err := st.Create(in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, ok, err := st.Get(id)
	if err != nil || !ok {
		t.Fatalf("Get(%d): ok=%v err=%v", id, ok, err)
	}
	if got.Name != "Jellyfin" || len(got.Upstreams) != 1 || got.Upstreams[0].Port != 8096 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if len(got.Locations) != 1 || len(got.Locations[0].Servers()) != 1 {
		t.Fatalf("location round-trip mismatch: %+v", got.Locations)
	}

	// Update path too.
	got.Name = "Jellyfin2"
	if err := st.Update(got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	list, err := st.List()
	if err != nil || len(list) != 1 || list[0].Name != "Jellyfin2" {
		t.Fatalf("List after update: len=%d err=%v", len(list), err)
	}
}
