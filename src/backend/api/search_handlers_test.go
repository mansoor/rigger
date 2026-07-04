package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// TestSearchIndex verifies the palette's deep-search endpoint surfaces env-var KEYS,
// routes, custom domains and pipelines — while never leaking an env-var VALUE and
// deduping a key repeated across environments.
func TestSearchIndex(t *testing.T) {
	tmp := t.TempDir()
	ws, proj := "acme", "app"
	// The dev env's NEXT_PUBLIC_API_BASE_URL value is a canary: it must never appear.
	cfg := `{
		"project": {"name": "My App"},
		"environments": {
			"dev":  {"env_vars": {"NEXT_PUBLIC_API_BASE_URL": "http://sekret-value-xyz", "APP_KEY": "base64:abc"}},
			"prod": {"env_vars": {"NEXT_PUBLIC_API_BASE_URL": "http://prod"}}
		},
		"routes": [
			{"match": "/api/v1", "service": "api", "type": "path"},
			{"match": "/", "service": "web", "type": "path"}
		]
	}`
	dir := wspath.ProjectDir(tmp, ws, proj)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(
		`INSERT INTO custom_domains (workspace, project, env, domain, token, verified, is_primary) VALUES (?,?,?,?,?,?,?)`,
		ws, proj, "prod", "app.example.com", "tok", 1, 1,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(
		`INSERT INTO pipelines (workspace, project, name, stages, enabled) VALUES (?,?,?,?,?)`,
		ws, proj, "Release", "[]", 1,
	); err != nil {
		t.Fatal(err)
	}

	h := &Handler{db: d, workspacesDir: tmp}

	r := httptest.NewRequest("GET", "/api/workspaces/"+ws+"/search-index", nil)
	r.SetPathValue("workspace", ws)
	r = r.WithContext(auth.WithClaims(r.Context(), &auth.Claims{UserID: 1, Username: "root", Role: auth.RoleSuperadmin}))
	w := httptest.NewRecorder()
	h.SearchIndex(w, r)

	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sekret-value-xyz") {
		t.Fatalf("env-var VALUE leaked into the search index:\n%s", w.Body.String())
	}

	var entries []searchEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	has := func(cat, sub string) bool {
		for _, e := range entries {
			if e.Category == cat && strings.Contains(e.Title, sub) {
				return true
			}
		}
		return false
	}
	if !has("Env var", "NEXT_PUBLIC_API_BASE_URL") {
		t.Error("missing env-var key entry")
	}
	if !has("Route", "/api/v1") {
		t.Error("missing route entry")
	}
	if !has("Domain", "app.example.com") {
		t.Error("missing custom-domain entry")
	}
	if !has("Pipeline", "Release") {
		t.Error("missing pipeline entry")
	}

	// The key exists in both dev and prod, but should collapse to a single entry.
	n := 0
	for _, e := range entries {
		if e.Category == "Env var" && e.Title == "NEXT_PUBLIC_API_BASE_URL" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("expected env-var keys deduped across envs (1 entry), got %d", n)
	}
}
