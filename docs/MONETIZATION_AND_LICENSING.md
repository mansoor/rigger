# Rigger — Monetization, Open-Core Split & Licensing (Design Doc)

Status: **design / for review** — no code yet. This is the plan to turn Rigger into a
commercial open-core product (free self-hosted CE + paid self-hosted tiers + an optional
hosted SaaS), covering: what to open-source vs keep private, how to name the editions, the
license-key mechanism, the feature-gate plumbing (grounded in the current code), and what
running the SaaS actually requires.

---

## 0. The strategic shape (decision first)

Rigger is **self-hosted** — it runs on the customer's hardware with their Docker socket and
their SQLite DB. That single fact rules out SaaS-style metering for the self-hosted product
and pushes us to the model that every comparable tool converged on:

> **Open-core**: an genuinely-useful free core (open source) + advanced/scale/team features
> behind a **runtime-validated license key**, optionally plus a **hosted "Rigger Cloud"** tier
> where we run the control plane and SaaS metering actually works.

Enforcement is **"good enough" + license terms**, not DRM. The buyers who pay (companies) are
exactly the ones who won't ship unlicensed software. Hobbyists get a great free tool forever;
that's the funnel, not the leak.

Three revenue surfaces, in order of build effort:

1. **Rigger Pro/Team license keys** (self-hosted, paid) — *lowest effort, build first.* Reuses
   everything we have; adds one `internal/license` package + feature gates + a Stripe payment
   link. Sell keys by hand on day one.
2. **Rigger Cloud** (hosted) — *highest revenue ceiling, highest effort.* We run the control
   plane; customers connect their own Docker/Swarm hosts (or rent ours). This is where you've
   *already* built the enabling plumbing: multi-host, managed registry, DNS-01 wildcard,
   per-project build hosts.
3. **Support / Enterprise** (SSO, audit, SLA) — sells itself once 1 & 2 exist.

---

## 1. Naming the editions

**Recommendation: keep "Rigger" as the brand, never as an edition.** Don't make the free thing
literally `rigger` and the paid thing `rigger-pro` — that frames the paid features as a bolt-on
and dilutes the brand. Instead, one brand with editions (the GitLab / Plausible / Sentry model):

| Edition | What it is | Distribution | Repo |
|---|---|---|---|
| **Rigger** (or **Rigger CE** / *Community Edition*) | Free, open source, self-hosted. The whole core. | public Docker image + public repo | `rigger` (public) |
| **Rigger Pro** | Paid self-hosted, single org/team. Unlocks scale + automation. | same image, license key | core public + `ee/` private |
| **Rigger Business / Enterprise** | Paid self-hosted, RBAC/SSO/audit/SLA. | same image, license key | core public + `ee/` private |
| **Rigger Cloud** | Hosted SaaS. We run it. | n/a (our infra) | core public + `ee/` + `cloud/` private |

Why this naming over your two options:

- **"rigger-ce" vs "rigger"** — fine, this is the GitLab convention (CE/EE). Clear to
  developers. Slightly enterprise-y/heavy. Use **CE/EE** if you want to signal "serious infra
  tool"; the suffix lives in the *repo/image tag*, the product is still just "Rigger".
- **"rigger" vs "rigger-pro"** — cleaner consumer feel, but "Pro" implies exactly two tiers and
  doesn't leave obvious room for Business/Enterprise/Cloud. Works if you commit to a simple
  Free/Pro ladder.

**My pick:** brand = **Rigger**. Free edition surfaced to users as **Rigger Community** (image
tag `:ce`, repo `rigger`). Paid = **Rigger Pro** / **Rigger Business** (same image, key-gated).
Hosted = **Rigger Cloud**. One image, edition determined by the license key — *not* separate
download artifacts (keeps the build + update path single, which your self-updater depends on).

> Key consequence: **one binary, one image.** The CE image and the Pro image are byte-identical;
> the license key flips features on. This is what keeps `install.sh`, the self-updater, and
> GHCR distribution from forking into two pipelines. (The alternative — a separate closed binary
> — doubles the release matrix and breaks the in-app updater story. Avoid.)

---

## 2. Open-source vs private: what goes where

> **See `OPEN_CORE_CODE_SPLIT.md` for the concrete package-level split** (all 39 `internal/`
> packages + handler gate locations + repo layout + the EE overlay seam). This section is the
> summary.

The whole current tree is already coherent. The split is about **which packages compile into the
public CE build** vs **which live in a private overlay that only the commercial build pulls in**.

### 2a. Stays OPEN (public `rigger` repo, CE build)

Everything that exists today, minus the handful of items in §2b. Concretely the CE core is:

- **Core deploy engine** — `internal/composegen`, `internal/dockerops`, `internal/shell/bridge`,
  `internal/envgen`, `internal/builder`, `internal/wsconfig`, `internal/wspath`,
  `internal/workspace`.
- **Single-host everything** — create/deploy/update/rollback, templates, blueprints, scan,
  upload-source, env management, logs, terminal, file browser.
- **Auth + security** — `internal/auth`, `internal/crypto`, `internal/keygen`, secrets,
  **2FA, password policy** (never paywall security — see §3).
- **Self-updater** — `internal/buildinfo`, `api/updates_handlers.go`, `api/version_handlers.go`
  (free for everyone, including CE — updating must never be gated).
- **The entire frontend** (`src/frontend`) — but it renders upgrade prompts where Pro features
  are gated (see §5). UI is open; the *server* enforces.
- **One** notification channel, **manual** backup, the local registry-less build path.

Open-sourcing the core is the marketing engine (stars, trust, contributions, "self-host it
yourself" credibility) and it's already 95% of the codebase.

### 2b. Goes PRIVATE (commercial overlay — `ee/` packages)

Move the *server-side enforcement and the genuinely-advanced subsystems* into an `ee/` tree that
the public build stubs out and the commercial build compiles in. Candidates (these map to your
existing memory-tracked features):

- **`internal/license`** — the key verifier itself (public key can be public; the *issuer*/signer
  is private — see §4). The verifier can actually be open; what's private is the **signing key**
  and the **issuing service**.
- **Multi-host / Swarm orchestration extras** — the per-env host binding, swarm deploy policy,
  per-project build hosts (`BuildHostFor`, host capability probe). *(Core single-host deploy stays
  open; the multi-host fan-out is the Pro line.)*
- **Deployment Pipelines** (`internal/pipelines`, `api/pipeline_handlers.go`) + webhooks + promote
  gates + pipeline alerting.
- **Preview/PR environments** (`api/preview_*`).
- **Managed registry** (`internal/managedregistry`) + DNS-01 wildcard automation
  (`internal/acme` out-of-band issuer).
- **Backup scheduling + verification + offsite sync** (`api/backup_scheduler.go`,
  `backup_verify_handlers.go`, `backup_sync_handlers.go`). *(Manual one-off backup stays open.)*
- **REST API v1 + API keys** (`internal/apikey`, `api/apiv1_*`).
- **RBAC / multi-user / workspace membership** (the `rbac_gate.go` enforcement + members) — Team
  tier. *(Single-admin stays open.)*

> **Reality check:** a lot of this is *already written and committed in the public history.* You
> cannot un-publish git history. So the "private" split is forward-looking — it governs where
> *new* commercial code lands and which *enforcement gates* are added. The pragmatic open-core
> move for already-published code is: **keep the code open, gate it with the license check.** The
> license check is cheap to add and the value is in the key + support + Cloud, not in hiding code
> that's already on GitHub. (This is exactly Plausible's and Dokploy's posture.)

### 2c. Repo mechanics — two clean options

**Option A — build tags in one repo (simplest):**
- Public repo `rigger`. Commercial code in files tagged `//go:build ee`.
- CE build: `go build` (no tag) → `ee` files excluded; a `license.Allows()` stub returns
  `false` for paid features (or the gate is simply absent and the feature is CE-included).
- Pro build: `go build -tags ee` → real `ee` implementations compiled in.
- ❌ Downside: the `ee`-tagged source still sits in the public repo unless you also split repos.

**Option B — private overlay module (GitLab EE model, recommended for real separation):**
- Public repo `rigger` (CE) — compiles and runs fully on its own.
- Private repo `rigger-ee` — a Go module that *imports* `rigger` and provides the `ee/`
  implementations + a `main` that wires them in. The commercial image builds from `rigger-ee`.
- The public `Handler`/registration exposes **extension points** (interfaces) that CE fills with
  no-op/`false` stubs and EE fills with the real thing.
- ✅ Clean: nothing commercial in the public repo. ❌ More plumbing: you maintain the seam.

Start with **Option A** (tags) to ship Pro keys fast; graduate to **Option B** when Cloud +
Enterprise justify the separation.

---

## 3. The tier / feature matrix

Gate on **scale + collaboration + automation**, never on core function or security. A solo user
should run one app forever, free, and love it.

| Capability | Community (free) | Pro | Business | Cloud |
|---|---|---|---|---|
| Core deploy/update/rollback, templates, scan, upload-source | ✅ | ✅ | ✅ | ✅ |
| Hosts | 1 | 3 | unlimited | managed |
| Users | 1 (single admin) | 1 | unlimited + RBAC | unlimited + RBAC + SSO |
| Projects / envs | small cap (e.g. 3 projects) | unlimited | unlimited | metered |
| 2FA, secrets, password policy | ✅ | ✅ | ✅ | ✅ |
| Self-update | ✅ | ✅ | ✅ | n/a |
| Manual backup | ✅ | ✅ | ✅ | ✅ |
| Backup scheduling + verification + offsite sync | — | ✅ | ✅ | ✅ |
| Deployment pipelines + webhooks + promote gates | — | ✅ | ✅ | ✅ |
| Preview / PR environments | — | ✅ | ✅ | ✅ |
| Swarm deploy + per-project build hosts | — | ✅ | ✅ | ✅ |
| Managed registry + DNS-01 wildcard automation | — | ✅ | ✅ | ✅ |
| REST API v1 + API keys | — | ✅ | ✅ | ✅ |
| Notification channels | 1 | many | many | many |
| Audit log, SSO/SAML, SLA support | — | — | ✅ | ✅ |

**Never paywall:** 2FA, secrets, security patches, the self-updater. Gating those reads as
predatory to the exact technical buyers you need, and it's a support/PR liability.

### Pricing posture (your numbers, reframed)

$4.99/9.99/19.99 is **consumer pricing**; Rigger's buyer runs infrastructure. Keep the low end
for the solo/hobby Pro tier, but meter the team/business tier on something tied to value and
observable in-product (**hosts** or **seats**), because a flat per-account number is both
under-priced and hard to defend self-hosted:

| Tier | Audience | Indicative price | Meter |
|---|---|---|---|
| Community | hobby / solo | $0 | 1 host, 1 user |
| Pro | power solo | $12–19/mo | unlimited projects, 1 user, ≤3 hosts |
| Business | team | $49–99/mo or per-seat | multi-host, RBAC, offsite backup |
| Enterprise | company | custom | SSO, audit, SLA |

Annual = "2 months free" (~17%) — your ~20% instinct is right. (Not financial/pricing advice;
validate against willingness-to-pay before committing.)

---

## 4. License mechanism (the actual code)

Mirror the **`buildinfo`** pattern (ldflag-injected, package-level) for the public key, and the
**`rbac_gate.go`** pattern (a gate that returns `bool` and writes the 4xx) for enforcement.

### 4a. Key format — offline-verifiable, signed JSON (Ed25519)

No phone-home required (works on air-gapped self-hosts). A key is a base64 blob = JSON payload +
Ed25519 signature. We sign with a **private key we never ship**; the binary embeds only the
**public key** (safe to open-source — it can only *verify*).

```jsonc
// payload (signed)
{
  "lic_id":   "rl_8f3a…",
  "edition":  "pro",                 // community | pro | business | enterprise
  "customer": "Acme GmbH",
  "issued":   "2026-06-19",
  "expires":  "2027-06-19",          // grace handling below
  "limits":   { "hosts": 3, "users": 1, "projects": 0 },  // 0 = unlimited
  "features": ["pipelines","preview_envs","backup_schedule","rest_api","managed_registry"]
}
```

### 4b. `internal/license` package

```go
// Package license validates the Rigger commercial license key. The PUBLIC key is
// baked in (verify-only; safe to open-source). The PRIVATE signing key lives only
// in the issuer service (see §4d) and never ships in any binary or repo.
package license

// PublicKeyB64 is the Ed25519 public key, injected at link time like buildinfo:
//   -ldflags "-X github.com/mansoor/rigger/ui/internal/license.PublicKeyB64=<b64>"
// Defaults empty ⇒ all paid features locked (a CE build with no key configured).
var PublicKeyB64 = ""

type Edition string
const (
    Community  Edition = "community"
    Pro        Edition = "pro"
    Business   Edition = "business"
    Enterprise Edition = "enterprise"
)

type License struct {
    ID       string
    Edition  Edition
    Customer string
    Issued   time.Time
    Expires  time.Time
    Limits   map[string]int   // "hosts","users","projects"; 0 = unlimited
    Features map[string]bool
}

// Service holds the currently-loaded license. Lives on api.Handler next to auth/db.
type Service struct {
    mu  sync.RWMutex
    lic License // zero value == Community (everything paid is off)
}

func NewService(db *db.DB) *Service { /* load key from app_settings 'license_key', Parse */ }

// Parse verifies the signature against PublicKeyB64 and decodes the payload.
// Returns Community + error on any failure (tamper, wrong key, malformed).
func Parse(key string) (License, error) { /* split blob, ed25519.Verify, json.Unmarshal */ }

// Allows reports whether the loaded license grants a feature. Expired licenses
// fall back to Community for paid features (see grace, §4c).
func (s *Service) Allows(feature string) bool { … }

// WithinLimit reports current<limit for a counted resource ("hosts","users",…).
func (s *Service) WithinLimit(name string, current int) bool { … }

func (s *Service) Edition() Edition { … }
func (s *Service) Info() License    { … } // for the Admin → License panel (masked id)
```

The key string itself is stored in the existing **`app_settings`** KV table (key
`license_key`), set via an Admin → License screen — consistent with how every other global
setting is stored (`settings.AppSetting(db, …)`). No new storage subsystem.

### 4c. Expiry & grace (don't brick production)

A control plane bricking your prod deploys because a card expired is unacceptable and would kill
trust. Policy:

- **Expired key → features stay ON for a grace window** (e.g. 14–30 days) with a loud Admin
  banner, then **downgrade to Community** (paid features lock, *existing deployments keep
  running* — we never stop running workloads, only disable new privileged actions).
- **No key / invalid key → Community.** CE is the safe floor.
- The verifier is **offline**; renewal just means pasting a new key. (Cloud handles billing
  server-side, so this only applies to self-hosted.)

### 4d. Issuer (private — the only thing that must stay secret)

A tiny private service / CLI that holds the Ed25519 **private** key and, on a paid Stripe
checkout (or manual sale), emits a signed key and emails it. Day-one this can be a local CLI you
run by hand:

```
rigger-licgen --edition pro --customer "Acme" --expires 2027-06-19 \
              --limit hosts=3 --feature pipelines,preview_envs,…
```

This is the crown jewel: **guard the private key** (it's the thing that, if leaked, lets anyone
mint keys). Everything else can be open.

### 4e. CE activation model & adoption signal (decided 2026-06-20)

**Decision: CE is frictionless — no license required to install or use it.** A mandatory
email/license wall on the *base* tier is a real deterrent for exactly the self-hosted/homelab
audience open-core depends on (they self-host to avoid accounts/phone-home), and it costs the
top-of-funnel curious user who converts later. The accepted place for an email-gate is the
*upgrade boundary*, not the front door (cf. Portainer: **CE needs no license**; only the free
**Business** allowance — up to 3 nodes — requires an email-gated key).

So:
- **CE installs, creates its admin login, and runs fully with no license.** Unlicensed == today's
  free behavior. Never gate this.
- **Optional "Activate" prompt on first login — non-blocking.** A dismissible card with three
  actions: *Enter license · Buy Pro · Get a free Community license (email)*. Skipping leaves CE
  fully functional. Make the free Community license worth opting into with small carrots
  (release/security notifications, supporter badge, roadmap weight, newsletter) — NOT by crippling
  the unlicensed experience. People opt in for the perk → you get a willing, higher-intent email.
- **Get real adoption numbers from opt-out anonymous telemetry, not emails.** A periodic instance
  heartbeat (version, OS, anonymous instance ID, rough project/host counts) with a clear opt-out
  and a documented payload is the community-accepted way to measure installs/active instances —
  and it captures the *whole* population, not just the self-selected fraction that hands over an
  email. The Community-license email list is a *bonus* lead source on top, never the primary metric.
- **Reserve the hard email-gate for the Pro free trial / a free-tier allowance later** (Portainer
  model) — that's where users are reaching for more and the gate is expected + converts.

Plumbing impact: none beyond what's already specced. A "Community license" is just
`edition: community` signed by the same issuer (§4d) and verified offline — the verify path is
identical whether or not someone activates. The only additions are the frontend activation card
(needed for Pro anyway) and a small **opt-out telemetry** endpoint/heartbeat. None of it changes
the open-core split. Caveat: this is strategy from established patterns + named precedent, not a
controlled study — ship CE frictionless, measure with telemetry, and A/B the activation prompt
later if you want hard numbers.

---

## 5. Feature-gate plumbing (matches the current code)

### 5a. Wire the service onto `Handler`

`Handler` already carries `auth *auth.Service` and `db *db.DB` (handlers.go:84). Add one field:

```go
type Handler struct {
    auth *auth.Service
    db   *db.DB
    lic  *license.Service   // ← new
    …
}
```

…constructed in `cmd/server/main.go` alongside the others and reloaded when the key is saved.

### 5b. A `RequireFeature` gate — mirrors `GateWorkspace`

`rbac_gate.go` already establishes the "gate returns bool, writes the 402/403 itself" idiom.
Add the license analogue:

```go
// RequireFeature blocks a request unless the license grants `feature`. Returns
// false (and writes 402 Payment Required) when locked — the frontend turns 402
// into an "Upgrade to Pro" prompt rather than a generic error.
func (h *Handler) RequireFeature(w http.ResponseWriter, feature string) bool {
    if h.lic.Allows(feature) {
        return true
    }
    writeJSON(w, http.StatusPaymentRequired, map[string]string{
        "error":   "this feature requires Rigger " + license.FeatureTier(feature),
        "feature": feature,
        "upgrade": "https://getrigger.dev/pricing",
    })
    return false
}

// RequireWithin blocks when a counted resource would exceed the license limit.
func (h *Handler) RequireWithin(w http.ResponseWriter, name string, current int) bool { … }
```

Enforcement points (server-side is the source of truth — UI gating is convenience only):

- **Pipelines:** top of `pipeline_handlers.go` create/run handlers → `RequireFeature(w,
  "pipelines")`.
- **Preview envs:** `preview_handlers.go` enable/controller → `RequireFeature(w,
  "preview_envs")`.
- **Backup schedule/verify/sync:** the three handler files → `RequireFeature(w,
  "backup_schedule")`.
- **Managed registry / DNS-01:** `settings_handlers.go` managed-registry run +
  `acme_handlers.go` → `RequireFeature(w, "managed_registry")`.
- **Multi-host / build hosts / swarm:** `build_host_handlers.go`, host-bind, swarm deploy path →
  `RequireFeature(w, "multi_host")`, plus `RequireWithin(w, "hosts", n)` when adding a host.
- **REST API + keys:** `apikey_handlers.go` mint + `apiv1_handlers.go` middleware →
  `RequireFeature(w, "rest_api")`.
- **RBAC/members:** members handlers + `WithinLimit("users", n)` on invite.

### 5c. Frontend

- Add `lib/api.js` `fetchLicense()` → `GET /api/license` (edition, features, limits, expiry).
- A `useLicense()` hook + `<FeatureGate feature="pipelines">…</FeatureGate>` that renders the
  feature or an inline "Upgrade to Pro" card. Pure UX — the server gate is the real boundary.
- **Admin → License** tab (sibling to the existing Updates tab in `SettingsPage.jsx`): paste
  key, show edition/customer/expiry, grace-period banner. Reuses the Updates tab layout.
- Map HTTP **402** globally to an upgrade modal (same place the API client handles 401).

### 5d. New routes (in `cmd/server/main.go`, mirroring the updates routes)

```
GET  /api/license            → current license info (any authed user; masked)
PUT  /api/license            → set/replace the key (superadmin only)
DELETE /api/license          → remove key (back to Community)
```

---

## 6. What running Rigger Cloud (SaaS) actually requires

This is the bigger lift; sketch so the open-core split above doesn't paint us into a corner.

**Architecture:** Rigger Cloud = our hosted control plane(s) + customers' connect-your-own
hosts (BYO Docker/Swarm via the existing SSH-exec multi-host) and/or rented hosts we provision.
The good news: **the hard plumbing already exists** — multi-host (SSH-exec + file sync), managed
registry, DNS-01 wildcard, per-project build hosts, per-env host binding. Cloud is mostly
*productizing* what's built.

What's genuinely new for Cloud:

1. **Multi-tenancy isolation.** Today one Rigger instance ≈ one org. Cloud needs either (a)
   **instance-per-tenant** (simplest, strongest isolation — spin a Rigger container per
   customer; you already containerize cleanly) or (b) **true multi-tenant** (org column on every
   table — large refactor). **Start with instance-per-tenant**; it reuses the whole app and the
   self-host security model. Postgres-per-tenant or SQLite-per-tenant both work.
2. **Billing + metering** — Stripe subscriptions, usage metering (hosts/seats/builds), dunning,
   self-serve upgrade/downgrade. This is where SaaS metering finally works (we control the
   server). Replaces manual key issuance for Cloud customers (the license becomes server-issued
   and short-lived).
3. **Signup / tenant provisioning** — a control-plane-of-control-planes that provisions a Rigger
   instance + subdomain (`{tenant}.rigger.cloud`) on checkout. The DNS-01 wildcard + Traefik
   model you just stood up is exactly this primitive.
4. **Secrets at rest** — Cloud holds customers' deploy creds; needs envelope encryption + a KMS,
   stronger than the self-host `internal/crypto` posture.
5. **SOC2-track concerns** — audit log, backups of the control plane itself, status page,
   on-call. Table stakes before enterprise logos.
6. **Abuse / fair-use** — crypto-mining, egress, build minutes. Hosted infra invites abuse.

**Sequencing:** ship Pro **license keys** first (weeks, reuses everything), sell by hand,
validate willingness-to-pay. Stand up Cloud only once there's pull — and build it
**instance-per-tenant** so it rides the existing app instead of a multi-tenant rewrite.

---

## 7. Build order (minimal → ambitious)

1. **License core** — `internal/license` (Ed25519 verify, `app_settings` storage, grace),
   `license.Service` on `Handler`, `GET/PUT/DELETE /api/license`, Admin → License tab. *Ships
   the ability to be licensed; gates nothing yet.*
2. **Gates** — `RequireFeature`/`RequireWithin` + wire the §5b enforcement points + 402→upgrade
   UX + `<FeatureGate>`. *Pro now means something.*
3. **Issuer** — private `rigger-licgen` CLI (guard the signing key) + a Stripe payment link +
   manual email-the-key flow. *You can take money.*
4. **Repo split** — start with `//go:build ee` tags (Option A); the single image stays single.
5. **(Later) Rigger Cloud** — instance-per-tenant provisioner + Stripe metering + tenant
   subdomains on the DNS-01 wildcard you already run.

Verification per step matches the house pattern: `go build/vet/test` + `npm run build` + rebuild
rigger; the license verify path gets unit tests (valid/expired/tampered/wrong-key/grace), and
golden parity must hold for CE (no key) so existing self-hosters see zero behavior change.

---

## 8. Risks / honest caveats

- **Already-public code can't be un-published.** Open-core value here is the *key + support +
  Cloud*, not hiding code. Gate, don't hide.
- **Pricing is unvalidated.** The §3 numbers are placeholders; test against real WTP. (I'm not a
  licensed financial advisor — treat pricing as a hypothesis to validate, not advice.)
- **Don't brick prod.** Grace + "never stop running workloads" is non-negotiable for trust.
- **Don't paywall security.** 2FA/secrets/updates stay free in CE, always.
- **One image discipline.** The moment CE and Pro become different artifacts, the self-updater
  and `install.sh` fork. Keep edition = key, not = binary.
```
