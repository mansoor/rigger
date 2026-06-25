package settings

import (
	"testing"

	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/db"
)

// The workspace domain/auto-URL overrides resolve workspace → global: a workspace value
// wins when set, blank falls back to the global default.
func TestEffectiveDomainOverrides(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// Global defaults.
	SetAppSetting(d, "auto_url_mode", "localhost") //nolint:errcheck
	SetAppSetting(d, "app_host", "10.0.0.9")       //nolint:errcheck
	SetAppSetting(d, "apps_dns_provider", "")      //nolint:errcheck

	// A workspace with no overrides inherits the global defaults.
	if got := EffectiveAutoURLMode(d, "ws1"); got != "localhost" {
		t.Errorf("ws1 auto_url_mode = %q, want global localhost", got)
	}
	if got := EffectiveAutoURLHost(d, "ws1"); got != "10.0.0.9" {
		t.Errorf("ws1 auto_url_host = %q, want global 10.0.0.9", got)
	}
	if got := EffectiveDNSProvider(d, "ws1"); got != "" {
		t.Errorf("ws1 dns provider = %q, want global empty", got)
	}

	// A workspace with overrides wins over the global.
	SetWorkspaceSetting(d, "ws2", "auto_url_mode", "sslip")          //nolint:errcheck
	SetWorkspaceSetting(d, "ws2", "auto_url_host", "192.168.1.50")   //nolint:errcheck
	SetWorkspaceSetting(d, "ws2", "apps_dns_provider", "cloudflare") //nolint:errcheck
	if got := EffectiveAutoURLMode(d, "ws2"); got != "sslip" {
		t.Errorf("ws2 auto_url_mode = %q, want sslip (override)", got)
	}
	if got := EffectiveAutoURLHost(d, "ws2"); got != "192.168.1.50" {
		t.Errorf("ws2 auto_url_host = %q, want override", got)
	}
	if got := EffectiveDNSProvider(d, "ws2"); got != "cloudflare" {
		t.Errorf("ws2 dns provider = %q, want cloudflare (override)", got)
	}
	// ws1 is unaffected by ws2's overrides (still inherits global).
	if got := EffectiveAutoURLMode(d, "ws1"); got != "localhost" {
		t.Errorf("ws1 leaked ws2 override: got %q", got)
	}
}

// The workspace Cloudflare token is stored encrypted at rest; WorkspaceDNSToken decrypts
// it, and the stored value is NOT the plaintext.
func TestWorkspaceDNSTokenEncrypted(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()
	key, err := crypto.DeriveKey([]byte("test-jwt-secret-please-ignore"))
	if err != nil {
		t.Fatalf("derive key: %v", err)
	}

	// No token ⇒ "".
	if got := WorkspaceDNSToken(d, key, "ws"); got != "" {
		t.Errorf("unset token = %q, want empty", got)
	}

	const plain = "cf-zone-token-abc123"
	enc, err := crypto.Encrypt(key, []byte(plain))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	SetWorkspaceSetting(d, "ws", "apps_dns_token", enc) //nolint:errcheck

	// Stored value is ciphertext, not the plaintext.
	if raw := wsSetting(d, "ws", "apps_dns_token"); raw == plain || raw == "" {
		t.Errorf("stored token should be ciphertext, got %q", raw)
	}
	// WorkspaceDNSToken decrypts back to the plaintext.
	if got := WorkspaceDNSToken(d, key, "ws"); got != plain {
		t.Errorf("WorkspaceDNSToken = %q, want %q", got, plain)
	}
	// Wrong key ⇒ "" (never returns garbage / the ciphertext).
	other, _ := crypto.DeriveKey([]byte("a-different-jwt-secret-value"))
	if got := WorkspaceDNSToken(d, other, "ws"); got != "" {
		t.Errorf("WorkspaceDNSToken with wrong key = %q, want empty", got)
	}
}
