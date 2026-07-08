package envgen

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestResolveImageValueAppKey(t *testing.T) {
	// Empty APP_KEY → a correctly-shaped base64:<32 bytes> key, NOT a hex secret.
	got := ResolveImageValue("APP_KEY", "", nil, CryptoRand)
	if !strings.HasPrefix(got, "base64:") {
		t.Fatalf("APP_KEY should be base64: form, got %q", got)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "base64:"))
	if err != nil || len(raw) != 32 {
		t.Fatalf("APP_KEY must decode to 32 bytes: err=%v len=%d", err, len(raw))
	}

	// A placeholder value is replaced too.
	if g := ResolveImageValue("APP_KEY", "base64:CHANGE_ME", nil, CryptoRand); !strings.HasPrefix(g, "base64:") || strings.Contains(g, "CHANGE_ME") {
		t.Fatalf("placeholder APP_KEY not replaced: %q", g)
	}

	// A real existing key is preserved (stable across regens).
	real := "base64:q8f8Qk1m0m1m2m3m4m5m6m7m8m9mAmBmCmDmEmFmGmE="
	if g := ResolveImageValue("APP_KEY", "", map[string]string{"APP_KEY": real}, CryptoRand); g != real {
		t.Fatalf("existing APP_KEY not preserved: %q", g)
	}
	// A non-placeholder passed directly is kept.
	if g := ResolveImageValue("APP_KEY", real, nil, CryptoRand); g != real {
		t.Fatalf("real APP_KEY value changed: %q", g)
	}
}
