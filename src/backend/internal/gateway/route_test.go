package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAndRemove(t *testing.T) {
	dir := t.TempDir()
	r := Route{
		PublicHost:  "app.example.com",
		UpstreamURL: "http://10.10.10.55:80",
		TLS:         true,
		Middlewares: []string{"rigger-loading@file"},
	}
	if err := Write(dir, "mcl", "wot", "dev", r); err != nil {
		t.Fatal(err)
	}
	p := FragPath(dir, "mcl", "wot", "dev")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("fragment not written: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		"Host(`app.example.com`)",
		"entryPoints:",
		"- websecure",
		"passHostHeader: true",
		`url: "http://10.10.10.55:80"`,
		"rigger-loading@file",
		"tls: {}",
		"gw-mcl-wot-dev:",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("fragment missing %q\n---\n%s", want, s)
		}
	}
	if err := Remove(dir, "mcl", "wot", "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("fragment should be gone after Remove")
	}
	// Remove is idempotent.
	if err := Remove(dir, "mcl", "wot", "dev"); err != nil {
		t.Errorf("second Remove should be a no-op, got %v", err)
	}
}

func TestHTTPRouteNoTLS(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, "w", "p", "e", Route{PublicHost: "p-e.10.0.0.1.nip.io", UpstreamURL: "http://10.0.0.2:80"}); err != nil {
		t.Fatal(err)
	}
	s, _ := os.ReadFile(filepath.Join(dir, "gw-w-p-e.yml"))
	if strings.Contains(string(s), "tls:") {
		t.Error("HTTP route must not emit tls")
	}
	if !strings.Contains(string(s), "- web\n") {
		t.Error("HTTP route should use the web entrypoint")
	}
}

func TestWriteValidates(t *testing.T) {
	if err := Write(t.TempDir(), "w", "p", "e", Route{PublicHost: "", UpstreamURL: "http://x:80"}); err == nil {
		t.Error("expected error for empty public host")
	}
}
