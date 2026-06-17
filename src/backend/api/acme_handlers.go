package api

import (
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/acme"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// domainLooksLocal reports a domain that can't get a public Let's Encrypt cert:
// localhost / *.localhost or a bare IP. Mirrors the frontend looksLocalOrIP guard.
func domainLooksLocal(domain string) bool {
	h := strings.ToLower(strings.TrimSpace(domain))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	return net.ParseIP(h) != nil
}

// maybeIssueOverrideCert issues an out-of-band Let's Encrypt cert after a deploy when
// the env's EFFECTIVE ACME email (env → workspace → global) differs from the global —
// i.e. it's a per-env/per-workspace override Traefik's own resolver can't honour. Only
// envs with an explicit public domain + SSL qualify (base-domain wildcards are served by
// Traefik's dns resolver under the global email). Best-effort: streamed to out, never
// affects deploy status. When the override is cleared, any prior override cert is removed.
func (h *Handler) maybeIssueOverrideCert(workspace, project, env string, out io.Writer) {
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, workspace, project))
	if err != nil {
		return
	}
	ec, ok := cfg.Environments[env]
	if !ok || !ec.SSLEnabled {
		return
	}
	domain := strings.TrimSpace(strings.ToLower(ec.Domain))
	if domain == "" || domainLooksLocal(domain) {
		return // no explicit public domain ⇒ Traefik's resolver (global email) handles it
	}
	global := strings.TrimSpace(settings.AppSetting(h.db, "acme_email"))
	email := settings.EffectiveAcmeEmail(h.db, workspace, ec.AcmeEmail)

	// No override (uses the global email): let Traefik's own resolver handle it, and
	// retire any override cert we previously issued for this domain.
	if email == "" || strings.EqualFold(email, global) {
		if _, found, _ := h.acmeCerts.Get(domain); found {
			h.acmeIssuer.Remove(domain)
			h.acmeCerts.Delete(domain) //nolint:errcheck
		}
		return
	}

	if !h.acmeIssuer.Enabled() {
		fmt.Fprintf(out, "\n\033[33m⚠ %s uses a custom ACME email (%s) but DNS-01 isn't configured (CF_DNS_API_TOKEN). Skipping the override cert — Traefik will serve the global-email cert instead.\033[0m\n", domain, email)
		h.acmeCerts.RecordError(domain, email, workspace, project, env, "CF_DNS_API_TOKEN not set") //nolint:errcheck
		return
	}

	fmt.Fprintf(out, "\n\033[36m▶ issuing Let's Encrypt cert for %s (account %s) via DNS-01...\033[0m\n", domain, email)
	notAfter, ierr := h.acmeIssuer.Issue(domain, email, out)
	if ierr != nil {
		fmt.Fprintf(out, "\033[33m⚠ override cert issuance failed (deploy unaffected): %s\033[0m\n", ierr.Error())
		h.acmeCerts.RecordError(domain, email, workspace, project, env, ierr.Error()) //nolint:errcheck
		return
	}
	h.acmeCerts.Upsert(acme.Record{ //nolint:errcheck
		Domain: domain, Email: email, Workspace: workspace, Project: project, Env: env,
		NotAfter: notAfter.Unix(), IssuedAt: time.Now().Unix(),
	})
	fmt.Fprintf(out, "\033[32m✓ cert published for %s — expires %s. Traefik's file provider serves it (no restart).\033[0m\n", domain, notAfter.Format("2006-01-02"))
}
