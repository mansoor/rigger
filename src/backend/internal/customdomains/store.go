// Package customdomains manages external domains (e.g. app.example.com) attached to an
// environment IN ADDITION to its auto subdomain, Render-style. Ownership must be proven
// (see verify.go) before a domain is routed by Traefik and gets a per-host cert.
package customdomains

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
)

// Domain is one attached external domain for an env.
type Domain struct {
	ID         int64      `json:"id"`
	Workspace  string     `json:"workspace"`
	Project    string     `json:"project"`
	Env        string     `json:"env"`
	Domain     string     `json:"domain"`
	Token      string     `json:"token"`
	Verified   bool       `json:"verified"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Normalize lower-cases a domain and strips any scheme, path, port or trailing dot so
// "HTTPS://App.Example.com/" and "app.example.com" compare equal.
func Normalize(d string) string {
	d = strings.TrimSpace(strings.ToLower(d))
	if d == "" {
		return ""
	}
	if strings.Contains(d, "://") {
		if u, err := url.Parse(d); err == nil && u.Host != "" {
			d = u.Host
		}
	}
	d = strings.TrimSuffix(d, "/")
	if i := strings.IndexByte(d, '/'); i >= 0 {
		d = d[:i]
	}
	if i := strings.IndexByte(d, ':'); i >= 0 {
		d = d[:i]
	}
	return strings.TrimSuffix(d, ".")
}

// newToken returns a 32-hex-char random verification token.
func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func scan(rows *sql.Rows) ([]Domain, error) {
	var out []Domain
	for rows.Next() {
		var d Domain
		var verified int
		var verifiedAt sql.NullTime
		if err := rows.Scan(&d.ID, &d.Workspace, &d.Project, &d.Env, &d.Domain, &d.Token, &verified, &verifiedAt, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.Verified = verified != 0
		if verifiedAt.Valid {
			t := verifiedAt.Time
			d.VerifiedAt = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

const cols = `id, workspace, project, env, domain, token, verified, verified_at, created_at`

// List returns all custom domains for an env (verified + pending).
func List(d *db.DB, ws, project, env string) ([]Domain, error) {
	rows, err := d.Query(`SELECT `+cols+` FROM custom_domains WHERE workspace=? AND project=? AND env=? ORDER BY domain`, ws, project, env)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scan(rows)
}

// VerifiedDomains returns just the hostnames of the VERIFIED custom domains for an env —
// what composegen needs to emit extra routers. Never errors (returns nil on failure) so
// it can be called inline on the deploy path.
func VerifiedDomains(d *db.DB, ws, project, env string) []string {
	rows, err := d.Query(`SELECT domain FROM custom_domains WHERE workspace=? AND project=? AND env=? AND verified=1 ORDER BY domain`, ws, project, env)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// Get returns one domain by id.
func Get(d *db.DB, id int64) (*Domain, error) {
	rows, err := d.Query(`SELECT `+cols+` FROM custom_domains WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list, err := scan(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("custom domain %d not found", id)
	}
	return &list[0], nil
}

// Create attaches a new (unverified) domain to an env, generating its challenge token.
// The domain is normalized + globally unique; a duplicate returns an error.
func Create(d *db.DB, ws, project, env, domain string) (*Domain, error) {
	domain = Normalize(domain)
	if domain == "" {
		return nil, fmt.Errorf("domain is required")
	}
	if !strings.Contains(domain, ".") {
		return nil, fmt.Errorf("%q is not a valid domain", domain)
	}
	res, err := d.Exec(`INSERT INTO custom_domains (workspace, project, env, domain, token) VALUES (?,?,?,?,?)`,
		ws, project, env, domain, newToken())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("%q is already attached to an environment", domain)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return Get(d, id)
}

// SetVerified flips a domain's verified flag (stamping verified_at on success).
func SetVerified(d *db.DB, id int64, verified bool) error {
	if verified {
		_, err := d.Exec(`UPDATE custom_domains SET verified=1, verified_at=CURRENT_TIMESTAMP WHERE id=?`, id)
		return err
	}
	_, err := d.Exec(`UPDATE custom_domains SET verified=0, verified_at=NULL WHERE id=?`, id)
	return err
}

// Delete removes a domain mapping.
func Delete(d *db.DB, id int64) error {
	_, err := d.Exec(`DELETE FROM custom_domains WHERE id=?`, id)
	return err
}

// DeleteForEnv removes all domain mappings for an env (called when the env/project is
// deleted so a re-created same-key project doesn't inherit stale domains).
func DeleteForEnv(d *db.DB, ws, project, env string) error {
	_, err := d.Exec(`DELETE FROM custom_domains WHERE workspace=? AND project=? AND env=?`, ws, project, env)
	return err
}

// DeleteForProject removes all domain mappings for every env of a project.
func DeleteForProject(d *db.DB, ws, project string) error {
	_, err := d.Exec(`DELETE FROM custom_domains WHERE workspace=? AND project=?`, ws, project)
	return err
}
