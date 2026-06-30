# Extending Proxy plugins + Access Lists to Rigger-hosted workspaces & projects

**Status: BUILT (WP-1…WP-5 + WP-7 inline per-env controls, develop — gates green, not yet
live-E2E'd / committed).** Companion to
[proxy-service.md](proxy-service.md) (the global, super-admin Proxy Service that shipped these
features for standalone routes in v0.1.21) and to its Phase 3.1 plugin-activation target. This
doc covers taking the same three capabilities — **WAF / Cache / GeoIP plugins** and **reusable
Access Lists** (basic-auth users + IP allow/deny + GeoIP country policy) — and making them
available on **Rigger-hosted app domains** (per workspace / project / environment), owned by
workspace members rather than only the global super-admin.

> **Caveat (2026-06-30): Cache is hard-disabled instance-wide.** The Souin cache plugin panics
> under Traefik's Yaegi interpreter and took down all routing when enabled, so
> `traefikcfg.CacheSupported = false` (see [proxy-service.md](proxy-service.md) § Phase 3.1).
> Every "Cache" toggle/attach below is therefore **inert** until a Yaegi-compatible cache plugin
> (or a CDN / cache-sidecar alternative) exists. WAF (Coraza) + GeoIP (geoblock) are unaffected.

## Goal / non-goals

**Goal:** a workspace admin can protect their own apps — staging, preview, prod envs — with the
same WAF / cache / GeoIP / basic-auth / IP-allow vocabulary the Proxy Service already has, using
**workspace-scoped, reusable Access Lists**, without a super-admin touching anything per app.

**Non-goals:**
- Re-implementing the Proxy Service inside projects. The global Proxy Service stays super-admin and
  separate; this is the *project/workspace* counterpart that reuses the same engine.
- Letting workspaces *install* Traefik plugins (that stays a global/super-admin + infra action —
  see [proxy-service.md](proxy-service.md) § Phase 3.1). Workspaces only *consume + attach*.
- A global, blind, entrypoint-default WAF/cache (false-positive / stale-content risk — opt-in only).

## Why this is feasible — the two rendering paths + the bridge

Rigger renders Traefik config two ways:

| Path | Used by | How |
|---|---|---|
| **File provider** (`/dynamic/*.yml`) | Proxy Service routes; ACME override certs; maintenance pages | Rigger writes YAML; hot-reloaded, no restart |
| **Docker provider** (container labels) | Rigger-hosted app routers | `composegen` ([internal/composegen/generator.go](../../src/backend/internal/composegen/generator.go)) emits `traefik.http.*` labels per service |

**The bridge:** a Traefik middleware defined by the *file provider* can be referenced from a
*Docker-provider* router via the **`name@file`** suffix. So a workspace Access List rendered once
to `/dynamic` as a named middleware can be attached to many app routers through a composegen label —
**no per-app inlining, one definition reused.**

**Precedent already in the tree:** the per-env Security toggle (`protect_admin_uis`, AU-S2/S3)
already makes composegen emit a `basicAuth` middleware label on app routers. This work generalizes
that one-off into "attach any workspace Access List / plugin middleware."

## Scoping model (three tiers) — and the deliberate isolation rule

| Tier | Owner | Access Lists visible | Plugin enable |
|---|---|---|---|
| **Global** | super-admin | Proxy Service lists only | super-admin installs + enables instance-wide |
| **Workspace** | workspace admin | **only this workspace's lists** | consumes globally-enabled plugins |
| **Project / env** | workspace member | picks from this workspace's lists | per-env opt-in attach |

**Decision (locked — see [access-list-workspace-scoping] memory): STRICT workspace isolation.**
A workspace sees **only its own** Access Lists. Global (Proxy Service) lists and other workspaces'
lists are **not** visible or inheritable — deliberately breaking Rigger's usual
"global-inherits-down" convention (hosts / registries / backup-targets / channels all inherit
global defaults down).

**Why (record as integrity/least-privilege, not "password leak"):** the **credential portion**
(basic-auth users) makes a list non-shareable — a password is not a shared resource by nature.
Even though bcrypt hashes are already masked on read, sharing a *mutable* security policy across a
trust boundary lets one tenant weaken another's (or admin's) protection, and discloses
usernames/topology. Apps usually bring their own auth, so access-list basic-auth is the rare/edge
case — but it's the constraint that forces isolation.

**Possible future split (non-secret parts could be shared):** IP allow/deny + GeoIP country lists
are *not* secret; the owner is fine sharing those. A later iteration could separate **secret
(users)** from **non-secret (IP/geo)** — inherit/share the non-secret part globally, isolate the
credential part. Strict isolation is the clean v1; revisit only if duplicating common IP/country
lists across workspaces becomes a real annoyance.

## Data model

- **`proxy_access_lists` gains a scope.** Add `workspace TEXT NOT NULL DEFAULT ''` (`''` = global /
  Proxy Service; non-empty = that workspace key). All list reads filter by the caller's scope:
  - Proxy Service (super-admin) → `workspace = ''`.
  - Workspace context → `workspace = <wsKey>` **only** (no union with global — the isolation rule).
  - Mirrors the column pattern used by ws-scoped registries/backup-targets, minus the grant pool.
- **Project/env attach config.** On the project config (or per-env), store the chosen
  `access_list_id` (workspace-scoped) + independent `waf` / `cache` / `geo` opt-in booleans —
  analogous to today's per-route fields, but living in the project/env config that composegen reads.
- **Plugin enable stays global** (`proxy_waf_enabled` / `proxy_cache_enabled` / `proxy_geoip_enabled`
  app settings). Workspace toggles are *attach*, gated/greyed when the plugin isn't enabled.

## Rendering

**Shift from per-route-inline to named-shared middlewares.** Today the Proxy Service inlines
auth/IP/geo middlewares inside each `proxy-<id>.yml`. For apps, render each **workspace Access
List once** to a namespaced file-provider fragment, e.g. `/dynamic/acl-<wsKey>-<id>.yml`, defining:
- `acl-<wsKey>-<id>-auth` (basicAuth, `removeHeader` per pass-auth),
- `acl-<wsKey>-<id>-ipallow` (ipAllowList from allow rules),
- `acl-<wsKey>-<id>-geo` (geoblock, when GeoIP enabled).

Then composegen emits on the app router:
`traefik.http.routers.<r>.middlewares=acl-<wsKey>-<id>-auth@file,acl-<wsKey>-<id>-ipallow@file,…`
(plus the global `proxy-waf@file` / `proxy-cache@file` when those per-env toggles are on).

Reconciliation: a small writer (mirror `internal/maintenance` / `proxyroutes.Render`) regenerates
the `acl-*.yml` set from the DB whenever a workspace list changes, and prunes stale files. Editing
a list re-renders once and every app referencing it picks it up live (hot-reload).

**Namespacing matters** — filenames + middleware names are prefixed by workspace key both to avoid
collisions and to keep the isolation boundary legible on disk.

## RBAC

- Workspace Access List CRUD: **workspace admin** (`my_role === 'admin'`) or super-admin. Enforce in
  the API by deriving the scope from the membership, never trusting a client-supplied `workspace`.
- Attaching a list / plugin to a project env: existing project-edit permission.
- Plugin **install/enable**: super-admin only (unchanged).
- Reuse the established ws-scoping enforcement pattern (P3/P3b/P3c) for filtering + authz.

## UI

- **Manage Workspace → Access Lists tab** (new) — same editor component as the Proxy Service
  `AccessListModal`, but scoped to the workspace; lists only this workspace's entries.
- **Edit Project / per-env Security** — an "Access list" picker (filtered to the workspace) +
  WAF / Cache / GeoIP opt-in toggles (greyed with a hint when the global plugin isn't enabled),
  extending the existing `protect_admin_uis` Security control rather than a new surface.
- The global **Proxy Service** page keeps its own (global) Access Lists + Plugins card unchanged.

## Dependencies / sequencing

1. **Plugin activation (Phase 3.1 in [proxy-service.md](proxy-service.md))** should land first, or
   workspace WAF/Cache/GeoIP toggles are inert until a super-admin does the per-install plugin
   opt-in. Access Lists with just auth + IP need **no** plugin and could ship independently.
2. The GeoIP **client-IP caveat** applies to app domains too: behind a fronting proxy (NPM/CF),
   Traefik needs `forwardedHeaders.trustedIPs` or it geolocates the proxy, not the visitor.

## Phasing (when greenlit)

- **WP-1** — DB: `workspace` column on `proxy_access_lists` + scope-filtered store; migration
  defaults existing rows to `''` (global). Backfill none.
- **WP-2** — API: workspace-scoped Access List CRUD under the workspace routes; authz from
  membership; strict-isolation list filter.
- **WP-3** — Renderer: named-shared `acl-<ws>-<id>.yml` writer + reconcile/prune; reuse
  `effectiveAccess` shapes.
- **WP-4** — composegen: emit `…@file` middleware refs on app routers from project/env attach
  config + the per-env WAF/Cache/GeoIP toggles; golden-file coverage.
- **WP-5** — Frontend: Manage Workspace → Access Lists tab; Edit Project / env Security picker +
  toggles.
- **WP-6** — Verify: go build/vet/test + golden parity + npm build + rebuild rigger; live E2E
  (workspace list → attach to an env → confirm middleware on the app router, isolation across two
  workspaces).

## WP-7 — radio access model + inline per-env controls (BUILT, develop)

A later UX pass replaced the per-env "auth-gate dropdown + access-list dropdown" pair with a
single **mutually-exclusive access mode** and added **inline** alternatives to a reusable list:

- **Access mode (radio):** `Public` · `Basic Auth — HTTP password` · `Access list`. Maps onto the
  existing `auth_gate` + `access_list_id` fields (no backend change): Public → both cleared; Basic →
  `auth_gate=basic`; Access list → shows the workspace-list dropdown. Frontend (`EnvEditor`) holds
  the chosen mode in local state so "list mode, none picked yet" is representable.
- **Inline IP allow-list** (shown for Public & Basic; hidden when a list is attached): `ip_mode`
  (`""`|`allow`) + `ip_cidrs`. **Allow-list only** — Traefik's `ipAllowList` is allow-oriented and
  there is no native deny-list middleware (same limitation the Access List already has). Deny-listing
  specific IPs is an edge-firewall / WAF / future-plugin job, surfaced as a hint, not a silent no-op.
- **Inline GeoIP policy** (Public & Basic; gated on the instance-wide GeoIP plugin): `geo_mode`
  (`""`|`allow`|`block`) + `geo_countries` (ISO alpha-2 CSV). Both modes are enforced by the
  geoblock plugin.
- **Block common exploits** (always, plugin-free): `block_exploits` → a `headers` middleware
  (`frameDeny` / `contentTypeNosniff` / `browserXssFilter` / `referrerPolicy`). Request-pattern
  (SQLi / traversal) filtering remains the separate WAF toggle's job.

**Backend:** the inline fields live in `config.json` (`environments.<env>`, persisted verbatim by
`PutConfig`) and are read into `proxyroutes.envAttach`. A new
`proxyroutes.RenderEnvMiddlewares(dir, ws, proj, env, configBytes, geoEnabled)`
([internal/proxyroutes/envmw.go](../../src/backend/internal/proxyroutes/envmw.go)) renders one
`/dynamic/appmw-<ws>-<proj>-<env>.yml` (`…-ipallow` / `…-geo` / `…-sec`) and returns the `name@file`
refs; `bridge.routerMiddlewares` prepends them to the shared/WAF/cache refs from
`ResolveRouterMiddlewares` so they gate before WAF/cache. Inline IP/Geo are skipped when an access
list is attached (the list owns them); block-exploits applies in every mode. Reuses the shared
`writeAccessMiddlewares` writer (byte-identical IP/Geo output); covered by `envmw_test.go`.

**Known minor gap:** an env's `appmw-*.yml` is rewritten/pruned on each deploy (cleared rules →
file removed), but a *deleted* env leaves a stale (unreferenced, harmless) fragment until a future
prune-on-delete hook.

## Open questions / decisions to confirm

1. **Attach granularity** — per-project (applies to all its envs) vs per-env. Per-env is more
   flexible (prod stricter than staging) and matches existing per-env Security; likely per-env.
2. **One table vs split** — keep one `proxy_access_lists` with a `workspace` column (recommended),
   vs a separate workspace table. Column is simpler and mirrors existing ws-scoped resources.
3. **Secret vs non-secret split** — ship strict isolation now; defer the "share IP/geo, isolate
   credentials" refinement unless duplication becomes painful.
4. **Naming** — surface "Access Lists" identically in both places, or distinguish "Workspace access
   lists" vs the global Proxy Service ones to avoid confusion.

## Out of scope (this doc)

- Plugin install mechanics (covered by [proxy-service.md](proxy-service.md) § Phase 3.1).
- Stream (TCP/UDP) routing; rate-limiting; mTLS — separate future middlewares.
- Cross-workspace or global→workspace sharing of credentialed lists (explicitly rejected).

## Related

- [proxy-service.md](proxy-service.md) — the global engine + Phase 3.1 plugin activation.
- `internal/proxyroutes` (`accesslist.go`, `render.go`) — the store + renderer to generalize.
- `internal/composegen/generator.go` — app-router label emitter (already does per-env basic-auth).
- Memory: `access-list-workspace-scoping`, `proxy-plugin-activation-model`, `proxy-service`.
