# Implementation Plan: Image Distribution — System Registry + Build Hosts

> Companion to [image-distribution.md](image-distribution.md). Branch: `develop`, small incremental
> commits, each phase builds + tests green before the next. Co-author trailer on every commit.
> Never commit `.env` / `config.json`.

## Guiding principles
- **Reuse:** Registries (P3b), auto `docker login`, `--with-registry-auth`, Traefik/ACME, crypto,
  host inheritance, `wsconfig.ImageTag` — almost everything exists; this is mostly wiring + one
  resolver + one infra sidecar.
- **MVP first = make multi-node swarm custom apps deployable.** That's Phases 1–2 (system registry
  + provisioning). Build hosts (Phase 4) are a separate, later concern.
- Each phase independently verifiable (unit + a manual deploy).

---

## Phase 0 — Effective-registry resolver (foundation)
- `settings.EffectiveRegistry(db, ws, proj)` → project → workspace-system → global-system.
- Thread it into every site that currently reads the raw project registry: `wsconfig.ImageTag`
  callers (build/push/promote/advance), composegen `image:`, envgen `{SVC}_IMAGE`/`REGISTRY=`
  rebase, `dockerops` deploy/update.
- **No UI/behavior change yet** (system registry is empty → resolver returns project registry, i.e.
  today's behavior). Pure refactor + golden parity.
- **Commit:** `feat(registry): effective-registry resolver (project→ws→global system)`

## Phase 1 — System-registry designation (★ MVP backbone)
- **1a.** Registries model: `system bool` (+ migration). Enforce ≤1 global and ≤1 per workspace.
- **1b.** API: mark/unmark system (global = admin; workspace = ws-admin); `EffectiveRegistry` reads it.
- **1c.** Frontend: "Mark as system" toggle on the Registries lists (admin global + Manage
  Workspace). Project registry dropdown default becomes **"System registry"**.
- **1d.** Gate: project resolving to system registry with none defined → inline warning at save +
  **block deploy** for build-service projects with a clear "configure a system registry" message.
- **Verify:** point system registry at an existing/cloud registry → a custom app builds, pushes,
  and deploys to a **multi-node swarm** (nodes pull). This is the core win.
- **Commit:** `feat(registry): system registry designation + gate`

## Phase 2 — Rigger-managed registry (one-click)
- **2a.** Add a `registry:2` infra sidecar to Rigger's compose stack (named volume; internal by
  default). Lifecycle helpers (up/down/status) like the other infra services.
- **2b.** Admin action on the Registries page: "Run Rigger-managed registry" → starts the sidecar,
  auto-generates basic-auth creds (htpasswd; creds AES-GCM-encrypted in the registries entry),
  creates the registries entry, marks it system.
- **2c.** TLS/reachability: when `apps_base_domain` + DNS is set, front it via Traefik + ACME at
  `registry.{base}` (reuse domain-routing + ACME); else localhost/HTTP (local single-node only —
  surface this limitation in the UI).
- **2d.** GC: a "garbage-collect" admin action + a sane default retention; show registry disk usage.
- **Verify:** one-click on a base-domain instance → `registry.{base}` serves over HTTPS; a custom
  app deploys to the swarm with zero per-node config.
- **Commit:** `feat(registry): one-click Rigger-managed registry (Traefik/ACME + auto-auth + GC)`

## Phase 3 — Cleanup of vestigial no-registry paths  ✅ DONE (revised — local-only KEPT) `9cf…`
> **Revised:** local single-node (no-registry) builds are a first-class fallback, NOT removed
> (Rigger ships with no system registry; simple local apps need zero infra). The literal
> "remove `pull_policy: never`" would regress the local case (re-introduces the `kmb` pull bug),
> so Phase 3 became a clarity reframe rather than a removal. See design §9.
- ~~Remove the "Local — no registry" dropdown option~~ → already done in **Phase 1** (the empty
  option reads "System registry (workspace / global default)"; empty = inherit system, else local).
- ~~Remove `pull_policy: never` + the skip-pull branch~~ → **KEPT** as the automatic local
  single-node fallback (the deploy gate already blocks Swarm/remote build-service deploys when no
  registry resolves, so these only apply to the safe local case). Comments/messages **reframed** so
  "no registry" means *no EFFECTIVE registry resolves*.
- advance.go pointer resync → **already effective-registry-aware** (Phase 0 sets the effective
  registry in `builder.loadConfig`); a registry-store existence probe adds auth/cost for no gain. Left as-is.
- **Verify:** golden parity UNCHANGED (`pull_policy: never` still emitted for no-registry build
  services — intended); build/vet/test + eslint/vite green.
- **Commit:** `refactor(registry): reframe no-registry path as automatic local fallback (keep pull_policy)`

## Phase 4 — Build hosts
- **4a.** Host capability probe: `docker info` at add/bind → record `build`, `swarm-active`,
  `manager`. Store on the host record.
- **4b.** `BuildHost` on the project model (inherited workspace→project; default = local Rigger).
  Builder targets the build host's executor; on build-host ≠ deploy-host, build → push to effective
  registry (no source pushed to the deploy host).
- **4c.** Frontend: build-host picker (Manage Workspace default + Edit Project override); a
  **build-only** host is excluded from deploy-host pickers.
- **Verify:** bind a project's build host to a remote builder → build runs there, pushes to the
  registry, swarm deploys; deploy host never receives source.
- **Commit:** `feat(build): per-project build host (inherited) → push to registry`

## Phase 5 — Preflight / validation
- At bind/deploy: system-registry-resolvable (else block build deploys); env=swarm vs host
  `Swarm: active`; multi-node swarm → registry hard-required; build-only ∉ deploy pickers.
- Clear, early messages (not deploy-time failures).
- **Commit:** `feat(deploy): preflight — registry + swarm + host-role checks`

## Phase 6 — `save|load` transport (optional, single-node/compose only)
- For build ≠ deploy on a single daemon with no registry desired: `docker save | ssh | docker load`
  (reuse remotehost SSH). **Explicitly disabled for multi-node swarm.**
- May be skipped entirely if the registry covers all real cases — decide after Phases 1–4.
- **Commit:** `feat(deploy): save|load image transport (single-node)`

## Phase 7 — Verify, document, memory
- Full gate (`go build/vet/test ./...`, `npm run build`, rebuild rigger).
- Live E2E: custom app → multi-node swarm via system registry (open the cluster, confirm nodes
  pull); one-click managed registry over HTTPS; build-host-on-remote → registry → swarm.
- Update design status → SHIPPED; memory.
- **Commit:** `docs(registry): status + memory`

---

## Sequencing
```
Phase 0 (resolver) ──▶ Phase 1 (system registry + gate)  ★ MVP: multi-node swarm works
                          ├─▶ Phase 2 (one-click managed registry)
                          ├─▶ Phase 3 (cleanup no-registry)
                          └─▶ Phase 4 (build hosts) ──▶ Phase 5 (preflight)
                                                          └─▶ Phase 6 (save|load, optional)
                                                                 └─▶ Phase 7 (verify/docs)
```
**MVP = Phases 0–1** (designate any registry as system → custom apps deploy to multi-node swarm).
Phase 2 makes onboarding one-click; 3 removes cruft; 4–5 add build hosts + guardrails; 6 optional.

## Risks / watch-items
- **Golden churn** in Phase 0/3 (registry threading + `pull_policy` removal) — regenerate + parity-check.
- **Managed-registry reachability** is only solved with a base domain (TLS); localhost registry can't
  serve a remote swarm — must be surfaced, not silently broken.
- **Migration** of existing no-registry projects needs a rebuild+push to populate the registry; the
  first post-migration deploy isn't instant.
- **Control-plane-hosted registry = deploy dependency** on the control plane; document, offer cloud
  / on-swarm as the HA path.
- **Build host on Windows** does not fix the slow bind mount (source still tarred off it); position
  it as distribution/shared-build, not the Windows fix.
