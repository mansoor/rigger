# Rigger — Open-Core Code Split (public CE vs private)

Status: **design / for review.** Companion to `MONETIZATION_AND_LICENSING.md` (read its §1–2
first). This doc makes the split **concrete at the package level** for the current tree
(39 `internal/` packages, 61 `api/` handler files, one `cmd/server`).

## 0. The governing reality (don't skip)

**Almost the entire backend is already in public git history** and cannot be un-published.
So the split is NOT "move half the code to a private repo today." It is three separate things:

1. **What stays open and ungated** — the genuine free core (CE).
2. **What stays open but becomes license-*gated*** — advanced features already public; we keep
   the code open and add a runtime `license.Allows()` check. **Gate, don't hide** — the value
   is the key + support + Cloud, not hiding code that's already on GitHub (Plausible/Dokploy
   posture).
3. **What is genuinely private** — only two things: the **license signing key + issuer**, and
   **new commercial-only code written going forward** (SSO/SAML, audit, Cloud). These are born
   private; nothing has to be retracted.

Everything below classifies the code against that frame.

## 1. Per-package classification (`internal/`)

Legend: **CE** = public, always compiled, never gated (free core). **CE-gated** = public code,
runtime license check at the feature entry point. **EE-candidate** = if we were starting clean
this would live in the private repo; in practice it stays public + gated (history), but NEW
work in this area lands private.

| Package | Class | Why |
|---|---|---|
| `composegen`, `config`, `wsconfig`, `wspath` | **CE** | the compose generation core — the product |
| `envgen`, `dockerops`, `shell` (bridge), `builder` | **CE** | build/deploy engine |
| `workspace`, `db`, `detect`, `blueprints`, `srcarchive` | **CE** | project model, scan, upload-source |
| `settings`, `version`, `buildinfo`, `imagecheck`, `stats` | **CE** | config, self-version, image checks, dashboard stats |
| `notify` | **CE** | one channel free; *many* channels = gated at the handler |
| `auth`, `crypto`, `keygen` | **CE — never gate** | identity + secrets + 2FA. Gating security is predatory; always free |
| `backup` | **CE (manual) / CE-gated (schedule)** | manual backup free; the scheduler is the paid bit (gated in `api/backup_scheduler.go`) |
| `backupsync` | **CE-gated** | offsite S3/SFTP sync → Pro |
| `pipelines`, `executor`, `actionruns` | **CE-gated** | Deployment Pipelines → Pro |
| `previews` | **CE-gated** | Preview/PR environments → Pro |
| `managedregistry` | **CE-gated** | one-click managed registry → Pro |
| `remotehost`, `gitsync` | **CE-gated / EE-candidate** | multi-host SSH-exec + git sync → Pro/Business |
| `acme` | **CE-gated** | out-of-band per-env/wildcard DNS-01 cert automation → Pro |
| `apikey` | **CE-gated** | REST API v1 → Pro |
| `alerts`, `metrics` | **CE / CE-gated** | collection + basic alerts free; external/Prometheus export + advanced rules → Business |
| `databases` | **CE** | DB catalog (managed-DB shortcut is core UX) |
| `envorder`, `deployhistory` | **CE** | env ordering, rollback history |
| *(new)* `license` | **CE** | the **verifier** (Ed25519 verify-only, baked public key) is safe to open |
| *(new, private)* `cloudflare` (Tunnel v2 API), SSO/SAML, audit | **EE** | born in the private repo |

**Multi-host / Swarm nuance:** the gate is at the *feature entry* (adding a 2nd host, binding a
swarm deploy target, per-project build hosts), **not** the whole `dockerops`/`shell` packages —
single-host Docker + a single Swarm node stay CE. So `remotehost` code is public; `RequireWithin
("hosts", n)` / `RequireFeature("multi_host")` guards the *use*.

## 2. Feature → tier → gate location

This is the actionable list: where a `RequireFeature`/`RequireWithin` call goes (server-side is
the source of truth; UI gating is convenience).

| Feature | Tier | Gate at |
|---|---|---|
| Core deploy/update/rollback, templates, scan, upload-source, 1 host, 1 user, 2FA, secrets, self-update, manual backup | **CE** | — (never gated) |
| Deployment pipelines + webhooks + promote gates | Pro | `api/pipeline_handlers.go` create/run |
| Preview / PR environments | Pro | `api/preview_handlers.go` enable/controller |
| Backup scheduling + verification + offsite sync | Pro | `api/backup_scheduler.go`, `backup_verify_handlers.go`, `backup_sync_handlers.go` |
| Managed registry + DNS-01 wildcard / override-cert automation | Pro | `api/settings_handlers.go` (managed-registry run) + `api/acme_handlers.go` |
| Multi-host (>N) + Swarm deploy + per-project build hosts | Pro/Business | `api/hosts_handlers.go` (`RequireWithin("hosts")`), `build_host_handlers.go`, swarm deploy path |
| REST API v1 + API keys | Pro | `api/apikey_handlers.go` mint + `apiv1_handlers.go` middleware |
| RBAC / multiple users / workspace membership | Team | members handlers + `RequireWithin("users", n)` on invite |
| SSO/SAML/OIDC, audit log export, Cloud | Business/Enterprise | **born in `rigger-ee` / `rigger-cloud`** |

## 3. Repo / module layout

```
rigger              (PUBLIC)  github.com/mansoor/rigger      — CE: everything today + internal/license (verifier) + gates
rigger-ee           (PRIVATE) — Go module that imports rigger; EE feature impls + the commercial `main`
rigger-licgen       (PRIVATE) — the Ed25519 SIGNING key + key-issuing CLI/service (the crown jewel)
rigger-cloud        (PRIVATE, later) — multi-tenant control plane, billing/metering, tenant provisioner
```

- **One image, edition by key** (from the monetization doc): CE and Pro are the *same binary*;
  the license flips features. So `rigger-ee` is only needed once there are EE features that must
  not be public (SSO/SAML/audit). Until then, the commercial build = the public build + a key.
- **`rigger-licgen` is the only thing that must be secret from day one.** If its private key
  leaks, anyone can mint licenses. It is NOT imported by either app — it only emits signed keys
  the public verifier checks.

## 4. The seam (how EE plugs in without forking CE)

Two mechanisms, adopt in order:

**Option A — build tags (start here).** EE-only files in the public repo tagged `//go:build ee`.
- CE build: `go build` → tagged files excluded; gated features rely on `license.Allows()` (which
  returns false without a Pro key) — already enough for *gating already-public features*.
- Pro build: `go build -tags ee`.
- Good for the gating phase. ❌ Doesn't keep genuinely-new commercial source out of the public repo.

**Option B — private overlay module (graduate here for true secrecy).** Public CE exposes
**extension-point interfaces**; CE registers no-op/false stubs; `rigger-ee` registers the real
ones and provides `main`. Example seam:

```go
// PUBLIC rigger: internal/ext/ext.go — CE defines the contract + a default stub.
package ext
type SSOProvider interface { Authenticate(r *http.Request) (*auth.Claims, error); Metadata() SSOMeta }
var SSO SSOProvider = noopSSO{}          // CE default: SSO disabled
func RegisterSSO(p SSOProvider) { SSO = p }

// PRIVATE rigger-ee: ee/sso/saml.go — real impl, registered from the EE main.
func init() { ext.RegisterSSO(samlProvider{}) }
```
- CE compiles and runs fully standalone (stub = feature off).
- The commercial binary builds from `rigger-ee` (imports `rigger`, blank-imports `ee/...` to run
  the `init()` registrations). New commercial code (SSO, audit, Cloud connectors) lives only in
  `rigger-ee` → genuinely never public.
- Frontend mirror: the React app stays public and renders "upgrade" prompts where a feature is
  off; EE ships no separate frontend (server gates are the boundary; the UI just reflects them).

## 5. The `internal/license` boundary (what's public vs private inside licensing)

| Piece | Where | Why |
|---|---|---|
| Ed25519 **public** key (baked, ldflag-injected like `buildinfo`) | **public** CE | verify-only; safe to expose |
| `Parse`/`Allows`/`WithinLimit`/grace logic | **public** CE | it's just verification; hiding it adds nothing |
| Ed25519 **private** signing key | **private** `rigger-licgen` | mints licenses — the one true secret |
| `rigger-licgen` issuing CLI/service + Stripe hook | **private** `rigger-licgen` | issuance pipeline |

So even "licensing" is mostly public — only the signer is secret.

## 6. Migration path (low-risk, incremental)

1. **Add `internal/license` (verifier) + gates to the public repo.** No code moves. CE with no
   key = today's behavior for the free tier; gated features return 402. *(This is the whole of
   the monetization §7 step 1–2.)*
2. **Stand up `rigger-licgen`** (private) — sign keys by hand, sell via Stripe link. You can take
   money with just steps 1–2; **no `rigger-ee` needed yet**.
3. **Introduce `//go:build ee` tags** only when the first feature must differ between CE and Pro
   beyond a runtime flag.
4. **Spin up `rigger-ee` overlay module** when the first genuinely-private feature (SSO/SAML,
   audit) is built — author it there from birth; add the `ext` interfaces to CE as needed.
5. **`rigger-cloud`** is its own private repo when Cloud happens (instance-per-tenant; see
   monetization §6).

## 7. Hard rules (carry-overs, restated)
- **Never gate security** — `auth`/`crypto`/`keygen`/2FA/secrets/self-update stay CE forever.
- **Gate, don't hide** already-public code — the moat is the key + support + Cloud.
- **One image, edition = key** — don't fork the binary; that breaks the self-updater + install.sh.
- **Server-side gate is the boundary** — the public frontend only reflects it.
- **Guard `rigger-licgen`'s private key** above everything else.
```
