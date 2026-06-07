package keygen

import "testing"

func TestDerive(t *testing.T) {
	cases := []struct {
		name string
		min  int
		want string
	}{
		// The user's 3-char spec.
		{"web", 3, "web"},
		{"acme corp", 3, "acc"},
		{"web app svc", 3, "was"},
		{"a b c d", 3, "abc"},          // 4+ words -> first char of first 3
		{"alpha beta gamma delta", 3, "abg"},
		// Separators: dash + underscore behave like spaces.
		{"web-app", 3, "wea"},
		{"my_cool_service", 3, "mcs"},
		// Uppercase in the display name lowercases into the key.
		{"Acme Corporation", 3, "acc"},
		{"WebApp", 3, "web"},           // no camelCase split -> one word
		// Configurable longer keys.
		{"acme corp", 5, "acmco"},
		{"web", 5, "web"},              // can't reach 5 -> best-effort 3
		{"acme corp service", 5, "accos"}, // 3 words, min5: 2+2+1
		{"a bcdef", 4, "abcd"},         // top-up pulls from the longer word
		{"go", 3, "go"},                // short single word
		// Single-char min.
		{"web", 1, "w"},
	}
	for _, c := range cases {
		if got := Derive(c.name, c.min); got != c.want {
			t.Errorf("Derive(%q, %d) = %q, want %q", c.name, c.min, got, c.want)
		}
	}
}

func TestDeriveEmpty(t *testing.T) {
	if got := Derive("   ---  ", 3); got != "" {
		t.Errorf("Derive(non-alnum) = %q, want \"\"", got)
	}
}

func setOf(keys ...string) func(string) bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return func(s string) bool { return m[s] }
}

func TestResolveUnique(t *testing.T) {
	// No conflict: base is returned as-is.
	if got := ResolveUnique("web", 3, 4, setOf()); got != "web" {
		t.Errorf("no-conflict = %q, want web", got)
	}
	// One conflict (default 3/4 budget): appends the first suffix char (digit 2).
	if got := ResolveUnique("web", 3, 4, setOf("web")); got != "web2" {
		t.Errorf("one-conflict = %q, want web2", got)
	}
	// Several taken: walks the suffix alphabet (2..9 then a...).
	taken := setOf("web", "web2", "web3", "web4", "web5", "web6", "web7", "web8", "web9")
	if got := ResolveUnique("web", 3, 4, taken); got != "weba" {
		t.Errorf("suffix-walk = %q, want weba", got)
	}
	// min==max: no room to append, so cycle the last char (web -> wea, etc.).
	got := ResolveUnique("web", 3, 3, setOf("web"))
	if len(got) != 3 || got == "web" {
		t.Errorf("min==max conflict = %q, want a distinct 3-char key", got)
	}
}

func TestNormalizeAndValid(t *testing.T) {
	if got := Normalize("Web-App!"); got != "webapp" {
		t.Errorf("Normalize = %q, want webapp", got)
	}
	if !Valid("acc", 3, 4) {
		t.Error("acc should be valid for 3..4")
	}
	if Valid("ab", 3, 4) {
		t.Error("ab too short for 3..4")
	}
	if Valid("abcde", 3, 4) {
		t.Error("abcde too long for 3..4")
	}
	if Valid("AB1", 3, 4) {
		t.Error("uppercase must be invalid")
	}
}
