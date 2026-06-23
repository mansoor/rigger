# Access & Domains — redesign spec

Status: **AGREED, not yet implemented.** Supersedes the per-env "Domain + Request SSL"
model. Builds on the engine already shipped (custom-domain verification, per-host certs,
DNS-token-in-UI, auto-URL derivation) — this is mostly a **frontend consolidation + removal
of the legacy `domain` field**, plus a small composegen change and a migration.

## Why

Three overlapping ways to say "what address is this app at" accreted over time:
1. the auto URL (every routed env gets one from the base domain / fallback),
2. the editable per-env **Domain** field + **Request SSL** toggle (predates auto URLs),
3. the verified **Custom domains** list (added later).

(2) is now redundant with (1)+(3) and is the source of the confusion. SSL as a manual
toggle compounds it.

## Mental model (the whole thing)

An environment answers on a **set of addresses**:

1. **Primary URL** — every *routed* env always has one. **Derived, read-only.** Never typed.
2. **Custom domains** — zero or more, **always verified, always additive**. The single
   place to bring your own domain.

### Primary URL derivation (precedence)

The read-only Primary URL is resolved at deploy/render time, in this order:

1. **Workspace base domain** (Manage Workspace → General) → `{ws}-{prj}-{env}.{ws-base}`
2. else **Global base domain** (admin Settings → General) → `{ws}-{prj}-{env}.{base}`
3. else the **Auto-URL fallback** (admin Settings → General, applies only when NO base
   domain is set):

   | Fallback mode | Primary URL | Cross-machine? |
   |---|---|---|
   | `localhost` (**default**) | `{ws}-{prj}-{env}.localhost` | no — host-only |
   | `sslip` | `{ws}-{prj}-{env}.{App host IP}.sslip.io` | yes |
   | `nip` | `{ws}-{prj}-{env}.{App host IP}.nip.io` | yes |
   | `traefikme` | `…{App host IP}.traefik.me` | yes (shared cert) |
   | `off` | no auto URL | — |

   The `sslip`/`nip`/`traefikme` modes embed the **App host** IP (admin Settings); without
   an IP they degrade to `.localhost`. So nip.io appears **only when** the admin selected it
   AND set an App host IP — it is NOT an automatic default. A fresh install with nothing
   configured yields `.localhost`.

This is exactly today's `composegen.resolveRoute` behavior (via `EnvRouteURL`); the redesign
only makes the result **read-only/derived** instead of an editable field. The UI labels the
Primary URL with its source ("base domain" / "auto-URL: nip.io" / "local only") and, when on
the `.localhost` default, hints: *set a base domain (admin) or pick an sslip/nip fallback for
a cross-machine URL — or add a custom domain.*

**TLS is not a user choice** — it follows from the address type:

| Address | Cert | Trigger |
|---|---|---|
| `*.{base}` (primary, base domain) | wildcard DNS-01 (if CF token) or per-host HTTP-01 | automatic |
| custom domain | per-host Let's Encrypt HTTP-01 | automatic on verify |
| localhost / private IP / sslip (no real domain) | self-signed | only via the one contextual "Serve HTTPS on the local URL" checkbox |

The editable **Domain** field, the **Request SSL** toggle, and the standalone **Enable
local HTTPS** control all go away.

## Settings hierarchy (unchanged)

- **Global (admin):** base domain + DNS provider + token + auto-URL fallback (Settings → General).
- **Workspace:** base-domain override.
- **Environment:** exposure mode + custom domains + sign-in.

## The env "Access" section

One section. Mode selector first; mode-specific config below. Primary URL + custom domains
exist only under **Routed by Rigger** (they require Traefik).

```
Access
How is this environment reached?
 ◉ Routed by Rigger    — automatic URL + TLS  (default)
 ○ Host port           — publish a port; your proxy/DNS owns domain+TLS
 ○ Cloudflare Tunnel   — reachable anywhere, no open ports
 ○ Internal only       — in-network; optional attach-to-network

▸ Routed by Rigger
   Primary URL   https://mcl-act-dev.mansoorslab.com   [copy] [open ↗]   (read-only)
                 Automatic. TLS issued for you.
                 (no base domain ⇒ shows the sslip/localhost fallback + a hint to set
                  a base domain in admin, or add a custom domain for a real URL)

   Custom domains (optional)            ← the ONLY bring-your-own surface
     [ app.example.com            ] [ Add ]
     ★ www.acme.com   ✓ verified · cert active        [⋯ unstar] [✕]
       shop.acme.com  ⚠ pending                        [Verify] [How to] [✕]
          Add ONE, then Verify:
            CNAME  shop.acme.com → mcl-act-dev.mansoorslab.com   (also routes)
            TXT    _rigger-challenge.shop.acme.com = rigger-verify=<token>
            File   http://shop.acme.com/.well-known/rigger-verify/<token>

   Require sign-in
     ◉ None   ○ Basic auth   (⚠ breaks token-based apps — Activepieces, Grafana, SPAs)
```

### Canonical domain (the ★)
Auto URL is always the primary identity. The user may **★ one verified custom domain** as
*canonical*: it drives the env-card "Open app" link and feeds an `APP_URL`-style hint. This
covers "my app needs to know its real domain" without a separate input. Unstarred ⇒ Open
uses the auto URL.

### localhost / private primary
When the primary URL can't get a real cert (localhost, private IP, sslip), show a single
checkbox **"Serve HTTPS on the local URL (self-signed)"** for apps that refuse HTTP. That is
the only surviving TLS control.

## Backend changes

- **composegen `resolveRoute`:** remove the "explicit `e.Domain` wins / replaces the auto
  URL" branch. The primary URL is **always** the derived auto URL when routed; custom
  domains are **always** additive routers (already implemented via `traefikCustomDomains`).
  Goldens regenerated.
- **`custom_domains`:** add a `primary BOOLEAN` (the ★). At most one per env. Surfaced via
  the existing list endpoint; new `POST …/domains/{id}/primary` (and unset).
- **Canonical → APP_URL hint:** when a ★ domain exists, expose it to envgen as the route
  URL (optional, behind the existing route-URL mechanism). Non-breaking.
- **Migration:** for every env with a non-empty `cfg.domain`, seed a `custom_domains` row
  (verified=1, primary=1) from it, then stop reading `cfg.domain` for routing. Idempotent;
  run once on boot. Existing live envs keep their domain + cert with zero user action.
- Keep: verification (TXT/CNAME/file), per-host HTTP-01, DNS-token-in-UI, `EnvRouteURL`.

## Frontend changes

- **Remove** the editable Domain `<Input>`, the Request-SSL toggle, and Enable-local-HTTPS
  from the env editor.
- **Add** the read-only Primary URL row (value from `EnvRouteURL`; copy + open).
- **Promote** `CustomDomainsPanel` to the main Routed-mode body; add ★ set/unset + a
  "cert active/pending" hint per verified domain.
- Contextual self-signed checkbox only when the primary URL is local/private.
- Env-card URL = ★ custom (if any) else primary auto URL.

## Out of scope (later)
- Auto-redeploy on verify (today: verify regens compose; routing applies on next deploy).
- Custom-domain cert-expiry badge.
- Wildcard custom domains.

## Migration risk / rollout
- One-shot boot migration of `cfg.domain` → seeded primary custom domain is the only
  data-touching step; it's additive (writes new rows, doesn't delete config).
- Golden parity is intentionally broken for envs that USED `cfg.domain` (their compose now
  emits an additive custom-domain router instead of the primary-replacement router) — update
  goldens in the same change.
