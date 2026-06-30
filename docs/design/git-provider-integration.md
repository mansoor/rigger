# Git provider integration (private-repo access)

**Status: Phase 1 BUILT + live-verified (token path verified against a self-hosted Gitea, 2026-06-29).**
Adds first-class, securely-stored Git credentials so Rigger can clone/build/auto-deploy from
**private** repositories — the "click to connect, authorize, store, reuse" flow users expect
from Coolify / Dokploy / Render. Stored workspace-level (a sibling to Docker Registries),
creatable inline from New Project. The design below is the original plan; **"What's built"**
records the shipped reality.

## What's built (current state)

- **Phase 1 shipped:** `git_providers` + `global_git_provider_grants` tables (encrypted via
  `internal/crypto`), `internal/gitproviders` store, **HTTPS token + SSH deploy key + GitHub
  App** (manifest create → install → 1-hour installation tokens, never persisted). `gitsync.Sync`
  takes a `*gitsync.Auth`; the bridge resolves the project's `git_provider_id` → auth. UI:
  **Manage Workspace → "Git"** tab (`GitSection`/`GitProviderModal`) + reusable
  `GitProviderPicker` (select or inline-create) wired into New/Edit Project. Image ships
  `git` + `openssh-client`.
- **Token presets (GitHub / GitLab / Bitbucket / Gitea / Other), frontend-only:** the
  HTTPS-token engine was already provider-agnostic (`httpsTokenAuth` → Basic `user:token`
  extraheader for any host), so the presets just prefill host + the right username convention
  + secret label/help. All stored as `kind=token`; no new `Kind`.
- **Auth-scope fix (2026-06-29):** the credential header is scoped to the **exact repo
  origin** (`scheme://host[:port]/`) — not a hardcoded `https://<host>/`. Git only attaches an
  `http.<url>` extraheader when scheme + host + port match, so the old hardcode silently
  dropped the token for **self-hosted Gitea/GitLab served over plain http or a non-default
  port** (the "Test failed against a valid Gitea repo" bug). `Provider.BuildAuth(repo string)`
  threads the target repo through `Verify` (Test), the build/clone path, and repo scan; falls
  back to `https://<host>/` when no repo is known. Regression test
  `TestTokenAuthScopesToRepoOrigin`.
- **Service persistence fix (2026-06-29):** the chosen service preset is stored in the
  provider's `meta` json (`{"service":"…"}`), so **Edit** reopens the correct tab — the host
  string alone can't identify a self-hosted service. The modal inits from
  `serviceForEdit(initial)` (meta → fallback host-infer), and preset buttons only overwrite
  host/username when the preset has a non-empty default (so switching tabs no longer **blanks**
  typed values).
- **Backlog (Phase 2):** GitLab/Bitbucket/Gitea **OAuth apps** (repo dropdowns, token refresh)
  + App-driven auto-webhooks into pipelines/previews. Token presets cover plain cloning today;
  OAuth's real win is auto-webhooks. Deferred by decision.

## Problem / today's state

`internal/gitsync.Sync` runs a plain `git clone <repo>` / `fetch` with **no credentials**
(no token, no SSH key, no `GIT_ASKPASS`, no credential helper) — see
[gitsync.go](../../src/backend/internal/gitsync/gitsync.go), called by the builder and the
repo-scan-on-create. So **private repos are not first-class**: the only workaround is
embedding a token in the URL (`https://x-access-token:<PAT>@github.com/...`), which lands
**plaintext** in `config.json` (`git_repo`) and can leak into build/scan logs. SSH URLs
don't work at all. This is the gap this design closes.

Not git-auth today (don't conflate): Phase 7 host SSH keys (deploy hosts), Docker registry
creds (image pull/push), and the preview-env GitHub token (PR status write-back only).

## How the field does it (researched)

- **Coolify** — "Sources". The **GitHub App is the recommended path**: one-time create +
  install, which auto-configures webhooks (push → redeploy, PR → preview), commit-status
  posting, and granular per-repo permissions; clones private repos with no user PAT.
  **Deploy keys** (read-only, one key per repo) are the fallback when you can't install an
  App — but they don't support preview deployments. GitLab/Bitbucket via their own
  app/token flows.
- **Dokploy** — five source types: **GitHub (GitHub App)**, GitLab + Gitea (**OAuth 2.0**,
  access+refresh tokens, self-hosted supported), Bitbucket (API tokens / app passwords),
  and **custom Git (SSH key)**. For raw private repos it generates an **SSH key**, you add
  the public key as a deploy key, and use the SSH URL.
- **Render / Railway / Vercel** — GitHub/GitLab **OAuth App + per-repo install**, auto
  webhooks; the managed-cloud version of the same App model.

**Takeaway:** the GitHub App (created via the **App Manifest flow**) is the gold standard —
it's the "click a button → GitHub creates & you install the app → it redirects back with
credentials" UX, and it unlocks webhooks + previews + status for free. SSH deploy keys and
PATs are the universal fallbacks for everything else / self-hosted.

## Which Git services we support

| Provider | Mechanism | Covers | Phase |
|---|---|---|---|
| **GitHub** (github.com + Enterprise Server) | **GitHub App** (manifest flow) → short-lived installation tokens | clone, webhooks, PR previews, commit status | **1** |
| **Any provider** (GitLab, Bitbucket, Gitea/Forgejo, self-hosted, raw) | **SSH deploy key** (Rigger-generated keypair; user adds the public key) | clone (read-only) | **1** |
| **Any provider** | **HTTPS token / PAT** | clone | **1** (cheap) |
| **GitLab** (.com + self-hosted) | OAuth App (access+refresh) | clone, webhooks | 2 |
| **Bitbucket** | OAuth / app password / repo access token | clone, webhooks | 2 |
| **Gitea / Forgejo** (self-hosted) | OAuth App / PAT | clone, webhooks | 2 |

Phase 1 (GitHub App + SSH deploy key + PAT) already covers virtually every repo; the OAuth
apps in phase 2 are UX upgrades (repo dropdowns, auto-webhooks) for the non-GitHub hosts.

## The GitHub App manifest flow (the "one-click connect")

This is what makes it feel like "authorize the app and it's stored":

1. User clicks **Connect GitHub** in Manage Workspace → Git. Rigger renders a self-posting
   form to `https://github.com/settings/apps/new?state=<csrf>` (or `/organizations/{org}/...`)
   carrying a **manifest** (app name, the callback/webhook/setup URLs, requested
   permissions = `contents:read`, `metadata:read`, `pull_requests:write`,
   `checks/statuses:write`, and the webhook events push + pull_request).
2. GitHub shows a "Create App" confirmation; on submit it redirects back to Rigger's
   callback with a temporary `code`.
3. Rigger `POST /app-manifests/{code}/conversions` → GitHub returns the new App's **id,
   slug, private key (PEM), webhook secret, client id/secret**. Rigger stores them
   encrypted and shows an **Install** button.
4. User installs the App on their account/org and picks **all repos or specific repos** →
   GitHub calls the setup URL with an `installation_id`, which Rigger records.

Cloning then needs **no stored user token**: Rigger signs a JWT with the App private key →
exchanges it for a **1-hour installation access token** (`POST /app/installations/{id}/access_tokens`),
used as `x-access-token:<token>` for that one clone. Tokens are **ephemeral, never
persisted, never written to `config.json` or logs**.

Self-host caveat: the App lives in the *user's* GitHub account (created via manifest), so
each Rigger instance/workspace creates its own App — there's no central "Rigger" App to
trust. That's exactly how Coolify/Dokploy self-hosted work, and it's the right model for an
open-core self-hosted product.

## Storage & scoping — mirrors Docker Registries

A new **workspace-scoped** store, structurally identical to the registries pool
([[service-links-and-scan-choices]] / P3b pattern): `owner_scope` + a global grants pool so
an admin can share a provider connection across workspaces.

- DB table `git_providers` (id, workspace/owner_scope, name, kind = `github_app | ssh_key |
  token | gitlab_oauth | …`, host (for Enterprise/self-hosted), and a `secret_enc` blob
  holding the kind-specific material — App private key + ids + webhook secret, or the SSH
  private key, or the PAT). Encrypted at rest with the existing `internal/crypto`
  (AES-256-GCM, HKDF from JWT secret — same primitive that protects host SSH keys).
- `global_git_provider_grants` (mirrors `global_registry_grants`) for cross-workspace share.
- `internal/gitproviders` package: store + `Token(ctx, provider, repo)` (mint installation
  token / return PAT) + `SSHKey(provider)` + `Verify`/`Test`.

A project references a provider by id on its git source (new `git_provider_id` on the
project config, alongside `git_repo`/`git_branch`). **The credential itself never enters
`config.json`** — only the reference.

## Wiring it into clone/build (`gitsync`)

`Sync` gains a credential parameter and injects per-operation, never in the URL/disk:
- **HTTPS (App token / PAT):** `git -c http.<host>.extraheader="AUTHORIZATION: bearer <tok>"`
  or a transient `GIT_ASKPASS` script — token stays out of argv where possible and out of
  the remote URL.
- **SSH deploy key:** write the private key to a `0600` temp file for the call only, set
  `GIT_SSH_COMMAND="ssh -i <tmp> -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"`,
  delete after.
- Tokens are minted fresh each clone (App tokens expire in 1h); nothing long-lived is
  written. Build/scan log output is already streamed — add redaction for any accidental
  token echo.

Container prereq: the Rigger image already ships `git`; add `openssh-client` for the SSH
path.

## UX

- **Manage Workspace → Git** (new tab, sibling to Docker Registries): list connections;
  "Connect GitHub" (manifest flow), "Add SSH deploy key" (shows the generated **public**
  key to paste into the provider), "Add token". Per-row Test + Delete; admin "share to
  workspaces" like registries.
- **New Project / Edit Project git source:** a **GitProviderPicker** (mirrors the existing
  `RegistryPicker`) — choose an existing connection or **inline-create** one; whatever is
  created inline **persists to the same workspace `git_providers` store**, so a connection
  made during project creation shows up later in Manage Workspace → Git (exactly the
  behavior you asked for). After selecting a GitHub App connection, the repo field becomes a
  **searchable dropdown** of accessible repos (App installation repos) instead of a raw URL.
- "Test access" button → `gitproviders.Verify` (App: list installation repos; SSH/PAT: `git
  ls-remote`).

## Ties into existing features (bonus, mostly phase 2)

- **Auto-deploy / previews:** the GitHub App's webhook can drive the existing pipeline
  webhooks ([[phase9-pipelines]] 9a) and preview environments ([[preview-environments]])
  without the user hand-pasting a webhook secret — Rigger registers the webhook URL when the
  App is created. The preview write-back token ([previews/writeback.go](../../src/backend/internal/previews/writeback.go))
  can be replaced by App status/checks posting.
- **Build "if-changed"** already reads `HeadSHA`; private fetch just works once creds flow.

## Security

- All secrets encrypted at rest (`internal/crypto`); App installation tokens are ephemeral
  and never stored. Nothing credential-bearing in `config.json` or logs (redaction +
  no-token-in-URL). SSH known-hosts via `accept-new` (TOFU) — document, or pin host keys per
  provider in a later pass. Operator+ to create/edit a provider; viewers never see secrets
  (write-only, like registry passwords / DB secrets reveal rules).

## Phasing

1. **Phase 1** — store + `internal/gitproviders` + `git_providers` table (encrypted) +
   `gitsync` credential injection (HTTPS token + SSH key) + GitHub App manifest/install/
   callback handlers + Manage Workspace → Git tab + GitProviderPicker (inline-create) + repo
   reference on the project. Covers GitHub App, SSH deploy key, PAT.
2. **Phase 2** — GitLab/Bitbucket/Gitea OAuth apps (repo dropdowns, token refresh) +
   App-driven auto-webhooks into pipelines/previews + replace preview write-back token.
3. **Phase 3** — host-key pinning, fine-grained per-repo scoping UI, App token caching.

## Verification

- Unit: `gitproviders` store round-trip (encrypt/decrypt), manifest-conversion parsing,
  installation-token minting (mock), `Sync` credential injection shape (no token in argv/URL).
- Live E2E: connect a real GitHub App to a test org, clone a **private** repo end-to-end,
  deploy; SSH deploy-key path against a private repo; PAT path. (Needs a real GitHub
  account — flag for the live pass.)
- `go build/vet/test`, `npm run build`, rebuild rigger.

## Decisions to confirm

- Provider priority for phase 1 beyond GitHub App: include **both** SSH deploy key and PAT,
  or SSH-only? (rec: both — PAT is trivial and covers self-hosted quickly.)
- Tab name: you called it "Git Registries"; recommend **"Git"** or **"Source Providers"**
  (registry is a Docker term). Final call yours.
- Editions: keep private-repo support in **CE** (basic functionality, not gated), consistent
  with "never gate security/basics"; the managed-cloud central GitHub App is the EE/Cloud
  upsell.

## Related

- `internal/crypto` (encryption primitive, also used for host SSH keys).
- Docker Registries scoping pattern ([[service-links-and-scan-choices]], roadmap P3b) — the
  structural template for the store + grants + picker.
- [[preview-environments]], [[phase9-pipelines]] (webhook consumers), `internal/gitsync`,
  `internal/builder`.
