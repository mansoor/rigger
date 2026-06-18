# Design: Image Distribution — System Registry + Build Hosts

> Status: **Design — locked, review before building.** Branch: `develop` (incremental commits).
> Companion: [image-distribution-plan.md](image-distribution-plan.md) (phased plan).
>
> Origin: debugging custom (CodeCanyon/Botble) apps on a Local-no-registry project surfaced that
> a built image has nowhere to live for remote/multi-node deploys. Brainstormed 2026-06-18.

## 1. Problem

Rigger builds images itself (`internal/builder` runs `docker build`; compose/swarm only *run* the
image). That leaves an unanswered question: **how does a built image reach the daemon(s) that run
it** when build ≠ run host — and especially on a **multi-node swarm**, where every node must be
able to pull the image?

Today a project can be **"Local — no registry"**. That works only when build and run share one
daemon. It breaks for:
- **Remote-host deploys** — the image is built on the control plane, absent on the remote.
- **Multi-node swarm** (the target topology) — `docker stack deploy` schedules tasks across nodes;
  nodes without the image fail. There is no transport, so the image is unreachable.

It also produced a real bug (fixed `17867b2`): a no-registry custom app built fine but `compose up`
tried to pull the local-only tag from Docker Hub.

## 2. Locked decisions

1. **A single admin-designated *system registry*** is used wherever a project doesn't set its own —
   reusing the existing Registries feature (P3b) plus a "this is the system registry" flag.
2. **Inheritance:** effective registry = **project → workspace-system → global-system**. The
   **"Local — none" option is removed.** Mirrors host/domain inheritance.
3. **Provisioning the system registry, two ways:**
   - **Rigger-managed** — an admin one-click on the Docker Registries page spins up a `registry:2`
     **as a Rigger infra sidecar** (next to `rigger-traefik`/`rigger-socket-proxy`, *not* a user
     project — this is why a "system workspace" is unnecessary), auto-creates its Registries entry,
     and auto-marks it system.
   - **Point-at-existing** — admin adds any existing/cloud registry (GHCR/ECR/Docker Hub private/…)
     and marks it system. Lowest-ops (TLS + reachability already handled).
4. **Gate, don't silently fail:** if a project resolves to the system registry but none is defined,
   prompt at save (link to settings) and **block deploy** with a clear message.
5. **Registry-first transport.** Multi-node swarm makes a registry **mandatory**. `docker
   save | ssh | docker load` is kept only as a **single-node / compose convenience**, never the
   multi-node path.
6. **Build hosts** are selectable **per project**, optional, default = **local Rigger machine**;
   they follow the **workspace → project inheritance** like remote (deploy) hosts. A deploy host may
   also build; a **build-only host may not be a deploy target**.

## 3. Effective-registry resolution

Replace every `registry == ""` decision with a resolver:

```
effectiveRegistry(ws, proj) =
    project.Registry            // explicit per-project (unchanged)
 || workspaceSystemRegistry(ws) // workspace override (new)
 || globalSystemRegistry()      // admin default (new)
 // → if still empty: not deployable for a BUILD service (gate)
```

- Registries are workspace-scoped today (P3b); the **global** system registry is chosen from the
  admin/global pool, the **workspace** override from that workspace's pool.
- Image tags become `{effectiveRegistry}/{prefix}-{svc}:{ver}-{env}` everywhere (build, push,
  compose `image:`, `.env {SVC}_IMAGE`, deploy-history). `wsconfig.ImageTag` already prefixes the
  registry — the only change is feeding it the *effective* registry instead of the raw project one.
- **Image-type** services (a pinned public image like `nginx`) are unaffected — they never used a
  Rigger registry.

## 4. System registry

### 4.1 Designation
- A `system` flag in the registries model; **at most one global + at most one per workspace**.
- Admin UI: a "Mark as system" toggle / star on the Registries list (global pool) and on a
  workspace's registries (override). Resolver reads global, workspace overrides it.

### 4.2 Rigger-managed registry (one-click)
- Adds a `registry:2` **infra sidecar** to Rigger's own compose stack (a named data volume for
  storage). Not a workspace project — no "system workspace" needed.
- **Reachability / TLS — the crux for multi-node:** auto-front it via **Traefik + ACME at
  `registry.{base}`**, reusing the domain-routing + ACME model already in Rigger. Then:
  - `apps_base_domain` + DNS configured → registry is **HTTPS at `registry.{base}`**, pullable by
    all swarm nodes with no per-node config → genuinely one-click. ✅
  - No base domain → `localhost`/HTTP only → fine for **local single-node**; **not** usable by a
    remote multi-node swarm. So a base domain is the prerequisite for the managed registry to serve
    a cluster.
- **Auth:** Rigger auto-generates basic-auth creds (htpasswd), stores them encrypted in the
  registries entry (same AES-256-GCM key as host SSH keys / write-back tokens). The build host
  auto-`docker login`s (already does); swarm gets creds via `--with-registry-auth`
  ([swarm.go:103](../../src/backend/internal/dockerops/swarm.go) — already wired).
- **Storage + GC:** named volume + a default retention; a "garbage-collect" admin action
  (`registry garbage-collect`). Must ship with the feature — a shared registry fills disk fast.
- **Availability note:** a managed registry on the control-plane box couples deploys to that box's
  reachability (nodes pull from it). Acceptable for v1; HA = run it on the swarm or point at cloud
  (the point-at-existing path covers that).

## 5. Build hosts

- **Model:** hosts are pool resources with **probed capabilities** (`build`,
  `deploy-compose`, `deploy-swarm`/manager) — `docker info` at add/bind time records
  `Swarm: active` / `ControlAvailable`.
- **Build host = per-project**, optional, default **local Rigger**; resolved via **workspace →
  project** inheritance (deploy host stays per-env). Build is a project-level concern.
- **Flow:** build host builds → tags with the effective registry → **pushes**; control plane runs
  `stack deploy --with-registry-auth` (swarm) / `compose up` (compose) on the deploy host → it/the
  nodes **pull**. When build host == deploy host (or both local), build+run colocate and **no
  transport happens** at all.
- **Asymmetry:** a deploy host is build-eligible (superset); a **build-only host is never offered in
  the deploy-host picker**.
- **Honest caveat (Windows):** a remote build host does **not** escape the slow Windows bind mount
  while Rigger runs on Windows — `PushDir` still tars the source off that mount (full copy, not
  incremental — `remotehost/sync.go`). The Windows fix is moving the control plane to Linux (planned)
  or workspaces onto a fast volume; the build host's value is **image distribution + shared/busy
  builds**, not the Windows workaround.

## 6. Validation / preflight

A guaranteed registry makes preflight simple:
- **System registry resolvable?** — block a custom (build-service) deploy if not, with a link to
  settings (replaces all the empty-registry handling).
- **Multi-node swarm** custom build → registry **hard-required** (the cross-node version of the
  fixed bug; not fixable by `pull_policy`).
- **Deploy host vs env mode** — env = swarm but `Swarm: inactive` on the bound host → block early
  with a clear message instead of a cryptic deploy failure.
- **Build-only host** ∉ deploy-host pickers.

## 7. Transport details

- **Registry (primary, required for multi-node swarm):** build → push → pull. Layer dedup; works for
  compose, single-node swarm, and multi-node swarm uniformly.
- **`docker save | ssh | docker load` (secondary, single-node/compose only):** zero-infra,
  out-bound SSH only (works when the control plane is behind NAT). **Cannot** seed a multi-node
  swarm (only loads one node). Offered as a convenience when build ≠ deploy on a *single* daemon and
  no registry is desired — explicitly disabled for multi-node swarm. (May be deferred entirely; the
  registry covers every case.)
- **`pull_policy`:** a `compose up` concept; `docker stack deploy` ignores it. The `pull_policy:
  never` guard added in `17867b2` is compose-only and becomes vestigial once "none" is removed
  (see §9).

## 8. Reuse map

| Need | Reuse |
|---|---|
| Registry CRUD + creds + picker | Registries feature (P3b), `internal/registry`/registries store |
| Registry auth before push/pull | auto `docker login` (registry-auth-build-push) |
| Swarm node pull auth | `stack deploy --with-registry-auth` (already emitted) |
| Managed-registry TLS | Traefik + ACME at `registry.{base}` (domain-routing-model, acme-cert-lifecycle) |
| Managed-registry secret at rest | `internal/crypto` AES-256-GCM (host keys / write-back token) |
| Build/push/promote | `internal/builder` (build, push, promote, advancePointers) |
| Host inheritance + capability | per-env host binding (Phase 7 multi-host) → extend with build role |
| Image tag w/ registry prefix | `wsconfig.ImageTag` (feed it the effective registry) |

## 9. Cleanup (retire vestigial no-registry code)

Once "Local — none" is gone and a registry always resolves:
- Remove `pull_policy: never` emission (composegen `buildService`) and the `registry==""`
  skip-pull branch in `dockerops.update()`.
- Remove the "Local — no registry" dropdown option + handling.
- **Keep** the stale-pointer **resync** (advance.go) — pointers still lag after failed builds *with*
  a registry — but make its existence check **registry-aware** (a tag may be pushed but pruned
  locally; "exists" should mean "resolvable for deploy", not "in the local store").
- The vendor/composer template fix (laravel-template-vendor-skip) is independent and untouched.

## 10. Open / future (not v1)

- **Shared build server across multiple control planes** — registry as the common artifact store;
  each control plane registers the same build host. Needs cross-CP auth/isolation + builder cache
  pruning. v2.
- **Registry on the swarm / cloud for HA** — beyond the control-plane-hosted managed registry.
- **Incremental source sync** to build hosts (rsync/delta) — `PushDir` is a full tar today; only
  matters if remote builds are frequent and the context is large.
- **Per-env build host** (vs per-project) — only if a real need appears.
