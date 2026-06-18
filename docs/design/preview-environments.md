# Design: Preview / PR Environments

> Status: **Design — review before building.** Branch: `develop` (incremental commits).
> Companion: [preview-environments-plan.md](preview-environments-plan.md) (implementation plan).

## 1. Goal

When a pull request is opened against a project's repo, Rigger automatically spins up an
**ephemeral environment** that deploys that PR's branch, gives it a unique public URL, keeps it
in sync as new commits land, and **tears it down** when the PR is closed or merged. This is the
"preview environment" / "deploy preview" feature shipped by Coolify, Dokploy, Render, Vercel, etc.

A PR preview is, mechanically, **a short-lived Rigger environment** named `pr{n}` that is cloned
from a designated **template environment**, with its git branch overridden to the PR head ref.
Everything downstream (compose generation, routing, TLS, managed deps, build/deploy) reuses the
machinery Rigger already has.

## 2. Architecture decision — host-daemon ephemeral env, **not** Docker-in-Docker

We evaluated running each preview inside a per-PR Docker-in-Docker (DinD) sandbox. **Rejected as
the default** for three reasons:

1. **Ingress.** Rigger's Traefik runs on the host daemon and routes to containers on the
   host's `rigger-traefik` network. Containers inside a DinD live on the DinD's internal
   networks — Traefik can't see them without publishing a port out of the DinD container and
   adding an extra dynamic hop. The host-daemon model gets routing for free (the preview's web
   service is just another labelled container).
2. **Privileged / security.** Classic DinD needs `--privileged` (host-escape risk) — and the
   whole point of previews is running *unmerged, possibly untrusted* code. Safe DinD needs
   rootless/sysbox runtime on the host: extra infra.
3. **Build speed.** A fresh DinD has an empty image cache, so every preview re-pulls/rebuilds
   from scratch. Previews want to be fast and cheap.

Rigger already gets DinD's one real advantage — **clean teardown** — via `compose down -v` +
env-dir/config cleanup. So:

- **Default:** deploy each preview as a namespaced ephemeral env on the **host daemon** (or the
  project's bound host), torn down on PR close. This is exactly the Coolify/Dokploy model.
- **Isolation option (later / opt-in):** for untrusted forks, target previews at a **dedicated
  preview host** via Rigger's existing per-env host binding + SSH-exec (Phase 7), instead of
  privileged DinD. Same isolation goal, no host-escape footgun.

## 3. End-to-end lifecycle

```
GitHub/GitLab/Gitea  ──PR event──▶  Rigger preview webhook  ──▶  Preview controller
        │                              (signed, per-project)         │
        │                                                            ▼
        │   opened/reopened  ─────────────────────────────▶  clone template env → pr{n}
        │                                                     override branch = PR head
        │                                                     deploy (build+up)
        │   synchronize (new commits) ────────────────────▶  redeploy pr{n} (build+up)
        │                                                            │
        │   closed / merged  ─────────────────────────────▶  teardown pr{n} (down -v + cleanup)
        │                                                            │
        ▼                                                            ▼
   (optional) PR comment / commit status  ◀──────────  write-back preview URL + status
```

A background **reaper** (scheduler) also tears down previews past their TTL, as a safety net for
missed/!delivered "closed" webhooks.

## 4. Components

### 4.1 Git provider integration (webhook receiver + optional write-back)

Reuse the **9a pipeline-webhook pattern** (`internal/pipelines/webhooks.go`,
`api/pipeline_handlers.go` `InboundWebhook`), but PR events are richer than a fire-and-forget
trigger, so this is a **new, dedicated receiver**:

- New table `preview_webhooks` (mirrors `pipeline_webhooks`: `id, workspace, project,
  token_hash, secret, provider, enabled, created_at, last_triggered_at`). Token in the URL,
  sha256-hashed at rest; `secret` verifies the provider HMAC.
- Public route: `POST /api/previews/hooks/{token}` (unauthenticated like pipeline hooks;
  security is the token + HMAC signature).
- **Signature verification:** GitHub/Gitea `X-Hub-Signature-256` (HMAC-SHA256), GitLab
  `X-Gitlab-Token`. Reuse the existing HMAC verify helper from `InboundWebhook`.
- **Event parsing:** read `X-GitHub-Event` (`pull_request`) + payload `action`
  (`opened|reopened|synchronize|closed`), PR `number`, head `ref` (branch), head `sha`, repo
  full name, and `merged` bool. Map to controller actions (§4.3).
- **Write-back (optional, Phase 4):** post the preview URL + status as a PR comment and/or a
  commit status (`POST /repos/{o}/{r}/statuses/{sha}`). Requires a token with `repo:status` /
  PR-comment scope, stored per-project (write-only/masked, like the DNS token pattern). If no
  write-back token is configured, the feature still works — the URL just shows only in Rigger's
  UI.

> A full **GitHub App** (auto-installs webhooks, mints short-lived installation tokens) is the
> polished long-term form, but is **not required for v1**. v1 ships a manual webhook URL +
> secret the user pastes into their repo settings, exactly like pipeline webhooks today. GitHub
> App is a later enhancement.

### 4.2 Preview configuration (per-project)

Add to `wsconfig.Project` (and `workspace.Project`) — round-tripped through config.json:

```go
type PreviewConfig struct {
    Enabled       bool   `json:"enabled,omitempty"`
    TemplateEnv   string `json:"template_env,omitempty"`    // env to clone (e.g. "dev")
    Provider      string `json:"provider,omitempty"`        // "github"|"gitlab"|"gitea"
    BranchFilter  string `json:"branch_filter,omitempty"`   // optional glob, e.g. "feature/*"; "" = all
    MaxConcurrent int    `json:"max_concurrent,omitempty"`  // cap active previews (0 = unlimited)
    TTLHours      int    `json:"ttl_hours,omitempty"`       // auto-reap after inactivity (0 = until PR closes)
    ProtectAuth   bool   `json:"protect_auth,omitempty"`    // basic-auth the preview URLs
    AutoDeployForks string `json:"auto_deploy_forks,omitempty"` // "off"|"approved"|"on" (security gate)
    WriteBack     bool   `json:"write_back,omitempty"`      // post URL/status back to the PR
    DBStrategy    string `json:"db_strategy,omitempty"`     // "isolated-empty"|"isolated-seed"|"clone-from"|"shared"
    DBSource      string `json:"db_source,omitempty"`       // env to clone-from / share (for those two modes)
}
// on Project:
Preview *PreviewConfig `json:"preview,omitempty"`
```

The **template env** is the source of truth for what a preview looks like: its services,
managed-dep selections, per-service env vars, deployment mode, host binding. Cloning it (§4.3)
means previews automatically track changes the team makes to `dev`.

### 4.3 Ephemeral environment lifecycle

A **preview controller** (`internal/previews` package + `api/preview_handlers.go`) drives three
operations, each built on existing primitives:

**a) Create (`opened`/`reopened`)** — extend `CopyEnvironment`
(`api/copy_env_handlers.go`) into a reusable internal function:
1. Clone the template env block verbatim into a new env keyed **`pr{n}`** (no hyphen in the
   suffix — keeps the flat routing label collision-free; see §4.4).
2. **Blank `domain`** (auto-route will assign the preview subdomain).
3. Set `Env.Git.Branch = <PR head ref>` (per-env branch override; `wsconfig.Config.Branch`
   already prefers the per-env value over the project default).
4. Regenerate secret-flagged env vars (fresh DB password etc.) — reuse the copy handler's
   `regenerate_secrets` path so previews never share prod secrets.
5. `bridge.Bootstrap(ws, proj, "pr{n}")` → writes `.env` + compose.
6. Deploy: run the project's pipeline if one is designated, else a plain build+up
   (`continueRun` / the standard deploy path).
7. Record a row in `preview_environments` (§4.5).

**b) Update (`synchronize`)** — the branch ref is unchanged (same PR), so just **redeploy**:
re-run gitsync (fetch + reset to new head sha) + build + up for `pr{n}`. Reuse the pipeline /
deploy path. Bump `last_deployed_at`.

**c) Teardown (`closed`/`merged`, or TTL reap)** — **extend the EXISTING delete-env path**, not
a from-scratch build. Env deletion already works today via `PutConfig` (`api/handlers.go`):
removing an env from `config.json.environments` triggers `down` (containers + networks) →
overwrite config → `os.RemoveAll(envs/{env})` — this is the `122ad60` orphan-cleanup that
respects the env-list-union invariant. Two deltas are needed for previews:
1. **Volume purge.** Today's path runs `compose down` (no `-v`) — deliberately, so deleting an
   env doesn't destroy its data. Previews churn one-per-PR, so they must remove their volumes
   too or orphaned per-PR volumes accumulate forever. Add an opt-in `down --volumes` variant.
2. **Headless entry point.** Existing deletion is user-driven and diff-based (a side effect of a
   config-PUT that omits an env). The controller needs to tear down `pr{n}` directly from a
   webhook. So **extract PutConfig's removal block** (the `down` loop + `os.RemoveAll(EnvDir)`)
   into a shared internal `removeEnv(ws, proj, env, purgeVolumes bool)` that both PutConfig and
   the controller call.
3. *(Minor)* Existing deletion doesn't purge associated DB records (backup schedules/runs,
   secrets); the controller additionally deletes the `preview_environments` row.
4. If write-back is on, mark the PR comment/status as "preview removed".

> The shared `removeEnv` helper is generally useful beyond previews (it de-duplicates the
> teardown logic currently inline in PutConfig), so it lives in the env subsystem.

### 4.3a Database & data isolation

Each environment in Rigger gets its **own** managed-backend instances — the stack prefix is
`{resource_prefix}_{env}` (`generator.go:234`), so the DB container/volume is named e.g.
`{ws}_{proj}_pr{n}_{engine}` and `DB_HOST` resolves to it. ("Project-level" backends means the
engine + version *choice* is shared across envs, **not** the instance.) Therefore:

- A preview `pr{n}` runs its **own dedicated, ephemeral DB** (+ Redis / MinIO / Mailpit),
  fully isolated from `dev`/`staging`/`prod`. It never connects to another env's database.
- Same **engine + version** as the rest of the project (project-level setting) → faithful to
  dev/prod.
- Credentials: cloning regenerates secret-flagged env vars, so the preview's DB password is
  fresh — **prod secrets are never copied into a preview**.
- Teardown with `purgeVolumes=true` destroys the preview's DB volume → no orphaned per-PR data.

**Persistence across rebuilds.** A new commit (`synchronize`) triggers a *redeploy* (build +
`up`), **not** `down -v` — so the per-PR DB volume **survives every push within a PR**. Data is
destroyed only on PR **close** (teardown). Thus the install wizard / first-run setup runs at most
**once per PR**, not per commit. A fully-empty isolated DB is still of limited use for
data-driven apps, so the *seeding* strategy below matters.

#### DB strategy (per-project) — `Preview.DBStrategy`

An empty isolated DB is fine for static/stateless apps but useless when a review needs real data
(e.g. "does this fix render the last-7-days chart correctly?"). So the preview DB is a choice:

| Mode | Behavior | Use case | Notes |
|---|---|---|---|
| `isolated-empty` (default) | own fresh DB; app migrations run on deploy | static/simple apps, schema-only review | exists today |
| `isolated-seed` | own DB; auto-import the project SQL seed (v3 `db_seed`) | canned data; **skips install wizard** | reuses existing hook |
| `clone-from:<env>` ⭐ | own DB; **restore latest backup/snapshot of `DBSource` env** on first deploy, then PR migrations run on top | **data-driven review + migration testing**: real data, isolated, safe for destructive migrations | restore time on first deploy; PII if cloning prod (anonymize = future) |
| `shared:<env>` | app points at the **existing `DBSource` env's live DB** — no per-PR DB created | max realism, read-heavy review | ⚠ PR migrations/writes mutate a shared DB; concurrent PRs collide |

**`clone-from` is the recommended mode for data-driven apps.** It gives a real copy of the
chosen env's data (so charts/reports render), stays isolated (PR writes never touch the source),
skips the install wizard (cloned schema is already installed), and lets you safely exercise the
PR's migrations against realistic data. Sequencing reuses the v3 seed pattern: bring up DB →
wait healthy → restore → then bring up the app (so the app never sees an empty DB and re-runs
install). Seed/clone happens **once on create**, not on every rebuild (re-seeding would wipe a
reviewer's test changes).

**`shared` guardrails** (opt-in only): explicit warning in the UI; recommend a **non-prod**
source env; suited to read-mostly review, **not** migration testing. Critically, in shared mode
there is no per-PR DB volume — **teardown removes only the app containers and must never purge
the source env's volume**. (`removeEnv(purgeVolumes=...)`: `true` for `isolated-*`, never touches
the shared DB.)

> `DBExternal` is **not** "use a remote DB" — it only publishes the in-stack DB's port on the
> host. There's no managed-DB feature that attaches an env to a shared/external database, so a
> preview can't accidentally bind to prod's DB. **Edge case:** if a template env hand-sets
> `DB_HOST` in its env vars to a real external host, the clone copies that string verbatim — the
> controller should detect a hardcoded external `DB_HOST` and warn/refuse.

### 4.4 Routing & TLS

No new TLS work. The existing flat-label scheme + wildcard cert already covers previews:

- `composegen.resolveRoute` builds the host as `{prefix}-{env}.{base}` where
  `prefix = dashify(ws_proj)`. With env `pr{n}`, the host becomes
  **`{ws}-{proj}-pr{n}.{base}`** — a single DNS label, so the existing `*.{base}` **wildcard
  cert covers it with zero new issuance** (DNS-01/Cloudflare path).
- Collision safety: keep the env suffix **hyphen-free** (`pr42`, not `pr-42`). ws/proj keys are
  already enforced hyphen-free; a hyphen-free `pr{n}` suffix keeps the flat label unambiguous.
- No base domain? Falls back to magic-DNS (`{label}.{host}.sslip.io`) exactly like any other
  env — previews work on a LAN/dev box too.
- `protect_auth` → set the env's `protect_admin_uis`-style basic-auth middleware so preview URLs
  aren't world-open (reuse the existing `${ADMIN_UI_USERS}` Traefik basic-auth middleware).

### 4.5 State tracking

New table `preview_environments`:

| column | meaning |
|---|---|
| `id` | pk |
| `workspace`, `project` | scope |
| `pr_number` | PR id |
| `provider` | github/gitlab/gitea |
| `branch` | PR head ref |
| `head_sha` | last deployed commit |
| `env_key` | `pr{n}` |
| `url` | resolved preview URL |
| `status` | `creating|running|updating|failed|torn_down` |
| `created_at`, `last_deployed_at`, `expires_at` | lifecycle |
| `last_run_id` | FK to the pipeline run that deployed it (for log linking) |

Store package `internal/previews` (Create/Get/ListForProject/Update/Delete + `ListExpired`).

### 4.6 Concurrency caps & TTL reaper

- `MaxConcurrent`: on `opened`, if active previews for the project ≥ cap, queue or reject
  (reject + write-back a "preview limit reached" comment is simplest for v1).
- **Reaper:** mirror `StartBackupScheduler()` — a goroutine on a `time.NewTicker` (e.g. 30 min)
  that calls `previews.ListExpired(now)` and runs `TeardownEnvironment` for each. Catches PRs
  whose `closed` webhook was missed and enforces `TTLHours` inactivity.

### 4.7 Security model

- **Webhook auth:** URL token + provider HMAC signature (no signature → reject if a secret is
  configured).
- **Untrusted code (forks):** `AutoDeployForks`:
  - `off` (default) — never auto-deploy PRs from forks; same-repo branches only.
  - `approved` — deploy a fork PR only after a maintainer adds a label / approves in Rigger UI.
  - `on` — deploy all (only sensible with the isolation option / a throwaway preview host).
- **Secrets:** previews always regenerate secret-flagged env vars; production secrets are never
  copied into a preview env.
- **Scope:** preview controller honors workspace RBAC — the webhook acts as the project's
  service identity; previews are confined to the project's workspace.

### 4.8 UI

New **Project → Preview Environments** tab:
- Enable toggle + config form (template env picker, provider, branch filter, max concurrent,
  TTL, protect-auth, fork policy, write-back).
- Webhook URL + secret (one-time reveal, copy button) with paste-into-GitHub instructions.
- **Active previews list:** PR #, branch, URL (click-through), status badge, last deploy time,
  per-row actions — **Redeploy**, **View logs** (links to `last_run_id`), **Tear down now**.
- Surface a small "N preview envs active" indicator on the project page.

## 5. Mapping to existing Rigger primitives

| Need | Reuse |
|---|---|
| Receive PR events | 9a webhook pattern (`webhooks.go`, `InboundWebhook`) → new signed receiver |
| Clone an env | `CopyEnvironment` (`copy_env_handlers.go`) → extract reusable internal fn |
| Per-PR branch | `EnvGit.Branch` + `wsconfig.Config.Branch` (per-env override) |
| Fresh secrets | copy handler `regenerate_secrets` path |
| Build/deploy | pipelines `continueRun` / standard deploy path |
| Per-PR subdomain | `composegen.resolveRoute` flat label `{ws}-{proj}-pr{n}` |
| TLS | existing `*.{base}` wildcard (DNS-01) — zero new certs |
| URL protection | `protect_admin_uis` basic-auth middleware |
| Auto-cleanup | `StartBackupScheduler` ticker pattern → reaper |
| Isolation (opt) | per-env host binding + SSH-exec (Phase 7) |

## 6. New building blocks required (the actual net-new work)

1. `removeEnv(ws, proj, env, purgeVolumes)` — **refactor** of PutConfig's existing teardown into
   a shared helper + a new `down --volumes` path (env delete itself already exists).
2. `internal/previews` package — model, store, controller (create/update/teardown orchestration).
3. `preview_webhooks` + `preview_environments` tables + migrations.
4. PR-event webhook receiver + provider signature/payload parsing.
5. `PreviewConfig` on the project model + settings plumbing.
6. Optional write-back client (GitHub/GitLab status + comment).
7. Reaper goroutine.
8. Frontend Preview Environments tab.

## 7. Open decisions (confirm before/while building)

1. **Providers for v1** — GitHub only, or GitHub + GitLab + Gitea? (Receiver is provider-pluggable;
   each adds a signature/payload adapter.)
2. **Manual webhook vs GitHub App** for v1 — recommend manual webhook + secret (matches pipeline
   webhooks, no app registration). GitHub App later.
3. **Deploy mechanism** — always run a designated pipeline, or a built-in build+up when no
   pipeline is set? (Recommend: use `Preview.PipelineID` if set, else default build+up.)
4. **Fork default** — `off` (recommended) for v1.
5. **Write-back in v1 or deferred** — recommend deferring to Phase 4 (URL visible in Rigger UI
   first; write-back is additive).
