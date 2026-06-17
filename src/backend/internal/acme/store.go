package acme

import "github.com/mansoor/rigger/ui/internal/db"

// Record is one tracked out-of-band override cert. The email is the ACME account
// contact (not stored in the cert), so the renewal scheduler needs it persisted.
type Record struct {
	Domain    string
	Email     string
	Workspace string
	Project   string
	Env       string
	NotAfter  int64 // unix seconds (0 = unknown)
	IssuedAt  int64
	LastError string
}

// Store persists the (domain → email) override-cert registry in SQLite.
type Store struct{ DB *db.DB }

func NewStore(d *db.DB) *Store { return &Store{DB: d} }

// Upsert records a successful issuance/renewal (clears any prior error).
func (s *Store) Upsert(r Record) error {
	_, err := s.DB.Exec(`
		INSERT INTO acme_certs (domain, email, workspace, project, env, not_after, issued_at, last_error)
		VALUES (?,?,?,?,?,?,?,'')
		ON CONFLICT(domain) DO UPDATE SET
			email=excluded.email, workspace=excluded.workspace, project=excluded.project,
			env=excluded.env, not_after=excluded.not_after, issued_at=excluded.issued_at, last_error=''`,
		r.Domain, r.Email, r.Workspace, r.Project, r.Env, r.NotAfter, r.IssuedAt)
	return err
}

// RecordError stores the (domain, email) intent with a failure message so a retry can
// still find the pair; keeps any previously-known not_after.
func (s *Store) RecordError(domain, email, ws, project, env, msg string) error {
	_, err := s.DB.Exec(`
		INSERT INTO acme_certs (domain, email, workspace, project, env, last_error)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(domain) DO UPDATE SET
			email=excluded.email, workspace=excluded.workspace, project=excluded.project,
			env=excluded.env, last_error=excluded.last_error`,
		domain, email, ws, project, env, msg)
	return err
}

// Get returns the record for a domain (found=false when absent).
func (s *Store) Get(domain string) (Record, bool, error) {
	var r Record
	err := s.DB.QueryRow(`SELECT domain, email, workspace, project, env, not_after, issued_at, last_error
		FROM acme_certs WHERE domain=?`, domain).
		Scan(&r.Domain, &r.Email, &r.Workspace, &r.Project, &r.Env, &r.NotAfter, &r.IssuedAt, &r.LastError)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return Record{}, false, nil
		}
		return Record{}, false, err
	}
	return r, true, nil
}

// List returns all tracked override certs.
func (s *Store) List() ([]Record, error) {
	rows, err := s.DB.Query(`SELECT domain, email, workspace, project, env, not_after, issued_at, last_error
		FROM acme_certs ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.Domain, &r.Email, &r.Workspace, &r.Project, &r.Env, &r.NotAfter, &r.IssuedAt, &r.LastError); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete drops a domain's record (when SSL is disabled or the env/override is removed).
func (s *Store) Delete(domain string) error {
	_, err := s.DB.Exec(`DELETE FROM acme_certs WHERE domain=?`, domain)
	return err
}
