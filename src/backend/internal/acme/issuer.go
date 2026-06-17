// Package acme issues per-email Let's Encrypt certificates OUT-OF-BAND so a
// per-workspace / per-environment ACME email can take effect WITHOUT recreating the
// primary Traefik (whose resolver email is static, one per resolver).
//
// How it works: a one-shot `lego` container (Traefik's own ACME engine) obtains the
// cert via the DNS-01 challenge (Cloudflare), writing it into the shared rigger-dynamic
// volume. Rigger then drops a small dynamic TLS config into the same volume, which
// Traefik's file provider hot-reloads — no restart, no routing blip. The env's router
// just sets tls=true (no certresolver); Traefik matches the SNI to this file cert.
//
// Issuance is always local to the rigger host: the central proxy and the file-provider
// volume live there, and every env routes through rigger-traefik.
package acme

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/executor"
)

// DefaultDynamicDir is the file-provider directory Traefik watches; rigger mounts the
// same volume here read-write (see src/docker-compose.yml rigger-dynamic).
const DefaultDynamicDir = "/dynamic"

// defaultLegoImage is pinned for reproducibility; override with RIGGER_LEGO_IMAGE.
const defaultLegoImage = "goacme/lego:v4.21.0"

// defaultVolume is the named docker volume backing DefaultDynamicDir — passed to the
// one-shot lego container so its output lands where Traefik (and rigger) can read it.
const defaultVolume = "rigger-dynamic"

// Issuer obtains + publishes out-of-band override certs.
type Issuer struct {
	Exec   executor.Executor // runs `docker` on the rigger host (local daemon)
	DynDir string            // file-provider dir as seen by rigger AND Traefik
	Image  string            // lego image
	Token  string            // Cloudflare DNS API token (CF_DNS_API_TOKEN)
	Volume string            // docker volume name to mount into the lego container
}

// New builds an Issuer from the environment (CF_DNS_API_TOKEN, optional overrides).
func New(exec executor.Executor) *Issuer {
	dir := os.Getenv("RIGGER_DYNAMIC_DIR")
	if dir == "" {
		dir = DefaultDynamicDir
	}
	img := os.Getenv("RIGGER_LEGO_IMAGE")
	if img == "" {
		img = defaultLegoImage
	}
	vol := os.Getenv("RIGGER_DYNAMIC_VOLUME")
	if vol == "" {
		vol = defaultVolume
	}
	return &Issuer{
		Exec:   executor.Default(exec),
		DynDir: dir,
		Image:  img,
		Token:  strings.TrimSpace(os.Getenv("CF_DNS_API_TOKEN")),
		Volume: vol,
	}
}

// Enabled reports whether out-of-band DNS-01 issuance is possible (token present).
func (i *Issuer) Enabled() bool { return i.Token != "" }

// certBase reproduces lego's certificate filename rule: the domain, with a leading
// wildcard "*" rewritten to "_" (we only issue exact domains, but keep parity).
func certBase(domain string) string {
	return strings.ReplaceAll(strings.TrimSpace(strings.ToLower(domain)), "*", "_")
}

func (i *Issuer) certFile(domain string) string {
	return filepath.Join(i.DynDir, "certificates", certBase(domain)+".crt")
}
func (i *Issuer) keyFile(domain string) string {
	return filepath.Join(i.DynDir, "certificates", certBase(domain)+".key")
}
func (i *Issuer) configFile(domain string) string {
	return filepath.Join(i.DynDir, certBase(domain)+".yml")
}

// legoArgs builds the `docker run` argument list for a lego command (action is "run"
// for first issuance or "renew" for renewal). lego global flags MUST precede the
// action subcommand. The Cloudflare provider reads CF_DNS_API_TOKEN from the env.
func (i *Issuer) legoArgs(domain, email, action string) []string {
	args := []string{
		"run", "--rm",
		"-e", "CF_DNS_API_TOKEN=" + i.Token,
		"-v", i.Volume + ":/data",
		i.Image,
		"--accept-tos",
		"--email", email,
		"--dns", "cloudflare",
		"--domains", domain,
		"--path", "/data",
		action,
	}
	if action == "renew" {
		// Only renew when within 30 days of expiry (idempotent re-runs are cheap).
		args = append(args, "--days", "30")
	}
	return args
}

// dynamicConfigYAML is the Traefik file-provider config that registers the issued cert.
// Paths are as TRAEFIK sees them (same volume mounted at DynDir on both sides).
func (i *Issuer) dynamicConfigYAML(domain string) string {
	return "tls:\n" +
		"  certificates:\n" +
		"    - certFile: " + i.certFile(domain) + "\n" +
		"      keyFile: " + i.keyFile(domain) + "\n"
}

// CertExists reports whether a cert for domain has already been obtained (so a re-run
// should renew rather than issue fresh).
func (i *Issuer) CertExists(domain string) bool {
	_, err := os.Stat(i.certFile(domain))
	return err == nil
}

// Issue obtains (or renews) the cert for domain under email and publishes the dynamic
// TLS config. Progress is streamed to out (may be nil). Returns the cert's NotAfter.
func (i *Issuer) Issue(domain, email string, out io.Writer) (time.Time, error) {
	if !i.Enabled() {
		return time.Time{}, fmt.Errorf("out-of-band issuance needs a Cloudflare DNS token (CF_DNS_API_TOKEN); per-env ACME email overrides require DNS-01")
	}
	domain = strings.TrimSpace(strings.ToLower(domain))
	email = strings.TrimSpace(email)
	if domain == "" || email == "" {
		return time.Time{}, fmt.Errorf("domain and email are required")
	}
	action := "run"
	if i.CertExists(domain) {
		action = "renew"
	}
	var buf bytes.Buffer
	sink := io.Writer(&buf)
	if out != nil {
		sink = io.MultiWriter(&buf, out)
	}
	if err := i.Exec.Docker(executor.Spec{Args: i.legoArgs(domain, email, action), Stdout: sink, Stderr: sink}); err != nil {
		return time.Time{}, fmt.Errorf("lego %s for %s failed: %s", action, domain, strings.TrimSpace(buf.String()))
	}
	if err := os.WriteFile(i.configFile(domain), []byte(i.dynamicConfigYAML(domain)), 0o644); err != nil {
		return time.Time{}, fmt.Errorf("write dynamic config: %w", err)
	}
	return i.CertNotAfter(domain)
}

// CertNotAfter parses the published cert and returns its expiry (zero on error).
func (i *Issuer) CertNotAfter(domain string) (time.Time, error) {
	raw, err := os.ReadFile(i.certFile(domain))
	if err != nil {
		return time.Time{}, err
	}
	for block, rest := pem.Decode(raw); block != nil; block, rest = pem.Decode(rest) {
		if block.Type != "CERTIFICATE" {
			continue
		}
		if leaf, err := x509.ParseCertificate(block.Bytes); err == nil {
			return leaf.NotAfter, nil
		}
		break
	}
	return time.Time{}, fmt.Errorf("no parseable certificate for %s", domain)
}

// Remove deletes the published cert + dynamic config for domain (called when SSL is
// turned off or the env is removed). Best-effort.
func (i *Issuer) Remove(domain string) {
	_ = os.Remove(i.configFile(domain))
	_ = os.Remove(i.certFile(domain))
	_ = os.Remove(i.keyFile(domain))
}
