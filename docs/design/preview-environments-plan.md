# Implementation Plan: Preview / PR Environments

> Companion to [preview-environments.md](preview-environments.md). Branch: `develop`, small
> incremental commits, each phase builds + tests green before the next. Co-author trailer on
> every commit. Never commit `.env` / `config.json` (live user data).

## Guiding principles

- **Reuse, don't reinvent:** every phase leans on an existing primitive (copy-env, webhooks,
  composegen routing, pipelines, scheduler). The only genuinely new orchestration is the
  preview controller and `TeardownEnvironment`.
- **Ship value early:** the host-daemon ephemeral-env model means Phases 1–3 produce a working
  preview the moment a webhook fires — no GitHub App, no DinD, no new TLS.
- **Each phase is independently verifiable** (unit + a manual cURL/webhook-replay E2E).

---

## Phase 0 — Foundations: refactor teardown + config model

The two pieces everything else needs.

> **Note:** env deletion already works — `PutConfig` (`api/handlers.go:1230`) tears down envs
> dropped from config.json: `down` (containers + networks) → overwrite config →
> `os.RemoveAll(envs/{env})`, honoring the env-list-union invariant (`122ad60`). Phase 0 is a
> *refactor + extension*, not a from-scratch build.

**0a. Extract a reusable `removeEnv(ws, proj, env, purgeVolumes bool)`.**
- Lift PutConfig's inline removal block (the `down` loop + `os.RemoveAll(EnvDir)`) into a shared
  internal function; have PutConfig call it (`purgeVolumes=false`, preserving today's behavior —
  deleting an env must NOT destroy its data volumes).
- Add a `down --volumes` path in `dockerops` (`r.down()` currently runs plain `compose down`;
  add a volumes flag) and thread `purgeVolumes` through the bridge `RunOptions`. Previews call
  with `purgeVolumes=true` so per-PR volumes don't accumulate.
- Optional record cleanup (backup schedules/runs, secrets) behind the same helper — for previews
  the controller also deletes the `preview_environments` row.
- Tests: golden — `removeEnv(purge=false)` leaves the named volume (parity with today);
  `purge=true` removes it; env absent from both dir + config; idempotent on a missing env.

**0b. `PreviewConfig` on the project model.**
- Add `Preview *PreviewConfig` to `wsconfig.Project` and `workspace.Project` (fields per design
  §4.2). `omitempty` so existing config.json round-trips byte-identical (golden parity).
- No behavior yet — just the struct + JSON round-trip test.

**Verify 0:** `go build/vet/test ./...`; golden parity for an existing project (no preview cfg →
no diff).

**Commit:** `feat(env): TeardownEnvironment + manual env delete` / `feat(preview): project config model`

---

## Phase 1 — Data model + preview store

**1a.** DB migrations (`internal/db/db.go` `migrate`): create `preview_webhooks` and
`preview_environments` tables (schemas in design §4.1, §4.5). Use `addColumn`/`CREATE TABLE IF
NOT EXISTS` idiom already in db.go.

**1b.** `internal/previews` package:
- `webhooks.go` — `CreateWebhook/GetByToken/Touch/Delete` (copy the `pipelines/webhooks.go`
  shape; token = 24-byte hex, sha256-hashed; `secret` stored for HMAC).
- `store.go` — `PreviewEnv` model + `Create/Get/GetByPR/ListForProject/Update/Delete/ListExpired`.

**Verify 1:** unit tests for token hashing/lookup and store CRUD; `go test ./internal/previews/...`.

**Commit:** `feat(preview): tables + store + webhook store`

---

## Phase 2 — Preview controller (lifecycle orchestration)

The heart of the feature. New `internal/previews/controller.go` (or methods on the api Handler
since it needs the bridge + pipelines).

**2a. `createPreview(ws, proj, prNumber, branch, sha)`:**
- Refactor `CopyEnvironment` body into an internal `cloneEnv(ws, proj, srcEnv, dstEnv,
  regenSecrets)` so both the HTTP handler and the controller call it.
- Clone `Preview.TemplateEnv` → `pr{n}`; blank domain; set `Env.Git.Branch = branch`;
  `regenSecrets = true`; `bridge.Bootstrap`.
- **DB strategy (v1 = `isolated-empty` + `isolated-seed`):** `isolated-empty` is the existing
  behavior; `isolated-seed` already works via the v3 `db_seed` auto-import on first deploy — no
  new code, just don't suppress it. `clone-from` / `shared` are deferred to Phase 5b.
- Deploy: if `Preview.PipelineID` set → `continueRun`; else default build+up path.
- Insert `preview_environments` row; capture resolved URL via the composegen URL helper.

**2b. `updatePreview(...)`:** redeploy `pr{n}` (gitsync fetch+reset to new sha → build → up);
bump `last_deployed_at`, `head_sha`.

**2c. `teardownPreview(...)`:** call `TeardownEnvironment(ws, proj, "pr{n}")` + delete the
`preview_environments` row.

**2d. Guards:** branch filter (glob), `MaxConcurrent` check, fork policy (`AutoDeployForks`).

**Verify 2:** unit-test the guard logic (filter/cap/fork) with table tests; integration-test
clone→deploy→teardown against the dev rigger instance with a throwaway project.

**Commit:** `feat(preview): lifecycle controller (create/update/teardown)`

---

## Phase 3 — Webhook receiver + routing

**3a.** Public route `POST /api/previews/hooks/{token}` in `cmd/server/main.go` (unauthenticated;
token + HMAC is the auth). Handler `InboundPreviewWebhook` in `api/preview_handlers.go`:
- Resolve webhook by token; verify provider signature (GitHub `X-Hub-Signature-256` / GitLab
  `X-Gitlab-Token`) — reuse the HMAC helper from `pipeline_handlers.go`.
- Parse provider event → `{action, prNumber, branch, sha, isFork}`. Start with a GitHub adapter;
  keep the parse behind a `providerParse(provider, headers, body)` seam.
- Dispatch: `opened|reopened`→create, `synchronize`→update, `closed`→teardown. Run async
  (`go ...`) and return `202` fast (webhooks must not block).

**3b.** Confirm routing: deploy a `pr{n}` env and assert the host resolves to
`{ws}-{proj}-pr{n}.{base}` under the existing wildcard (no new cert). Add a `resolveRoute` unit
case for a `pr{n}` env label.

**Verify 3:** replay a captured GitHub `pull_request` payload with `curl` (correct signature) →
preview env created + reachable; `synchronize` → redeploy; `closed` → gone. Bad signature → 401.

**Commit:** `feat(preview): signed PR webhook receiver + dispatch`

---

## Phase 4 — Status write-back (optional, additive)

**4a.** Per-project write-back token (write-only/masked, stored like the DNS token).
**4b.** `internal/previews/writeback.go` — GitHub commit status (`POST /statuses/{sha}`) +
upsert a single PR comment with the preview URL + status. No-op if no token configured.
**4c.** Hook into controller transitions (creating/running/failed/torn_down).

**Verify 4:** against a test repo, confirm the commit status + comment appear and update.

**Commit:** `feat(preview): PR status + comment write-back`

---

## Phase 5 — Reaper + concurrency enforcement

**5a.** `StartPreviewReaper()` goroutine (mirror `StartBackupScheduler`): 30-min ticker →
`previews.ListExpired(now)` → `teardownPreview`. Start it in `cmd/server/main.go` alongside the
other schedulers. `expires_at` set from `TTLHours` on each deploy (sliding window).
**5b.** Enforce `MaxConcurrent` in `createPreview` (reject + optional write-back message).

**Verify 5:** set `TTLHours=0`-equivalent short window in a test build, confirm reap; cap=1 →
second PR rejected.

**Commit:** `feat(preview): TTL reaper + concurrency cap`

---

## Phase 5b — DB strategy: data fidelity (`clone-from` + `shared`)

The high-value follow-up — makes previews useful for data-driven apps (see design §4.3a).

**5b-1. `clone-from:<env>`** ⭐
- On preview create, after the DB service is up + healthy and **before** the app starts, restore
  the latest backup/snapshot of `DBSource` into the preview DB. Reuse the backup/restore
  subsystem (restore an `.rwb` / per-env snapshot into the `pr{n}` DB) and the v3 seed
  sequencing (DB-ready → import → then app), so the app never boots against an empty DB.
- Clone happens **once on create**, not on rebuilds (re-cloning would wipe a reviewer's test
  data). PR migrations run on top via the normal deploy path.
- Guard: `DBSource` must be an env the key/user can read; warn when it's prod (PII). Anonymize
  on restore = future.

**5b-2. `shared:<env>`**
- Don't create a per-PR DB; wire the app's `DB_HOST`/credentials to `DBSource`'s DB instead
  (composegen/envgen: skip the managed-DB service for `pr{n}`, point env vars at the source).
- **Teardown safety:** in shared mode `removeEnv` must remove only app containers and **never**
  purge the source env's volume. Add an explicit guard + test.
- UI warning (Phase 6): suited to read-mostly review, not migration testing; recommend non-prod.

**Verify 5b:** clone-from a seeded test env → preview shows the data, source untouched after the
preview writes + teardown; shared mode → preview reads source data, teardown leaves source DB
and its volume intact (assert volume still present).

**Commit:** `feat(preview): clone-from-env + shared DB strategies`

## Phase 6 — Frontend

**6a.** `api.js`: preview endpoints (get/set config, list previews, create webhook + reveal,
manual redeploy/teardown).
**6b.** **Project → Preview Environments** tab (`EditProjectPage.jsx` or a new page component):
config form (template-env picker, provider, branch filter, max concurrent, TTL, protect-auth,
fork policy, write-back, **DB strategy selector + source-env picker with the shared-mode
warning**), webhook URL/secret one-time reveal + paste instructions.
**6c.** Active-previews list: PR#, branch, URL link, status badge, last-deploy, row actions
(Redeploy / View logs → `last_run_id` / Tear down). Project-page "N previews active" indicator.

**Verify 6:** `npm run build` (ESLint gate + vite); live click-through in the dev instance —
enable, reveal webhook, replay a PR event, watch the row appear/update/disappear.

**Commit:** `feat(preview): Preview Environments tab + active list`

---

## Phase 7 — Verify, document, memory

- Full gate: `go build/vet/test ./...` in golang:1.25-alpine (rigger-gomod volume);
  `npm run build`; rebuild rigger (`docker compose -f .../src/docker-compose.yml up --build -d
  rigger`); smoke the new routes (401 on bad sig, 202 on good).
- **Live E2E:** real repo + webhook → open PR → preview URL serves the branch → push → redeploy
  → close PR → torn down → reaper catches a simulated missed-close.
- Docs: add a `## Preview Environments` section to `docs/API.md` if any of it is exposed via the
  external `/api/v1` surface (likely the list/teardown ops); update the design doc status to
  SHIPPED.
- Memory: write `preview-environments.md` (decisions: host-daemon not DinD; flat-label wildcard
  reuse; template-env clone; missing-then-built `TeardownEnvironment`; reaper) + index line; link
  `[[domain-routing-model]]`, `[[phase9-pipelines]]`, `[[acme-cert-lifecycle]]`,
  `[[per-env-services-and-mailpit]]`.

**Commit:** `docs(preview): API + design status + memory`

---

## Phasing rationale & sequencing

```
Phase 0 (teardown + config)        ── unblocks everything
   └─▶ Phase 1 (tables + store)
          └─▶ Phase 2 (controller)         ── working clone/deploy/teardown via internal calls
                 └─▶ Phase 3 (webhook)      ── ★ MVP: real PRs drive previews end-to-end
                        ├─▶ Phase 4 (write-back)   ┐ both optional/additive,
                        └─▶ Phase 5 (reaper/caps)  ┘ can land in either order
                               └─▶ Phase 6 (UI)
                                      └─▶ Phase 7 (verify/docs/memory)
```

**MVP = Phases 0–3** (a PR webhook produces a live, auto-updating, auto-removed preview URL).
Phases 4–6 are polish/operability. Phase 7 hardens + records.

## Risks / watch-items

- **Teardown completeness** — orphaned volumes/networks/dirs are the classic failure (the
  `122ad60` union bug). Phase 0 tests must assert *nothing* survives.
- **Webhook delivery gaps** — never rely solely on the `closed` event; the reaper (Phase 5) is
  the safety net. Don't defer it indefinitely.
- **Untrusted fork code** — keep `AutoDeployForks=off` as the default; document the dedicated-host
  isolation option before anyone flips it to `on`.
- **Resource exhaustion** — `MaxConcurrent` + TTL are mandatory guards, not nice-to-haves, on a
  shared host daemon.
- **Secret leakage** — previews must regenerate secret-flagged env vars; never copy prod secrets.
