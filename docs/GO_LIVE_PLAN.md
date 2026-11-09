# Rigger — go-live and go-to-market plan

- **Status:** proposal for the owner's review — the decisions in §12 are open.
- **Written:** 2026-11-09, against `develop` at `f2296ab`.
- **Supersedes:** the strategy, editions, tier matrix, pricing and sequencing in
  [`MONETIZATION_AND_LICENSING.md`](MONETIZATION_AND_LICENSING.md) (§0–§3, §6–§7). That document's
  license-key mechanism and gate plumbing (§4–§5) remain the reference if a self-hosted commercial
  tier is ever built (§3.4).
- **Inputs:** [`PRODUCT_REVIEW_2026-11.md`](PRODUCT_REVIEW_2026-11.md) for the gaps and launch
  blockers; [`HOST_ADOPTION.md`](HOST_ADOPTION.md) for the migration engine; competitor pricing
  checked on 2026-11-09 (sources at the end).

---

## 0. The plan on one page

1. **The business is Rigger Cloud: a hosted control plane, customers bring their own servers
   (BYOS).** This is the model Laravel Forge has proven with agencies, and the one Coolify and
   Dokploy now sell.
2. **Self-hosted Rigger stays free and complete.** Everything that ships today stays free,
   unlimited, with no feature key. That is both the funnel and the weapon: you cannot flip a
   Coolify user by charging for things Coolify gives away.
3. **Paid exclusives are service-shaped.** Things that need infrastructure Rigger operates — an
   outside vantage point, a public edge, global DNS, durable off-site storage, a control plane
   that survives your server dying — plus organisation-scale administration (SAML/SCIM, audit
   export, white-label, SLA). Never security, reliability or data portability.
4. **Self-hosters can buy Cloud services without moving.** "Cloud Connect" sells off-site
   backups, external monitoring, status pages and hosted domains to self-hosted instances. It
   monetises the free audience without crippling it.
5. **Lead with release discipline, not price.** Environments with exact-image promotion,
   approval-gated pipelines and preview environments are what the cheaper tools lack. Coolify will
   always be about $5; Rigger should not try to undercut it.
6. **Wedge market: small agencies and dev shops**, then **teams leaving Heroku, Render and
   Railway**. Homelabbers are the community and the top of the funnel.
7. **The flip engine is migration tooling**, starting with adopt-in-place: point Rigger at a
   server already running another tool's apps and manage them without recreating anything.
8. **License future releases under AGPLv3**, and protect the name with a trademark policy.
9. **Sequence:** fix the blockers → harden and verify → Community 1.0 with importers → Cloud
   private beta with design partners → Cloud GA.

---

## 1. What changed from the previous plan, and why

The previous plan is a careful design, but three of its premises no longer hold.

**It charged for features the alternatives give away.** Its tier matrix put multi-user RBAC,
multiple hosts, scheduled backups, pipelines, preview environments and the REST API behind a Pro
key. Coolify's self-hosted edition is free with unlimited servers, teams and members. A user
comparing the two would lose features by switching, so nobody flips.

**GPLv3 makes key-gated self-hosted features unenforceable.** The plan shipped paid features in
the same GPL-licensed image and switched them on with a key. GPL explicitly permits anyone to
modify and redistribute that code — including removing the check. The only paid features Rigger
can actually enforce are the ones it runs itself.

**Hosting is the business.** The previous plan treated Rigger Cloud as "later, once there's
pull". This plan puts it at the centre.

**What still holds and is carried forward:** one image (edition is never a separate binary),
never brick production or stop running workloads, never paywall security, a frictionless free
install with opt-out telemetry, instance-per-tenant isolation for Cloud, and the license-key
mechanics for any future self-hosted commercial tier.

---

## 2. The market

### 2.1 What the alternatives cost (checked 2026-11-09)

| Product | Model | Price | Notes |
|---|---|---|---|
| **Coolify** | self-hosted or hosted control plane, BYOS | Self-hosted: free, all features, unlimited servers/teams/members. Cloud: **$5/mo including 2 servers, +$3/mo per extra server**; −20% yearly | Cloud adds hosting, backups, email alerts and managed updates — no extra features. Each additional *team* needs its own subscription. |
| **Dokploy** | per-server plans | **Hobby $4.50/server/mo** — 1 user, 2 environments, 1 database backup. **Startup from $15/mo + $4.50/server** — 3 servers, basic RBAC and 2FA. **Enterprise** — contact sales | Enterprise holds SSO/SAML, SCIM, audit logs, fine-grained RBAC, white-label, SLA. An Agency programme exists. |
| **Laravel Forge** | hosted control plane, BYOS | **Hobby $12, Growth $19, Business $39 /mo**, flat | Includes zero-downtime deploys, hosted `on-forge.com` domains, server monitoring, health checks and heartbeats. Resells its own VPS. Built for agencies. |
| **Railway** | managed PaaS | **Hobby $5, Pro $20 /mo** plus usage: about **$10/GB-month of memory, $20/vCPU-month**, $0.05/GB egress | A modest app with 2 GB and 1 vCPU is about $40/month before its database. A comparable VPS costs a fraction of that. |

Two things stand out. Coolify Cloud sells *convenience*, not features. Forge proves agencies pay
$19–$39 a month for a control plane, and it already charges for exactly the service-shaped
extras this plan proposes: hosted domains and heartbeats.

### 2.2 Who to flip, in priority order

**A. Small agencies and dev shops — the wedge.**

- *Using today:* Laravel Forge or Ploi (PHP-centred), Coolify (a subscription per team), or
  hand-built Docker.
- *Their problem:* many clients times many servers; keeping clients separate; showing clients
  uptime; maintenance windows; promoting staging to production per client; handing over.
- *Rigger's fit:* workspaces map one-to-one to clients; environments with promotion; preview
  environments for client review; maintenance mode; managed databases; any language, not just
  PHP. Your own production apps are Laravel, so this is a market you can speak to firsthand.
- *Missing:* client-facing status pages, white-label, read-only client access, per-client usage
  reports (§3.3).

**B. Teams leaving Heroku, Render and Railway.**

- *Their problem:* bills that grow with every service and gigabyte; lock-in.
- *Rigger's fit:* the same workflow — pipelines like Heroku Pipelines, preview environments like
  Review Apps, promote the exact image, managed databases — on a $5–$20 VPS for a flat fee.
- *Missing:* importers for their platform configs (§7), zero-downtime Compose deploys, encrypted
  backups with point-in-time recovery, uptime and status pages, a documentation site.

**C. Coolify, Dokploy and CapRover users hitting limits.**

- *Rigger's fit:* release discipline (above), WAF and geo-blocking, fleet housekeeping and disk
  alerts, free RBAC and 2FA (Dokploy's listed plans start those at Startup), no per-team
  surcharge.
- *Missing:* a zero-downtime way to switch — adopt-in-place (§7).

**D. Homelabbers — community and funnel, low revenue per user.**

- *Their problem:* breadth of apps, ARM boards, GPUs, no open ports, staying up to date safely.
- *Missing:* ARM64 images, a bigger catalog, GPU passthrough, Tailscale, SSO/forward-auth,
  scheduled updates with rollback, uptime checks (review §4).
- *How they pay:* Cloud Connect — off-site backups, status pages, hosted domains and, later,
  Relay for servers behind carrier-grade NAT.

### 2.3 Positioning

- **Headline:** *Heroku Pipelines and Review Apps, on your own servers.*
- **For agencies:** *Every client, every environment, one control plane.*
- **Message order:** release discipline first (environments, promotion, approvals, previews), then
  managed services, then fleet operations. Never lead with price.

---

## 3. Editions

### 3.1 How to decide what is free and what is paid

1. **If it runs on the user's own server without Rigger operating anything, it is free** in
   Community.
2. **If it needs infrastructure Rigger operates, it is a Cloud feature.**
3. **If only an organisation needs it** — directory sync, audit export, white-label, an SLA — it
   is a Business feature.
4. **Security, reliability and data portability are never paid.** That includes 2FA and enforcing
   it, encryption, backups to your own storage, exports, and anything in the review's fix-first
   list.

This is enforceable (Cloud features are never distributed, so no license can leak them), honest
(no crippleware, which matters enormously in self-hosting communities), and worth paying for
(the exclusives solve problems a single box cannot solve for itself).

### 3.2 Community — self-hosted, free

Everything shipping today, unlimited, plus the review's security and reliability work:

- unlimited servers, users, workspaces and projects
- RBAC; 2FA with an org-wide enforcement switch; OIDC login and forward-auth (homelabbers already
  run Authentik and Authelia and expect to sign in with them)
- environments and promotion, approval-gated pipelines, preview environments, rollback
- managed databases and engines with their consoles
- scheduled **encrypted** backups to your own S3 or SFTP, restore, verification, and a backup of
  Rigger's own database
- the proxy service (WAF, geo-blocking), maintenance mode, custom domains with Let's Encrypt,
  magic DNS, Cloudflare Tunnel
- housekeeping, fleet disk alerts, in-box uptime checks, notifications
- the REST API and a CLI
- importers and host adoption

### 3.3 Rigger Cloud — what is exclusive

Each of these needs something only Rigger can run.

| Feature | Why it cannot be free self-hosted | Primary buyer |
|---|---|---|
| **Hosted control plane** — high availability, managed upgrades, point-in-time recovery of the control plane | removes the single point of failure the review found; needs operated infrastructure | everyone |
| **Hosted domains** — `app.<team>.rigger.app` with automatic TLS, shareable before any DNS exists | needs a global DNS zone and certificate automation Rigger runs | agencies, Heroku leavers |
| **External uptime monitoring and status pages** — probes from several regions, public and client-branded status pages, incident posts | needs a vantage point outside the customer's network | agencies, teams |
| **Heartbeat ("dead man's switch") alerts** — know when a server or a whole site goes dark | the box that died cannot report its own death | everyone |
| **Off-site encrypted backup storage** — immutable retention, scheduled restore drills with a report | needs durable storage Rigger operates | teams, homelabbers |
| **Notification delivery** — email, SMS and push without configuring SMTP | needs delivery infrastructure | everyone |
| **Rigger Relay** (Cloud v2) — outbound-only tunnel, so apps are reachable without open ports, including behind carrier-grade NAT | needs a public edge | homelabbers, security-conscious teams |
| **Priority support** | people | teams, agencies |

**Agency and Business additions:** a client portal (branded, read-only client sign-ins),
white-label status and maintenance pages, per-client usage reports, SAML SSO with SCIM
provisioning, audit-log retention and export to a SIEM, an SLA.

**Cloud Connect** sells the service-shaped features above — off-site storage, external monitoring,
status pages, heartbeats, hosted domains, later Relay — to *self-hosted* instances through an API
key. A homelabber who will never move their control plane can still pay for off-site backups and
a status page.

### 3.4 Self-hosted Business (optional, later)

For companies that must self-host but need SAML/SCIM, audit export, white-label and support. Under
AGPL the value is support plus a commercial license for companies that will not accept AGPL terms
(dual licensing), with the extra modules distributed only to those customers. The license-key
mechanics in `MONETIZATION_AND_LICENSING.md` §4–§5 apply. **Do not build this until Cloud is
running and buyers ask for it.**

---

## 4. Pricing — a hypothesis to validate

Meter on **connected servers**, the unit customers already understand from Coolify and Dokploy,
with quotas on storage and monitors. Do not meter deploys or builds: in a BYOS model they run on
the customer's own CPU.

| Plan | Price (monthly) | Includes | For |
|---|---|---|---|
| **Community** | $0 | everything in §3.2, self-hosted | homelab, solo |
| **Cloud Connect** | $4 | for a self-hosted instance: hosted domains, 10 external monitors, heartbeats, 1 status page, 25 GB off-site backup storage | homelabbers, self-hosters |
| **Cloud Solo** | $9 | hosted control plane, 1 user, 3 servers, everything in Connect | indie developers |
| **Cloud Team** | $29 | unlimited users, 10 servers (+$3 each), 50 monitors, status pages, 100 GB storage, standard support | startups, small teams |
| **Cloud Agency** | $59 | 30 servers (+$2 each), unlimited client workspaces, client portal, white-label status and maintenance pages, per-client reports, 250 GB storage | agencies |
| **Business** | from ~$300, or custom | SAML/SCIM, audit export, SLA, onboarding | companies |

- **Yearly:** two months free.
- **Founder pricing:** 50% off for life for the first 100 paying accounts, to reward beta users.
- **Trial:** 14 days of Cloud Team with one server; no card required.

**How this compares.** Solo is above Coolify Cloud's $5 because it includes things Coolify Cloud
does not (status pages, off-site storage, hosted domains). Agency is above Forge Business ($39),
but bundles uptime monitoring, status pages and backup storage an agency otherwise buys
separately — **test that trade-off with design partners before publishing.**

**Unit economics, roughly.** An instance-per-tenant control plane needs a few hundred megabytes of
memory, so one 16 GB host carries dozens of tenants. S3-compatible storage costs a few dollars per
terabyte. Monitoring probes are cheap. Gross margin should be high everywhere **except Relay,
whose bandwidth must be capped or metered.**

---

## 5. Licensing and brand

- **Move future releases to AGPLv3.** It closes the gap GPLv3 leaves open: anyone running a
  modified Rigger as a hosted service would have to publish their changes. You are the sole
  copyright holder — all 501 commits are yours and no outside contributions have been merged — so
  you can relicense new versions. Releases already published stay GPLv3. Confirm the change with a
  lawyer before announcing it.
- **Considered and not recommended: FSL or BSL.** They forbid competing hosted use outright and
  turn into open source after two to four years. Stronger protection, but they lose the "open
  source" label that self-hosting communities care about.
- **Cloud-only code lives in a private repository** (`rigger-cloud`) and is never distributed, so
  copyleft never applies to it.
- **Add a contributor license agreement** before accepting outside pull requests, so dual
  licensing (§3.4) stays possible.
- **Trademark is the real protection against clones.** "Rigger" is a common word: run a trademark
  search in the software classes before launch, register if clear, and publish a policy that
  forks must rename.
- **Secure the domains:** the product site and the zone for hosted app domains.

---

## 6. Production readiness — launch gates

Dates in §9 are estimates; these exit criteria are what actually gate each stage.

### Gate 0 — security and correctness (before any public announcement)

- [x] Container access confined to the requester's own stack, with regression tests (review §1.1;
      v0.1.46, `a66235c`)
- [ ] Security advisory published with the release; it must tell operators **not** to rotate
      `JWT_SECRET` by hand, because it also derives the data-encryption key (review §1.4)
- [ ] A separate data-encryption key, with a backed-up re-encryption migration and a rotation
      command for both keys (review §1.4)
- [ ] Installer works: a `main` stable branch or a tag-based install URL (review §1.2)
- [ ] SQLite pragmas fixed, orphaned rows repaired, foreign keys on (review §1.3)
- [ ] HTTP server timeouts, graceful shutdown, panic recovery in every background task
- [ ] One operation at a time per environment
- [ ] Session token removed from URLs (short-lived ticket for event streams)
- [ ] `/:/host:ro` removed or narrowed
- [ ] Tests for `internal/auth`
- [ ] A `SECURITY.md` with a private reporting address

**Exit:** a second reviewer (or a focused security review) finds no high-severity issue in auth,
RBAC, container access or file access.

### Gate 1 — hardening and verification (before Community 1.0)

- [ ] ARM64 images
- [ ] Encrypted backups; backup and documented restore of Rigger's own database
- [ ] CPU and memory limits per service
- [ ] An end-to-end suite on a fresh VM in CI: install → create → deploy → back up → restore →
      upgrade from the previous release
- [ ] Every "built, not verified" item in `STATUS.md` either verified live or labelled *beta* in
      the UI — multi-node Swarm, override certificates, custom domains, preview environments on a
      real pull request, self-update apply, remote-host housekeeping and alerts
- [ ] Visual check of every authenticated screen
- [ ] Reproducible builds: commit `go.sum` and build with `go mod download` and `go mod verify`
      instead of regenerating it with `go mod tidy`
- [ ] Signed images and a software bill of materials
- [ ] A documentation site: install, concepts, migration guides, troubleshooting, security model,
      upgrade notes
- [ ] Opt-out anonymous telemetry, with the payload documented

**Exit:** five design partners have run their real workloads on it for two weeks without a
data-loss or security incident.

### Gate 2 — Cloud readiness (before taking money)

- [ ] Tenancy: instance-per-tenant provisioning, isolated storage, per-tenant backups with
      point-in-time recovery
- [ ] Secrets: per-tenant data keys wrapped by a managed KMS; no customer key at rest in
      plaintext; Rigger-managed keypairs only (no pasted personal keys — the managed-key option
      already exists in the host form)
- [ ] Operations: monitoring and alerting for Cloud itself, on-call rota, incident runbooks, a
      public status page for Rigger Cloud
- [ ] An external penetration test of the control plane and tenant isolation
- [ ] Billing: Stripe subscriptions, server metering, tax handling (EU VAT included — many BYOS
      users run Hetzner in the EU), dunning that never stops running workloads
- [ ] Legal: terms of service, privacy policy, data processing agreement, list of
      sub-processors, acceptable-use policy
- [ ] Support: help desk, documented response targets per plan

**Exit:** a tabletop exercise of "a tenant's control plane is lost" and "a KMS key is compromised"
ends with a working recovery.

---

## 7. The flip engine — migration tooling

Switching cost is the real barrier. In priority order:

1. **Adopt in place** — [`HOST_ADOPTION.md`](HOST_ADOPTION.md) Phases 0–1. Discover what is running
   on a server and manage it without recreating anything. Because it reads `docker inspect`, it
   works for apps deployed by Coolify, Dokploy, CapRover, Portainer or plain Compose. This is the
   zero-downtime switch, and the single most persuasive flip feature.
2. **Compose on disk** (Dockge, plain Compose) — largely covered by the existing Compose importer.
3. **Coolify importer** through its API — applications, environment variables, domains, databases.
4. **Dokploy importer** through its API.
5. **Heroku importer** through the Platform API — config vars, Procfile processes, buildpacks to
   Nixpacks, add-ons to managed services, pipelines, review apps to preview environments.
6. **Render** (`render.yaml`) and **Railway** importers.
7. **CapRover** (`captain-definition`) and **Portainer** stacks.

Each importer ships with a migration guide, which doubles as the page people find when they search
for an alternative.

---

## 8. Go-to-market

### 8.1 Sequence

1. **Design partners** (from now until Gate 1): recruit 5–10 — mostly agencies and Heroku leavers,
   plus one serious homelabber. A free year of Cloud in exchange for a weekly call and honest
   feedback.
2. **Community 1.0 launch:** Show HN, r/selfhosted, r/homelab, r/docker, Lobsters, the Laravel
   community. The launch post argues *why environments matter*; comparison and migration pages are
   live on day one; the Cloud waitlist opens.
3. **Cloud private beta:** waitlist invites, founder pricing, design partners first.
4. **Cloud GA:** self-serve signup and billing, public pricing page.
5. **Agency programme:** partner directory and a recurring referral share.

### 8.2 Content

- Comparison pages: Rigger versus Coolify, Dokploy, Forge, CapRover, Portainer and Heroku — factual
  and dated.
- Migration guides, one per importer.
- "Deploy X with Rigger" guides for the most popular catalog apps.
- A public changelog and roadmap.

### 8.3 Community

GitHub Discussions and a chat server; a template contribution guide (the catalog grows fastest
with outside help); labelled good-first issues; a predictable release cadence.

### 8.4 Growth inside the product

- After the first successful deploy, offer Cloud Connect where it solves a visible problem: "back
  this up off-site", "get a status page for this app".
- When a self-hosted instance alerts on its own disk, offer heartbeat alerts that work when the
  whole server is down.

---

## 9. Timeline — estimates for one developer with AI assistance

| Phase | Work | Estimate | Ends around |
|---|---|---|---|
| 0 | Gate 0 | 2 weeks | late November 2026 |
| 1 | Gate 1; recruit design partners | 6 weeks | end of January 2027 |
| 2 | Community 1.0 launch; adopt-in-place; Coolify and Heroku importers | 4 weeks | February 2027 |
| 3 | Cloud v1 (§10) and Gate 2; private beta | 10–12 weeks | May 2027 |
| 4 | Cloud GA; Agency plan; Cloud Connect | 4 weeks | June 2027 |
| 5 | Outbound agent and Relay; Business (SAML/SCIM, audit export) | ongoing | second half of 2027 |

Exit criteria beat dates: slip the date rather than skip a gate.

---

## 10. Cloud architecture

**Version 1 — reuse what exists.**

- *Instance per tenant:* each customer gets their own Rigger container and database on
  Rigger-operated hosts. The application runs unchanged, isolation is strong, and a tenant
  problem stays a tenant problem.
- *Provisioner:* a small service that turns a signup into an instance, a subdomain such as
  `{team}.rigger.cloud`, and a billing record. The wildcard DNS and Traefik automation Rigger
  already runs is the core of this.
- *Fleet operations:* upgrades roll out to canary tenants first; every tenant database is backed up
  with point-in-time recovery.
- *Connectivity:* SSH from each tenant's control plane to the customer's servers, as today, using
  Rigger-managed keypairs whose private halves are encrypted with KMS-wrapped per-tenant keys.

**Version 2 — an outbound agent.** A small binary on each server dials out to its control plane
over mutual TLS and carries management traffic, heartbeats and Relay ingress. Cloud then holds no
SSH keys, needs no inbound port on the customer's server, and works behind NAT. This **revisits the
earlier "stay on SSH" decision**: for self-hosting that decision was right, but in Cloud, Rigger
would hold root-equivalent keys to every customer's servers, and a breach would become every
customer's breach. "Rigger Cloud never holds your SSH keys" is also a selling point.

---

## 11. Metrics

| Area | Measure | Starting target (to validate) |
|---|---|---|
| Community | installs per week; weekly active instances | trend up month on month |
| Community | time from install to first successful deploy | under 15 minutes |
| Community | instances still active after 30 days | 40% |
| Cloud | signups that connect a server | 60% |
| Cloud | signups that deploy something | 50% |
| Cloud | trials that convert to paid | 15–25% |
| Cloud | monthly churn | under 3% |
| Cloud | support tickets per account per month | under 1 |
| Revenue | first paying customer → $1k MRR → $10k MRR | milestones, not dates |

---

## 12. Risks

| Risk | Mitigation |
|---|---|
| Cloud holds keys to customers' servers — one breach compromises many | KMS envelope encryption, managed keypairs only, a penetration test before GA, a bug bounty, then the outbound agent |
| Coolify's mindshare and $5 price floor | compete on release discipline, agencies and switching tools, not price |
| Support load on a solo maintainer | documentation site, community channels, support tiers, design partners who report clearly |
| Relay bandwidth cost and abuse | quotas, fair-use policy, metering, Relay only on paid plans |
| Backlash to the license change | explain it plainly; GPLv3 to AGPLv3 is a small step that most self-hosters accept |
| Name conflict | trademark search before launch |
| Scope creep | only the gate checklists block launch; everything else stays in the backlog |
| Unproven features embarrassing the launch | Gate 1 verifies them or labels them beta |

---

## 13. Decisions for you

1. **License:** AGPLv3 (recommended), FSL, or stay on GPLv3.
2. **Wedge:** agencies first (recommended), or Heroku leavers first.
3. **Prices:** publish the §4 numbers, or validate with design partners first (recommended).
4. **Cloud connectivity:** ship v1 on SSH and build the agent for v2 (recommended), or build the
   agent first.
5. **Relay:** build it, given its bandwidth economics, or rely on Cloudflare Tunnel and Tailscale.
6. **Cloud Connect** for self-hosted instances: yes (recommended) or no.
7. **Company:** legal entity, jurisdiction, domains, trademark filing.
8. **Self-hosted Business:** only when asked (recommended), or earlier.

---

## Sources

Checked 2026-11-09; prices change, so recheck before publishing comparison pages.

- [Coolify pricing](https://coolify.io/pricing/)
- [Dokploy pricing](https://dokploy.com/pricing)
- [Laravel Forge pricing](https://laravel.com/forge/pricing)
- [Railway pricing](https://railway.com/pricing)
