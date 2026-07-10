# Host Adoption — Scan & Adopt Existing Docker / Compose Workloads

**Status:** design / backlog (build AFTER remote hosts are solid + Docker/Swarm hardening).
**Cross-refs:** enhancements-backlog #16 / #16b / #16c, `phase7-multihost`, `git-source-fullstack-import`, `phase11-backups`, `deployment-target-scope`, `project-name-is-resource-prefix`.

## Goal

Let a workspace **discover** what's already running on a connected host and **adopt** it into Rigger — turning brownfield Docker/compose/other-PaaS workloads into Rigger projects. Onboarding existing infra is a headline feature for a self-hosted PaaS.

## Guiding principles

- **Read-only discovery is always safe.** Nothing is mutated until an explicit, previewed, typed-confirm cutover.
- **`docker inspect` is the universal backbone.** It works for every source (incl. buildpack apps) because it reads what's *actually running*, not how it was defined. File/API adapters are fidelity upgrades layered on top.
- **Non-buildpack sources first** (#16c). Buildpack apps (Dokku herokuish; Nixpacks in Coolify/Dokploy) have no Dockerfile/compose → Rigger can't reproduce the build → image-snapshot only, deferred.
- **The naming tension is deferred, not fought.** Rigger's `resource_prefix` ({ws}_{proj}_{svc}) is immutable and drives container/volume names. Phase 1 sidesteps it by managing existing containers *in place* (no recreation). Phase 3 resolves it (reuse external volume names, or migrate).

---

## Phase 0 — Discovery (read-only inventory)

Enumerate what's on a host; adopt nothing.

- **Backend:** a discovery service that runs, over the existing executor (local daemon or remote SSH-exec), `docker ps` + `docker inspect`, `docker compose ls --format json` (compose projects → name + `config_files` + status), and `docker network/volume ls` for context. Group by the `com.docker.compose.project` label (compose stack) vs standalone (no label). Skip Rigger's own resources (our prefix / a Rigger-owned label).
- **API:** `GET /api/hosts/{id}/discover` → `[{kind: compose|standalone, project, services[], config_files[], image, ports, volumes, networks, labels, status}]`.
- **Frontend:** workspace-level "Discover on host" panel — a read-only tree. Badge each item **importable** (compose/Dockerfile/plain image) / **inspect-only** / **buildpack (limited)**.
- **Value:** inventory + situational awareness before any adoption. Fully self-contained, zero risk.

## Phase 1 — Adopt in place (universal, non-destructive)

Adopt a discovered container/stack as a Rigger project **without recreating it** — "manage in place."

- Reconstruct a `config.json services[]` from `docker inspect`: image+tag, published ports, env, volumes (verbatim), networks, restart policy, command, healthcheck — the **prebuilt-image service** shape Rigger already supports.
- New project mode flag: **`adopted` / external**. In this mode Rigger points at the EXISTING containers by their real names, shows status/logs/metrics, and proxies lifecycle (`docker start/stop/restart <real-name>`) — but does **NOT** run compose-gen or redeploy (which would rename→recreate). No prefix rename because no recreation.
- **Secrets:** env from inspect is ingested; classify likely-secret keys (PASSWORD/KEY/TOKEN/SECRET) → store encrypted, mask in UI.
- **Covers every source** including buildpack apps (it's inspect-based). This is the backbone; everything else is an upgrade.
- **Boundary:** an in-place project is a shim; promoting it to fully Rigger-managed is Phase 3.

## Phase 2 — High-fidelity definition import (the "easy" sources)

For sources that carry a real definition, import the **actual compose/Dockerfile** so the project becomes editable and Rigger-native (not just an image snapshot). Produce the same `detect.Draft` the repo scanner emits, so it flows through existing **ScanReview + create + managed-dep folding**.

Adapters (non-buildpack scope):
- **compose-on-disk** (standalone, **Dockge** `/opt/stacks/<name>/compose.yaml`+`.env`): SSH `cat` → `detect.DetectComposeBytes` → draft.
- **Portainer**: user supplies URL + API token → `GET /api/stacks` + `/stacks/{id}/file` → compose text → draft. (Or read the filestore `…/compose/<id>/` over SSH.)
- **CapRover / Coolify / Dokploy (Dockerfile & compose apps only)**: read their config (files/API) → build service for Dockerfile apps, service graph for compose apps. **Buildpack apps are detected and flagged, not imported.**

Output: a Rigger-managed project **definition** (reviewed, editable) still pointing at existing resources — **not yet cut over**. The actual takeover (which recreates containers) is Phase 3.

## Phase 3 — Safe cutover to Rigger management (data-preserving)

Promote an in-place / imported-definition project to **fully Rigger-managed** (Rigger owns lifecycle + compose-gen + prefix) without losing data. Split by storage type (#16 P3a/P3b):

- **P3a — bind-mounts (easy first):** data lives on host paths. Rigger's regenerated compose references the **same host paths** (reuse the existing bind-mount host-path translation). Recreate container, data untouched.
- **P3b — named volumes:**
  - *Reuse (no data move):* composegen references the **existing volume as an external volume** (keep its original name instead of prefixing). Recreate container, same volume. Needs composegen support for external/unprefixed volume refs.
  - *Migrate:* create Rigger-prefixed volumes and copy data via the **backup/restore engine** (`phase11-backups`) or a one-shot `docker run --rm -v old -v new alpine cp`. Clean prefix, slower.
- **Networks:** reconcile app networks — reuse or recreate + reconnect.
- **Routing re-map:** translate the source's ingress into Rigger's Traefik. Traefik-label sources (Coolify/Dokploy) parse cleanly; nginx sources (CapRover/Dokku) re-derive routes from their domain/SSL config into `Config.Routes[]`.
- **Cutover flow:** preview (what recreates, which volumes reused vs migrated, **downtime warning**) → typed confirm → stop old → deploy Rigger-managed → verify → optional cleanup of old containers.

## Phase 4 — Buildpack sources & polish (deferred / stretch)

- **Buildpack apps** (Dokku, Nixpacks Coolify/Dokploy): Phase-1 image snapshot only, or a "commit running image → push to registry → run as prebuilt" flow so it survives redeploys. True reproducibility (adding Nixpacks/buildpack build support to Rigger) is a large separate effort — out of scope.
- **Metadata enrichment:** Dokku `config:export`/`domains:report`/`storage:list`; CapRover captain-definition; Coolify/Dokploy API for domains/env.
- **Drift detection:** warn when the on-host compose file differs from the running container's inspect.

---

## Cross-cutting concerns

- **Ownership labels:** stamp adopted resources with a Rigger-owned label so we know what we manage; detect if another manager (Portainer/CapRover agent) still owns them → warn before cutover.
- **Secrets:** never surface plaintext; classify + encrypt on ingest.
- **RBAC:** workspace-admin (adoption creates projects).
- **Safety:** discovery read-only; every destructive step previewed + typed-confirm; source untouched until explicit cutover.
- **Swarm vs compose:** discovery must handle both (`docker stack`/`service ls` for Swarm hosts). Dokploy/CapRover are Swarm-native — aligns with Rigger's swarm mode.

## Build order (recommended)

Phase 0 → Phase 1 (universal backbone; ships real value alone) → Phase 2 compose-on-disk adapter (Dockge/standalone — the easiest, highest-ROI) → Phase 3a (bind-mounts) → Phase 2 Portainer/other adapters → Phase 3b (named volumes) → Phase 4. Each phase is independently shippable and non-destructive up to Phase 3's explicit cutover.
