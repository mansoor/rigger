package managedregistry

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestConfigURLAndHost(t *testing.T) {
	cases := []struct {
		name      string
		cfg       Config
		wantURL   string
		wantHost  string
		wantHTTPS bool
	}{
		{"base domain", Config{BaseDomain: "apps.example.com"}, "registry.apps.example.com", "registry.apps.example.com", true},
		{"base domain trims", Config{BaseDomain: "  apps.example.com  "}, "registry.apps.example.com", "registry.apps.example.com", true},
		{"local default port", Config{}, "localhost:5000", "", false},
		{"local custom port", Config{Port: "5001"}, "localhost:5001", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.URL(); got != tc.wantURL {
				t.Errorf("URL = %q, want %q", got, tc.wantURL)
			}
			if got := tc.cfg.Host(); got != tc.wantHost {
				t.Errorf("Host = %q, want %q", got, tc.wantHost)
			}
			if got := tc.cfg.HTTPS(); got != tc.wantHTTPS {
				t.Errorf("HTTPS = %v, want %v", got, tc.wantHTTPS)
			}
		})
	}
}

func TestHtpasswdLineValidates(t *testing.T) {
	line, err := HtpasswdLine("rigger", "s3cret-pw")
	if err != nil {
		t.Fatalf("HtpasswdLine: %v", err)
	}
	user, hash, ok := strings.Cut(line, ":")
	if !ok || user != "rigger" {
		t.Fatalf("malformed htpasswd line %q", line)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("s3cret-pw")); err != nil {
		t.Errorf("bcrypt hash does not validate the password: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("wrong")); err == nil {
		t.Errorf("bcrypt hash validated a wrong password")
	}
}

func TestRunArgsBaseDomain(t *testing.T) {
	m := New(nil)
	args := strings.Join(m.runArgs(Config{BaseDomain: "apps.example.com", DNSProvider: "cloudflare"}), " ")
	for _, want := range []string{
		"--name " + Container,
		"--network " + Network,
		DataVolume + ":/var/lib/registry",
		"REGISTRY_AUTH=htpasswd",
		"REGISTRY_STORAGE_DELETE_ENABLED=true",
		"traefik.enable=true",
		"Host(`registry.apps.example.com`)",
		"tls.certresolver=dns",
		"loadbalancer.server.port=5000",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("base-domain runArgs missing %q in:\n%s", want, args)
		}
	}
	// Base-domain mode must NOT publish a host port (Traefik fronts it).
	if strings.Contains(args, "-p 5000:5000") {
		t.Errorf("base-domain runArgs should not publish a host port:\n%s", args)
	}
}

func TestRunArgsLocalNoDomain(t *testing.T) {
	m := New(nil)
	args := strings.Join(m.runArgs(Config{Port: "5001"}), " ")
	if !strings.Contains(args, "-p 5001:5000") {
		t.Errorf("local runArgs should publish the host port:\n%s", args)
	}
	if strings.Contains(args, "traefik.enable") {
		t.Errorf("local runArgs should not emit Traefik labels:\n%s", args)
	}
	// No DNS provider + no base domain → HTTP-01 resolver isn't referenced at all.
	if strings.Contains(args, "certresolver") {
		t.Errorf("local runArgs should not reference a cert resolver:\n%s", args)
	}
}

func TestRunArgsBaseDomainNoDNSUsesHTTP01(t *testing.T) {
	m := New(nil)
	args := strings.Join(m.runArgs(Config{BaseDomain: "apps.example.com"}), " ")
	if !strings.Contains(args, "tls.certresolver=letsencrypt") {
		t.Errorf("base-domain without DNS provider should use the letsencrypt (HTTP-01) resolver:\n%s", args)
	}
}
