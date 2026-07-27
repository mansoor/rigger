# Status and backlog

Internal tracking: what's verified, what's known-shaky, and what's queued. Deliberately kept **out of the README**, which is a product guide for people adopting Rigger, not a progress log.

Last reviewed: **2026-07-26** (v0.1.39).

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
| Swarm deploy + Traefik routing | Live-verified |
| Managed databases (Postgres, MySQL, MariaDB, Mongo) | Live-verified |
| Auxiliary engines (OpenSearch, VictoriaMetrics, RabbitMQ) | Live-verified |
| Nixpacks build backend | Live-verified (local); remote-host install path exercised, not deployed end-to-end |
| Blueprint scaffolding | Live-verified |
| Backups, restore, verify dry-run | Live-verified |
| Maintenance mode | Live-verified |
| Managed-secret pinning | Live-verified (recovered a drifted Postgres password in place) |
| Stale-network detection and repair | Live-verified against a reproduced failure |
| Follow-stream cancellation | Live-verified (process tree confirmed clean after cancel) |
| Remote hosts, SSH exec, file sync | Verified on one host (10.10.10.55) |
| Per-workspace and per-env ACME override certs | **Built, not live-verified end-to-end** |
| Custom domains (TXT / CNAME / file verification) | **Built, not live-verified end-to-end** |
| Preview environments | **Built; never exercised against a real pull request** |
| Self-update apply and rollback | **Built; apply never run from a dev box** |
| UI control-kit refactor (v0.1.38) | Login screen verified; **screens behind auth not visually inspected** |

The last group is the honest risk list. Anything there may work exactly as designed — it just hasn't been proven.

---

## Backlog

### Queued

- **Host adoption** — `docs/HOST_ADOPTION.md` phases 0–1: discover and adopt containers already running on a host that Rigger didn't create. Note that the backlog entry's buildpack exclusion is partly stale now that Nixpacks has shipped, so the scope needs re-deciding before starting.
- **Managed-secret drift detector** — surface projects whose pinned secret doesn't match either `.env` or the live volume, before it becomes an outage. Prompted by the `mcl_qrs` incident, where the pin held a value matching neither.
- **Visual QA of the authenticated UI** — the v0.1.38 control refactor touched every page. Proxy Service and Settings are the highest risk: both had a local `Btn` folded into the kit, and Settings also lost its own `Label`/`Input`/`Select`/`Toggle`.

### Known gaps (documented as constraints in the README)

- Remote-host `build` / `promote` need a build context and a reachable registry.
- Editing a remote env's variables writes the control plane's cache; it isn't pushed to the host.
- Backup S3/SFTP sync excludes remote-host environments.
- Preview environments support GitHub and Gitea only; no GitLab.
- Routing table has no weight/canary or rate-limit support.
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
