package acme

import (
	"log"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
)

// renewWindow is how close to expiry a cert must be before we renew it.
const renewWindow = 30 * 24 * time.Hour

// Renewer periodically renews the out-of-band override certs Rigger issued (Traefik
// auto-renews only its OWN resolver certs, not the file-provider ones). It walks the
// acme_certs registry on an interval and re-runs lego for any cert within renewWindow
// of expiry, rewriting the file-provider cert + dynamic config in place.
type Renewer struct {
	store    *Store
	issuer   *Issuer
	interval time.Duration
}

// NewRenewer wires a renewer to the cert registry + issuer. Interval defaults to 12h.
func NewRenewer(d *db.DB, issuer *Issuer) *Renewer {
	return &Renewer{store: NewStore(d), issuer: issuer, interval: 12 * time.Hour}
}

// Run starts the renewal loop in a background goroutine. No-op (logs once) when DNS-01
// isn't configured, since out-of-band certs can't be issued/renewed without the token.
func (r *Renewer) Run() {
	if r.issuer == nil || !r.issuer.Enabled() {
		log.Printf("acme: renewer idle — no Cloudflare DNS token (override certs disabled)")
		return
	}
	go func() {
		r.runOnce()
		t := time.NewTicker(r.interval)
		defer t.Stop()
		for range t.C {
			r.runOnce()
		}
	}()
}

// runOnce renews every tracked cert within renewWindow of expiry (or with unknown
// expiry). Each failure is recorded and skipped; one bad cert never blocks the rest.
func (r *Renewer) runOnce() {
	certs, err := r.store.List()
	if err != nil {
		log.Printf("acme: renewer list failed: %v", err)
		return
	}
	now := time.Now()
	for _, c := range certs {
		if c.NotAfter != 0 && time.Unix(c.NotAfter, 0).Sub(now) > renewWindow {
			continue // plenty of life left
		}
		notAfter, err := r.issuer.Issue(c.Domain, c.Email, nil) // existing cert ⇒ lego renew
		if err != nil {
			log.Printf("acme: renew %s failed: %v", c.Domain, err)
			r.store.RecordError(c.Domain, c.Email, c.Workspace, c.Project, c.Env, err.Error()) //nolint:errcheck
			continue
		}
		r.store.Upsert(Record{ //nolint:errcheck
			Domain: c.Domain, Email: c.Email, Workspace: c.Workspace, Project: c.Project, Env: c.Env,
			NotAfter: notAfter.Unix(), IssuedAt: now.Unix(),
		})
		log.Printf("acme: renewed %s — now expires %s", c.Domain, notAfter.Format("2006-01-02"))
	}
}
