// Package proxyroutes is the standalone reverse-proxy manager (the "Proxy Service"
// page): instance-level routes that map a public host/path to arbitrary upstreams
// (a Rigger container, a LAN device, a remote host), or a redirect, or the single
// catch-all default. Routes are persisted in SQLite (proxy_routes) and rendered into
// Traefik's file provider (/dynamic/proxy-*.yml) by render.go — independent of the
// project/compose model. See docs/design/proxy-service.md.
package proxyroutes

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/mansoor/rigger/ui/internal/db"
)

// Upstream is one backend target. >1 on a route load-balances.
type Upstream struct {
	Scheme string `json:"scheme"` // http | https
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Weight int    `json:"weight,omitempty"`
}

// BasicUser is one htpasswd entry (hash is bcrypt; Traefik basicAuth accepts it).
type BasicUser struct {
	User string `json:"user"`
	Hash string `json:"hash"`
}

// Location is an NPM-style custom location: a sub-path on the route's host(s) that
// forwards to its own upstream(s). It inherits the route's TLS / auth / headers. An
// optional ForwardPath rewrites the upstream path (sub-folder forwarding). Multiple
// upstreams load-balance, exactly like the route's own upstreams.
//
// Scheme/Host/Port are the legacy single-target fields kept for back-compat with rows
// written before per-location load balancing; Servers() reconciles the two shapes.
type Location struct {
	Path        string     `json:"path"`              // PathPrefix on the route's host(s), e.g. /api
	Upstreams   []Upstream `json:"upstreams"`         // load-balanced targets
	Scheme      string     `json:"scheme,omitempty"`  // legacy single target
	Host        string     `json:"host,omitempty"`    // legacy single target
	Port        int        `json:"port,omitempty"`    // legacy single target
	ForwardPath string     `json:"forward_path"`      // optional upstream sub-path; "" = forward as-is
}

// Servers returns the location's effective upstream list, folding the legacy single
// host/port into the slice form when no explicit upstreams are present.
func (l Location) Servers() []Upstream {
	if len(l.Upstreams) > 0 {
		return l.Upstreams
	}
	if l.Host != "" {
		return []Upstream{{Scheme: l.Scheme, Host: l.Host, Port: l.Port}}
	}
	return nil
}

// Route is one proxy entry. Secrets (the custom-cert private key) live in tls_key_enc
// and are never returned to clients (see api masking); the renderer decrypts on write.
type Route struct {
	ID                 int64      `json:"id"`
	Name               string     `json:"name"`
	Enabled            bool       `json:"enabled"`
	Type               string     `json:"type"`         // proxy | redirect
	IsDefault          bool       `json:"is_default"`   // catch-all for unmatched hosts
	DefaultMode        string     `json:"default_mode"` // page|404|403|close|redirect|proxy
	Host               string     `json:"host"`
	PathPrefix         string     `json:"path_prefix"`
	Upstreams          []Upstream `json:"upstreams"`
	PassHostHeader     bool       `json:"pass_host_header"`
	InsecureSkipVerify bool       `json:"insecure_skip_verify"`
	RedirectTo         string     `json:"redirect_to"`
	RedirectCode       int        `json:"redirect_code"`
	TLSMode            string     `json:"tls_mode"` // none|le-http|le-dns|existing|custom
	TLSCertRef         string     `json:"tls_cert_ref"`
	TLSCertPEM         string     `json:"tls_cert_pem"`        // custom: cert (public)
	TLSKeyEnc          string     `json:"-"`                   // custom: encrypted key (never serialized)
	HasKey             bool       `json:"has_key"`             // custom: a key is stored
	ACMEEmail          string     `json:"acme_email"`          // '' = inherit
	ForceHTTPS         bool       `json:"force_https"`
	HSTSSeconds        int        `json:"hsts_seconds"`
	HSTSSubdomains     bool       `json:"hsts_subdomains"`
	HSTSPreload        bool       `json:"hsts_preload"`
	AuthMode           string     `json:"auth_mode"` // none|basic
	AuthUsers          []BasicUser `json:"auth_users"`
	IPAllow            string     `json:"ip_allow"`
	AccessListID       int64      `json:"access_list_id"` // 0 = none; else use the named access list's users + IP rules
	SecurityHeaders    bool       `json:"security_headers"`
	StripPrefix        bool       `json:"strip_prefix"`
	WAF                bool       `json:"waf"`
	Cache              bool       `json:"cache"`
	Locations          []Location `json:"locations"`
	AcceptToS          bool       `json:"accept_tos"`
	Notes              string     `json:"notes"`
	CreatedAt          int64      `json:"created_at"`
	UpdatedAt          int64      `json:"updated_at"`
}

// Store persists proxy routes in SQLite.
type Store struct{ DB *db.DB }

func NewStore(d *db.DB) *Store { return &Store{DB: d} }

const cols = `id, name, enabled, type, is_default, default_mode, host, path_prefix,
	upstreams, pass_host_header, insecure_skip_verify, redirect_to, redirect_code,
	tls_mode, tls_cert_ref, tls_cert_pem, tls_key_enc, acme_email, force_https,
	hsts_seconds, hsts_subdomains, hsts_preload, auth_mode, auth_users, ip_allow,
	security_headers, strip_prefix, waf, cache, locations, accept_tos, notes, created_at, updated_at, access_list_id`

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func scan(s interface{ Scan(...any) error }) (Route, error) {
	var r Route
	var enabled, isDefault, passHost, insecure, forceHTTPS, hstsSub, hstsPre, secHdr, strip, waf, cache, acceptToS int
	var upstreamsJSON, authUsersJSON, locationsJSON string
	err := s.Scan(
		&r.ID, &r.Name, &enabled, &r.Type, &isDefault, &r.DefaultMode, &r.Host, &r.PathPrefix,
		&upstreamsJSON, &passHost, &insecure, &r.RedirectTo, &r.RedirectCode,
		&r.TLSMode, &r.TLSCertRef, &r.TLSCertPEM, &r.TLSKeyEnc, &r.ACMEEmail, &forceHTTPS,
		&r.HSTSSeconds, &hstsSub, &hstsPre, &r.AuthMode, &authUsersJSON, &r.IPAllow,
		&secHdr, &strip, &waf, &cache, &locationsJSON, &acceptToS, &r.Notes, &r.CreatedAt, &r.UpdatedAt, &r.AccessListID,
	)
	if err != nil {
		return Route{}, err
	}
	r.Enabled, r.IsDefault, r.PassHostHeader = enabled != 0, isDefault != 0, passHost != 0
	r.InsecureSkipVerify, r.ForceHTTPS = insecure != 0, forceHTTPS != 0
	r.HSTSSubdomains, r.HSTSPreload = hstsSub != 0, hstsPre != 0
	r.SecurityHeaders, r.StripPrefix, r.WAF, r.Cache = secHdr != 0, strip != 0, waf != 0, cache != 0
	r.AcceptToS = acceptToS != 0
	r.HasKey = r.TLSKeyEnc != ""
	_ = json.Unmarshal([]byte(upstreamsJSON), &r.Upstreams)
	_ = json.Unmarshal([]byte(authUsersJSON), &r.AuthUsers)
	_ = json.Unmarshal([]byte(locationsJSON), &r.Locations)
	if r.Upstreams == nil {
		r.Upstreams = []Upstream{}
	}
	if r.AuthUsers == nil {
		r.AuthUsers = []BasicUser{}
	}
	if r.Locations == nil {
		r.Locations = []Location{}
	}
	return r, nil
}

// List returns every route (default route(s) last for stable display).
func (s *Store) List() ([]Route, error) {
	rows, err := s.DB.Query(`SELECT ` + cols + ` FROM proxy_routes ORDER BY is_default, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Route{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns one route (found=false when absent).
func (s *Store) Get(id int64) (Route, bool, error) {
	r, err := scan(s.DB.QueryRow(`SELECT `+cols+` FROM proxy_routes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Route{}, false, nil
	}
	if err != nil {
		return Route{}, false, err
	}
	return r, true, nil
}

// argsFor builds the INSERT/UPDATE value list (everything but id).
func argsFor(r Route) []any {
	up, _ := json.Marshal(r.Upstreams)
	au, _ := json.Marshal(r.AuthUsers)
	loc, _ := json.Marshal(r.Locations)
	return []any{
		r.Name, b2i(r.Enabled), r.Type, b2i(r.IsDefault), r.DefaultMode, r.Host, r.PathPrefix,
		string(up), b2i(r.PassHostHeader), b2i(r.InsecureSkipVerify), r.RedirectTo, r.RedirectCode,
		r.TLSMode, r.TLSCertRef, r.TLSCertPEM, r.TLSKeyEnc, r.ACMEEmail, b2i(r.ForceHTTPS),
		r.HSTSSeconds, b2i(r.HSTSSubdomains), b2i(r.HSTSPreload), r.AuthMode, string(au), r.IPAllow,
		b2i(r.SecurityHeaders), b2i(r.StripPrefix), b2i(r.WAF), b2i(r.Cache), string(loc), b2i(r.AcceptToS), r.Notes,
		r.CreatedAt, r.UpdatedAt, r.AccessListID,
	}
}

// Create inserts a route and returns its new id.
func (s *Store) Create(r Route) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO proxy_routes (
		name, enabled, type, is_default, default_mode, host, path_prefix,
		upstreams, pass_host_header, insecure_skip_verify, redirect_to, redirect_code,
		tls_mode, tls_cert_ref, tls_cert_pem, tls_key_enc, acme_email, force_https,
		hsts_seconds, hsts_subdomains, hsts_preload, auth_mode, auth_users, ip_allow,
		security_headers, strip_prefix, waf, cache, locations, accept_tos, notes, created_at, updated_at, access_list_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, argsFor(r)...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Update writes all mutable fields of an existing route.
func (s *Store) Update(r Route) error {
	args := append(argsFor(r), r.ID)
	_, err := s.DB.Exec(`UPDATE proxy_routes SET
		name=?, enabled=?, type=?, is_default=?, default_mode=?, host=?, path_prefix=?,
		upstreams=?, pass_host_header=?, insecure_skip_verify=?, redirect_to=?, redirect_code=?,
		tls_mode=?, tls_cert_ref=?, tls_cert_pem=?, tls_key_enc=?, acme_email=?, force_https=?,
		hsts_seconds=?, hsts_subdomains=?, hsts_preload=?, auth_mode=?, auth_users=?, ip_allow=?,
		security_headers=?, strip_prefix=?, waf=?, cache=?, locations=?, accept_tos=?, notes=?, created_at=?, updated_at=?, access_list_id=?
		WHERE id=?`, args...)
	return err
}

// Delete removes a route.
func (s *Store) Delete(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM proxy_routes WHERE id=?`, id)
	return err
}
