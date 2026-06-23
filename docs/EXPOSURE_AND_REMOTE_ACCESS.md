# Rigger — App Exposure Model: Cloudflare Tunnel + Auth-Gating (Design Doc)

Status: **Phase 1 (basic auth-gate) + Phase 2 (Cloudflare Tunnel v1, token) SHIPPED** on
develop (backend aa6343e, frontend e2b478f): `expose_mode`/`auth_gate` tri-state model +
`cloudflared` connector + `${APP_AUTH_USERS}` basic-auth on any web router + env-editor
Exposure section + env-card badges. Default output byte-identical (golden parity). Live
E2E (real CF tunnel token) pending. Remaining: **P3** forwardAuth→Authentik, **P4** Tunnel
v2 (API-managed), wizard-side controls, env-card URL = CF hostname. Original design below.

Status (original): **design / for review** — no code yet. Scope-compatible with the Docker + Swarm
harden-first stance (additive; does not touch the core deploy paths). Build *after*
hardening; this locks the model so it isn't re-litigated.

## 0. Problem

Today a Rigger env's web entry is binary: either **routed publicly via rigger-traefik**
(on the base/custom domain, optionally with Let's Encrypt) or **not routed** (host-port
only / internal). Real apps need a middle ground: *"reachable by a limited audience, or
just me when I'm out, without opening my firewall to the world."*

There are three established ways to do that. They are **complementary, not competing**, and
Rigger should model them as a per-app **exposure mode** plus an orthogonal **auth-gate**:

| Need | Mechanism | Public ports? | Client on device? | Auth |
|---|---|---|---|---|
| Public but locked | Traefik + auth middleware | yes (443) | no | basic / SSO |
| Private origin, reachable anywhere | **Cloudflare Tunnel** (+ Access) | **no** | no | CF Access (email OTP / SSO) |
| Fully private overlay | Tailscale / Netbird / Twingate | no | **yes** | mesh identity |

## 1. Why Cloudflare Tunnel is the right primary target for Rigger

The mesh-VPN options (Tailscale/Netbird/Twingate connectors, WireGuard) all need
**`NET_ADMIN` + a `/dev/net/tun` device** inside the container — the exact
capability/device-passthrough problem we hit with the `wg-easy` template and **cannot
express in the current service schema** (no `cap_add`/`devices`/`sysctls`).

`cloudflared` is a plain **outbound TCP** process: no caps, no devices, no inbound ports,
no host networking. That makes it:
- **Templatable today** — just an image + one secret (`TUNNEL_TOKEN`) + the env network.
- **Swarm-native** — runs as a normal (even replicated, auto-load-balanced) service; no
  single-node constraint, unlike `wg-easy`/Tailscale.
- **Multi-host friendly** — no NAT/firewall/port work on any node.
- **Zero attack surface on the origin** — nothing is published; the firewall stays closed.

So of all "remote/limited access" options, Cloudflare Tunnel is the one that fits Rigger's
architecture cleanly *right now*. Mesh VPNs are deferred behind a future capability/device
passthrough mechanism (the same gap that blocks `wg-easy`).

## 2. The exposure model (data model)

Two **orthogonal** per-env (and ideally per-`web_routed`-service) dimensions:

### 2a. `expose_mode` — how the app is reachable
- `traefik` (default, current behavior) — public router on rigger-traefik.
- `cloudflare_tunnel` — no public router; a `cloudflared` sidecar dials Cloudflare's edge
  and forwards to the app in-network.
- `none` — internal/host-port only (today's "not routed").

### 2b. `auth_gate` — who gets in (independent of mode)
- `none` (default).
- `basic` — Traefik HTTP basic-auth (reuses the existing `${ADMIN_UI_USERS}` middleware).
- `forward_auth` — Traefik forwardAuth → an Authentik/OIDC endpoint (we now ship an
  Authentik template) for real per-user SSO/MFA.

  > Note: `auth_gate` applies to the **`traefik`** mode. Under `cloudflare_tunnel`, auth is
  > Cloudflare Access's job (configured CF-side), so the two auth systems don't stack — the
  > UI should make `auth_gate` mutually contextual with the mode.

Mirrors the existing tri-state `Eff*` pattern (`EffMailpit`, `EffWebSQL`): project-level
default + per-env override. New fields on the env config (`internal/wsconfig` `Env` +
`internal/composegen` `Env`):
```go
ExposeMode string `json:"expose_mode,omitempty"` // "" => traefik (default)
AuthGate   string `json:"auth_gate,omitempty"`   // "" => none
```
with `EffExposeMode(e)` / `EffAuthGate(e)` resolvers alongside the others in
`wsconfig.go:218+`.

## 3. Cloudflare Tunnel — how it wires in

### 3a. v1 — token-based ("remotely-managed"), the MVP
The user creates a tunnel in the Cloudflare Zero Trust dashboard, sets the public hostname
+ ingress (→ `http://{app-service}:{port}`) and an Access policy there, and copies the
**tunnel token** into Rigger as a secret. Rigger's only job is to run the connector wired to
the app. composegen, when `EffExposeMode(e) == "cloudflare_tunnel"`, emits:

```yaml
{prefix}_cloudflared:
  image: cloudflare/cloudflared:2024.x
  command: tunnel --no-autoupdate run
  environment:
    - TUNNEL_TOKEN=${CF_TUNNEL_TOKEN}     # Rigger secret (Swarm secret in prod)
  restart: unless-stopped
  networks: [ {prefix}_net ]              # reach the app by service name
  # no ports, no caps, no volumes
```
and **suppresses the app's public Traefik labels** (the app needs no `host_port`, no cert,
no DNS record on the origin). The connector reaches the app as `http://{app}:{port}` over
the env network.

- **Secret:** `CF_TUNNEL_TOKEN` is the tunnel — store it as a Rigger secret (compose `.env`
  in dev, Docker Swarm secret in prod, matching the Phase 8 secrets model). Never plaintext
  in `config.json`.
- **Swarm:** `cloudflared` deploys like any service; can be replicated for HA (CF
  load-balances multiple connectors of the same tunnel).
- **Split-brain caveat:** hostname/ingress/Access policy live in the CF dashboard, not
  Rigger. Rigger runs the connector; CF holds the routing + auth. Acceptable for v1; closed
  by v2.

### 3b. v2 — API-managed ("locally-managed"), deeper integration
With a **Cloudflare API token** (Zero Trust + DNS edit, stored once like the existing
`CF_DNS_API_TOKEN`), Rigger creates the tunnel, pushes ingress rules, the public DNS record,
and optionally an **Access policy** (email allowlist / SSO) — all from Rigger. The connector
then runs from a generated `config.yml` + credentials file (bind-mounted), or still via
token. This removes the split-brain: the env's exposure + who-can-reach-it is configured in
Rigger's UI. Bigger lift (CF API client in a new `internal/cloudflare` package); do it only
if v1 sees use.

Reuses an asset we already have: the account-level **Cloudflare integration** that backs
DNS-01 wildcard certs (`CF_DNS_API_TOKEN`, `apps_dns_provider=cloudflare`). v2 extends that
same Cloudflare account linkage to Zero Trust.

## 4. Auth-gating (the complement) — mostly already built

The `basic` gate is a near-free reuse of the **`protect_admin_uis`** machinery:
- `generator.go:418` already emits
  `traefik.http.middlewares.{router}_auth.basicauth.users=${ADMIN_UI_USERS}` and
  `builders.go:386` attaches it via `traefikLabels(router, host, port, svc.AuthProtect &&
  e.ProtectAdminUIs, ...)`.
- Generalize: drive the same middleware from `EffAuthGate(e) == "basic"` on **any
  `web_routed` service** (not just synthesized admin sidecars), with per-env creds
  (`${APP_AUTH_USERS}` bcrypt htpasswd, same single-quoted-`$` handling as
  `ADMIN_UI_USERS`).

The `forward_auth` gate emits Traefik's forwardAuth middleware pointing at an Authentik
outpost/endpoint:
```
traefik.http.middlewares.{router}_fa.forwardauth.address=https://{authentik}/outpost.goauthentik.io/auth/traefik
traefik.http.middlewares.{router}_fa.forwardauth.trustForwardHeader=true
traefik.http.middlewares.{router}_fa.forwardauth.authResponseHeaders=X-authentik-username,...
```
This turns the Authentik template into the SSO front for *any* Rigger-routed app — a strong
story now that Authentik is one click away.

## 5. Frontend

- **Env editor → a "Exposure" section** (sits next to the existing Traefik/SSL + Tooling
  tri-state controls): pick `expose_mode` (Traefik / Cloudflare Tunnel / Internal-only) and,
  when Traefik, an `auth_gate` (None / Basic / Authentik SSO).
- **Cloudflare Tunnel chosen** → reveal a `CF_TUNNEL_TOKEN` secret field + concise inline
  guidance ("create a tunnel in CF Zero Trust → point its public hostname at
  `http://{app}:{port}` → paste the token; add a Cloudflare Access policy to limit who can
  reach it"). v2 replaces this with Rigger-side hostname + Access-email fields.
- **Env-card URL/badge** (`resolveEnvRoute` / env card): show a "via Cloudflare Tunnel"
  badge and the CF hostname instead of the Traefik URL when in tunnel mode; show a lock icon
  when an auth-gate is set.

## 6. Security caveats (surface in the UI)

- **Cloudflare is in the path.** For proxied hostnames, Cloudflare terminates TLS at its
  edge — they can see decrypted traffic there. Fine for "reach my dashboard when out";
  for highly sensitive data a mesh VPN (CF not in path) is better. State it plainly.
- **Needs a Cloudflare account + a zone on Cloudflare + Zero Trust enabled.** Free tier
  covers personal use (Access free up to 50 users).
- **The tunnel token == the tunnel.** Treat as a first-class secret.
- **Auth-gate `basic` is shared-credential** — fine for "just me", weak for a team; steer
  teams to `forward_auth`/Access.
- **`cloudflared` image pinning** — pin a stable `cloudflare/cloudflared` tag; it
  auto-updates by default (we pass `--no-autoupdate` to keep deploys deterministic).

## 7. Phasing
- **Phase 1 — auth-gate generalization (smallest, highest reuse):** `EffAuthGate` + extend
  the existing basic-auth middleware to any `web_routed` service + env-editor toggle. Ships
  "public but locked to me" with almost no new infra.
- **Phase 2 — Cloudflare Tunnel v1 (token):** `expose_mode` field + `cloudflared` sidecar in
  composegen (suppress public labels) + `CF_TUNNEL_TOKEN` secret + env-editor mode picker +
  env-card badge. The headline feature.
- **Phase 3 — forwardAuth → Authentik:** wire the Authentik template as an SSO front
  (`auth_gate=forward_auth`).
- **Phase 4 — Cloudflare Tunnel v2 (API-managed):** `internal/cloudflare` client; create
  tunnel + ingress + DNS + Access from Rigger; remove the dashboard split-brain.
- **Deferred — mesh VPN (Tailscale/Netbird):** blocked on a container capability/device
  passthrough mechanism (same gap as `wg-easy`). Revisit if that lands.

## 8. Critical files
- `internal/wsconfig/wsconfig.go` (`Env` struct ~340 + `EffExposeMode`/`EffAuthGate` by
  the other `Eff*` resolvers ~218).
- `internal/composegen/config.go` (mirror `Env` fields), `internal/composegen/builders.go`
  (emit `cloudflared` sidecar; gate app's web labels on `expose_mode`; extend the
  `traefikLabels(... authProtect ...)` call at :386 to honour `auth_gate`),
  `internal/composegen/generator.go` (the basic-auth middleware line at :418; add a
  forwardAuth variant).
- `internal/shell/bridge.go` + `internal/dockerops` (thread the new env opts, same as
  BaseDomain/auto-URL are threaded).
- Secrets: reuse the Phase 8 secret path for `CF_TUNNEL_TOKEN` / `APP_AUTH_USERS`.
- Frontend: `EnvEditor` (Exposure section), `lib/envRoute.js` + env card (badge/URL).
- v2 only: new `internal/cloudflare` (API client), settings for the Zero Trust API token
  (extends the existing `apps_dns_provider`/`CF_DNS_API_TOKEN` Cloudflare linkage).

## 9. Verification
- Unit/golden: composegen emits the `cloudflared` sidecar + suppresses public labels under
  `expose_mode=cloudflare_tunnel`; emits basic/forwardAuth middleware under `auth_gate`;
  **golden parity unchanged** for the default (`expose_mode` empty ⇒ today's output
  byte-for-byte). Tri-state override (project default vs per-env) like the Mailpit tests.
- Live (Phase 2): a real CF tunnel token → deploy an app with `expose_mode=cloudflare_tunnel`
  → the app is unreachable on the origin's ports but reachable at the CF hostname, gated by a
  CF Access email policy. Confirm no inbound ports opened and it works behind NAT.
- Swarm: `cloudflared` deploys + reconnects across nodes; replicas load-balance.
```
