# Rigger — Rig once. Deploy anywhere

> **Yes, it's called Rigger.** It's the deckhand who lashes your containers to the crane, double-checks every knot, and hoists them into prod without dropping one in the harbor. It remembers exactly which line went where, never fat-fingers a `docker run` at 2 a.m., and quietly judges you for deploying on a Friday. You bring the cargo — Rigger handles the heavy lifting, the rigging, and the part where everything stays afloat.

A Go-powered, self-hosted **PaaS for Docker & Docker Swarm** with a full web UI. Create a workspace, add projects, and build → deploy → route → back up → monitor them across dev/stage/prod — on one host or a fleet of remote hosts, all from one control plane. The entire runtime is a single ~15 MB Go binary (no Bash scripts) plus a thin host CLI wrapper.

---

## Table of Contents

**Getting started**
1. [Quick Install](#1-quick-install)
2. [Core Concepts](#2-core-concepts)
3. [Architecture](#3-architecture)
4. [Prerequisites](#4-prerequisites)
5. [Quick Start](#5-quick-start)

**Building & configuring**
6. [Creating a Project](#6-creating-a-project)
7. [Source-Built Stacks & Build Backends](#7-source-built-stacks--build-backends)
8. [Managed Databases & Services](#8-managed-databases--services)
9. [Environment Configuration](#9-environment-configuration)
10. [Settings Levels](#10-settings-levels)
11. [Pre-built Stack Templates](#11-pre-built-stack-templates)

**Deploying & operating**
12. [Command Reference](#12-command-reference)
13. [Deployment Strategies (Compose & Swarm)](#13-deployment-strategies-compose--swarm)
14. [Build, Version & Promote](#14-build-version--promote)
15. [Domains, TLS & Routing](#15-domains-tls--routing)
16. [App Exposure](#16-app-exposure)
17. [Multi-Host Support](#17-multi-host-support)
18. [Deployment Pipelines](#18-deployment-pipelines)
19. [Preview / PR Environments](#19-preview--pr-environments)
20. [Backup & Restore](#20-backup--restore)
21. [Secrets](#21-secrets)
22. [Maintenance Mode & Danger Zone](#22-maintenance-mode--danger-zone)

**Platform services**
23. [Alerting & Notifications](#23-alerting--notifications)
24. [Metrics & Monitoring](#24-metrics--monitoring)
25. [Housekeeping](#25-housekeeping)
26. [Self-Update](#26-self-update)
27. [REST API v1](#27-rest-api-v1)
28. [Proxy Service](#28-proxy-service)
29. [Git Providers & Registries](#29-git-providers--registries)
30. [Users, Roles & Auth](#30-users-roles--auth)

**Reference**
31. [Rigger UI Reference](#31-rigger-ui-reference)
32. [Directory & Config Layout](#32-directory--config-layout)
33. [Environment Variables & Volumes](#33-environment-variables--volumes)
34. [Maintenance Guide](#34-maintenance-guide)
35. [Troubleshooting](#35-troubleshooting)

---

## 1. Quick Install

One-line installer — detects your OS, installs dependencies, clones the repo, generates a JWT secret, and starts Rigger:

```bash
curl -sSL https://raw.githubusercontent.com/mansoor/rigger/main/install.sh | bash
```

**With overrides:**

```bash
curl -sSL https://raw.githubusercontent.com/mansoor/rigger/main/install.sh \
  | RIGGER_DIR=/opt/rigger RIGGER_PORT=9090 ACME_EMAIL=admin@example.com bash
```

| Variable | Default | Description |
|----------|---------|-------------|
| `RIGGER_DIR` | `~/rigger` | Where to clone the repo |
| `RIGGER_PORT` | `9999` | UI host port |
| `RIGGER_BRANCH` | `main` | Git branch to install |
| `ACME_EMAIL` | — | Let's Encrypt contact email (required for SSL) |
| `SKIP_DOCKER` | `0` | Set to `1` to skip the Docker installation check |

**Supported OS:** Ubuntu/Debian, RHEL/CentOS/AlmaLinux/Fedora, Arch, Alpine, macOS (Docker Desktop required).

After install, open `http://localhost:9999` — first visit prompts you to create an admin account.

**Manual install:**

```bash
git clone https://github.com/mansoor/rigger.git
cd rigger/src
cp .env.example .env
# Edit .env: set JWT_SECRET (openssl rand -hex 32) and ACME_EMAIL
docker network create traefik_net 2>/dev/null || true   # Swarm manager? use: docker network create -d overlay --attachable traefik_net
docker compose up --build -d
```

The published image is pulled from **GHCR** (`ghcr.io/mansoor/rigger`) — a plain `docker compose up -d` runs it without building; `--build` builds from source for contributors.

---

## 2. Core Concepts

Rigger has a three-level hierarchy:

```
Workspace  (scoping tier — a team / tenant / grouping)
  └─ Project  (the deployable unit — an app + its dependencies)
       └─ Environment  (dev · stage · prod · …)
```

- **Workspace** — a top-level grouping selected from the nav's workspace switcher. Has a short lowercase **key** + a free-form display **name**. Workspaces own members, hosts, registries, git providers, backup targets, notification channels, domains, and their own settings (which fall back to global defaults).
- **Project** — the thing you build, deploy, and operate (formerly called a "workspace" in older docs). Contains one or more environments. Also has a short **key** + display **name**.
- **Environment** — a named deployment tier of a project (`dev`, `stage`, `prod`, or anything you like), each with its own `.env`, generated `docker-compose.yml`, and data.
- **`resource_prefix`** — the immutable Docker resource prefix `{workspaceKey}_{projectKey}`. Every container/volume/network/stack an environment creates is named `{resource_prefix}_{env}_…`. It's fixed at creation so renames can't orphan running resources.
- **`config.json`** — the single source of truth for a project. Everything (stack, services, environments, managed deps, routes, domains, backup schedules) lives here. Edit it (UI or file), then **refresh** an env to regenerate its compose file and redeploy.

API/URL shape reflects the hierarchy: `/api/workspaces/{workspace}/projects/{name}/envs/{env}/…`.

**Design principles**

- **Config-driven.** `config.json` is authoritative; compose files are generated from it, never hand-maintained.
- **One Go binary, no Bash.** Compose generation, deploy (compose & swarm), bootstrap, `.env` generation, build/promote, backup/restore, and version management are all native Go. The image ships no shell scripts.
- **Self-contained projects.** Everything to operate a project lives under `workspaces/<ws>/projects/<name>/` — archivable, movable, restorable.
- **No host toolchain.** All build steps run inside Docker. Hosts need only `docker`; the `rigger` CLI wrapper needs only `curl`.
- **Bind mounts by default.** Volume data lives in `envs/<env>/volumes/` on the host — readable, backupable, portable.
- **One control plane, many hosts.** Projects (or individual environments) can run on remote hosts over SSH. Remotes need only Docker + SSH — no Rigger binary. See [Multi-Host Support](#17-multi-host-support).

---

## 3. Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  rigger container  (~15 MB Alpine, single Go binary, from GHCR)    │
│                                                                    │
│   Go backend  →  React SPA (embedded via embed.FS)                 │
│   REST + WebSocket + SSE API  +  thin `rigger` host CLI wrapper     │
│   Native Go runtime: compose-gen, deploy (compose+swarm),          │
│     bootstrap, env-gen, build/promote, backup/restore, version,    │
│     pipelines, previews, ACME issuer, alerts, metrics              │
│   templates/  ← Dockerfiles, scaffold starters, stack templates    │
└───────────────────────────────┬──────────────────────────────────┘
                                 │ generates / operates ↓
        ┌────────────────────────▼───────────────────────────┐
        │  Traefik edge  (control plane)                       │
        │   rigger-traefik  (v3.4, :80/:443)                   │
        │   rigger-socket-proxy  (Docker-API version shim)     │
        │   rigger-fallback  (catch-all "app starting" page)   │
        └────────────────────────┬───────────────────────────┘
                                 │  routes by Host/Path labels ↓
        ┌────────────────────────▼───────────────────────────┐
        │  workspaces/<ws>/projects/<name>/                    │
        │   config.json   ← single source of truth             │
        │   envs/dev|stage|prod/                               │
        │     .env, docker-compose.yml (generated), volumes/  │
        └──────────────────────────────────────────────────────┘
```

The control-plane compose stack (`src/docker-compose.yml`) runs four containers on the external `traefik_net` network:

- **`rigger`** — the Go server + embedded UI. Image `ghcr.io/mansoor/rigger` (also the tag a local `--build` produces).
- **`rigger-traefik`** — Traefik **v3.4**, ports 80/443. Its static config is **Rigger-owned** (written to a volume Rigger controls), so Proxy Service plugins (WAF/GeoIP) toggle from the UI without editing compose.
- **`rigger-socket-proxy`** — an nginx that rewrites Traefik's hardcoded Docker API version to one modern daemons accept, and keeps Traefik off the raw socket.
- **`rigger-fallback`** — a lowest-priority catch-all that serves a friendly auto-refreshing page when a host has no app router yet (app starting, stopped, or wrong address).

An optional Apprise API sidecar exists but isn't needed — non-email notifications use an embedded `apprise-go` library by default.

---

## 4. Prerequisites

| Tool | Required for | Install |
|------|-------------|---------|
| `docker` + Compose v2 plugin | Everything (build / deploy / runtime) | [docker.com](https://docs.docker.com/get-docker/) or `./install.sh` |
| `curl` | One-line installer + the `rigger` CLI wrapper | Ships with most systems |

> The Rigger engine runs entirely inside the container as a single Go binary — the host needs no `bash`, `jq`, `openssl`, `git`, or `ssh`. All build steps happen inside Docker; remote-host access uses a pure-Go SSH client.

> **Planning to use Docker Swarm?** Run `docker swarm init` **before** installing Rigger, so the shared `traefik_net` is created as an `overlay --attachable` network from the start (compose and swarm projects then coexist). Installing compose-first and enabling Swarm later works too, but needs a one-time network conversion (Rigger warns you at startup — see [Deployment Strategies](#13-deployment-strategies-compose--swarm)).

---

## 5. Quick Start

Most work happens in the **web UI** at `http://localhost:9999`. Two non-UI paths exist:

**Headless create** — the `rigger` binary's `init-workspace` subcommand takes a prepared `config.json` and scaffolds + bootstraps every environment:

```bash
docker exec rigger rigger init-workspace -name myapp -config /toolkit/workspaces/myapp.config.json
```

**Host CLI wrapper** — `rigger.sh` (+ `rigger.ps1` / `rigger.bat`) authenticate and call the REST API of a running server:

```bash
./rigger.sh login                 # stores a refresh session in ~/.rigger
./rigger.sh list                  # list projects
./rigger.sh myapp dev start       # run any allowlisted command
./rigger.sh myapp dev ps
```

---

## 6. Creating a Project

Projects are created through the **New Project wizard**. Step 2 offers **six stack types**:

| Type | What it does |
|------|--------------|
| **Pre-built template** | Deploy a curated stack (WordPress, Vaultwarden, Uptime Kuma, …) from the [template library](#11-pre-built-stack-templates). Searchable browser with Popular / Browse-all. |
| **Image stack / Docker Compose** | Deploy ready-made images (name, tag, ports, volumes), **or** paste/fetch and edit a `docker-compose.yml` — Rigger converts it to a project. `${VAR}` references resolve from `.env` at deploy time. |
| **Managed service hosting** | Provision databases/Redis/object-storage/search/metrics only, no app code — a place to host a shared DB (see [Managed Databases & Services](#8-managed-databases--services)). |
| **From a Git repository** | Scan a repo, review the detected service graph, and build from source ([onboarding](#git-source-import)). |
| **Start from a stack template** | No repo yet — pick a framework, get a runnable starter scaffolded into a fresh Git repo ([scaffolding](#blueprint-scaffolding)). |
| **Custom application** | Upload a source archive (`.zip`/`.tar.gz`), auto-detect the stack, and build. |

The wizard's 7 steps: **Project** (name, registry, default host) → **Stack** → **Environments** (name, domain, Traefik, SSL, deployment mode, host) → **Services** (ports/volumes/healthcheck/web-entry/managed deps) → **Backup** → **Review** → **Result** (live bootstrap terminal).

For automation, `rigger init-workspace -name <name> -config <config.json|->` performs the same creation headlessly. A `config.json` can also be produced from an existing image-stack project or a `docker-compose.yml` via **Tools → Template Manager**.

---

## 7. Source-Built Stacks & Build Backends

Rigger builds images from source using one of two backends, per build service.

### Frameworks (Dockerfile scaffolding)

When a stack is recognized, Rigger scaffolds a per-framework Dockerfile from `templates/dockerfiles/<id>/`. Ten framework templates ship today:

| Backend / language | Frontend |
|--------------------|----------|
| `laravel` (PHP-FPM + Composer, Nginx) · `nodejs` · `django` · `go` · `rails` · `spring` / `spring-gradle` (Java) · `dotnet` | `nextjs` (standalone) · `react` (Vite → Nginx) |

Each framework carries a **managed-dependency env contract** so a detected app wires up with no hand-mapping — e.g. Laravel gets discrete `DB_*` + `AWS_*`/`FILESYSTEM_DISK`; Node/Next/Django/Go get `DATABASE_URL`/`REDIS_URL`; Rails gets a `mysql2://` `DATABASE_URL`; Spring gets `SPRING_DATASOURCE_*` (JDBC); .NET gets `ConnectionStrings__*`.

Stack identification (`internal/detect`) never executes repo code — it reads the filesystem in signal order: existing `docker-compose.yml` → Dockerfile(s) (monorepo-aware) → language manifests → `Procfile` (workers) → dependency/env hints for managed deps.

### <a id="nixpacks"></a>Nixpacks (zero-Dockerfile build backend)

Any build service can opt into **Nixpacks** instead of a Dockerfile via **Build method: Nixpacks** on the service card. Nixpacks auto-detects the stack and builds without a Dockerfile — an escape hatch when the generic scaffold doesn't fit (native deps, monorepos, odd runtimes), and the fallback for languages Rigger doesn't template.

- **Auto-fallback:** when detection can't identify any framework, the scanner seeds a single web-routed `app` service that builds with Nixpacks — so unrecognized languages are still deployable.
- Nixpacks produces a normal OCI image, so env-gen, managed deps, routing, image pointers, rollback, and pipelines all operate unchanged.
- The Nixpacks CLI is **bundled in the Rigger image** for local builds; for remote build hosts, install it with one click (Settings → Remote Hosts → Install Nixpacks). Shipped v0.1.34.

### <a id="blueprint-scaffolding"></a>Start from a stack template (scaffolding)

Picking **Start from a stack template** with the scaffold option generates a minimal runnable starter app for a framework, commits it, and pushes it to a fresh Git repo you specify (or offers a ZIP download). Starters ship for **django, go, laravel, nodejs, react**. After the initial push, every push rebuilds and deploys.

### App onboarding

- **<a id="git-source-import"></a>Git-source import** — scan a repo (with a Git provider for private repos). Handles monorepo/nested subdirs, multi-file compose overlays, a **managed-dependency offer** (use a Rigger-managed DB vs keep the repo's own container), profile-gated services, bundled DB-seed auto-import, and per-service pre-deploy commands. Cookiecutter/Copier/Yeoman *template* repos are detected and blocked (they're not apps).
- **Upload source** — drop a `.zip`/`.tar.gz`; the archive is safely extracted (hardened against zip-slip/symlink/bomb attacks, never executed), detected, and reviewed through the same flow. A Dockerfile is scaffolded if the archive has none.

---

## 8. Managed Databases & Services

Rigger provisions managed dependencies from a **catalog** (`internal/databases`, `GET /api/databases`). They are **project-level** — shared across a project's environments, with a per-env legacy fallback — and configured in the wizard **Services** step and Edit Project → **Services**.

### Databases

One **primary database** per project:

| Engine | Default | Port | Notes |
|--------|---------|------|-------|
| **PostgreSQL** | `15-alpine` | 5432 | Schemas + users management; `POSTGRES_*` |
| **MySQL** | `8.0` | 3306 | Schemas + users; `MYSQL_*` |
| **MariaDB** | `11` | 3306 | Reuses the `MYSQL_*` contract |
| **MongoDB** | `7` | 27017 | Document store — connection info only (no SQL schema/user management); `MONGO_*` |

Plus **auxiliary engines** that run *alongside* a primary DB (info-only, internal):

| Engine | Category | Port | Notes |
|--------|----------|------|-------|
| **OpenSearch** | search | 9200 | HTTPS + security plugin; needs host `vm.max_map_count=262144`; `OPENSEARCH_*` |
| **VictoriaMetrics** | tsdb | 8428 | Prometheus-compatible TSDB; `VICTORIA_*` |
| **RabbitMQ** | queue | 5672 | AMQP broker; `-management` image serves a web UI on 15672; `RABBITMQ_*` + `AMQP_URL` |

The managed-dep env contract (`POSTGRES_*`/`MYSQL_*`/`MONGO_*`/aux + a baseline `DATABASE_URL`) is emitted into each service's `.env`; framework blueprints translate it to framework-native keys. Passwords are generated once and **pinned** so they survive regens (see [Secrets](#21-secrets)).

Only the **external port** (`DB_EXTERNAL_PORT`) is per-env — the dev-on/prod-off host-port exposure pattern. Everything else is project-level.

### Object storage, Redis & cache

- **Object storage** — `local` (a persistent volume) and/or **MinIO** (managed S3). MinIO emits the app-facing S3 contract (`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_BUCKET`/`AWS_ENDPOINT`/`AWS_DEFAULT_REGION`/`AWS_USE_PATH_STYLE_ENDPOINT`) and an `mc-init` one-shot that creates the per-env bucket. *(The old Garage backend is retired.)*
- **Redis** — a project-level toggle; emits `REDIS_HOST`/`REDIS_PORT`/`REDIS_URL`.

### Consoles & per-env sidecars

Several helper UIs are per-env tri-state toggles (on / off / inherit the project default):

- **Adminer** (SQL) / **mongo-express** (Mongo) — the `web_sql` toggle; Adminer supports one-click **auto-login** (HMAC-signed).
- **MinIO console** — a web console for the object store.
- **Mailpit** — a test-SMTP catch-all; when on, the app's mail is wired to `mailpit:1025` and the inbox UI is served.

The **Service Console** modal (env card) gives a tabbed view over every enabled service — database, Redis, S3, storage, Mailpit, search, TSDB, queue — with a single operator-gated "reveal credentials" toggle and open-UI links.

### Database users & schemas

**Manage Database** lists and creates schemas and dedicated per-schema DB **users** (passwords AES-256-GCM encrypted at rest), running the DB client **inside the container** via `docker exec` (no exposed port). It's list-and-create only — deliberately not a free-form SQL console. SQL engines only (Mongo/aux don't expose user management).

> **Template-bundled DBs vs managed deps.** Pre-built stack templates bundle their own version-pinned database service (self-contained; backups cover it). That's separate from the managed-dependency catalog above — Rigger won't inject a managed DB into a template that ships its own.

---

## 9. Environment Configuration

All settings live in `config.json`. Edit it (UI editor or file), then `refresh <env>`.

### Per-environment block

```json
"environments": {
  "prod": {
    "domain":          "example.com",
    "http_port":       80,
    "traefik_enabled": true,
    "traefik_network": "traefik_net",
    "ssl_enabled":     true,
    "deployment":      "compose",
    "replicas":        { "backend": 2, "frontend": 1 }
  }
}
```

**`http_port`** matters only for custom stacks with Traefik **off** (the host port Nginx binds). Under Traefik it's not needed (see [keep-host-ports](#keep-host-ports)).

### Service fields (image stacks)

```json
"images": [
  {
    "name": "app", "image": "ghost", "tag": "5-alpine",
    "port": 2368, "host_port": "${GHOST_PORT}", "link_ports": ["${GHOST_PORT}"],
    "volumes": ["./volumes/ghost_data:/var/lib/ghost/content"],
    "depends_on": ["db"], "extra_ports": [],
    "healthcheck": "curl -sf http://localhost:2368/ -o /dev/null || exit 1",
    "healthcheck_config": { "interval": "30s", "timeout": "10s", "retries": "3", "start_period": "60s" },
    "restart": "unless-stopped",
    "env_vars": { "url": "${GHOST_URL}" },
    "extra_compose": ""
  }
]
```

- **`link_ports`** — which host ports appear as clickable links on the env card (defaults to `host_port`).
- **`extra_compose`** — raw YAML appended verbatim to the service block (mem_limit, cpus, logging, …), applied to all envs. Per-env overrides go under `environments.<env>.service_overrides.<service>.extra_compose`, applied after the base.
- **Custom-app processes** — worker/scheduler services that reuse the app image with a command and no ports.
- **Pre-deploy (migrate) command** — a build service's `PreDeploy` synthesizes a `{svc}-migrate` one-shot that runs before the app (gated via `depends_on: service_completed_successfully`). Compose-only (Swarm ignores the condition).

### Version block (custom stacks)

```json
"versions": { "postgres": "15-alpine", "mysql": "8.0", "redis": "7-alpine",
              "nginx": "1.25-alpine", "node": "20-alpine", "php": "8.3-fpm-alpine" }
```

### Secret env vars

Any env var can be **flagged as a secret** in the env-var editor. On Compose it stays plaintext in `.env`; on Swarm it becomes a native Docker secret (see [Secrets](#21-secrets)). Placeholder secrets (`CHANGE_ME`, `CHANGEME`, `YOUR_…`) in a template are auto-replaced with generated values at create time.

> **`NODE_ENV` is always `production`** for deployed containers, regardless of the Rigger env tier — the tier means "which data/config", not the Node runtime mode. A pruned production image with `NODE_ENV=development` crash-loops (e.g. Fastify → pino-pretty). Override via project env vars if you truly need dev mode.

---

## 10. Settings Levels

Settings resolve across four tiers, most-specific first, via `Effective*` fallback:

```
User  →  Project (config.json)  →  Workspace  →  Global (admin)
```

- **Global** (Admin → Settings) — ACME email, base domain, App host/IP, auto-URL mode, DNS provider/token, confirm-destructive defaults, key-length bounds, **password policy**, default appearance.
- **Workspace** (Manage Workspace) — overrides many global keys (domain, ACME email, auto-URL, DNS token), plus workspace defaults (registry/host/backup/build target), `keep_host_ports_under_traefik`, env tier names, and the [wipe-allowed env list](#22-maintenance-mode--danger-zone).
- **Project** — everything in `config.json` (stack, services, managed deps, per-env overrides).
- **User** — per-account appearance and confirm-destructive preference (Profile), editable only where the workspace/global tier allows an override.

---

## 11. Pre-built Stack Templates

**37 templates** ship in `templates/stacks/` (a JSON file each):

```
activepieces · actual-budget · adguard-home · authentik · bookstack · code-server
cyberchef · dockhand · flowise · ghost · gitea · grafana · homepage · immich
it-tools · mealie · metabase · minio · n8n · nextcloud · nginx-proxy-manager
nocodb · pairdrop · paperless-ngx · plausible · pocketbase · privatebin · sftpgo
sonarqube · syncthing · teslamate · transmission · umami · uptime-kuma
vaultwarden · wg-easy · wordpress
```

### How templates work

Each template declares images, ports, volumes, healthchecks, `default_env_vars`, an optional `files` map (seed files), and — for multi-service stacks — a `web_routed` flag on the service the domain routes to. The wizard discovers templates by globbing `templates/stacks/*.json` (no registration).

**Secrets.** A placeholder secret in `default_env_vars` is auto-replaced with a generated value at create time, for any secret-shaped key (`*PASSWORD*`, `*SECRET*`, `*TOKEN*`, `*KEY*`, `*SALT*`). The resolved value is written to `.env` and **pinned** in `config.json` so it survives regens and matches the initialized data volume (see [Secrets](#21-secrets)). Non-secret placeholders (ports, hosts) are left for you to fill.

**Web entry.** In a multi-service stack, exactly one service is the web entry (the domain routes to it) — mark it `"web_routed": true`. If none is marked, Rigger promotes the first non-datastore service (skipping postgres/mysql/mariadb/mongo/redis/clickhouse/…). The wizard's Services step lets you pick it directly.

**Static config files (seed files).** Some apps bind-mount a *config file* (e.g. Prometheus's `prometheus.yml`). A bind mount whose source doesn't exist is silently created by Docker as a directory, which breaks the file mount — so ship it in a top-level `files` map (path → string or line-array), persisted into `config.json` and written into the env dir only if absent (your edits are never clobbered). Author them in **Tools → Template Manager → Static files**; Validate warns when a service bind-mounts a config file with no `files` entry.

**Template Manager (Tools).** Create/edit/validate/save templates in the browser: convert a `docker-compose.yml`, generate from an existing image-stack project (secrets masked server-side), or upload a template JSON. **Save as template** writes to `templates/stacks/<name>.json` live (no rebuild).

---

## 12. Command Reference

Commands issue from the **web UI**, the **`rigger` CLI wrapper**, or the **REST action endpoint** — all hit the same Go runtime.

The authoritative REST call is:

```
POST /api/workspaces/{ws}/projects/{name}/envs/{env}/action
     { "command": "...", "extra": [...] }
```

The `rigger` host wrapper (`rigger.sh` / `.ps1` / `.bat`) is a thin convenience over that endpoint — e.g. `./rigger.sh <project> <env> <command>` (the wrapper predates the workspace tier and refers to a project as a "workspace").

**Allowlisted commands** (strict allowlist in `bridge.go`, each routed to a native Go op — no shell):

```
Lifecycle:   start · stop · down · restart · update · refresh · regen
Build:       build · promote · version
Ops:         ps · logs · backup · restore · migrate · init · test · script
```

- **`refresh`** regenerates the compose file and redeploys. **`regen`** regenerates the compose file *without* bringing the stack up (used to fix a stopped env's routing while keeping it stopped).
- **`build`** / **`promote`** apply to custom (source-built) stacks; `promote <dst_env>` retags an existing image to the next env with no rebuild.
- Container shell access (`> bash`) is available from the UI env-card terminal, not as a CLI command.

---

## 13. Deployment Strategies (Compose & Swarm)

### Docker Compose (default)

```json
"deployment": "compose"
```

Best for dev and single-host stage/prod.

### Docker Swarm

```json
"deployment": "swarm",
"replicas": { "backend": 2, "frontend": 1 }
```

Enables replica scaling via `docker stack deploy --with-registry-auth`. Per-env swarm settings — replica counts, restart policy, update/rollback config, placement — are emitted into compose `deploy:` blocks. Swarm deploys are Traefik-hardened (providers.swarm, `deploy.labels`, overlay `traefik_net`). Requires `docker swarm init`.

The shared `traefik_net` must be **overlay/attachable** for swarm stacks. Easiest path is `docker swarm init` before installing. To convert an existing compose install:

```bash
docker network rm traefik_net && docker network create -d overlay --attachable traefik_net
```

**Switching modes** on an existing env auto-tears-down the old deployment (while the old config still resolves it), so its bridge network/containers don't collide with the new overlay of the same name — then you redeploy cleanly.

---

## 14. Build, Version & Promote

### Build

```bash
rigger myapp stage build --bump minor --push
```

- **Image pointers.** After a build, each built service's `.env` `{SVC}_IMAGE` pointer advances to the new tag **only if it was tracking** (empty or equal to the prior tag). A pointer set to anything else is a deliberate **pin** (rollback / hand-edit) and left alone (a pin whose image is missing is re-synced). The compose file is regenerated so the baked default matches.
- **Rebuild policy.** `if-changed` skips a build when the source signature (`.build-ref`) is unchanged (source only — Dockerfile/build-arg changes aren't detected); `force`/`--no-cache` always rebuilds. Pipeline build stages expose this via a `When` field.
- **Build args** expand `${ENV}`, `${VERSION}`, `${ROUTE_URL}` (warns on any leftover `${…}`).

### Version

| Command | Before → After |
|---------|----------------|
| `version bump` | `1.2.3-build.41` → `1.2.3-build.42` |
| `version bump patch` | → `1.2.4-build.0` |
| `version bump minor` | → `1.3.0-build.0` |
| `version bump major` | → `2.0.0-build.0` |
| `version set 3.0.0-build.0` | → exact `M.m.p-build.N` |

Image tags follow `registry/project-service:2.1.0-build.47-stage`.

### Promote

```bash
rigger myapp stage promote prod
```

Retags the exact stage image to prod (pull → tag → push, **no rebuild**) then deploys — you ship byte-for-byte what you validated. `--dry-run` supported.

**Recommended pipeline:** `dev → build stage --push → validate → promote stage→prod`.

### Image update detection

For image stacks, Rigger checks the registry hourly:

- **Update available** = a newer **digest for the same tag** (an amber "↑ update available" badge; one-click Update clears it).
- **Newer stable tag** = a separate, non-actionable signal (its own alert).
- **"? digest unknown"** (grey) = the local image has no `RepoDigest` (pulled via compose without an explicit `docker pull`, or built locally) so the comparison is indeterminate.

---

## 15. Domains, TLS & Routing

### The URL model (three-layer)

An env's URL resolves per project/workspace:

1. **Base domain** — workspace `domain` override → global `apps_base_domain`. When set, an env gets `{ws}-{project}-{env}.{base}` over **HTTPS (Let's Encrypt)**.
2. **Magic-DNS fallback** — when no base domain is set, `auto_url_mode` = `sslip` | `nip` | `traefikme` gives `{ws}-{project}-{env}.<host>.sslip.io` (etc.) over HTTP, where `<host>` = the **App host/IP** (see below). `localhost` mode → `*.localhost`; `off` disables.
3. **`localhost`** — the last-resort default.

`EnvRouteURL` builds the exact string the UI shows on the env card.

### App host / IP (Settings → General)

The **App host** is the address users reach this host's apps at — it builds the direct `host:port` links and the magic-DNS host for **locally-deployed** envs (remote-host envs use their own host's address).

- Seeded at install from the detected host IP (`RIGGER_APP_HOST`). Update it only if the host IP changes.
- The server runs in a container and can't detect the host LAN IP itself; **Detect** works on native Linux, but on Docker Desktop returns the VM IP — enter it manually there. **Use {hostname}** fills the address you reached Rigger by.
- **Must be an IP for sslip/nip.** A hostname won't resolve; for a base domain the App host is irrelevant.
- **Changing it warns about affected apps.** The magic-DNS host is baked into each running app's Traefik labels, so an IP change updates displayed URLs but running containers keep the old host until redeployed. As you edit the field (or a remote host's address), Rigger shows which envs' URLs would break and offers to **refresh & redeploy** them on save — state-aware (a running env is redeployed in place; a stopped env only has its compose regenerated). Reverting the value dismisses the warning.

### ACME / certificates

Traefik carries two resolvers: **`letsencrypt`** (HTTP-01, per-host, default) and **`dns`** (DNS-01 via Cloudflare). When `apps_dns_provider = cloudflare` is set, base-domain envs request one **`*.{base}` wildcard** cert; otherwise each host gets a per-host HTTP-01 cert.

The ACME **email hierarchy** is per-env → workspace → global. Per-env/workspace overrides (and per-workspace wildcards under a workspace's own Cloudflare token) are issued **out-of-band** by a one-shot `lego` container that writes a cert Traefik hot-reloads (no restart), with a 12-hour renewer. The Cloudflare token is set in Settings → General (materialized to a file Traefik reads).

> *These override/wildcard cert paths and custom domains are implemented but not yet fully live-verified end-to-end.*

### Custom domains (per env)

Add external domains to an env (in addition to the auto subdomain). Verify ownership by **TXT** (`_rigger-challenge.<domain>`), **CNAME** (→ the env's auto subdomain), or an **HTTP file** — then Rigger issues a per-host Let's Encrypt cert and adds an HTTPS router (+ http→https redirect). One domain can be marked **primary** (canonical, drives `APP_URL`/Open-app).

### Routing table (per project)

The **Routing** tab lets a project map **path prefixes** or **subdomains** on the env domain to specific services (`Config.Routes[]`). With any routes defined, composegen switches from a single `Host()` label to route-driven labels: path routes become `Host(domain) && PathPrefix(/x)`, with prefix rewrite (strip/add) for **version aliasing**. The catch-all `/` route owns the wildcard cert + custom domains. *(Weight/canary and rate-limit columns are planned, not built.)*

### <a id="keep-host-ports"></a>Traefik vs direct ports

With Traefik **off**, a custom stack's Nginx binds `http_port`; an image stack maps each `host_port` directly. With Traefik **on**, routing is by domain label with no host-port binding. Under Traefik the web-entry's primary host port is redundant, so it's **stripped by default** (`keep_host_ports_under_traefik`, per-env `keep`/`strip` override); `extra_ports` and non-web services always publish.

---

## 16. App Exposure

Per environment, `expose_mode` controls how an app is reachable, and `auth_gate` adds an access gate (both override → project default → baseline):

| `expose_mode` | Behavior |
|---------------|----------|
| **`traefik`** (default) | Public Traefik router (the URL model above). |
| **`cloudflare_tunnel`** | No public router — a `cloudflared` sidecar (`TUNNEL_TOKEN=${CF_TUNNEL_TOKEN}`) makes an outbound-only tunnel. Swarm-native; good for exposing an app without opening the firewall. |
| **`none`** | Internal only; optional explicit `host_port` and optional attach to an external Docker network. |

| `auth_gate` | Behavior |
|-------------|----------|
| **`none`** (default) | No gate. |
| **`basic`** | Traefik basic-auth from `${APP_AUTH_USERS}` (htpasswd in `.env`). |
| `forward_auth` | *Reserved but not implemented — treat as planned.* |

Synthesized admin sidecars (Adminer/consoles) have a separate basic-auth knob (`protect_admin_uis` → `${ADMIN_UI_USERS}`).

---

## 17. Multi-Host Support

Rigger runs projects — or individual environments — on **remote Docker hosts** while you manage everything from one control plane. Keep `dev` local, put `stage`/`prod` on beefier servers, all from one UI.

### How it works

An **SSH-exec + file-sync** model (pure-Go SSH — no `ssh` client in the image):

1. Files (compose, `.env`, build context) are generated **locally** and pushed to the host via **tar-over-SSH**.
2. `docker` / `docker compose` run **on the remote host** over SSH, so bind mounts and build contexts resolve there.
3. A remote host needs only **Docker + an SSH server** — no Rigger binary, no agent.

Lifecycle commands are host-transparent: Rigger resolves each env's host and runs the command in the right place. On first deploy, Rigger provisions a **Traefik edge** (the same trio) + `traefik_net` on the remote host over SSH (idempotent).

### Registering a host (Manage Workspace / Admin → Remote Hosts)

| Field | Notes |
|-------|-------|
| Display name / Address / SSH user / port | Connection details |
| SSH private key | Paste a passphrase-less key, **or** toggle **Use Rigger-managed key** (Rigger holds one identity; install its public key with the shown one-liner — the private key never transits your browser) |
| Remote workspaces directory | Absolute path where workspaces live/are pushed (e.g. `/opt/rigger/workspaces`); blank = the `REMOTE_WORKSPACES_DIR` default |

**Test** connects + runs `docker version` (captures the host-key fingerprint TOFU). **Health** shows the host's Docker/system stats. **Scan / Import** lists workspaces already on the host and imports selected ones.

### Reachability: public vs private

Each host is flagged for how it's reached — this decides how its apps get a public URL:

- **Public (direct)** — the host is its own front door (own public IP/DNS). Its apps route directly at the host, which issues its own HTTP-01 certs. Optional `public_address` override. When a base domain + Cloudflare token + auto-manage-DNS are set, Rigger upserts a **DNS-only A record** `{label}.{base} → host IP` so the name resolves straight to it.
- **Private (behind gateway)** — only the control plane is exposed; it fronts the host's apps. Rigger writes a **gateway forward-route** on the control plane that proxies `Host(name) → http://{host}:80` (the host's edge), serving the workspace wildcard by SNI. Use for LAN/homelab boxes with no public IP.

Per-env host binding: each environment is either local or bound to one remote host (set on the Project step's default-host selector or Edit Project → Environment hosts). Different envs of one project can live on different hosts.

### Moving / migrating

- **Per environment** or **whole project** (all envs at once, when they share a host). A deployed migration backs up the source → ships files (incl. `.env`) → repoints → restores on the target. **The source copy is stopped but its data is left intact** — wipe it from [Housekeeping → Migration Leftovers](#25-housekeeping) before decommissioning the host.
- Both moves warn about downtime + leftover data, run in the background, and notify on completion.

### Current limitations

- `build` / `promote` for remote-bound projects need the build context + a remote registry (build/promote locally, or migrate after building).
- Editing a remote env's variables writes the **local** cache; it doesn't push to the host yet.
- Backup S3/SFTP sync currently excludes remote-host envs (planned).

---

## 18. Deployment Pipelines

Pipelines (Edit Project → **Pipelines** tab) are ordered stage lists — a lightweight CI/CD for a project. Viewing is viewer+, managing/running is operator+.

- **Stage types:** deploy · refresh · update · build · restart · backup · test · script · version · push (promote) · **gate**.
- **Runs** launch in the background; the run modal **polls** live per-stage progress/logs (no socket — you can close and reopen the window). Log viewer has search, per-stage jump anchors, line numbers, wrap, download. **History** keeps the last runs with per-stage status chips; runs can be cancelled.
- **Promote gate** — a `gate` stage pauses the run (`awaiting`) for manual **Approve / Reject**.
- **Webhooks** — a per-pipeline inbound trigger (hashed URL token + optional HMAC secret) runs the pipeline on push.
- **Notifications** — per-pipeline Started / Succeeded / Failed events fan out to chosen notification channels (failure messages name the failing stage + a short error tail).
- **Generate from environments** — a one-click generator drafts a release (build↑bump+push → deploy → promote chain, optional gate) or hotfix pipeline from the env topology + workspace tier order; the draft opens in the editor (nothing auto-saved). Env tier order is set in Manage Workspace and reorderable per project.

### Type-aware rollback

A separate env-level feature (env card → Rollback): each deploy snapshots the resolved image refs to `deploy_history`. **Custom stacks** roll back by pinning a prior entry's images in `.env` and redeploying; **image stacks** (no per-env image override) roll back via a Backup restore instead.

---

## 19. Preview / PR Environments

Opt-in per project (Edit Project → **Preview Environments**). A signed webhook from GitHub/Gitea (GitLab not supported in v1) spins up an ephemeral **`pr{n}`** environment cloned from a chosen template env when a pull request opens, redeploys it on each push, and tears it down when the PR closes.

- **Config:** enabled, template env, provider, branch filter, fork policy (off/approved/on), max concurrent, TTL hours, DB strategy (isolated-empty / isolated-seed / clone-from), auth-protect, PR write-back.
- **Write-back** (optional) posts a commit status + comment on the PR (needs an encrypted token).
- **Reaper** — a 30-minute safety net tears down previews past their TTL (which slides on each deploy; TTL 0 = live until the PR closes) in case a "closed" webhook is missed.

---

## 20. Backup & Restore

### Per-env snapshots

```bash
rigger myapp prod backup        # database + all volumes
rigger myapp prod backup db     # database only
rigger myapp prod backup files  # volumes only
rigger myapp prod restore 2026-06-01_14-30-00
```

Snapshots write to `backups/{env}/{timestamp}/` with a manifest. SQL engines get a logical dump (gzipped, run **inside** the DB container — no host client needed); non-SQL engines (Mongo/OpenSearch/VictoriaMetrics) and file volumes are archived at the volume level. `restore` does stop → restore DB → restore volumes → start.

- **Backup targets** (Manage Workspace) — S3/object-storage and SFTP destinations, with a connectivity **Test**.
- **Sync** a snapshot to a target (per-snapshot button; auto-upload after archive backups). *(Not yet supported for remote-host envs.)*
- **Schedules** — per-env, interval-based (every 2/4/6/12h, daily, weekly). The scheduler ticks every 30 minutes and runs a schedule when its interval has elapsed (state survives restarts). Count-based retention keeps the newest N per schedule.
- **Health** — the dashboard shows per-env backup coverage (last-backup age, count, sync state) with a verdict (current / stale / never / disabled).
- **Restore dry-run** — a non-destructive **Verify** checks every snapshot file is present, non-empty, and gzip-intact before you commit.

### Project backups & snapshots (Tools → Backup & Restore)

Two portable, whole-project artifacts:

- **`.rps`** (Rigger Project **Snapshot**) — config only: `config.json` + each env's `.env` + a portable DB bundle. Fast; rollback overwrites config in place (data untouched) then regenerates compose. Legacy `.rws` still imports.
- **`.rpb`** (Rigger Project **Backup**) — the full project: the whole project dir + the latest per-env data snapshot + the portable DB bundle. Legacy `.rwb`/`.tar.gz` still import.

Both embed **`rigger-project-db.json`** — pipelines (+webhooks), alert rules, custom domains, maintenance schedules, host bindings (by host name), and the project build host. It **excludes** deploy/rollback history, access grants, raw DB-user secrets, and logs. Re-imported on restore/rollback.

*(The Proxy Service has its own `.rpx` export/import for routes + access lists.)*

### Migrate vs Copy environment

- **Migrate data** (Tools → Migrate data) — moves **data** between two existing envs: safety-backup the target → back up the source → restore the source snapshot into the target (source read-only; target overwritten). Operator role + typed target-name confirmation; async.
- **Copy environment** (Edit Project → Environments) — a **config-only** clone (env block + config files + fresh `.env`, domain blanked, optional secret regen) with **fresh empty volumes — no data copied**. (Data-copy is a planned Phase 2.)

---

## 21. Secrets

Two distinct mechanisms:

### Flagged secrets (Compose plaintext / Swarm-native)

Env vars you flag as secrets are stored plainly in `.env` for **Compose** deployments, and as **native Docker Swarm secrets** for **Swarm** deployments (mounted at `/run/secrets/<KEY>`; DBs use the `<KEY>_FILE` convention). Rigger runs no custom crypto — Swarm encrypts secret values at rest in its Raft log. Swarm secrets are immutable, so **rotation** creates a new version, redeploys, and removes the old one. A secret-event audit trail records the key + action only, never the value. Compose deployments are byte-for-byte unchanged.

### Managed-secret pinning (drift protection)

Rigger's own auto-generated secrets (DB/app/MinIO passwords) are **pinned** in `config.json`'s authoritative `secrets` map — captured on first `.env` generation **and re-captured on every deploy**. The pin is the record of what the data volume was initialized with, so it **wins over `.env`**: if the live `.env` ever drifts (rerolled or hand-edited), a regen heals it back to the pinned value instead of locking the app out of an already-initialized volume (the classic Postgres `P1000`). Remote-bound envs pin from the host's authoritative `.env`. An existing pin is never overwritten; there's no UI — it's an internal durability mechanism.

---

## 22. Maintenance Mode & Danger Zone

### Maintenance mode

Per-env (env card → 🛠️), Rigger serves a Traefik **503 maintenance page** (ad-hoc or scheduled). Because the router points at the Rigger UI itself, it **works even when the env is stopped**. Live-verified.

### Danger Zone (Edit Project → Danger Zone)

Irreversible actions, **workspace-admin (or super-admin) only**, each gated by a copy-paste acknowledgement sentence **plus your password** (a wrong password just re-prompts — it never logs you out):

- **Wipe application data** — resets **one environment** to empty: removes its named volumes *and* clears its bind-mount data directories (keeping `docker-compose.yml`/`.env`, so settings and secrets are preserved), then redeploys it fresh. Offered **only** for environments a workspace admin allow-lists under **Manage Workspace → Environments → "Environments allowed to wipe data"** (keep production off the list and it's never wipeable). Runs as an async background job with live progress — safe to leave the page. Also a clean recovery path for a drifted managed-DB password (it re-initialises the volume against the current `.env`). Confirm: `Wipe data for Project: <name> Environment: <env>`.
- **Delete project** — permanently removes the project directory (config, env files, backups). Confirm: `Delete Project: <name>`. Running containers aren't stopped automatically.

---

## 23. Alerting & Notifications

An **alert rules engine** (Phase 6) re-evaluates every enabled rule on a 60-second tick, with an open/resolve state machine (auto-resolves when the condition lifts) and a per-rule cooldown (default 15 min). **12 rule types:**

| Category | Rules |
|----------|-------|
| Container | `container_down`, `container_unhealthy`, `stack_partial`, `container_oom_killed`, `restart_count` (threshold) |
| Resources | `cpu_above_pct` / `memory_above_pct` (per-project aggregate), `disk_above_pct` (host-global, admin-managed) |
| Backups | `backup_failed`, `backup_stale` (hours) |
| Images | `image_update_available`, `image_version_available` |

Severities are info/warning/critical.

**Inbox** — fired/resolved events fan out live over SSE to the **🔔 bell** (unread badge) and a slide-out inbox (dismiss / dismiss-all); the dashboard shows per-project alert dots.

**Notification channels** — two types: **email** (direct SMTP) and **apprise** (one or more Apprise URLs covering Slack/Discord/Telegram/webhook/etc.; delivered by an embedded `apprise-go` library, or an Apprise API sidecar if `APPRISE_URL` is set). Channels are global (with per-workspace grants) or workspace-private. Alerts and channels are configured in Admin → Settings (global) and Manage Workspace → Notifications (per workspace). Pipeline and migration events also route to channels.

---

## 24. Metrics & Monitoring

A collector samples each project/environment every **`METRICS_INTERVAL_SECONDS`** (default 30) — CPU %, memory bytes, disk bytes, and net rx/tx — aggregated across the env's containers and written to SQLite.

- **Tiered downsample:** full resolution ≤ 24h, thinned to 1 sample/minute for 24–120h, 1 sample/5 min beyond; the downsample+prune job runs every 6h.
- **Retention:** hard prune at **90 days**.
- **Optional long-term history:** dual-write to a managed VictoriaMetrics sidecar (Admin toggle) exposes `rigger_cpu_pct` / `rigger_memory_bytes` / `rigger_disk_bytes` / `rigger_net_rx_bytes` / `rigger_net_tx_bytes` for Grafana etc.
- **UI:** env cards render CPU / memory / disk / network sparkline tiles; the dashboard aggregates live usage across the control plane and every remote host with workloads.

---

## 25. Housekeeping

The Housekeeping page (admin) has four tabs:

- **Dashboard** — Docker storage breakdown, a health verdict (>10 GB reclaimable = critical, >2 GB = cleanup-advised), and safe one-click actions (prune networks, prune dangling images).
- **Safety Center** — approval-gated destructive cleanup: unused/dangling images, stopped containers (type `PRUNE`), volumes (3-second hold-to-authorize), build cache (slide-to-unlock), old-kernel cleanup. Guards exclude Rigger-managed stacks and compose-labelled volumes, and lock the active/previous kernels.
- **Migration Leftovers** — after an env migration, wipe the containers/volumes/files (incl. `.env` secrets) left on the source host, with a permanent-wipe confirmation.
- **Automation & Logs** — a daily **03:00 UTC** cron runs two safe tasks (prune-networks, prune-dangling-images), plus manual host-OS tools (APT clean, journal vacuum, `/tmp` cleanup — require privileged/`pid: host`) and a task-history table.

> Host-OS actions assume a **Linux host** (`nsenter`/`apt`/`journalctl`); the Docker prune actions work anywhere Docker runs.

---

## 26. Self-Update

Rigger publishes images to **GHCR** (`ghcr.io/mansoor/rigger`), tagged by release (`git tag vX.Y.Z`). Admin → Settings → **Updates**:

- **Rigger self-update** — Check for updates (compares GitHub Releases `latest` to the running version, with changelog), one-click **Update to vX.Y.Z** (a detached helper does `compose pull && up -d` after checking out the tag and rewriting `RIGGER_IMAGE_TAG`), and **Roll back** to the previous tag. One-click apply needs `RIGGER_HOST_DIR` set (done by the installer); otherwise update manually.
- **Docker Engine updates** — shows the Docker version for the local daemon and each registered host vs the latest release, with an in-place over-SSH **Update Docker** button (Linux + sudo). Nixpacks is shown per host as a version line (installed from the Remote Hosts tab).

---

## 27. REST API v1

An external, versioned API at `/api/v1`, authenticated by **API keys** (`rgk_…`, only the hash stored, shown once at create). Keys have granular **operation scopes** (grouped Read / Operate / Pipeline), an optional **rate limit** (per-key, per-project, 60s window; 429 + `Retry-After`), and **project access** (`all` or a specific list; keys can be workspace-confined). Manage them in Admin → API Keys (global) or Manage Workspace → API Keys (workspace-scoped).

Endpoints (Bearer `rgk_…` or `X-API-Key`):

```
GET  /api/v1/openapi.json                                        # OpenAPI spec (public)
GET  /api/v1/docs                                                # Redoc docs page (public)
GET  /api/v1/workspaces/{ws}/projects                            # list projects
GET  .../projects/{project}/envs/{env}/services                  # list services
GET  .../services/{service}/logs?tail=N                          # log snapshot (default 200, cap 2000)
POST .../envs/{env}/actions/{action}                             # start|stop|restart|refresh|inactivate|backup
GET  .../projects/{project}/pipelines                            # list pipelines
POST .../pipelines/{id}/runs                                     # trigger a run
GET  .../pipelines/{id}/runs/{runId}                             # run status + stage logs
POST .../pipelines/{id}/runs/{runId}/cancel                      # cancel a run
```

The OpenAPI spec is generated from the scope catalog, so the docs can't drift from what the keys allow.

---

## 28. Proxy Service

A standalone, NPM-style reverse-proxy manager (`/proxy`, admin) — route public hostnames to **any** upstream (on Rigger, your LAN, or a remote host), independent of projects, via Traefik's file provider. Requests reach these routes only when Rigger's Traefik receives them on :80/:443.

- **Routes** — host(s) → upstream(s): multi-upstream load balancing, custom locations, path rewrite, redirects, and default-status catch-alls. Per-route TLS: LE HTTP-01, LE DNS-01, an uploaded cert (encrypted at rest), an existing cert by SNI, or a per-route ACME-email override.
- **Access lists** — reusable named bundles of basic-auth users + IP allow rules + **GeoIP** country policy, attachable to routes (and to project app routers per-env). Global routes apply only global lists (strict workspace isolation).
- **Plugins** (UI-managed via Rigger-owned Traefik static config; enabling one restarts Traefik briefly): **WAF (Coraza + OWASP CRS)** and **GeoIP blocking** (offline IP2Location LITE DB downloaded from the UI). **Cache (Souin) is hard-disabled** — it panics under Traefik's Yaegi interpreter and would drop all routed apps; use a CDN or a cache sidecar instead.
- Backup/restore the proxy config as a **`.rpx`** bundle.

---

## 29. Git Providers & Registries

### Git providers (Manage Workspace → Git)

Workspace-scoped, encrypted at rest. Kinds:

- **Token (HTTPS PAT)** — with service presets: **GitHub** (`x-access-token`), **GitLab** (`oauth2`, `read_repository`), **Bitbucket** (username + App password), **Gitea/Forgejo** (token). Works against any HTTPS host, including self-hosted Gitea/GitLab on plain HTTP or non-default ports (the auth header is scoped to the repo's exact origin).
- **SSH deploy key** — Rigger generates an ed25519 keypair; add the public key as a read-only deploy key and use an `ssh://`/`git@…` URL.
- **GitHub App** — one-click Connect (App-manifest flow) → install → clones with short-lived 1-hour installation tokens (never persisted).

Pick a provider on a project's source (or inline-create one); **Test** verifies with `git ls-remote`.

### Container registries

- **System / managed registry** — one-click a `registry:2` sidecar on the Rigger host. With a base domain it's fronted by Traefik at `registry.{base}` over HTTPS (cluster-pullable); without one it's a local HTTP port (single-node only). Auth = bcrypt htpasswd.
- **Registry picker** — a project uses the System registry (workspace/global default), a configured registry, or "enter manually". Empty = inherit system, or local-only. Swarm/remote deploys need a real registry (blocked until one exists).
- **Build hosts** — a host can be flagged **build-only** (a dedicated builder, excluded from deploy pickers). Remote builds run on the env's build daemon and `--load` the image into the target daemon (no-registry local fallback via `pull_policy:never`). Rigger auto-`docker login`s the project's registry before build/push.

---

## 30. Users, Roles & Auth

Two role systems:

- **Global role** — `superadmin` | `user` (a JWT claim). Superadmin manages global settings and is admin everywhere.
- **Workspace role ladder** — `viewer < developer < operator < admin`, held via workspace membership and optional per-project ACLs (a project ACL wins over workspace membership). `EffectiveRole` gates every action.

Manage users in Admin → Users (invite, edit role, delete, resend invite); grant workspace membership / project ACLs in Manage Workspace → Members.

| Mechanism | Detail |
|-----------|--------|
| Password storage | bcrypt (cost 12) |
| Access token | JWT, 15-min expiry, in-memory only |
| Refresh token | httpOnly cookie, 7-day rolling, `/api/auth/refresh` only |
| Proactive refresh | Timer fires every 13 min |
| Password policy | Min length / complexity / rotation max-age (global); expired password forces a change |
| Invites | Invited accounts can't log in until they complete registration |
| Email verification | Unverified banner + resend; falls back to surfacing the link when no system SMTP |
| Forgot / reset password | Public self-service flow |
| 2FA (TOTP) | Opt-in enrollment (QR + recovery codes), enforced at login |
| Access requests | Self-service request → admin inbox approve/reject |
| Audit log | Every action (user, project, command, env, host) → Recent-activity feed |

> **In-session re-auth (wipe / delete / change-password) returns 422 on a wrong password, not 401** — a typo re-prompts instead of tripping the global session-expiry logout.

---

## 31. Rigger UI Reference

**Top nav:** Dashboard · Housekeeping (admin) · Tools · Proxy Service (admin) · Admin/Settings (admin) · help-text toggle · theme toggle · 🔔 alert bell · user menu. A **workspace switcher** selects the active workspace. **⌘/Ctrl-K** opens a command palette (quick-jump over projects + a role-gated nav registry + an on-demand deep index of env-var keys, routes, domains, pipelines).

**Left sidebar:** the workspace's project list with live status dots · New project · slide-out panels (Recent activity, Backup history, Version log).

### Dashboard (`/`)

Six stat cards (active alerts, workspaces, projects, running containers, images, networks); a projects table with env status dots (+ 🖥 host chip for remote), services/containers counts, live CPU/mem/disk/net; Docker-engine and host-system panels. Live stats fan out across the control plane and every remote host.

### Project page (`/workspaces/:ws/projects/:name`)

Per-env cards: status badge (running/partial/stopped/unknown, read from the env's actual host), compose/swarm badge, 🖥 host badge, access links (domain or ports, disabled when not healthy), image-update badge, `> bash` terminal, **Deploy ▾** (Deploy/Stop/Down), Restart, Update, 🛠️ maintenance, Rollback. Panels: streaming action output; a multi-container log viewer (per-service colour, filter, pause, maximize, download); a read-only compose viewer; per-env metrics tiles; the Service Console modal.

### Manage Workspace (`/workspaces/:ws/manage`)

Vertical tabs: **General · Preferences · Members · Access Requests · API Keys · Domains & TLS · Remote Hosts · Docker Registries · Git · Backup Targets · Notifications · Access Lists · Alert Rules · Danger Zone.**

### Edit Project

Tabs for Services (image/managed deps), Environments (+ hosts, copy env), Routing, Backup schedules, Pipelines, Preview Environments, Notes, and Danger Zone. Dirty-state save; Cancel confirms before discarding.

- **Notes/Wiki** — multiple named Markdown docs per project, rendered + sanitized server-side.

### Tools (`/tools`)

- **Template Manager** — create/convert/upload/validate/save templates (see [Templates](#11-pre-built-stack-templates)).
- **Backup & Restore** — the `.rps` / `.rpb` project artifacts (see [Backups](#20-backup--restore)).
- **Migrate data** — env→env data movement.

### Theming

Light/dark via semantic CSS-var tokens (`data-theme` on root) — System / Dark / Light, plus font, density, and log-viewer prefs. A global help-text toggle gates all inline `<Hint>` help.

---

## 32. Directory & Config Layout

### Repo root

```
rigger/
├── install.sh / uninstall.sh              # OS-aware installer
├── rigger.sh / rigger.ps1 / rigger.bat    # Thin host CLI wrappers (HTTP → API)
├── templates/                             # Baked into the image as a seed
│   ├── dockerfiles/    # django dotnet go laravel nextjs nodejs rails react spring spring-gradle
│   ├── scaffold/       # django go laravel nodejs react (starter apps)
│   └── stacks/         # 37 pre-built stack templates (*.json)
└── src/
    ├── Dockerfile                         # node → golang → alpine (single image, bakes templates/)
    ├── docker-compose.yml                 # rigger + Traefik v3.4 + socket-proxy + fallback
    ├── .env.example
    ├── backend/
    │   ├── cmd/server/                    # server + `init-workspace` subcommand
    │   ├── api/                           # ~76 HTTP handler files
    │   └── internal/                      # ~45 packages: composegen, envgen, dockerops, builder,
    │       #   detect, blueprints, scaffold, databases, managedregistry, gitproviders, remotehost,
    │       #   executor, acme, customdomains, gateway, clouddns, proxyroutes, pipelines, previews,
    │       #   backup, backupsync, alerts, notify, metrics, managedmetrics, maintenance, apikey,
    │       #   auth, crypto, settings, shell, wsconfig, workspace, imagecheck, version, …
    └── frontend/src/                      # React SPA (pages, components, hooks, lib)
```

### Generated project

```
workspaces/<workspace>/projects/<name>/
├── config.json                 # Edit this (UI or file), then refresh <env>
├── notes/                      # Wiki markdown docs
├── backups/<env>/<ts>/         # Per-env snapshots
└── envs/
    └── dev/
        ├── .env                # Live secrets — NEVER commit
        ├── .env.example        # Redacted template — safe to commit
        ├── docker-compose.yml  # Generated natively in Go
        └── volumes/            # Bind-mounted data
```

**Commit** `config.json`, `envs/*/.env.example`, `envs/*/docker-compose.yml`, and any generated Dockerfiles/nginx. **Don't commit** `envs/*/.env` (secrets) or `envs/*/volumes/` (runtime data).

---

## 33. Environment Variables & Volumes

### Server environment (`src/.env`)

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_SECRET` | — | **Required.** `openssl rand -hex 32` (also derives the AES key that encrypts SSH keys, tokens, DB-user passwords) |
| `RIGGER_PORT` | `9999` | UI host port |
| `RIGGER_APP_HOST` | — | Host LAN/public IP the installer detected → seeds the `app_host` setting for local app links + magic-DNS |
| `ACME_EMAIL` | `admin@example.com` | Let's Encrypt account email (for SSL) |
| `CF_DNS_API_TOKEN` | — | Cloudflare DNS-01 token for wildcard / override certs (also settable in the UI) |
| `RIGGER_HOST_DIR` | — | Host path to the install dir; enables one-click self-update |
| `RIGGER_IMAGE_TAG` | `latest` | The image tag this instance runs (self-update default) |
| `REMOTE_WORKSPACES_DIR` | = `WORKSPACES_DIR` | Default workspaces path on remote hosts (overridable per host) |
| `TZ` | `UTC` | Timezone for server-rendered timestamps |
| `LISTEN_ADDR` | `:8080` | Bind address (inside the container) |
| `WORKSPACES_DIR` / `TEMPLATES_DIR` / `DATA_DIR` | `/toolkit/workspaces` · `/toolkit/templates` · `/data` | Mount paths |
| `METRICS_INTERVAL_SECONDS` | `30` | Metrics sampling cadence |
| `APPRISE_URL` | — | Optional Apprise API sidecar (else embedded apprise-go) |

### Volume mounts

The toolkit logic is baked into the binary, so only data is mounted:

| Mount | Mode | Purpose |
|-------|------|---------|
| `../workspaces → /toolkit/workspaces` | rw | Projects (config, envs, volumes, backups) |
| `../templates → /toolkit/templates` | rw | Templates (writable so Tools can save new ones) |
| `/var/run/docker.sock` | rw | Docker socket (command bridge) |
| `/ → /host` | ro | Host filesystem for real disk metrics |
| `rigger-data → /data` | rw | SQLite DB + workspace archives |
| `traefik-certs`, `rigger-dynamic`, `rigger-traefik-config`, `rigger-geoip` | mixed | ACME store, out-of-band certs/TLS config, UI-managed Traefik static config, GeoIP DB |

### Development mode

```bash
# Terminal 1 — Go backend
cd src/backend && go run ./cmd/server
# Terminal 2 — React dev server
cd src/frontend && npm install && npm run dev   # http://localhost:5173 (proxies /api to :8080)
```

The containerized build gates (used in CI and locally) run `go build ./... && go vet ./... && go test ./...` in `golang:1.25` and `npm run build` (eslint + vite) in `node:20`.

---

## 34. Maintenance Guide

### Add a pre-built template
Create `templates/stacks/<name>.json` (see [Templates](#11-pre-built-stack-templates)) — no registration; the wizard globs `*.json`. Or generate it in **Tools → Template Manager** and save from the browser.

### Add a source-build framework
1. Add Dockerfiles to `templates/dockerfiles/<id>/` (+ optional `templates/scaffold/<id>/` starter).
2. Register the blueprint in `src/backend/internal/blueprints/blueprints.go` (language, port, healthcheck, template id, env contract).
3. Teach `internal/detect` to recognize its manifest.
4. Add the default image tag to `internal/workspace/create.go`.

### Add a managed database engine
Add an `Engine` entry to `src/backend/internal/databases/databases.go` (id, image, versions, port, volume, env prefix, capability flags) — the catalog, wizard, and env contract pick it up. Auxiliary engines follow the documented multi-touchpoint pattern (composegen service, envgen contract + secret, detect folding, frontend toggle).

### Add an environment to a project
Edit Project → Add environment (inherits vars from the first env; bootstrapped on first deploy), or add the env block to `config.json` and `rigger <ws> <proj> <new_env> init`. Keep `http_port` unique across envs on the same host.

---

## 35. Troubleshooting

### Compose file looks stale
`docker-compose.yml` is generated from `config.json` — the old Bash escape-bug class is gone. If it's out of date (e.g. you edited `config.json` on disk), run `refresh <env>`.

### Managed database auth fails (`P1000` / "password authentication failed")
A managed DB applies its `*_PASSWORD` only on **first** init of an empty data dir; later starts ignore it. If the env's `.env` password diverges from what the volume was initialized with, the app (and the pre-deploy migrate gate) fail and the env reads **partial**. Rigger defends against this by [pinning](#21-secrets) each managed secret and re-capturing it every deploy, so a regen heals a drifted `.env`. If a volume still mismatches (e.g. a project pinned before its volume existed), fix it by (a) **Danger Zone → Wipe data** to re-initialise against the current `.env` (dev/test — destroys data), or (b) `ALTER USER <user> WITH PASSWORD '<value from .env>'` over the DB's local socket to realign in place (keeps data).

### App URL updated but doesn't load after a host/IP change
The magic-DNS host is baked into the running containers' Traefik labels; changing the App host/IP (or a remote host's address) updates the *displayed* URL but not the live labels. Rigger warns and offers to refresh & redeploy affected envs on save — accept it, or `refresh <env>` manually. See [Domains](#15-domains-tls--routing).

### Non-root image crash-loops on a bind mount
A prebuilt image running as a fixed non-root UID can't write a Docker-created (root-owned) bind dir. Rigger auto-synthesizes a `{svc}-init-perms` one-shot that chowns the bind dirs before the app starts — so this self-heals; if you see it, redeploy so the init gate runs.

### "? digest unknown" on an image-update check
The local image has no `RepoDigest` (pulled via compose without an explicit `docker pull`, or built locally), so the digest comparison is indeterminate. Run **Update** to populate it.

### Dashboard loads slowly
Per-project disk usage (`du`) is served from an async cache (background refresh), not computed inline — dashboard responses are sub-second. A slow bind mount (Windows/Docker Desktop) only delays the cached size, not the page.

### Remote host: "No workspaces found" on Scan
Scan looks under the host's **Remote workspaces directory**; the default is the control-plane container path, which doesn't exist on a bare host. Set it to the real path (e.g. `/opt/rigger/workspaces`) and scan again.

### Bind-mounted config file missing
If a deploy fails listing missing config file(s), a service bind-mounts a *file* (e.g. `prometheus.yml`) that doesn't exist — Docker would create it as a directory and break the mount, so Rigger stops first. Create the file in `envs/<env>/`, or ship it as a template [seed file](#11-pre-built-stack-templates).

### Running behind Cloudflare
Set Cloudflare SSL mode to **Full** (not Flexible). Flexible sends plain HTTP to your server, breaking the Let's Encrypt HTTP-01 challenge Traefik uses.
