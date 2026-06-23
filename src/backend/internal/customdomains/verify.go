package customdomains

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ChallengeHost is the DNS name a TXT verification record must live at.
func ChallengeHost(domain string) string { return "_rigger-challenge." + Normalize(domain) }

// TXTValue is the exact TXT record value proving ownership of a domain.
func TXTValue(token string) string { return "rigger-verify=" + token }

// FilePath is the well-known path a verify file must be served at (relative).
func FilePath(token string) string { return "/.well-known/rigger-verify/" + token }

// Verify confirms control of domain via any supported method, in order:
//   - TXT:   _rigger-challenge.<domain> contains rigger-verify=<token>
//   - CNAME: <domain> resolves (canonically) to target (the env's auto subdomain)
//   - file:  http://<domain>/.well-known/rigger-verify/<token> serves the token
//
// Returns the method that succeeded ("txt"|"cname"|"file"), or an error summarizing what
// was checked. target may be empty (skips the CNAME check).
func Verify(domain, token, target string) (string, error) {
	domain = Normalize(domain)
	target = Normalize(target)
	var failures []string

	// TXT
	if ok, detail := checkTXT(domain, token); ok {
		return "txt", nil
	} else if detail != "" {
		failures = append(failures, "TXT: "+detail)
	}

	// CNAME → target
	if target != "" {
		if ok, detail := checkCNAME(domain, target); ok {
			return "cname", nil
		} else if detail != "" {
			failures = append(failures, "CNAME: "+detail)
		}
	}

	// HTTP file
	if ok, detail := checkFile(domain, token); ok {
		return "file", nil
	} else if detail != "" {
		failures = append(failures, "file: "+detail)
	}

	if len(failures) == 0 {
		failures = append(failures, "no matching TXT, CNAME or verify file found")
	}
	return "", fmt.Errorf("could not verify %s — %s", domain, strings.Join(failures, "; "))
}

func resolver() *net.Resolver { return net.DefaultResolver }

func checkTXT(domain, token string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	recs, err := resolver().LookupTXT(ctx, ChallengeHost(domain))
	if err != nil {
		return false, "" // no record / NXDOMAIN — silent, this method just isn't set up
	}
	want := TXTValue(token)
	for _, r := range recs {
		if strings.TrimSpace(r) == want {
			return true, ""
		}
	}
	return false, fmt.Sprintf("record(s) present but none equal %q", want)
}

func checkCNAME(domain, target string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cname, err := resolver().LookupCNAME(ctx, domain)
	if err != nil {
		return false, ""
	}
	got := strings.TrimSuffix(strings.ToLower(cname), ".")
	if got == target {
		return true, ""
	}
	return false, fmt.Sprintf("resolves to %q, expected %q", got, target)
}

func checkFile(domain, token string) (bool, string) {
	// Do NOT follow cross-host redirects (an attacker could bounce us to a site they
	// control); allow same-host redirects only.
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Hostname() != Normalize(domain) {
				return http.ErrUseLastResponse
			}
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	url := "http://" + domain + FilePath(token)
	resp, err := client.Get(url)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("%s returned HTTP %d", url, resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if strings.Contains(string(body), token) {
		return true, ""
	}
	return false, "file served but did not contain the token"
}
