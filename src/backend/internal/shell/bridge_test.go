package shell

import "testing"

func TestNormalizeRegistryHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"registry.example.com", "registry.example.com"},
		{"https://registry.example.com", "registry.example.com"},
		{"https://registry.example.com/", "registry.example.com"},
		{"http://registry.example.com/v2/", "registry.example.com"},
		{"Registry.Example.COM", "registry.example.com"},
		{"registry.example.com:5000", "registry.example.com:5000"},
		{"https://registry.example.com:5000/path", "registry.example.com:5000"},
		{"  registry.example.com  ", "registry.example.com"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeRegistryHost(c.in); got != c.want {
			t.Errorf("normalizeRegistryHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
