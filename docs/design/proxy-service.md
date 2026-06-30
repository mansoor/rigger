# Proxy Service — standalone reverse-proxy manager (top-level page)

**Status: BUILT (PX-1–PX-6, develop + pushed). PX-6 plugins are now UI-managed (Phase 3.1):
WAF (Coraza) + GeoIP (geoblock) activate from the Plugins card — Rigger owns Traefik's static
config, no docker-compose editing. Cache (Souin) is HARD-DISABLED — it panics under Traefik's
Yaegi interpreter and took down all routing when enabled (see Phase 3.1).**
Backend: `internal/proxyroutes` (store + file-provider renderer, unit-tested),
`api/proxy_handlers.go` (CRUD + `/test` probe + `/certs` + plugin toggles), migration
`proxy_routes`, boot re-render, catch-all `/__proxydefault/{mode}` responder. Frontend:
top-level `Proxy Service` page (`/proxy`, admin-only) with route list, add/edit modal,
default-route card, plugins card. Live: clean boot, migration applied, `proxy-base.yml`
rendered. Full interactive E2E pending user testing.

**Enhancements since PX (develop):**
- **Tabbed route form** — Basics & Security / Locations / Certs & SSL / Advanced (redirect
  routes show only Basics + Certs). Page widened to `max-w-7xl` (parity with Admin/Tools).
- **Custom locations** are load-balanced — each location has its own upstream **list**
  (legacy single host/port folded in via `Location.Servers()`).
- **Multi-domain** host field (`Host(a)||Host(b)`), **ACME email override** (per-route, via
  the DNS-01 issuer) + **LE ToS** gate, default cert **None**.
- **WAF/Cache** per-route toggles moved to the **Advanced** tab. (Cache is inert instance-wide
  — Souin is hard-disabled, see Phase 3.1 — so its per-route toggle never emits a middleware
  until a working cache plugin exists.)
- **Routes section** now has its own card header ("Routes" + a `hostname → upstream` badge) with
  the **Add route** button at the section level, matching the Access lists and Plugins cards
  (page-header Add-route button removed; empty state folded into the card).
- **Error UX**: save failures render in a collapsible details box (real server message via
  `errMsg`), clear on edit; **Test upstream** result is a toaster next to the button that
  auto-dismisses (~12s). Create/Update handlers wrapped in `recoverProxy` (panic → logged
  JSON 500 instead of a silent connection reset).
- **Access Lists** (`proxy_access_lists` table + `internal/proxyroutes/accesslist.go`):
  reusable, named bundles of basic-auth users + IP allow/deny rules + a **pass-auth** flag,
  referenced by a route via `access_list_id` (replaces inline auth/IP when set). CRUD under
  `/api/proxy/access-lists`. Card sits between the route list and Plugins. `effectiveAccess()`
  resolves auth/IP/Geo from the list when set, else inline.
- **GeoIP country blocking** (per access list): `geo_mode` (off|allow|block) + `countries`
  (ISO 3166-1 alpha-2). Renders an `nscuro/traefik-plugin-geoblock` middleware per route when
  the instance-wide **GeoIP** plugin toggle is on. Offline IP2Location LITE BIN DB mounted at
  `/geoip`; docker-compose carries commented opt-in for the plugin + DB mount + a
  `forwardedHeaders.trustedIPs` note (needed for the real client IP behind a fronting proxy).
  Traefik `ipAllowList`/geoblock are allow-oriented — deny-only IP lists aren't enforceable
  (documented in the UI).

A first-class, top-level **Proxy Service** page (nav peer of Housekeeping / Tools) that
turns Rigger's baked-in Traefik into a general reverse proxy — an NPM-style "proxy hosts"
manager for routing public hostnames/paths to **any** upstream: a container Rigger runs, a
service elsewhere on the LAN, or a remote host. It is deliberately **independent of the New
Project / workspace / project model** — proxy routes are an instance-level concern that sits
beside Rigger, not inside it.

Background / feasibility: see the inline analysis that produced this doc — the key enabler is
that `rigger-traefik` already runs the **file provider** watching `/dynamic`
(`--providers.file.directory=/dynamic --providers.file.watch=true`,
[src/docker-compose.yml:168](../../src/docker-compose.yml)) and Rigger mounts the same
`rigger-dynamic` volume **read-write** ([src/docker-compose.yml:54](../../src/docker-compose.yml);
`internal/acme` already writes dynamic TLS config there). The file provider can point a
service at an arbitrary URL (`http://192.168.1.50:8096`), which the Docker provider cannot.

## Goals / non-goals

**Goals**
- Top-level **Proxy Service** page (route `/proxy`), NOT under Tools or Admin settings.
- Define **proxy routes** generically: inbound match (host and/or path) → one or more
  upstream URLs, with TLS, auth, and common middlewares — covering NPM proxy-host parity.
- Route to upstreams **off the Rigger host** (other LAN devices / remote IPs).
- Hot-apply with no Traefik restart (file provider), no project/compose involvement.

**Non-goals (v1)**
- Replacing the per-project routing (`Config.Routes`, the project "Routing" tab) — that stays.
- TCP/UDP (stream) routing; access control beyond basic-auth + IP allowlist; an in-core WAF or
  HTTP cache (both are Traefik plugins — see the parity table, Phase 3).
- Being the thing that *decides* whether Rigger's Traefik is your edge (see topology note).

## Edge topology (the one operational prerequisite)

`rigger-traefik` binds host **80/443**. For a proxy route to actually serve traffic, requests
for its hostname must reach this Traefik. Two supported setups (document, don't enforce):
- **A — Rigger Traefik is the edge:** router/DNS forwards 80/443 to the Rigger host; retire or
  bypass the existing NPM. Cleanest.
- **B — Behind an existing edge (e.g. NPM):** the edge forwards selected hostnames to
  rigger-traefik. Works, but two proxies to maintain.

The page should show a short, dismissable note explaining this, and (Phase 2) a reachability
hint. The feature itself is identical in both topologies.

## Data model

New SQLite table `proxy_routes` (instance-level; mirrors the store pattern of
`internal/registries` / `internal/alerts`). One row = one route.

```
proxy_routes(
  id            INTEGER PK,
  name          TEXT,                 -- display label, e.g. "Jellyfin"
  enabled       INTEGER DEFAULT 1,
  type          TEXT DEFAULT 'proxy', -- proxy | redirect
  is_default    INTEGER DEFAULT 0,    -- catch-all for unmatched hosts (low priority); at most one
  default_mode  TEXT,                  -- is_default only: page | 404 | 403 | close | redirect | proxy
  host          TEXT,                 -- FQDN matcher (Host rule); may be "" if path-only/default
  path_prefix   TEXT,                 -- optional PathPrefix; "" = whole host
  -- proxy type:
  upstreams     TEXT,                 -- JSON: [{scheme,host,port,weight}] (1+; >1 = LB)
  pass_host_header INTEGER DEFAULT 1,
  insecure_skip_verify INTEGER DEFAULT 0, -- accept self-signed upstream (https upstreams)
  -- redirect type:
  redirect_to   TEXT,                 -- target URL/scheme for a redirection host
  redirect_code INTEGER DEFAULT 301,  -- 301 | 302
  -- TLS:
  tls_mode      TEXT,                 -- none | le-http | le-dns | existing | custom
  tls_cert_ref  TEXT,                 -- existing mode: id/name of a cert Rigger already manages
  tls_cert_pem  TEXT,                 -- custom mode only (cert); key encrypted at rest
  tls_key_enc   TEXT,                 -- custom mode only (key, crypto.Encrypt)
  acme_email    TEXT,                 -- "" = inherit Rigger's ACME email; else per-route override
  force_https   INTEGER DEFAULT 1,    -- add web->websecure redirect when TLS on
  hsts_seconds  INTEGER DEFAULT 0,    -- 0 = off; else Strict-Transport-Security max-age
  hsts_subdomains INTEGER DEFAULT 0,
  hsts_preload  INTEGER DEFAULT 0,
  -- security / access:
  auth_mode     TEXT,                 -- none | basic
  auth_users    TEXT,                 -- basic mode: JSON [{user, hash}] (bcrypt/htpasswd)
  ip_allow      TEXT,                 -- optional CIDR allowlist (comma list) -> ipAllowList
  security_headers INTEGER DEFAULT 0, -- baseline hardening headers (see below); NOT a WAF
  strip_prefix  INTEGER DEFAULT 0,    -- stripPrefix middleware for path routes
  headers       TEXT,                 -- optional JSON of custom request/response headers
  notes         TEXT,
  created_at, updated_at
)
```

HTTP/2 and WebSocket need **no fields** — Traefik does both automatically (HTTP/2 on the TLS
entrypoint; WebSocket `Upgrade` is proxied transparently, unlike nginx which needs an explicit
toggle). They're surfaced in the UI as always-on info, not switches.

Secrets (TLS key, basic-auth is already hashed) are encrypted with the existing
`crypto.Encrypt` (HKDF key), masked on GET like every other secret surface.

## Rendering to Traefik (the core)

New package `internal/proxyroutes` with a `Render(db) -> error` that writes **one
consolidated** file-provider config from all enabled rows, atomically (temp + rename):

- Target: `/dynamic/proxy.yml` on the `rigger-dynamic` volume (top-level — the file provider
  parses top-level `*.yml`; cert/secret material that must NOT be parsed already lives under
  `/dynamic/secrets`, per `internal/acme`). **Confirm provider recursion** during build; if it
  recurses, a `/dynamic/proxy/` subdir of per-route files is the alternative.
- Re-render on every create/update/delete/toggle (and once on boot, for drift repair).
- Traefik `watch=true` hot-reloads — no restart, no routing blip.

Each route emits a router + service (+ middlewares), namespaced `proxy-<id>` to avoid clashing
with composegen's project routers and acme's per-host files:

```yaml
http:
  routers:
    proxy-7:
      rule: "Host(`media.example.com`)"          # + " && PathPrefix(`/x`)" when set
      entryPoints: ["websecure"]                  # ["web"] when tls_mode=none
      service: proxy-7
      middlewares: ["proxy-7-auth", "proxy-7-ipallow"]   # only those configured
      tls: { certResolver: letsencrypt }          # le-http; or {certResolver: dns}; or {} for custom
  services:
    proxy-7:
      loadBalancer:
        passHostHeader: true
        servers:
          - url: "http://192.168.1.50:8096"
        serversTransport: proxy-7-transport        # only if insecureSkipVerify
  middlewares:
    proxy-7-auth:    { basicAuth: { users: ["user:$2y$..."] } }
    proxy-7-ipallow: { ipAllowList: { sourceRange: ["192.168.0.0/16"] } }
  serversTransports:
    proxy-7-transport: { insecureSkipVerify: true }
tls:
  certificates:                                    # custom tls_mode only
    - certFile: /dynamic/secrets/proxy-7.crt
      keyFile:  /dynamic/secrets/proxy-7.key
```

`force_https` adds a per-router web→websecure redirect middleware (mirrors composegen's
per-router redirect, NOT a global one — keeps HTTP-only routes intact).

## TLS

Reuse the two ACME resolvers already configured ([docker-compose.yml:144-159](../../src/docker-compose.yml)):
- `none` — HTTP only (`web` entrypoint). For internal/plain hosts.
- `le-http` — `certResolver: letsencrypt` (HTTP-01). Works for any public FQDN because Traefik
  owns :80. Default for a public hostname.
- `le-dns` — `certResolver: dns` (Cloudflare DNS-01) — for wildcard / hosts under the apps base
  domain, or when :80 isn't reachable. Requires the CF token already wired in Settings. When the
  host falls under the existing `*.{apps_base_domain}` wildcard, this **reuses the shared
  wildcard cert** (no new issuance).
- `existing` — **reuse a cert Rigger already manages.** `tls_cert_ref` points at one of the
  certs in the store (the wildcard, a project / custom-domain cert, or a previously uploaded
  one). Mechanically this is just "TLS on, no resolver": Traefik serves whichever stored cert
  matches the SNI, so nothing is re-issued. The UI lists known certs so the choice is explicit.
- `custom` — admin-uploaded cert/key, written to `/dynamic/secrets/proxy-<id>.{crt,key}` and
  referenced via a `tls.certificates` entry.

**ACME email:** for `le-http`/`le-dns`, the LE account email defaults to **Rigger's configured
ACME email** (the existing global → workspace hierarchy; `ACME_EMAIL` / Settings → General). The
route form shows the inherited address read-only with an optional **Override** (`acme_email`)
for the rare case a host needs a different LE account — not entered per route by default.

## Middlewares (NPM parity)

basic-auth (`auth_users`, htpasswd/bcrypt — reuse the managed-registry/auth-gate hashing
helper), IP allowlist (`ip_allow` → `ipAllowList`), forced HTTPS redirect, **HSTS** (via the
`headers` middleware: `stsSeconds`/`stsIncludeSubdomains`/`stsPreload`), **baseline security
headers** (the `headers` middleware: `frameDeny`, `contentTypeNosniff`,
`browserXssFilter`, a sane `referrerPolicy` — NPM's "block common exploits" *lite*; see the
parity note below), optional stripPrefix for path routes, optional custom headers. All emitted
only when configured.

## Route types

- **proxy** (default): host/path → upstream(s), as above.
- **redirect** (NPM "redirection host"): host → `redirect_to` with `redirect_code` (301/302),
  no upstream. Emitted as a router with a `redirectRegex`/`redirectScheme` middleware and a
  no-op service. Useful for `www.→apex`, vanity domains, retired hosts.

## Default page / catch-all (unmatched hosts + project URLs)

Rigger already ships a catch-all: `rigger-fallback` (a priority-1 router serving an
auto-refreshing "app starting / not reachable" page — [docker-compose.yml:212-235](../../src/docker-compose.yml)).
A host pointed at Rigger that matches nothing already lands there. This feature makes the
default **configurable** via a route with `is_default=1` and a `default_mode`:
- **page** (default): keep the existing friendly "not reachable" page.
- **404 / 403**: return a bare status — no page.
- **close** (444, nginx-style): drop the connection, hiding that anything is hosted here from
  scanners hitting the IP directly.
- **redirect**: send unmatched hosts to a URL (e.g. your main site).
- **proxy**: serve a real default site from an upstream.

The status/close/page modes are served by extending `rigger-fallback` (already the catch-all,
already receives the host) to honour the configured `default_mode`; redirect/proxy reuse the
normal route emit. The default route is emitted at a low priority (above `rigger-fallback`'s 1,
below host-specific routers). **Precedence** end to end: project/app routers (`Host()`,
composegen) → proxy routes (`Host()`) → configurable default (catch-all) → built-in fallback. So
**project URLs and configured proxy hosts always win**; the default only catches the
truly-unconfigured.

## NPM feature parity

| NPM option | Plan | Phase |
|---|---|---|
| WebSocket support | Automatic in Traefik — info-only in UI | v1 |
| HTTP/2 | Automatic on TLS entrypoint — info-only | v1 |
| Force SSL | per-router web→websecure redirect (`force_https`) | v1 |
| HSTS | `headers` middleware (`hsts_*` fields) | v1 |
| Redirection hosts | `type: redirect` route | v1 |
| Block common exploits | **Partial**: baseline security headers (`security_headers`). Full WAF (request/pattern blocking) = a Traefik **plugin** (Coraza/ModSecurity) — needs Traefik static-config + restart | v1 lite / Phase 3 WAF |
| Cache Assets | **Not in Traefik core** — the **Souin** HTTP-cache plugin; needs static-config + restart | Phase 3 |
| Default site | configurable catch-all (built-in page / redirect / proxy) | v1 |

WAF and cache are the only two that aren't a plain middleware — they're Traefik **plugins**
(Yaegi-loaded). They get their own phase below rather than being dropped.

## Phase 3 — WAF (block common exploits) + asset cache, via Traefik plugins

These behave as **two layers**, and only the first restarts anything:

1. **Enable (static, restart — rare):** the plugin is declared in Traefik's *static* config
   (`--experimental.plugins.<name>.moduleName=… --…version=…` on `rigger-traefik`). Changing
   that restarts Traefik (a few-second blip for *all* hosted apps). This is "set it and
   forget it" — done once per plugin, recurring only on a deliberate version bump. Rigger
   manages it from the Proxy Service page (an "Enable WAF" / "Enable cache" admin action that
   rewrites the Traefik command via the managed-sidecar pattern and restarts it for you, like
   the CF-token flow already does).
2. **Use/tune (dynamic, hot-reload — no restart):** once enabled, the plugin is instantiated
   as a *middleware* in the file provider. Which routes get the WAF, rule-set/paranoia level,
   cache TTLs and exclusions are all dynamic config → applied live. So day-to-day on/off and
   tuning never restart.

**Candidates:** Coraza (OWASP CRS) for the WAF; Souin for the HTTP cache.

**Sharing with Rigger-hosted apps (opt-in, not global):** once a plugin is enabled, it's
available to *every* router Traefik serves — including the Docker-provider routers composegen
emits for projects. A middleware only acts on routers that reference it, so apps benefit via an
**opt-in toggle**, not automatically:
- **Proxy routes:** a `waf`/`cache` toggle per route (this page) → adds the middleware ref.
- **Projects:** a per-project (or per-env) "WAF" / "Cache assets" toggle that makes composegen
  attach the middleware to the app's router label (hot-reloaded, granular).

Deliberately **avoid a global entrypoint-default middleware** (`--entrypoints.websecure.http.
middlewares=…`) for these two: a WAF applied blindly causes false-positive blocks (admin
panels, uploads, odd-payload APIs), and a blind cache can serve stale/dynamic or per-user
content. Per-route/per-app opt-in with sane defaults is the model; a global default can be a
later explicit choice, not the baseline.

**Caveats to surface in the UI:** Yaegi-interpreted plugins add per-request overhead vs native
middlewares; enabling/version-bumping restarts the proxy (brief blip for all apps); cache
correctness depends on cache-key + cacheable-method config (default to GET/HEAD + asset paths).
The restart caveat is shown as a **tooltip on a small info icon** next to the Plugins heading,
not a persistent banner.

## Phase 3.1 — UI-managed plugin activation

**STATUS: BUILT — "UI-managed config, no bundling" variant (develop, uncommitted, 2026-06-29).**
Rigger now OWNS the full Traefik static config (`internal/traefikcfg` → `traefik.yml` on the shared
`rigger-traefik-config` volume, auto-loaded by Traefik at `/etc/traefik`), reproducing the former
compose `command:` args + an `experimental.plugins` block for the enabled plugins. The compose
`command:` was removed (static-config sources are mutually exclusive); Traefik `depends_on` rigger
(new `/healthz` + healthcheck) so the config exists before it starts. The Proxy Service → Plugins
toggle rewrites the file + restarts Traefik (`acme.Issuer.RestartProxy`); per-route attach stays
dynamic. GeoIP DB download from a token is `internal/geoipdb` (POST `/api/settings/proxy/geoip`).
Plugin versions are operator-overridable settings. **Live-verified:** Coraza WAF (WASM, v0.2.1) +
geoblock (v0.14.0) load + serve.

**Souin/Cache — HARD-DISABLED (2026-06-30).** Souin (v1.7.8) loads but then **PANICS under
Traefik's Yaegi interpreter** while building the middleware handler (`reflect.Value.Field` →
`souin … middleware.NewHTTPCacheHandler`). Critically, a failing plugin **poisons router
building**: with Souin declared, Traefik stayed "up" but dropped **all Docker-provider routers**
to 404 — every hosted app (a live Gitea project answered on its published host port but 404'd on
its domain) until Souin was removed and Traefik restarted. There is no Yaegi-compatible cache
plugin to swap in (Traefik is migrating plugins to WebAssembly). So Rigger no longer offers or
emits Souin: `traefikcfg.CacheSupported = false` is the single source of truth, AND-ed into
`FromSettings` + `Generate` (static config) and the `renderProxy`/bridge cache flags (dynamic
config), so neither the static plugin list nor any per-route/access-list/env `proxy-cache@file`
middleware is produced regardless of the stored `proxy_cache_enabled` value. The Plugins UI shows
Cache as **unavailable** with CDN / cache-sidecar guidance. **Re-enabling:** flip
`CacheSupported` to `true` once a Yaegi-compatible cache build exists. Robust alternatives that
don't ride Yaegi: a **CDN in front** (Cloudflare) or a dedicated **cache sidecar**
(Varnish / Nginx `proxy_cache`) — neither built. The image-bundling variant below was the
original path for Souin + air-gapped installs and remains deferred (it would not fix the Yaegi
panic — Souin's interpreted build is the problem).

---

## Phase 3.1 (image-bundled variant — DEFERRED) — bundled plugins + Rigger-owned static config

**Problem with the shipped opt-in.** Today enabling a plugin means each operator hand-edits
their own `docker-compose.yml` (uncomment `--experimental.plugins.*`, pin a version, mount the
GeoIP DB) and rebuilds/restarts `rigger-traefik` — on **every** installation, local and remote.
That's because Traefik plugins load from **static config at Traefik startup** (Yaegi interprets
the plugin *source* fetched from GitHub; Coraza is a WASM module) and the static config lives as
CLI args in the operator-owned compose. Rigger's image-distribution model (GHCR images + in-app
updater) swaps *images*, not the operator's compose — so plugins stay a manual per-install chore.

**Target model — make activation a UI toggle on every install, no compose editing.** Four parts,
all enabled by the fact that we now control the distributed artifact:

1. **Bundle the plugins inside the Rigger image we already ship.** Vendor the Yaegi plugin
   *sources* (Souin, geoblock) + the Coraza `.wasm` + the OWASP CRS ruleset into the rigger image.
   On boot, Rigger copies them into a shared `traefik-plugins` volume laid out as Traefik's
   `/plugins-local/src/<module>` (and a wasm dir). Effect: plugins travel **with the release** —
   pinned + reviewed once by us, **no per-install GitHub fetch**, works **air-gapped**. Traefik
   stays the **stock `traefik:v3.4`** image (the rigger image is the plugin *carrier*; no need to
   fork/build a custom Traefik image). Traefik loads them via `experimental.localPlugins.<name>`
   (local source) instead of `experimental.plugins.<name>` (remote fetch).
2. **Move the plugin *declaration* from compose CLI args to a Rigger-owned static config file.**
   Ship `rigger-traefik` pointed at a static `traefik.yml` Rigger writes on a shared volume
   (one-time compose change in a release; existing CLI args migrate into the file). Enabling a
   plugin = Rigger adds the `localPlugins` block to that file. (Static config still needs a
   restart — that's Traefik, not us.)
3. **Rigger orchestrates the restart it already knows how to do.** The Plugins toggle →
   Rigger ensures the source is staged (done at boot) → regenerates the static file → runs
   `docker restart rigger-traefik` (same capability used for CF-token rotation, via socket-proxy).
   **Per-route / per-access-list attach stays fully dynamic** (file provider, instant) — only
   enable/disable hits the restart path, a rare admin action.
4. **GeoIP DB from the UI too.** Instead of a manual `./geoip` file, Rigger downloads + refreshes
   the IP2Location LITE `.BIN` to the shared volume from a free download token the admin pastes in
   Settings (mirrors the CF-token pattern), on a monthly schedule. Removes the last manual asset.

**Net:** "manage plugins from the UI" = toggle → stage bundled plugin + write static file +
Rigger restarts Traefik → done, identically on every install.

**Trade-offs / impact (decide before building):**
- **Unavoidable:** a Traefik restart on enable/disable briefly interrupts *all* routed traffic
  (proxy + every app env) for a few seconds → a maintenance action, surfaced in the UI.
- **Image grows** by plugin sources/wasm + CRS rules (modest), and **we own keeping plugin
  versions current** (security) — one review per release vs every operator pinning arbitrary
  versions (smaller supply-chain surface overall).
- **Licensing/attribution pass** before bundling: Coraza (Apache-2.0), Souin (MIT), geoblock
  (MIT/Apache), CRS (Apache-2.0), IP2Location LITE (CC-BY → needs attribution). Generally
  redistributable; ties to the open-core/monetization thinking.
- **One-time migration:** the static-config-file switch is a compose change delivered in a release.
- This supersedes the "rewrite the Traefik command via managed-sidecar" idea sketched in Phase 3
  step 1 — the static-config-file + localPlugins approach is the concrete mechanism.

## UI notes (from mockup review)

- Form always opens with **Name** + **Type** (proxy / redirect segmented control), then host /
  path. (The v2 mockup compressed these for space — they remain in the form.)
- **Upstreams**: each row has its own **delete (trash)** control plus a **＋ add** to append a
  row; 2+ rows load-balance.
- **Advanced** disclosure stays: pass host header, strip path prefix, allow self-signed upstream.
- Plugins restart caveat → info-icon tooltip, not an always-visible banner.
- TLS "Certificate" select: `Let's Encrypt (HTTP-01)` / `Let's Encrypt (DNS wildcard) — reuse` /
  `Existing certificate — reuse` (reveals a cert picker) / `Custom upload` / `None`. ACME email
  shown inherited with an Override link.
- The default route renders as a distinct card (not a normal row): a "When unmatched" select
  (page / 404 / 403 / close / redirect / proxy).
- List-row badges: keep meaningful state (TLS source, auth, HSTS, WAF, cache, redirect/default);
  drop always-on ones (WebSocket) to reduce noise.

## API + routes

`internal/proxyroutes` store + `api/proxy_handlers.go`:
- `GET    /api/proxy/routes` — list (secrets masked).
- `POST   /api/proxy/routes` — create → validate → persist → `Render`.
- `PUT    /api/proxy/routes/{id}` / `DELETE …` — update/remove → `Render`.
- `POST   /api/proxy/routes/{id}/test` — reachability probe of each upstream (TCP connect /
  optional HTTP HEAD) from the Rigger container; returns per-upstream ok/latency/error.
- (Phase 2) `GET /api/proxy/status` — cross-check against Traefik's API for "router live".

Wire in `cmd/server/main.go` in the same admin-gated settings switch. **RBAC:** super-admin
only (editing the shared edge is instance-wide) — but the **page is top-level**, not buried in
Admin; non-admins simply don't see the nav item (like Housekeeping's `isAdmin &&` gate at
[Layout.jsx:493](../../src/frontend/src/components/Layout.jsx)).

## Validation & conflict detection

- `host`+`path_prefix` must be unique among proxy routes.
- Warn (not block) when a `host` collides with a project env's Traefik Host rule — both would
  match; Traefik resolves by priority. Surface it so the user is aware.
- Validate FQDN, CIDRs, upstream URL scheme/port, and that custom TLS cert/key parse & pair.

## Frontend

- `src/pages/ProxyServicePage.jsx` — list of route cards (name, host→upstreams, TLS badge,
  auth badge, enabled toggle, status dot) + an add/edit modal (`ProxyRouteForm`): host, path,
  upstream rows (add/remove), TLS mode, auth, IP allowlist, advanced (insecureSkipVerify,
  passHostHeader, stripPrefix, headers), notes. A "Test" button per route (probe).
- `src/lib/api.js` — `fetchProxyRoutes / createProxyRoute / updateProxyRoute /
  deleteProxyRoute / testProxyRoute`.
- Nav: add `{isAdmin && <NavBtn to="/proxy" label="Proxy Service" />}` beside Housekeeping in
  [Layout.jsx:493-494](../../src/frontend/src/components/Layout.jsx); route in
  [App.jsx:133-134](../../src/frontend/src/App.jsx).
- A top-of-page info card explaining the 80/443 edge prerequisite (topology note above).

## Optional niceties (later)

- **Import from NPM:** parse an exported NPM proxy-hosts JSON into draft routes.
- Per-upstream weights / sticky sessions; passive health checks (`loadBalancer.healthCheck`).
- Live status via Traefik API (enable `--api` internal-only on traefik_net).
- Access logs per route.

## Out of scope (v1)

TCP/UDP stream routing; OAuth/forward-auth; WAF/ratelimit beyond IP allowlist; making Rigger's
Traefik the edge automatically (operator does the port-forward/NPM change).

## Touched files (summary)

New: `internal/proxyroutes/{store.go,render.go,render_test.go}`, `api/proxy_handlers.go`,
`src/pages/ProxyServicePage.jsx`, `docs` (this file).
Edited: `cmd/server/main.go` (routes + boot re-render), `src/lib/api.js`,
`src/components/Layout.jsx` (nav), `src/App.jsx` (route), DB migration for `proxy_routes`.

## Verify

Unit: `render_test.go` asserts the YAML for a representative route set (host+path, multi
upstream, le-http vs custom TLS, basic-auth, ip allowlist, insecureSkipVerify). Build gates
(go build/vet/test + npm build), rebuild rigger. Live: add a route to a LAN service, confirm
`/dynamic/proxy.yml` is written, Traefik hot-loads it (no restart), and the hostname proxies
through with a valid cert; toggle off → route disappears.

## Implementation plan (build order)

Each phase is independently shippable; P1–P5 deliver full v1 (no plugins). P6 is the deferred
plugin layer (Phase 3 above). Gates after every phase: containerized `go build/vet/test` +
`npm run build`; rebuild rigger before live checks.

**PX-1 — Model + store + renderer (backend, no UI).**
- Migration: `proxy_routes` table (full schema above).
- `internal/proxyroutes`: types + store CRUD; `Render(db) error` → atomic write of
  `/dynamic/proxy.yml` (temp+rename) covering proxy + redirect types, all TLS modes, middlewares
  (basic-auth, ipAllowList, HSTS, security headers, force-https, stripPrefix), multi-upstream LB,
  passHostHeader, insecureSkipVerify, and the `is_default`/`default_mode` catch-all router.
- Boot re-render (drift repair). Unit test `render_test.go` over a representative route set.
- ⚠ confirm Traefik file-provider recursion before choosing one file vs `/dynamic/proxy/<id>.yml`.

**PX-2 — API + routes (backend).**
- `api/proxy_handlers.go`: `GET/POST/PUT/DELETE /api/proxy/routes`, `POST …/{id}/test`
  (per-upstream TCP/HTTP probe), `GET /api/proxy/certs` (enumerate reusable certs: wildcard +
  project/custom-domain + uploaded — feeds the "existing cert" picker).
- Wire in `cmd/server/main.go` (super-admin gate); re-`Render` after each mutation.
- Validation + conflict detection (host+path uniqueness; warn on project-host collision).

**PX-3 — Frontend page + nav.**
- `pages/ProxyServicePage.jsx` (route list rows + badges + toggle + Test/Edit/Delete; default
  route card with the "when unmatched" select; edge-topology info banner).
- `ProxyRouteForm` modal per the mockups (name + type, host/path, upstreams add/del, TLS w/ cert
  picker + ACME inherit/override, access + hardening, advanced disclosure; plugin toggles
  rendered only once a plugin is enabled).
- `lib/api.js` helpers; nav item `{isAdmin && <NavBtn to="/proxy" label="Proxy Service"/>}`
  ([Layout.jsx:493](../../src/frontend/src/components/Layout.jsx)); route in
  [App.jsx:133](../../src/frontend/src/App.jsx).

**PX-4 — Default-page / catch-all integration.**
- Extend `rigger-fallback` to honour `default_mode` per host (page / 404 / 403 / close-444);
  redirect/proxy default modes reuse the normal route emit.

**PX-5 — Verify + document.**
- Build gates + rebuild rigger; live: add a LAN route → confirm `/dynamic/proxy.yml` written,
  Traefik hot-loads (no restart), host proxies with a valid cert; toggle off → gone; default
  mode = 404 verified. Flip doc status to BUILT; add a memory entry.

**PX-6 — Plugins (deferred, Phase 3).**
- *Shipped (v0.1.21):* WAF (Coraza) + cache (Souin) + GeoIP (geoblock) scaffolding — settings
  flags, render emits middleware refs, per-route/access-list toggles. Activation is a one-time,
  per-install **commented opt-in** in docker-compose (uncomment plugin decl + DB mount + restart).
- *Target activation (Phase 3.1, not built):* image-bundled plugins + Rigger-owned Traefik static
  config (`localPlugins`) + UI toggle that stages the plugin and restarts `rigger-traefik` — so
  plugins are managed from the UI on every install, no compose editing. GeoIP DB auto-fetched from
  a token in Settings. See the **Phase 3.1** section above. Ships only when greenlit.

## Related

- [domain-routing-model](domain-routing-model.md), the project "Routing" tab (`Config.Routes`)
  — the project-scoped analogue this sits beside.
- `internal/acme` — the existing file-provider writer (cert + dynamic TLS config) to mirror.
- `internal/maintenance` — another existing Traefik file-fragment writer pattern.
