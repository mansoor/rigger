package api

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/proxyroutes"
)

// Proxy Service — standalone reverse-proxy manager (docs/design/proxy-service.md).
// Instance-level routes (super-admin only) rendered into Traefik's file provider.
// All routes here are mounted under the adminOnly dispatcher in main.go.

// renderProxy regenerates the Traefik file-provider config from the DB. Plugin
// middleware refs are emitted only when that plugin is enabled instance-wide (PX-6).
func (h *Handler) renderProxy() error {
	waf := h.appSetting("proxy_waf_enabled") == "true"
	cache := h.appSetting("proxy_cache_enabled") == "true"
	geo := h.appSetting("proxy_geoip_enabled") == "true"
	return proxyroutes.Render(h.db, proxyroutes.DynDir(), h.cryptoKey, waf, cache, geo)
}

func (h *Handler) proxyStore() *proxyroutes.Store { return proxyroutes.NewStore(h.db) }

// recoverProxy turns a panic in a proxy handler into a logged, JSON 500 instead of a
// silent connection reset (which surfaces in the UI as a detail-less "Save failed").
func recoverProxy(w http.ResponseWriter, op string) {
	if v := recover(); v != nil {
		log.Printf("proxy: %s handler panic: %v\n%s", op, v, debug.Stack())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("internal error (%s): %v", op, v)})
	}
}

// RenderProxyRoutes re-renders the file-provider config at boot (drift repair). Writes
// the base file even when no routes exist. Errors are logged, never fatal.
func (h *Handler) RenderProxyRoutes() {
	if err := h.renderProxy(); err != nil {
		log.Printf("proxy: initial render failed: %v", err)
	}
}

// proxyReq is the create/update payload. It shadows the secret-bearing fields: auth
// users carry a plaintext Password (hashed here), and the custom TLS key arrives as
// TLSKey (encrypted here). Both are write-only — GET never returns them.
type proxyReq struct {
	proxyroutes.Route
	AuthUsers []struct {
		User     string `json:"user"`
		Password string `json:"password"`
	} `json:"auth_users"`
	TLSKey string `json:"tls_key"`
}

// mask blanks the secret material before returning a route to a client (bcrypt hashes
// and the encrypted key never leave the server).
func mask(r proxyroutes.Route) proxyroutes.Route {
	r.TLSKeyEnc = ""
	for i := range r.AuthUsers {
		r.AuthUsers[i].Hash = ""
	}
	return r
}

// toRoute folds a request onto a stored route. On update, existing is the current row
// so blank passwords keep their hash and a blank TLS key keeps the stored one.
func (h *Handler) toRoute(req proxyReq, existing *proxyroutes.Route) (proxyroutes.Route, error) {
	r := req.Route
	now := time.Now().Unix()
	r.UpdatedAt = now
	if existing != nil {
		r.ID = existing.ID
		r.CreatedAt = existing.CreatedAt
	} else {
		r.CreatedAt = now
	}

	// Basic-auth users: hash new passwords; preserve unchanged ones by username.
	prior := map[string]string{}
	if existing != nil {
		for _, u := range existing.AuthUsers {
			prior[u.User] = u.Hash
		}
	}
	users := make([]proxyroutes.BasicUser, 0, len(req.AuthUsers))
	for _, u := range req.AuthUsers {
		name := strings.TrimSpace(u.User)
		if name == "" {
			continue
		}
		hash := prior[name]
		if u.Password != "" {
			b, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
			if err != nil {
				return proxyroutes.Route{}, err
			}
			hash = string(b)
		}
		if hash == "" {
			continue // a new user with no password is dropped
		}
		users = append(users, proxyroutes.BasicUser{User: name, Hash: hash})
	}
	r.AuthUsers = users

	// Custom TLS key: encrypt a newly-supplied key, else keep the existing one.
	if strings.TrimSpace(req.TLSKey) != "" {
		enc, err := crypto.Encrypt(h.cryptoKey, []byte(req.TLSKey))
		if err != nil {
			return proxyroutes.Route{}, err
		}
		r.TLSKeyEnc = enc
	} else if existing != nil {
		r.TLSKeyEnc = existing.TLSKeyEnc
	}
	return r, nil
}

// validate enforces the basic invariants before persisting.
func validateRoute(r proxyroutes.Route) string {
	if strings.TrimSpace(r.Name) == "" {
		return "name is required"
	}
	if (r.TLSMode == "le-http" || r.TLSMode == "le-dns") && !r.AcceptToS {
		return "accept the Let's Encrypt Terms of Service to use a Let's Encrypt certificate"
	}
	if r.IsDefault {
		return "" // catch-all: host/upstream rules don't apply
	}
	if strings.TrimSpace(r.Host) == "" && strings.TrimSpace(r.PathPrefix) == "" {
		return "a host (or path) is required"
	}
	if r.Type == "redirect" {
		if strings.TrimSpace(r.RedirectTo) == "" {
			return "a redirect target URL is required"
		}
		return ""
	}
	if len(r.Upstreams) == 0 {
		return "at least one upstream is required"
	}
	for _, u := range r.Upstreams {
		if strings.TrimSpace(u.Host) == "" {
			return "every upstream needs a host"
		}
	}
	return ""
}

// issueOverrideCerts obtains a cert for each of the route's domains under a per-route
// ACME email override, out-of-band via the existing DNS-01 issuer (the same mechanism
// as per-env override certs). No-op when no override / non-LE mode. Returns an error
// when an override is requested but DNS-01 issuance isn't available.
func (h *Handler) issueOverrideCerts(rt proxyroutes.Route) error {
	if strings.TrimSpace(rt.ACMEEmail) == "" || (rt.TLSMode != "le-http" && rt.TLSMode != "le-dns") {
		return nil
	}
	if h.acmeIssuer == nil || !h.acmeIssuer.Enabled() {
		return fmt.Errorf("a per-route ACME email override needs a Cloudflare DNS token (Settings → General → DNS provider)")
	}
	for _, d := range strings.FieldsFunc(rt.Host, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if _, err := h.acmeIssuer.Issue(d, strings.TrimSpace(rt.ACMEEmail), nil); err != nil {
			return fmt.Errorf("issue cert for %s: %w", d, err)
		}
	}
	// NOTE: proxy override certs are issued once here; auto-renewal via the acme
	// scheduler is a follow-up (don't record with a zero expiry — that re-issue-loops).
	return nil
}

// ListProxyRoutes — GET /api/proxy/routes.
func (h *Handler) ListProxyRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := h.proxyStore().List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]proxyroutes.Route, len(routes))
	for i, rt := range routes {
		out[i] = mask(rt)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateProxyRoute — POST /api/proxy/routes.
func (h *Handler) CreateProxyRoute(w http.ResponseWriter, r *http.Request) {
	defer recoverProxy(w, "create")
	var req proxyReq
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	route, err := h.toRoute(req, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if msg := validateRoute(route); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if err := h.issueOverrideCerts(route); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	id, err := h.proxyStore().Create(route)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := h.renderProxy(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "saved, but failed to apply: " + err.Error()})
		return
	}
	route.ID = id
	writeJSON(w, http.StatusCreated, mask(route))
}

// UpdateProxyRoute — PUT /api/proxy/routes/{id}.
func (h *Handler) UpdateProxyRoute(w http.ResponseWriter, r *http.Request) {
	defer recoverProxy(w, "update")
	id, err := parseTrailingID(r.URL.Path, "/api/proxy/routes/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	existing, ok, err := h.proxyStore().Get(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	var req proxyReq
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	route, err := h.toRoute(req, &existing)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if msg := validateRoute(route); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if err := h.issueOverrideCerts(route); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if err := h.proxyStore().Update(route); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := h.renderProxy(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "saved, but failed to apply: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, mask(route))
}

// DeleteProxyRoute — DELETE /api/proxy/routes/{id}.
func (h *Handler) DeleteProxyRoute(w http.ResponseWriter, r *http.Request) {
	id, err := parseTrailingID(r.URL.Path, "/api/proxy/routes/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.proxyStore().Delete(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := h.renderProxy(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "deleted, but failed to apply: " + err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Access Lists ────────────────────────────────────────────────────────────────
// Reusable named sets of basic-auth users + IP rules a route can reference.

// accessListReq shadows the user list so passwords arrive in plaintext (hashed here);
// GET never returns the bcrypt hashes.
type accessListReq struct {
	proxyroutes.AccessList
	Users []struct {
		User     string `json:"user"`
		Password string `json:"password"`
	} `json:"users"`
}

func maskAccessList(a proxyroutes.AccessList) proxyroutes.AccessList {
	for i := range a.Users {
		a.Users[i].Hash = ""
	}
	return a
}

// toAccessList folds a request onto a stored list, hashing new passwords and preserving
// unchanged ones (blank password = keep the existing hash for that username).
func (h *Handler) toAccessList(req accessListReq, existing *proxyroutes.AccessList) (proxyroutes.AccessList, error) {
	a := req.AccessList
	now := time.Now().Unix()
	a.UpdatedAt = now
	if existing != nil {
		a.ID = existing.ID
		a.CreatedAt = existing.CreatedAt
	} else {
		a.CreatedAt = now
	}
	prior := map[string]string{}
	if existing != nil {
		for _, u := range existing.Users {
			prior[u.User] = u.Hash
		}
	}
	users := make([]proxyroutes.BasicUser, 0, len(req.Users))
	for _, u := range req.Users {
		name := strings.TrimSpace(u.User)
		if name == "" {
			continue
		}
		hash := prior[name]
		if u.Password != "" {
			b, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
			if err != nil {
				return proxyroutes.AccessList{}, err
			}
			hash = string(b)
		}
		if hash == "" {
			continue // new user with no password is dropped
		}
		users = append(users, proxyroutes.BasicUser{User: name, Hash: hash})
	}
	a.Users = users
	// Keep only well-formed rules.
	rules := make([]proxyroutes.AccessRule, 0, len(a.Rules))
	for _, r := range a.Rules {
		addr := strings.TrimSpace(r.Address)
		if addr == "" {
			continue
		}
		action := r.Action
		if action != "deny" {
			action = "allow"
		}
		rules = append(rules, proxyroutes.AccessRule{Action: action, Address: addr})
	}
	a.Rules = rules

	// GeoIP: normalize mode + country codes (upper-cased ISO 3166-1 alpha-2, deduped).
	if a.GeoMode != "allow" && a.GeoMode != "block" {
		a.GeoMode = "off"
	}
	seen := map[string]bool{}
	codes := make([]string, 0, len(a.Countries))
	for _, c := range a.Countries {
		c = strings.ToUpper(strings.TrimSpace(c))
		if len(c) != 2 || seen[c] {
			continue
		}
		seen[c] = true
		codes = append(codes, c)
	}
	a.Countries = codes
	if a.GeoMode == "off" || len(codes) == 0 {
		a.GeoMode, a.Countries = "off", []string{}
	}
	return a, nil
}

// ListProxyAccessLists — GET /api/proxy/access-lists.
func (h *Handler) ListProxyAccessLists(w http.ResponseWriter, r *http.Request) {
	lists, err := h.proxyStore().ListAccessLists()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]proxyroutes.AccessList, len(lists))
	for i, a := range lists {
		out[i] = maskAccessList(a)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateProxyAccessList — POST /api/proxy/access-lists.
func (h *Handler) CreateProxyAccessList(w http.ResponseWriter, r *http.Request) {
	defer recoverProxy(w, "create access list")
	var req accessListReq
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	a, err := h.toAccessList(req, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(a.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	id, err := h.proxyStore().CreateAccessList(a)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	a.ID = id
	writeJSON(w, http.StatusCreated, maskAccessList(a))
}

// UpdateProxyAccessList — PUT /api/proxy/access-lists/{id}. Re-renders so routes that
// reference the list pick up the new users/rules immediately.
func (h *Handler) UpdateProxyAccessList(w http.ResponseWriter, r *http.Request) {
	defer recoverProxy(w, "update access list")
	id, err := parseTrailingID(r.URL.Path, "/api/proxy/access-lists/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	existing, ok, err := h.proxyStore().GetAccessList(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	var req accessListReq
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	a, err := h.toAccessList(req, &existing)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(a.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if err := h.proxyStore().UpdateAccessList(a); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := h.renderProxy(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "saved, but failed to apply: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, maskAccessList(a))
}

// DeleteProxyAccessList — DELETE /api/proxy/access-lists/{id}. Detaches it from routes
// (store sets their access_list_id=0) and re-renders.
func (h *Handler) DeleteProxyAccessList(w http.ResponseWriter, r *http.Request) {
	defer recoverProxy(w, "delete access list")
	id, err := parseTrailingID(r.URL.Path, "/api/proxy/access-lists/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.proxyStore().DeleteAccessList(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := h.renderProxy(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "deleted, but failed to apply: " + err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestProxyRoute — POST /api/proxy/routes/{id}/test. Probes each upstream's TCP port
// from the rigger container. Accepts {upstreams:[…]} in the body (test before saving);
// falls back to the saved route's upstreams when none are supplied.
func (h *Handler) TestProxyRoute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Upstreams []proxyroutes.Upstream `json:"upstreams"`
	}
	_ = readJSON(r, &req)
	ups := req.Upstreams
	if len(ups) == 0 {
		if id, err := parseTrailingID(strings.TrimSuffix(r.URL.Path, "/test"), "/api/proxy/routes/"); err == nil {
			if route, ok, _ := h.proxyStore().Get(id); ok {
				ups = route.Upstreams
			}
		}
	}
	type result struct {
		Target  string `json:"target"`
		OK      bool   `json:"ok"`
		Latency int64  `json:"latency_ms,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	out := []result{}
	for _, u := range ups {
		host := strings.TrimSpace(u.Host)
		port := u.Port
		if port == 0 {
			if u.Scheme == "https" {
				port = 443
			} else {
				port = 80
			}
		}
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		start := time.Now()
		conn, err := net.DialTimeout("tcp", addr, 4*time.Second)
		res := result{Target: addr}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
			res.Latency = time.Since(start).Milliseconds()
			conn.Close()
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// GetProxyPlugins — GET /api/settings/proxy/plugins. Instance-wide WAF/cache enable
// state (PX-6). Enabling makes the renderer emit the plugin middleware instances; the
// plugins must also be declared in Traefik's static command (one-time, see compose).
func (h *Handler) GetProxyPlugins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"waf_enabled":   h.appSetting("proxy_waf_enabled") == "true",
		"cache_enabled": h.appSetting("proxy_cache_enabled") == "true",
		"geoip_enabled": h.appSetting("proxy_geoip_enabled") == "true",
	})
}

// SetProxyPlugins — POST /api/settings/proxy/plugins {waf_enabled, cache_enabled}.
func (h *Handler) SetProxyPlugins(w http.ResponseWriter, r *http.Request) {
	var b struct {
		WAF   *bool `json:"waf_enabled"`
		Cache *bool `json:"cache_enabled"`
		Geo   *bool `json:"geoip_enabled"`
	}
	if err := readJSON(r, &b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if b.WAF != nil {
		h.setAppSetting("proxy_waf_enabled", strconv.FormatBool(*b.WAF))
	}
	if b.Cache != nil {
		h.setAppSetting("proxy_cache_enabled", strconv.FormatBool(*b.Cache))
	}
	if b.Geo != nil {
		h.setAppSetting("proxy_geoip_enabled", strconv.FormatBool(*b.Geo))
	}
	if err := h.renderProxy(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.GetProxyPlugins(w, r)
}

// ProxyDefault — PUBLIC GET /__proxydefault/{mode}. The catch-all default route (when
// its mode is 404/403/close) routes unmatched hosts here so Rigger returns a bare
// status instead of the friendly fallback page. "close" drops the connection (nginx
// 444-style) to hide that anything is hosted. No auth — it's a status responder.
func (h *Handler) ProxyDefault(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("mode") {
	case "403":
		http.Error(w, "Forbidden", http.StatusForbidden)
	case "close":
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		http.Error(w, "Not Found", http.StatusNotFound)
	default: // 404 and anything unexpected
		http.Error(w, "Not Found", http.StatusNotFound)
	}
}

// ListProxyCerts — GET /api/proxy/certs. Best-effort list of certificates Rigger
// already manages, for the "existing certificate" picker. The "existing" TLS mode
// works regardless (Traefik serves whatever stored cert matches the SNI) — this is a
// convenience list. Sources: the configured apps base-domain wildcard + tracked
// out-of-band override certs.
func (h *Handler) ListProxyCerts(w http.ResponseWriter, r *http.Request) {
	type cert struct {
		Ref   string `json:"ref"`
		Label string `json:"label"`
	}
	out := []cert{}
	if base := strings.TrimSpace(h.appSetting("apps_base_domain")); base != "" {
		out = append(out, cert{Ref: "*." + base, Label: "*." + base + " (wildcard)"})
	}
	if h.acmeCerts != nil {
		if list, err := h.acmeCerts.List(); err == nil {
			for _, c := range list {
				out = append(out, cert{Ref: c.Domain, Label: c.Domain})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"certs": out, "acme_email": h.appSetting("acme_email")})
}
