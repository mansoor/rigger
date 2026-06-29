package proxyroutes

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// AccessRule is one IP rule in an access list. Action is "allow" or "deny".
// Address is an IP or CIDR. Traefik's ipAllowList is allow-only, so the renderer
// builds the source range from the Allow rules (everything else is denied); Deny
// rules are recorded for clarity and future use (see render.go effectiveAccess).
type AccessRule struct {
	Action  string `json:"action"`  // allow | deny
	Address string `json:"address"` // IP or CIDR
}

// AccessList is a reusable, named set of basic-auth users + IP rules that a proxy
// route can reference instead of configuring auth/IP inline (NPM "Access Lists").
type AccessList struct {
	ID        int64       `json:"id"`
	Name      string      `json:"name"`
	PassAuth  bool        `json:"pass_auth"` // forward the Authorization header to the upstream
	Users     []BasicUser `json:"users"`
	Rules     []AccessRule `json:"rules"`
	GeoMode   string      `json:"geo_mode"`  // off|allow|block — GeoIP country policy
	Countries []string    `json:"countries"` // ISO 3166-1 alpha-2 codes
	// Workspace scopes the list: "" = global (super-admin Proxy Service), else the workspace
	// key. Strictly isolated — a workspace only ever sees its own lists. See
	// docs/design/workspace-plugins-and-access-lists.md.
	Workspace string `json:"workspace"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

const alCols = `id, name, pass_auth, users, rules, geo_mode, countries, workspace, created_at, updated_at`

func scanAccessList(s interface{ Scan(...any) error }) (AccessList, error) {
	var a AccessList
	var passAuth int
	var usersJSON, rulesJSON, countriesJSON string
	if err := s.Scan(&a.ID, &a.Name, &passAuth, &usersJSON, &rulesJSON, &a.GeoMode, &countriesJSON, &a.Workspace, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return AccessList{}, err
	}
	a.PassAuth = passAuth != 0
	_ = json.Unmarshal([]byte(usersJSON), &a.Users)
	_ = json.Unmarshal([]byte(rulesJSON), &a.Rules)
	_ = json.Unmarshal([]byte(countriesJSON), &a.Countries)
	if a.Users == nil {
		a.Users = []BasicUser{}
	}
	if a.Rules == nil {
		a.Rules = []AccessRule{}
	}
	if a.Countries == nil {
		a.Countries = []string{}
	}
	if a.GeoMode == "" {
		a.GeoMode = "off"
	}
	return a, nil
}

// ListAccessLists returns every access list (all scopes), name-ordered. Used by the renderer.
func (s *Store) ListAccessLists() ([]AccessList, error) {
	return s.queryAccessLists(`SELECT ` + alCols + ` FROM proxy_access_lists ORDER BY name, id`)
}

// ListAccessListsByScope returns only the lists in one scope ("" = global / Proxy Service,
// else a workspace key). This is the strict-isolation read used by the API — a workspace
// never sees global or other-workspace lists.
func (s *Store) ListAccessListsByScope(workspace string) ([]AccessList, error) {
	return s.queryAccessLists(`SELECT `+alCols+` FROM proxy_access_lists WHERE workspace=? ORDER BY name, id`, workspace)
}

func (s *Store) queryAccessLists(q string, args ...any) ([]AccessList, error) {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessList{}
	for rows.Next() {
		a, err := scanAccessList(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AccessListMap returns all access lists keyed by id (for the renderer).
func (s *Store) AccessListMap() (map[int64]AccessList, error) {
	list, err := s.ListAccessLists()
	if err != nil {
		return nil, err
	}
	m := make(map[int64]AccessList, len(list))
	for _, a := range list {
		m[a.ID] = a
	}
	return m, nil
}

// GetAccessList returns one access list (found=false when absent).
func (s *Store) GetAccessList(id int64) (AccessList, bool, error) {
	a, err := scanAccessList(s.DB.QueryRow(`SELECT `+alCols+` FROM proxy_access_lists WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AccessList{}, false, nil
	}
	if err != nil {
		return AccessList{}, false, err
	}
	return a, true, nil
}

func alArgs(a AccessList) []any {
	u, _ := json.Marshal(a.Users)
	r, _ := json.Marshal(a.Rules)
	c, _ := json.Marshal(a.Countries)
	mode := a.GeoMode
	if mode == "" {
		mode = "off"
	}
	return []any{a.Name, b2i(a.PassAuth), string(u), string(r), mode, string(c), a.Workspace, a.CreatedAt, a.UpdatedAt}
}

// CreateAccessList inserts an access list and returns its new id.
func (s *Store) CreateAccessList(a AccessList) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO proxy_access_lists
		(name, pass_auth, users, rules, geo_mode, countries, workspace, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, alArgs(a)...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateAccessList writes all mutable fields of an existing access list.
func (s *Store) UpdateAccessList(a AccessList) error {
	args := append(alArgs(a), a.ID)
	_, err := s.DB.Exec(`UPDATE proxy_access_lists SET
		name=?, pass_auth=?, users=?, rules=?, geo_mode=?, countries=?, workspace=?, created_at=?, updated_at=? WHERE id=?`, args...)
	return err
}

// DeleteAccessList removes an access list and detaches it from any routes that used it.
func (s *Store) DeleteAccessList(id int64) error {
	if _, err := s.DB.Exec(`UPDATE proxy_routes SET access_list_id=0 WHERE access_list_id=?`, id); err != nil {
		return err
	}
	_, err := s.DB.Exec(`DELETE FROM proxy_access_lists WHERE id=?`, id)
	return err
}
