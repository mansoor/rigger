package api

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/auth"
)

// Env-card TLS cert status. Traefik stores the ACME certs it manages in acme.json
// (HTTP-01, per-host) and acme-dns.json (DNS-01, wildcard) under the traefik-certs
// volume, which rigger mounts read-only at /certs. This endpoint finds the cert that
// serves an env's domain and reports its issuer + expiry so the env card can show
// "🔒 LE · expires in N days". It reads the REAL served cert (no network probe), so it
// also covers the file-provider store once per-email override certs land there.

type certInfoResponse struct {
	Found        bool   `json:"found"`
	Domain       string `json:"domain,omitempty"`
	Issuer       string `json:"issuer,omitempty"`        // issuer CN (or O)
	LetsEncrypt  bool   `json:"lets_encrypt,omitempty"`  // issuer is Let's Encrypt
	NotAfter     string `json:"not_after,omitempty"`     // RFC3339
	DaysRemaining int   `json:"days_remaining,omitempty"`
	Expired      bool   `json:"expired,omitempty"`
}

// acmeStore mirrors the parts of Traefik's acme.json we need. Go's JSON decoder is
// case-insensitive on keys, so this matches both v2 ("Certificates"/"Domain"/"Main")
// and any lowercase variants.
type acmeStore map[string]struct {
	Certificates []struct {
		Domain struct {
			Main string   `json:"main"`
			Sans []string `json:"sans"`
		} `json:"domain"`
		Certificate string `json:"certificate"` // base64-encoded PEM (full chain)
	} `json:"certificates"`
}

// GetCertInfo — GET …/envs/{env}/cert?domain=foo.example.com. Reports the live TLS
// cert (issuer + expiry) Traefik serves for the given domain. viewer+. The domain is
// supplied by the caller (the env card already knows its resolved domain); it's only
// used to look the cert up in our own ACME store, so it's not sensitive.
func (h *Handler) GetCertInfo(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	domain := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("domain")))
	writeJSON(w, http.StatusOK, h.certInfoForDomain(domain))
}

// certInfoForDomain reports the live TLS cert (issuer + expiry) serving `domain`, reading
// Traefik's ACME stores + the file-provider store; if none is served yet, it falls back to
// the tracked override store's recorded expiry. Zero value (Found:false) when nothing is
// known. Shared by the env-card cert endpoint and the proxy-routes list.
func (h *Handler) certInfoForDomain(domain string) certInfoResponse {
	domain = strings.TrimSpace(strings.ToLower(domain))
	if domain == "" {
		return certInfoResponse{}
	}
	certsDir := os.Getenv("TRAEFIK_CERTS_DIR")
	if certsDir == "" {
		certsDir = "/certs"
	}
	now := time.Now()
	if cert := findServedCert(certsDir, domain); cert != nil {
		issuer := cert.Issuer.CommonName
		if issuer == "" && len(cert.Issuer.Organization) > 0 {
			issuer = cert.Issuer.Organization[0]
		}
		le := strings.Contains(strings.ToLower(strings.Join(cert.Issuer.Organization, " ")), "let's encrypt") ||
			strings.Contains(strings.ToLower(issuer), "let's encrypt")
		return certInfoResponse{
			Found: true, Domain: domain, Issuer: issuer, LetsEncrypt: le,
			NotAfter:      cert.NotAfter.UTC().Format(time.RFC3339),
			DaysRemaining: int(cert.NotAfter.Sub(now).Hours() / 24),
			Expired:       now.After(cert.NotAfter),
		}
	}
	// Not served yet — report the tracked (out-of-band lego) cert's recorded expiry if any.
	if h.acmeCerts != nil {
		if rec, found, _ := h.acmeCerts.Get(domain); found && rec.NotAfter > 0 {
			t := time.Unix(rec.NotAfter, 0)
			return certInfoResponse{
				Found: true, Domain: domain, LetsEncrypt: true,
				NotAfter:      t.UTC().Format(time.RFC3339),
				DaysRemaining: int(t.Sub(now).Hours() / 24),
				Expired:       now.After(t),
			}
		}
	}
	return certInfoResponse{}
}

// findServedCert scans Traefik's ACME stores for the leaf cert that serves `domain`
// (exact main/SAN match or a `*.parent` wildcard covering it). Returns nil if none.
func findServedCert(dir, domain string) *x509.Certificate {
	for _, f := range []string{"acme.json", "acme-dns.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		var store acmeStore
		if json.Unmarshal(raw, &store) != nil {
			continue
		}
		for _, resolver := range store {
			for _, c := range resolver.Certificates {
				names := append([]string{c.Domain.Main}, c.Domain.Sans...)
				matched := false
				for _, n := range names {
					if certNameMatches(n, domain) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
				if leaf := parseLeaf(c.Certificate); leaf != nil {
					return leaf
				}
			}
		}
	}
	return nil
}

// certNameMatches reports whether a cert SAN/CN `name` serves `domain` — an exact
// (case-insensitive) match, or a single-label wildcard (`*.parent` ⇒ `label.parent`).
func certNameMatches(name, domain string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	if name == domain {
		return true
	}
	if strings.HasPrefix(name, "*.") {
		suffix := name[1:] // ".parent"
		if strings.HasSuffix(domain, suffix) {
			label := domain[:len(domain)-len(suffix)]
			return label != "" && !strings.Contains(label, ".")
		}
	}
	return false
}

// parseLeaf base64-decodes Traefik's stored cert, then returns the first (leaf)
// certificate in the PEM chain.
func parseLeaf(b64 string) *x509.Certificate {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil
	}
	for block, rest := pem.Decode(der); block != nil; block, rest = pem.Decode(rest) {
		if block.Type != "CERTIFICATE" {
			continue
		}
		if leaf, err := x509.ParseCertificate(block.Bytes); err == nil {
			return leaf
		}
		return nil
	}
	return nil
}
