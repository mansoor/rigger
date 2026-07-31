package imagecheck

import "testing"

// splitRef decides which host is contacted, so getting it wrong sends a GHCR
// reference to Docker Hub as a repository path — which is exactly the bug this
// replaced, and it 404s in a way that reads as "could not reach registry".
func TestSplitRef(t *testing.T) {
	cases := []struct{ image, host, repo string }{
		// No host component: Docker Hub, and official images live under library/.
		{"mysql", dockerHubAPI, "library/mysql"},
		{"redis", dockerHubAPI, "library/redis"},
		// A single slash with no dot or colon in the first part is still Hub —
		// "minio" is an org, not a hostname.
		{"minio/minio", dockerHubAPI, "minio/minio"},
		{"mher/flower", dockerHubAPI, "mher/flower"},
		// A dot in the first component makes it a registry host.
		{"ghcr.io/mansoor/rigger", "ghcr.io", "mansoor/rigger"},
		{"quay.io/prometheus/prometheus", "quay.io", "prometheus/prometheus"},
		{"lscr.io/linuxserver/sonarr", "lscr.io", "linuxserver/sonarr"},
		{"registry.k8s.io/pause", "registry.k8s.io", "pause"},
		// A colon (port) does too, as does the literal "localhost".
		{"localhost:5001/mcl_sdm-app", "localhost:5001", "mcl_sdm-app"},
		{"localhost/thing", "localhost", "thing"},
		// docker.io is the reference hostname; the API lives elsewhere. The
		// library/ prefix still applies.
		{"docker.io/library/mysql", dockerHubAPI, "library/mysql"},
		{"docker.io/mysql", dockerHubAPI, "library/mysql"},
		{"index.docker.io/minio/minio", dockerHubAPI, "minio/minio"},
	}
	for _, c := range cases {
		host, repo := splitRef(c.image)
		if host != c.host || repo != c.repo {
			t.Errorf("splitRef(%q) = %q / %q, want %q / %q", c.image, host, repo, c.host, c.repo)
		}
	}
}

// TLS everywhere except a local registry, which is commonly plain HTTP — and
// requiring TLS there would mean it could never be checked at all.
func TestScheme(t *testing.T) {
	for _, host := range []string{"localhost", "localhost:5001", "127.0.0.1:5000"} {
		if got := scheme(host); got != "http" {
			t.Errorf("scheme(%q) = %q, want http", host, got)
		}
	}
	for _, host := range []string{"ghcr.io", "quay.io", dockerHubAPI, "registry.example.com:5000"} {
		if got := scheme(host); got != "https" {
			t.Errorf("scheme(%q) = %q, want https — anything over a network must use TLS", host, got)
		}
	}
}

// The challenge names where to get a token. Real headers from three registries.
func TestParseAuthChallenge(t *testing.T) {
	cases := []struct{ header, realm, service string }{
		{`Bearer realm="https://auth.docker.io/token",service="registry.docker.io"`,
			"https://auth.docker.io/token", "registry.docker.io"},
		{`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:user/image:pull"`,
			"https://ghcr.io/token", "ghcr.io"},
		{`Bearer realm="https://quay.io/v2/auth",service="quay.io"`,
			"https://quay.io/v2/auth", "quay.io"},
		// Case-insensitive scheme, and a realm with no service.
		{`bearer realm="https://example.com/token"`, "https://example.com/token", ""},
	}
	for _, c := range cases {
		realm, service := parseAuthChallenge(c.header)
		if realm != c.realm || service != c.service {
			t.Errorf("parseAuthChallenge(%q) = %q / %q, want %q / %q",
				c.header, realm, service, c.realm, c.service)
		}
	}
}

// A scope value legitimately contains a comma ("...:pull,push"), so splitting on
// every comma would truncate the realm that follows it.
func TestParseAuthChallengeCommaInsideQuotes(t *testing.T) {
	const h = `Bearer scope="repository:user/image:pull,push",realm="https://ghcr.io/token",service="ghcr.io"`
	realm, service := parseAuthChallenge(h)
	if realm != "https://ghcr.io/token" || service != "ghcr.io" {
		t.Errorf("got realm=%q service=%q", realm, service)
	}
}

func TestParseAuthChallengeRejectsNonBearer(t *testing.T) {
	for _, h := range []string{"", "Basic realm=\"x\"", "Bearer", "nonsense"} {
		if realm, _ := parseAuthChallenge(h); realm != "" {
			t.Errorf("parseAuthChallenge(%q) returned realm %q, want none", h, realm)
		}
	}
}

// GHCR caps a tag page at 100 and returns the OLDEST first, so without following
// the Link header "no newer stable version" is a confident answer derived from a
// list that stops in 2021.
func TestNextPageLink(t *testing.T) {
	cases := []struct{ header, want string }{
		{`</v2/home-assistant/home-assistant/tags/list?n=100&last=2021.8.0>; rel="next"`,
			"/v2/home-assistant/home-assistant/tags/list?n=100&last=2021.8.0"},
		// Unquoted rel, and extra attributes.
		{`</v2/x/tags/list?n=2&last=b>; rel=next; type="application/json"`,
			"/v2/x/tags/list?n=2&last=b"},
		// Several links: only rel="next" is followed.
		{`</v2/x/tags/list?first>; rel="prev", </v2/x/tags/list?second>; rel="next"`,
			"/v2/x/tags/list?second"},
	}
	for _, c := range cases {
		if got := nextPageLink(c.header); got != c.want {
			t.Errorf("nextPageLink(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

func TestNextPageLinkStops(t *testing.T) {
	for _, h := range []string{
		"",
		`</v2/x/tags/list?p=2>; rel="prev"`, // no next ⇒ last page
		"garbage",
		`</v2/x>`, // no rel at all
		// An absolute URL would send the token to another host; not followed.
		`<https://evil.example.com/v2/x/tags/list>; rel="next"`,
	} {
		if got := nextPageLink(h); got != "" {
			t.Errorf("nextPageLink(%q) = %q, want none", h, got)
		}
	}
}
