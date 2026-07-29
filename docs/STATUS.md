# Status and backlog

Internal tracking: what's verified, what's known-shaky, and what's queued. Deliberately kept **out of the README**, which is a product guide for people adopting Rigger, not a progress log.

Last reviewed: **2026-07-28** (v0.1.42).

---

## How to use this file

- **Verification state** is the honest record of what has actually been exercised against a live system versus what only passes its build gate. Update it when something gets live-verified.
- **Backlog** is a holding pen, not a commitment. Items graduate into real work when they're picked up.
- When a backlog item ships, delete it from here and — only if it's user-facing — describe it in the README as a finished capability. Don't add "shipped in vX" notes to the README; the release tags are the changelog.

---

## Verification state

| Area | State |
|------|-------|
| Compose deploy, refresh, regen, rollback | Live-verified |
| Swarm deploy + Traefik routing | Live-verified **on a single-node Swarm only** |
| Managed databases (Postgres, MySQL, MariaDB, Mongo) | Live-verified |
| Auxiliary engines (OpenSearch, VictoriaMetrics, RabbitMQ) | Live-verified |
| Nixpacks build backend | Live-verified (local); remote-host install path exercised, not deployed end-to-end |
| Blueprint scaffolding | Live-verified |
| Backups, restore, verify dry-run | Live-verified |
| Maintenance mode | Live-verified |
| Managed-secret pinning | Live-verified (recovered a drifted Postgres password in place) |
| Stale-network detection and repair | Live-verified against a reproduced failure |
| Follow-stream cancellation | Live-verified (process tree confirmed clean after cancel) |
| Image-pull progress collapse | Live-verified against real pulls (871 lines → 1 row, 2 recorded) |
| Remote hosts, SSH exec, file sync | Verified on one host (10.10.10.55) |
| **Multi-node Swarm** | **Built, never run against a real cluster** — see below |
| Per-workspace and per-env ACME override certs | **Built, not live-verified end-to-end** |
| Custom domains (TXT / CNAME / file verification) | **Built, not live-verified end-to-end** |
| Preview environments | **Built; never exercised against a real pull request** |
| Self-update apply and rollback | **Built; apply never run from a dev box** |
| UI control-kit refactor (v0.1.38) | Login screen verified; **screens behind auth not visually inspected** |

The bold group is the honest risk list. Anything there may work exactly as designed — it just hasn't been proven.

### Multi-node Swarm (v0.1.42)

Worth its own note, because the *parsing* is well tested and the *behaviour* is not — an easy thing to mistake for proven.

Verified: `parseSwarmNodes` and `parseSwarmInfo` against real `docker node ls` / `docker info` output from Docker 29.6.1, and the placement analyser against a real generated compose (which is how the `${RIGGER_BIND_ROOT:-.}` colon-splitting bug was found).

Not verified, because every dev box here is a single-node Swarm:

- the node list rendering with more than one node, roles, or a non-leader manager;
- `Self` matching in a cluster where the ids actually differ;
- the unpinned-state warning firing at all — its whole trigger is `nodes >= 2`, so it has never executed;
- whether a stack genuinely schedules, reschedules and routes across nodes.

**The test that settles it:** register a host pointing at a real multi-node manager, hit Test (exercises the inventory), then Swarm-deploy anything stateful (exercises the warning). One session against a real cluster clears this whole entry.

---

## Backlog

### Queued

- **Host adoption** — `docs/HOST_ADOPTION.md` phases 0–1: discover and adopt containers already running on a host that Rigger didn't create. Note that the backlog entry's buildpack exclusion is partly stale now that Nixpacks has shipped, so the scope needs re-deciding before starting.
- **Managed-secret drift detector** — surface projects whose pinned secret doesn't match either `.env` or the live volume, before it becomes an outage. Prompted by the `mcl_qrs` incident, where the pin held a value matching neither.
- **Visual QA of the authenticated UI** — the v0.1.38 control refactor touched every page. Proxy Service and Settings are the highest risk: both had a local `Btn` folded into the kit, and Settings also lost its own `Label`/`Input`/`Select`/`Toggle`.
- **One session against a real multi-node Swarm** — clears the whole multi-node entry above. Needs nothing but access to a cluster; no code is expected to change.

### Known gaps (documented as constraints in the README)

- Remote-host `build` / `promote` need a build context and a reachable registry.
- Editing a remote env's variables writes the control plane's cache; it isn't pushed to the host.
- Backup S3/SFTP sync excludes remote-host environments.
- Preview environments support GitHub and Gitea only; no GitLab.
- Routing table has no weight/canary or rate-limit support.
- Rigger does not join, promote or drain Swarm nodes — it deploys to a cluster you built. Since v0.1.42 it *shows* the cluster and warns about unpinned state, but managing membership stays Docker's job.
- Nothing automatically pins a stateful service to the node holding its volume; the warning names the risk, the placement constraint is still yours to set.
- `forward_auth` is reserved in the config model but not implemented; only `basic` works.
- Response caching (Souin) is **hard-disabled** — it panics under Traefik's Yaegi interpreter and takes every routed app down with it. Do not re-enable without a different plugin.
- Host-OS housekeeping actions assume Linux.

### Deferred by decision

- **Kubernetes / ACA / ECS targets.** Docker and Swarm only, until both are properly hardened. Don't propose new deployment targets.
- **Managed OpenBao / Vault secrets backend** (`docs/design/vault-secrets-backend.md`) — the Compose-plaintext and Swarm-native mechanisms cover current needs.
- **Web-UI sidecars for the auxiliary engines** — the Service Console covers connection info; full consoles aren't worth the surface yet.
- **Monetization / open-core split** (`docs/MONETIZATION_AND_LICENSING.md`, `docs/OPEN_CORE_CODE_SPLIT.md`) — plan-level only. Not to start before Docker/Swarm hardening, and security features never go behind a paywall.

### Minor

- Container PID 1 doesn't reap, so a few zombie processes accumulate. Harmless (a pid slot each) and much rarer since follow streams are killed properly, but it's there.
- Swarm-mode deploys were not examined for the stale-network failure class. The orchestrator recreates tasks itself, so it likely doesn't apply — untested, not ruled out.
- Double-prefix naming cleanup in Swarm stack names.

---

## Conventions

- **Releases** — annotated tag `vX.Y.Z` on `develop`, pushed; CI builds the GHCR image and publishes the release from the tag message. The tag message is the changelog, so write it properly.
- **Terminology** — Workspace (scoping tier) → Project (deployable unit) → Environment (tier). Older documents and the `rigger.sh` wrapper call a project a "workspace"; that wrapper predates the workspace tier.
- **Design documents** live in `docs/design/`. Handover notes in `docs/handoff/` are historical and are not maintained.
