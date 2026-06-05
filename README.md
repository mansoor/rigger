# Rigger — Rig once. Deploy anywhere

> **Yes, it's called Rigger.** And like a good dad, it does all the heavy lifting without complaining, remembers exactly how everything was set up, and gets quietly upset if you don't follow the instructions. Unlike your actual dad, it won't ask why you're still using `docker run` manually in 2026.

A Go-powered toolkit for scaffolding, building, and operating multi-environment Docker application stacks — with a full-featured web UI for teams that prefer the browser. Create a self-contained workspace from the wizard, then build, deploy, promote, back up, and manage everything across dev, stage, and prod. The entire runtime is a single ~15 MB Go binary (no Bash scripts) plus a thin host CLI wrapper.

---

## Table of Contents

1. [Quick Install](#1-quick-install)
2. [Architecture Overview](#2-architecture-overview)
3. [Directory Structure](#3-directory-structure)
4. [Prerequisites](#4-prerequisites)
5. [Quick Start](#5-quick-start)
6. [Creating a Workspace](#6-creating-a-workspace)
7. [Workspace Layout](#7-workspace-layout)
8. [Command Reference](#8-command-reference)
9. [Environment Configuration](#9-environment-configuration)
10. [Version Management](#10-version-management)
11. [Deployment Strategies](#11-deployment-strategies)
12. [Build vs Promote](#12-build-vs-promote)
13. [Backup & Restore](#13-backup--restore)
14. [Git Sync](#14-git-sync)
15. [Supported Stacks](#15-supported-stacks)
16. [Pre-built Stack Templates](#16-pre-built-stack-templates)
17. [Image Stacks — Manual Configuration](#17-image-stacks--manual-configuration)
18. [Image Update Detection](#18-image-update-detection)
19. [Healthchecks](#19-healthchecks)
20. [Traefik vs Direct Port Routing](#20-traefik-vs-direct-port-routing)
21. [Rigger UI — Web Interface](#21-rigger--web-interface)
22. [Multi-Host Support](#22-multi-host-support)
23. [Maintenance Guide](#23-maintenance-guide)
24. [Troubleshooting](#24-troubleshooting)

---

## 1. Quick Install

One-line installer — detects your OS, installs all dependencies, clones the repo, generates a JWT secret, and starts Rigger:

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
| `RIGGER_PORT` | `8080` | UI host port |
| `RIGGER_BRANCH` | `main` | Git branch to install |
| `ACME_EMAIL` | — | Let's Encrypt contact email (required for SSL) |
| `SKIP_DOCKER` | `0` | Set to `1` to skip Docker installation check |

**Supported OS:** Ubuntu/Debian, RHEL/CentOS/AlmaLinux/Fedora, Arch, Alpine, macOS (Docker Desktop required).

After install, open `http://localhost:8080` — first visit prompts you to create an admin account.

**Manual install:**

```bash
git clone https://github.com/mansoor/rigger.git
cd rigger/src
cp .env.example .env
# Edit .env: set JWT_SECRET (openssl rand -hex 32) and ACME_EMAIL
docker network create traefik_net 2>/dev/null || true
docker compose up --build -d
```

---

## 2. Architecture Overview

```
┌──────────────────────────────────────────────────────────────────┐
│  rigger container  (~15 MB Alpine, single Go binary)               │
│                                                                  │
│   Go backend  →  React SPA (embedded via embed.FS)               │
│   REST + WebSocket API + thin `rigger` host CLI wrapper            │
│   Native Go runtime: compose-gen, deploy (compose+swarm),        │
│     bootstrap, env-gen, build/promote, backup/restore, version   │
│   templates/   ← Dockerfiles, Nginx, stack templates (baked seed)│
└───────────────────────────────┬──────────────────────────────────┘
                                │  generates / operates ↓
┌───────────────────────────────▼──────────────────────────────────┐
│  workspaces/<project>/        (one per application)              │
│                                                                  │
│   config.json      ← single source of truth for all settings    │
│   envs/                                                          │
│     dev/  stage/  prod/   ← scaffolded environments             │
│       .env                 ← secrets (never commit)             │
│       docker-compose.yml   ← generated from config.json (Go)    │
│       volumes/             ← bind-mounted data                  │
└──────────────────────────────────────────────────────────────────┘
```

**Key design principles:**

- **Config-driven.** `config.json` is the single source of truth. Edit it (UI or file) and refresh — compose files are regenerated and redeployed automatically.
- **One Go binary, no Bash.** Every operation — compose generation, deploy (compose & swarm), bootstrap, .env generation, build/promote, backup/restore, version — is implemented natively in Go. The image ships no shell scripts.
- **Workspace = self-contained.** Everything needed to operate a project lives in `workspaces/<project>/`. The workspace can be archived, moved, or restored independently.
- **No host toolchain required.** All build steps happen inside Docker. The host needs only `docker`; the optional `rigger` CLI wrapper needs only `curl`.
- **Bind mounts by default.** All volume data lives in `envs/<env>/volumes/` on the host — readable, backupable, and portable without Docker named volume gymnastics.
- **One control plane, many hosts.** Workspaces (or individual environments) can run on remote hosts over SSH — Rigger generates files locally and runs `docker`/`compose` on the host. Remotes need only Docker + an SSH server (no Rigger binary). See [Multi-Host Support](#22-multi-host-support).

---

## 3. Directory Structure

### Repo root

```
rigger/
├── install.sh                          # One-line OS-aware installer
├── rigger.sh / rigger.ps1 / rigger.bat       # Thin host CLI wrappers (HTTP → API)
├── templates/                          # Baked into the image as a seed
│   ├── dockerfiles/                    # laravel/ nodejs/ nextjs/ react/
│   ├── nginx/                          # laravel.conf nodejs.conf nextjs.conf react.conf
│   └── stacks/                         # Pre-built image stack templates (12 included)
│       ├── ghost.json … wordpress.json
└── src/
    ├── Dockerfile                      # 3-stage: node → golang → alpine (no scripts)
    ├── docker-compose.yml              # rigger + Traefik v3.1
    ├── .env.example
    ├── backend/                        # Go HTTP server + native runtime (CGO_ENABLED=0)
    │   ├── go.mod
    │   ├── cmd/server/
    │   │   ├── main.go                 # server + `init-workspace` subcommand dispatch
    │   │   └── initworkspace.go        # `rigger init-workspace` (headless create)
    │   ├── api/                        # handlers, action (REST), backup, settings, housekeeping
    │   └── internal/
    │       ├── auth/                   # JWT (access + refresh), bcrypt, rate limiter
    │       ├── db/                     # SQLite (modernc, CGO-free), auto-migration
    │       ├── shell/                  # Command allowlist + Go dispatch bridge
    │       ├── wsconfig/               # config.json reader + version/tag/stack helpers
    │       ├── composegen/             # docker-compose.yml generator (was compose-gen.sh)
    │       ├── envgen/                 # .env / .env.example generator (was env-gen.sh)
    │       ├── workspace/              # discovery, config R/W, env vars, Bootstrap (was bootstrap.sh)
    │       ├── dockerops/              # compose + swarm lifecycle (was deploy.sh)
    │       ├── builder/                # image build + promote (was build.sh / promote.sh)
    │       ├── backup/                 # DB dump + volume archive/restore (was backup.sh/restore.sh)
    │       ├── version/                # semver bump/set in config.json (was version.sh)
    │       ├── imagecheck/             # Docker Hub update checker + in-memory cache
    │       ├── metrics/ · stats/       # host/docker metrics + history collector
    │       ├── alerts/ · notify/       # alert rules engine + apprise-go notifications
    │       ├── crypto/                 # AES-GCM at-rest encryption + SSH keygen (Phase 7)
    │       ├── executor/               # docker exec abstraction (Local vs remote-over-SSH)
    │       ├── remotehost/             # SSH client, connection pool, tar-over-SSH file sync
    │       └── config/
    └── frontend/src/
        ├── pages/                      # Dashboard, Workspace, New (7-step), Edit, Settings, Housekeeping, Tools
        ├── components/                 # Layout, ComposeEditor, SlideOutPanel, TerminalModal, Sparkline
        ├── hooks/useDockerEvents.js    # SSE → React Query invalidation
        └── lib/api.js
```

### Generated workspace

```
workspaces/<project>/
├── config.json                         # Edit this (UI or file), then refresh <env>
└── envs/
    ├── dev/
    │   ├── .env                        # Live secrets — NEVER commit
    │   ├── .env.example                # Redacted template — safe to commit
    │   ├── docker-compose.yml          # Generated natively in Go
    │   └── volumes/                    # Bind-mounted data directories
    │       ├── app_data/
    │       └── db_data/
    ├── stage/
    └── prod/
```

> **No `run.sh`.** Earlier versions generated a per-workspace `run.sh` dispatcher; the Go runtime replaced it, and a startup sweep removes any leftover. Commands are issued via the web UI, the REST action endpoint, or the `rigger` CLI wrapper.

---

## 4. Prerequisites

| Tool | Required for | Install |
|------|-------------|---------|
| `docker` + Compose v2 plugin | Everything (build / deploy / runtime) | [docker.com](https://docs.docker.com/get-docker/) or `./install.sh` |
| `curl` | One-line installer + the `rigger` CLI wrapper | Ships with most systems |

> The Rigger engine runs entirely inside the container as a single Go binary — the host needs no `bash`, `jq`, `openssl`, or `git`. All build steps happen inside Docker.

---

## 5. Quick Start

Most users create and operate workspaces from the **web UI** at `http://localhost:8080`. For headless / scripted setups, two non-UI paths exist:

**Headless create** — the `rigger` binary's `init-workspace` subcommand takes a prepared `config.json` and scaffolds + bootstraps every environment:

```bash
# inside the rigger container (or any host with the binary)
docker exec rigger rigger init-workspace -name myapp -config /toolkit/workspaces/myapp.config.json
# then fill in secrets and deploy
```

**Host CLI wrapper** — `rigger.sh` (and `rigger.ps1` / `rigger.bat`) are thin wrappers that authenticate and call the REST API of a running Rigger server:

```bash
./rigger.sh login                       # stores a refresh session in ~/.rigger
./rigger.sh list                        # list workspaces
./rigger.sh myapp dev start             # run any allowlisted command
./rigger.sh myapp dev ps
```

---

## 6. Creating a Workspace

Workspaces are created through the **New Workspace wizard** in the web UI (see [Section 21](#21-rigger--web-interface)) — a 7-step flow covering project, stack, environments, services, backup, review, and a live bootstrap terminal.

For automation, `rigger init-workspace -name <name> -config <config.json|->` performs the same creation headlessly: it writes the workspace, generates each environment's `.env` (auto-generating placeholder secrets) and `docker-compose.yml`, and installs Dockerfiles/nginx for custom stacks — all natively in Go. A `config.json` can be exported from an existing workspace or produced by the **Tools → Compose → Template** converter.

The stack types you can configure:

- **Pre-built template** — 12 curated templates: Ghost, Gitea, Grafana, Immich, MinIO, n8n, Nextcloud, Nginx Proxy Manager, Plausible, Uptime Kuma, Vaultwarden, WordPress.
- **Image stack (manual)** — your own Docker images (name, image, tag, ports, volumes); `${VAR}` references resolve from `.env` at deploy time.
- **Custom stack (source-built)** — backend `laravel`/`nodejs`, frontend `none`/`nextjs`/`react`, database `postgres`/`mysql`, optional Redis and Garage S3.

---

## 7. Workspace Layout

```
workspaces/<project>/
├── config.json        ← edit this to change any setting, then refresh
└── envs/
    └── dev/
        ├── .env             ← fill in real secrets before first build
        ├── .env.example     ← commit this to version control
        ├── nginx.conf       ← auto-generated (custom stacks); regenerated on refresh
        ├── docker-compose.yml  ← auto-generated; regenerated on refresh
        └── volumes/         ← bind-mounted data (all volume data lives here)
```

**What to commit:**

```
✅ config.json
✅ envs/*/.env.example
✅ envs/*/nginx.conf
✅ envs/*/docker-compose.yml
✅ envs/*/backend/Dockerfile
✅ envs/*/frontend/Dockerfile  (if enabled)

❌ envs/*/.env          ← contains secrets
❌ envs/*/volumes/      ← runtime data
```

---

## 8. Command Reference

Commands are issued from the **web UI**, the **`rigger` CLI wrapper**, or the **REST action endpoint** — all hit the same Go runtime. The CLI form is:

```bash
rigger <workspace> <env> <command> [args]      # e.g. rigger myapp prod start
```

REST: `POST /api/workspaces/{name}/envs/{env}/action` with `{"command":"...","extra":[...]}`.

### Lifecycle

```
start                  # Deploy / bring up the stack
stop                   # Pause containers (state preserved)
down                   # Remove containers (volumes kept)
restart [service]      # Rolling restart (all or one service)
update                 # Pull latest images + recreate
refresh                # Regenerate compose file + redeploy
```

### Build & Release (custom stacks)

```
build [backend|frontend|all] [--push] [--bump [major|minor|patch|build]]
promote <dst_env> [--dry-run]    # retag + redeploy an existing image (no rebuild)
```

> `build`/`promote` operate inside the server (Docker socket + workspace files) and are reachable via the UI / CLI / REST action endpoint. (`sync` — git pull + build + deploy — is not currently available; git-driven sync is planned for a later phase.)

### Operations

```
ps                     # Show containers (+ image-update summary for image stacks)
logs [service]         # Follow logs (all or one service)
backup [db|files|all]  # Run backups (default: all)
restore <snapshot>     # e.g. 2026-06-01_14-30-00
```

> Container shell access (`exec` / `bash`) is available from the **web UI terminal** (the `> bash` button on an env card), not as a CLI/REST command.

### Configuration

```
init [--regen-env]     # Re-bootstrap env (regen Dockerfiles, nginx, compose; --regen-env rewrites .env)
version current
version bump [major|minor|patch|build]   # default: build
version set 2.5.0-build.0
```

---

## 9. Environment Configuration

All settings live in `config.json`. Edit it (UI editor or file), then run `refresh <env>`.

### Per-environment block

```json
"environments": {
  "prod": {
    "domain":           "example.com",
    "http_port":        80,
    "traefik_enabled":  true,
    "traefik_network":  "traefik_net",
    "ssl_enabled":      true,
    "deployment":       "compose",
    "replicas": { "backend": 2, "frontend": 1 },
    "git": {
      "enabled": true,
      "repo":    "git@github.com:org/repo.git",
      "branch":  "main"
    }
  }
}
```

**`http_port` note:** Only relevant for custom stacks when Traefik is **off**. It is the host port Nginx binds to for direct access. When Traefik is on, Nginx is reached via the Docker network — no host port binding is needed.

### Image stack service fields

```json
"images": [
  {
    "name":       "app",
    "image":      "ghost",
    "tag":        "5-alpine",
    "port":       2368,
    "host_port":  "${GHOST_PORT}",
    "link_ports": ["${GHOST_PORT}"],
    "volumes":    ["./volumes/ghost_data:/var/lib/ghost/content"],
    "depends_on": ["db"],
    "extra_ports": [],
    "healthcheck": "curl -sf http://localhost:2368/ -o /dev/null || exit 1",
    "healthcheck_config": { "interval": "30s", "timeout": "10s", "retries": "3", "start_period": "60s" },
    "restart": "unless-stopped",
    "env_vars": { "url": "${GHOST_URL}" },
    "extra_compose": ""
  }
]
```

**`link_ports`** — which host ports appear as clickable links on the UI env card. Defaults to `host_port` if not set.

**`extra_compose`** — raw YAML appended verbatim to the service block in the generated compose file. Use for options not covered by structured fields (mem_limit, cpus, logging, etc.). Applies to **all** environments.

**Per-environment service overrides** — stored under `environments.<env>.service_overrides.<service_name>.extra_compose`. Same raw YAML format, applied **after** the base `extra_compose`. Use for environment-specific tuning:

```json
"environments": {
  "prod": {
    "service_overrides": {
      "app": {
        "extra_compose": "mem_limit: 4g\ncpus: \"2.0\"\nlogging:\n  driver: none"
      }
    }
  },
  "dev": {
    "service_overrides": {
      "app": {
        "extra_compose": "mem_limit: 512m\nlogging:\n  driver: json-file"
      }
    }
  }
}
```

### Version block (custom stacks)

```json
"versions": {
  "postgres": "15-alpine",
  "mysql": "8.0",
  "redis": "7-alpine",
  "nginx": "1.25-alpine",
  "node": "20-alpine",
  "php": "8.3-fpm-alpine"
}
```

---

## 10. Version Management

Image tags follow: `registry/project-backend:2.1.0-build.47-stage`

| Command | Before → After |
|---------|----------------|
| `version bump` | `1.2.3-build.41` → `1.2.3-build.42` |
| `version bump patch` | → `1.2.4-build.0` |
| `version bump minor` | → `1.3.0-build.0` |
| `version bump major` | → `2.0.0-build.0` |
| `version set 3.0.0-build.0` | → `3.0.0-build.0` (full `M.m.p-build.N` form required) |

---

## 11. Deployment Strategies

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

Enables replica scaling via `docker stack deploy`. Requires `docker swarm init`.

---

## 12. Build vs Promote

### `build` — compile a fresh image from source
```bash
rigger myapp stage build --bump minor --push
```

### `promote` — move an existing validated image to the next environment
```bash
rigger myapp stage promote prod
```
No Dockerfile involved. The binary is byte-for-byte identical to what ran in stage.

### Recommended release pipeline

```
dev  →  build dev  →  build stage --push  →  validate  →  promote stage→prod
```

**Why not `build prod` directly?** Promoting the validated stage image guarantees you ship exactly what was tested — no build-time variance.

---

## 13. Backup & Restore

### Per-env backup snapshots (CLI)

Backups are written to `workspaces/<project>/backups/` as timestamped `*.sql.gz` / `*.tar.gz` files:

```bash
rigger myapp prod backup          # database + all volumes
rigger myapp prod backup db       # database only
rigger myapp prod backup files    # volumes only
rigger myapp prod restore 2026-06-01_14-30-00
```

What gets backed up (the dump tools run **inside the database container** via `docker compose exec`, so no DB client is needed on the host):
- **PostgreSQL** — live `pg_dump` → `*.sql.gz`
- **MySQL / MariaDB** — live `mariadb-dump`/`mysqldump` (auto-detected) → `*.sql.gz`
- **Volumes** — `tar.gz` per volume via a helper container → filesystem fallback if SQL dump fails

What `restore` does: stop → restore databases → restore volumes → start.

### Workspace archive backup (UI Tools page)

A separate, full-workspace archival feature available from **Tools → Workspace Backup & Restore**:

- Creates a `.tar.gz` of the entire workspace directory (config, .env files, all volume data)
- Excludes per-env backup snapshots to avoid archive-within-archive bloat
- Stored at `/data/workspace-archives/` (persisted volume, survives container restarts)
- Async — start the job and come back later to download; shows live status while running
- Download, delete from server, or restore from the UI
- Restore: upload the archive → backend validates `config.json` → extracts to `workspaces/`

---

## 14. Git Sync

> **Not currently available.** The earlier Bash `sync` command (git pull → build → deploy) was retired during the move to the native Go runtime. Git-driven sync is planned for a later phase (alongside remote backup targets). The `git` block can still be stored in `config.json` for forward compatibility:

```json
"git": {
  "enabled": true,
  "repo":    "git@github.com:org/repo.git",
  "branch":  "main"
}
```

In the meantime, deploy from a built/pushed image with `build` + `promote`, or rebuild from updated source with `build <env> --push`.

---

## 15. Supported Stacks

### Backend
| Key | Stack |
|-----|-------|
| `laravel` | PHP-FPM + Composer (multi-stage prod, Xdebug in dev) |
| `nodejs` | Node.js — Express / Fastify / etc. |

### Frontend
| Key | Stack |
|-----|-------|
| `nextjs` | Next.js with `output: 'standalone'` |
| `react` | React / Vite SPA → Nginx |
| `none` | API only |

### Database
| Key | Image |
|-----|-------|
| `postgres` | `postgres:15-alpine` |
| `mysql` | `mysql:8.0` |

### Optional services
| Service | Key |
|---------|-----|
| Redis | `redis_enabled: true` |
| Garage S3 | `garage_enabled: true` |

---

## 16. Pre-built Stack Templates

12 templates included in `templates/stacks/`:

| Template | Label | Services |
|----------|-------|----------|
| `ghost` | Ghost CMS + MySQL | ghost, mysql |
| `gitea` | Gitea + PostgreSQL | gitea, postgres |
| `grafana` | Grafana + Prometheus | grafana, prometheus |
| `immich` | Immich (photo manager) | immich-server, immich-microservices, postgres, redis |
| `minio` | MinIO object storage | minio |
| `n8n` | n8n workflow automation | n8n, postgres |
| `nextcloud` | Nextcloud + PostgreSQL + Redis | nextcloud, postgres, redis |
| `nginx-proxy-manager` | Nginx Proxy Manager + MariaDB | npm-app, mariadb |
| `plausible` | Plausible Analytics | plausible, postgres, clickhouse |
| `uptime-kuma` | Uptime Kuma | uptime-kuma |
| `vaultwarden` | Vaultwarden (Bitwarden) | vaultwarden |
| `wordpress` | WordPress + MariaDB | wordpress, mariadb |

### How templates work

Each template is a JSON file in `templates/stacks/`. It declares images, ports, volumes, healthchecks, and `default_env_vars`. The wizard discovers templates by globbing `templates/stacks/*.json` — no registration required.

You can also create templates from the UI:
- **Export as template** button on any image-stack workspace page (replaces secrets with `CHANGE_ME`)
- **Tools → Compose → Template** converter (paste any `docker-compose.yml`, download the generated template JSON)
- **Save as template** button in the converter — writes directly to `templates/stacks/` on the server (no rebuild needed; templates are live-mounted)

### Volume mounts in templates

All templates use **bind mounts**: `./volumes/<name>:/container/path`. Volume data lives in `envs/<env>/volumes/<name>/` on the host — no Docker named volume overhead.

---

## 17. Image Stacks — Manual Configuration

An image stack (`"type": "image"`) deploys existing Docker images with no Dockerfiles or source code. Configure once → fill `.env` → `start`.

### Applicable commands

| Command | Image stack | Custom stack |
|---------|:-----------:|:------------:|
| `start` / `stop` / `restart` / `down` | ✅ | ✅ |
| `update` (pull + recreate) | ✅ | ✅ |
| `refresh` (regen compose + redeploy) | ✅ | ✅ |
| `ps` / `logs` | ✅ | ✅ |
| `backup` / `restore` | ✅ | ✅ |
| `init` / `version` | ✅ | ✅ |
| `build` / `promote` | ❌ | ✅ |

### Per-image env var interpolation

`${VAR}` placeholders in `env_vars` resolve from the `.env` file at deploy time. Each service gets only the variables it needs — no shared env blob.

---

## 18. Image Update Detection

For image stacks, Rigger checks Docker Hub for updates hourly:

- **`latest` tags** — compares local digest vs remote manifest; shows "? digest unknown" (grey) if the image has no RepoDigest (e.g. pulled via compose without an explicit `docker pull`)
- **Pinned tags** — fetches the tags list and finds newer semver tags using numeric per-segment comparison (`10.0 > 9.0` correctly)

The "↑ update available" amber badge appears on the env card. After running **Update**, the cache is invalidated and a fresh check runs automatically (badge clears within ~10 seconds if the update succeeded).

---

## 19. Healthchecks

All containers include Docker healthchecks in the generated compose file.

### Custom stacks

Generated automatically by the Go compose generator (`internal/composegen`) per service type:

| Service | Healthcheck |
|---------|-------------|
| Laravel backend | `php -r 'exit(0);'` |
| Node.js backend | `wget -qO- http://localhost:3000/health` |
| PostgreSQL | `pg_isready -U <user>` |
| MySQL / MariaDB | `mariadb-admin ping -h localhost -u root -p${PASSWORD} --silent` |
| Redis | `redis-cli ping` |
| Nginx | `curl -sf http://localhost/` |

### Image stack templates

Each service in a template carries its own `healthcheck` command and `healthcheck_config` (interval, timeout, retries, start_period). Configurable per-service in Edit Workspace.

> **Note:** Healthcheck commands containing `${VAR}` references must not use inner double-quotes around the variable (e.g. `-p${PASSWORD}` not `-p"${PASSWORD}"`). The compose generator escapes any literal `"` characters in healthcheck commands to prevent YAML syntax errors.

---

## 20. Traefik vs Direct Port Routing

### Direct ports (default for dev / Traefik off)

```json
"traefik_enabled": false
```

For custom stacks: Nginx binds `http_port` on the host → container port 80. For image stacks: each service's `host_port` is mapped directly.

### Traefik (recommended for stage/prod)

```json
"traefik_enabled": true,
"traefik_network": "traefik_net",
"ssl_enabled": true
```

No host port binding. Traefik routes by domain name using Docker labels. SSL certificates are issued automatically via Let's Encrypt. For custom stacks, Traefik routes to Nginx's internal port 80. For image stacks, Traefik routes to each service's `port` (internal container port).

**One-time setup:**
```bash
docker network create traefik_net
```

**SSL requirements:** Port 80 open, DNS A record pointing to this server, `ACME_EMAIL` set in `src/.env`.

---

## 21. Rigger UI — Web Interface

Rigger UI is a browser-based control plane. It runs as a Docker container and provides full workspace management — creation, environment lifecycle, live log streaming, container terminals, backup history, image update detection, system dashboards, and admin tools.

The UI and the `rigger` CLI are fully interchangeable — both drive the same native Go runtime through the server's command bridge. There are no shell scripts: compose generation, deploy (compose & swarm), bootstrap, build/promote, backup/restore, and version management are all Go.

### Architecture

```
Browser  (JWT Bearer + httpOnly refresh cookie)
  │
  ▼
┌─────────────────────────────────────────────────────────────┐
│  rigger container (~15 MB Alpine, single Go binary)           │
│                                                             │
│  Go HTTP server (CGO_ENABLED=0)                             │
│   ├─ React SPA embedded via embed.FS                        │
│   ├─ REST API + WebSocket streams                           │
│   ├─ Auth: bcrypt + dual JWT (15-min access / 7-day refresh)│
│   ├─ Proactive token refresh (fires every 13 min)           │
│   ├─ Command bridge (strict allowlist → native Go ops)       │
│   ├─ Image update cache (hourly background checker)         │
│   ├─ Stats collector (Docker info + host /proc metrics)     │
│   └─ Async workspace archiver (tar.gz backup jobs)         │
└─────────────────────────────────────────────────────────────┘
  │  bridge.Run → composegen / dockerops / builder / backup / version
  ▼
docker / docker compose  +  workspaces/<project>/  (config, envs, volumes)
```

### Quick start

```bash
cd rigger/src
cp .env.example .env
# Set JWT_SECRET: openssl rand -hex 32
# Set ACME_EMAIL for SSL
docker network create traefik_net 2>/dev/null || true
docker compose up --build -d
# → http://localhost:8080
```

### Navigation

**Top bar:** Dashboard · Housekeeping · Tools · Settings · user menu

**Left sidebar:** workspace list with live status dots · New workspace button · Recent activity · Backup history · Version log (slide-out panels)

### Dashboard (`/`)

Skeleton loading animation while data fetches. Host/Docker panels refresh every 30 s; the workspaces table tracks live resource usage with a ~4 s poll plus SSE invalidation on Docker events.

- **6 stat cards:** Active alerts, Workspaces, Environments, Running containers, Docker images, Docker networks
- **Workspaces table:** name, type, env status dots (with a 🖥 host chip on environments running remotely), **Services** (distinct compose service count), **Containers** (running/total), **CPU**, **Memory**, **Disk**, **Network** (live throughput), Open link. Live stats fan out across the local control plane and every remote host with workloads
- **Docker engine panel:** version, storage driver, root dir, container/image/volume/network counts
- **Host system panel:** OS, arch, CPU, uptime, memory bar, disk bar (amber >65%, red >85%)

### Workspace page (`/workspaces/:name`)

**Environment cards** — each shows:
- Environment name + status badge (running / partial / stopped / unknown). Status is read from the env's actual host (local or remote over SSH)
- **🖥 host badge** for environments running on a remote host (Phase 7)
- **Access links:** domain badge (Traefik on) or port badge(es) (Traefik off) — clickable `↗` links. They're disabled (non-clickable) when the env isn't running/healthy, and use the **remote host's address** for direct `host:port` URLs. Supports multiple links per env for multi-port image stacks (configured via the 🔗 checkbox on port rows)
- **"↑ update available"** amber badge / **"? digest unknown"** grey badge (image stacks)
- **`> bash`** terminal button
- **Deploy ▾** split button (Deploy / Stop / Down)
- **Restart** and **Update** buttons

**Action output panel** — streams live output from deploy, stop, restart, backup, update actions.

**Log viewer panel:**
- Environment tabs + multi-container **checkbox** selector (tick "all" or specific services)
- Per-container log colouring — each service gets a distinct colour from a 10-colour palette. Colour swatches in the container selector legend serve as a visual guide
- Text filter (grep-style with filtered/total line count)
- Auto-scroll checkbox + ⏸ Pause/Resume stream button
- ↺ Reconnect button
- **⛶ Maximize** button — opens full-screen modal with all inline features plus:
  - Row limit dropdown (All / 100 / 500 / 1 000 / 5 000)
  - **# rows** toggle for line number prefix
  - ⎘ Copy (copies current view with filter + row limit applied)
  - ⬇ Download as `.txt`
  - Clear buffer

**Compose viewer** — read-only `docker-compose.yml` with line numbers, hover-highlight per row, **Copy** button (selection-aware: copies selected text if selection exists in the viewer, otherwise copies full file). Copy works on plain HTTP via `execCommand` fallback.

### New Workspace wizard (7 steps)

1. **Project** — name (checked for uniqueness against existing workspaces), registry dropdown
2. **Stack** — Pre-built template (12 options with search + Popular/Browse All views) / Image stack / Custom app
3. **Environments** — name, domain, Traefik, SSL, port (shown only for custom stacks with Traefik off), deployment, git sync. Adding an env inherits vars from the first env
4. **Services** — per-service port mappings (with 🔗 env-card link checkbox), volume mappings (with **RW/RO** segmented control), restart policy, healthcheck command + timing, `depends_on`, Advanced YAML
5. **Backup** — enable/disable, target (local or configured remote), schedule, retention
6. **Review** — summary of all choices
7. **Result** — live bootstrap terminal. Detects success/failure from output and shows a ✓ / ✗ banner. **Open workspace** button enabled only on success. **← Go back & fix** button on failure. Step numbers in the top bar are clickable once visited for direct navigation. Top-bar button changes from Cancel → Close after creation starts

### Edit Workspace

- **Services** (image stacks) — name, image, tag; port rows (with 🔗 link checkbox, **RW/RO** volume toggle); volume rows; restart policy; healthcheck; depends_on; Advanced YAML
- **Environments** — domain, Traefik, SSL, deployment, replica counts, git sync; `http_port` shown only when Traefik is off on custom stacks; `https_port` removed (not operationally used)
- **Per-environment service overrides** (image stacks) — collapsible section per env, one YAML textarea per service. Appended after the base Advanced YAML for that environment only. "active" badge when any override is set
- **Environment variables** — collapsible inline editor (existing envs) or new-env editor (unsaved envs) with Show/hide values toggle
- **Add environment** — inherits vars from first env; shows "new" badge; visible immediately after save
- **Delete environment** — disabled when only one env remains
- **Dirty-state save** — **Save changes** is enabled only when something actually changed; if you've made changes, **Cancel** asks for confirmation before discarding them
- **Environment hosts** (Phase 7) — per-environment "Move to…" control to run each env on a different host (e.g. `dev` local, `stage`/`prod` remote). Changing a *deployed* env's host migrates its data; an undeployed one just repoints
- **Move the whole workspace** (Phase 7) — migrate all environments to one host at once; available only when every env currently shares the same host. Both moves warn about downtime + leftover data, run in the background, and notify you on completion

### Housekeeping page (`/housekeeping`)

Four tabs:

**Dashboard** — health badge, Docker storage breakdown (images/containers/volumes/build cache), safe quick actions (prune networks, prune dangling images), recent log.

**Safety Center** — expandable cards for: unused image pruning (multi-select), stopped container removal (type `PRUNE` to unlock), volume purging (3-second hold button countdown), build cache overhaul (slider unlock), old kernel cleanup.

**Migration Leftovers** (Phase 7) — after an environment is migrated to another host, its data/volumes/files (including `.env` secrets) are intentionally left on the source. This tab lists each leftover with a confirmed, destructive **Clean up** (wipes the stack's containers, named volumes and env-dir files on the source — over SSH for remote sources) and a **Dismiss** for ones cleaned manually. Run it before decommissioning a host so secrets can't be recovered.

**Automation & Logs** — daily automated tasks at 03:00 UTC; host OS operations (APT, journal rotation, temp cleanup — require `privileged: true` + `pid: host`); full task history table with output viewer.

### Settings page (`/settings`)

**Docker Registries tab** — add/edit/delete/test registries. Each registry appears as an option in the wizard registry dropdown.

**Backup Targets tab** — S3/object storage and SFTP remote destinations. Configured targets appear in the wizard backup step.

**Remote Hosts tab** (Phase 7) — register, test, and manage remote Docker hosts for [multi-host](#22-multi-host-support) deployment. Per host: display name, address, SSH user/port, an SSH private key (paste your own **or** toggle **Use Rigger-managed key** to install Rigger's public key instead), and an optional remote workspaces directory. **Test** verifies SSH + remote Docker; **Health** shows the host's Docker/system stats; **Scan** discovers workspaces already on the host for one-click import.

### Tools page (`/tools`)

#### Compose → Template

Converts any `docker-compose.yml` into a Rigger template JSON:

- **⎘ Paste** button (clipboard API with HTTP fallback + focus-textarea fallback)
- **↑ Import file** button (file picker for `.yml`/`.yaml`/`.txt`, auto-fills template name from filename)
- Textarea supports Ctrl+V and right-click paste natively
- Conversion: services → `images[]`; ports → `port`/`host_port`/`extra_ports`; named volumes → `./volumes/name` bind mounts; env values → `${VAR}` references with originals as `default_env_vars`; healthchecks + depends_on extracted
- Output panel matched height to input panel; shows service badges + env var count
- **⎘ Copy**, **⬇ Download**, **💾 Save as template** (writes to `templates/stacks/<name>.json` on the server — no rebuild needed)

#### Workspace Backup & Restore

- **Create backup** — workspace dropdown + Start button; async tar.gz job with live 2-second polling; shows archive filename + size on completion
- **Archives on server** — lists all `.tar.gz` archives in `/data/workspace-archives/` with date, size, Download (authenticated fetch → blob URL), Delete
- **Restore** — drag-and-drop or file picker; "Overwrite if exists" checkbox; streams restore status; workspace appears in sidebar immediately after success

### Authentication

| Mechanism | Detail |
|-----------|--------|
| Password storage | bcrypt (cost 12) |
| Access token | JWT, 15-minute expiry, in-memory only |
| Refresh token | httpOnly cookie, 7-day rolling, `/api/auth/refresh` only |
| Session restore | Silent refresh on every page load |
| Proactive refresh | Timer fires every 13 minutes to renew before expiry |
| Audit log | Every action: user, workspace, command, env, timestamp |

### Security — shell bridge

The UI never runs arbitrary shell commands. Strict allowlist in `bridge.go`, each routed to a native Go operation (no shell):

```
Allowed: start | stop | down | update | restart | ps | logs | refresh
       | backup | restore | init | version | build | promote
```

The runtime invokes `docker` with fixed argv arrays — no string interpolation, no `bash` (the image ships no shell scripts at all). Workspace names are validated against a slug regex and confirmed to exist in the known workspaces directory before any command executes.

### SQLite tables

| Table | Purpose |
|-------|---------|
| `users` | Admin accounts (bcrypt passwords) |
| `audit_log` | Every action with user, workspace, command, env, timestamp |
| `backup_targets` | S3 and SFTP remote backup destinations |
| `docker_registries` | Pre-authenticated container registries |
| `housekeeping_log` | Task name, trigger, status, freed bytes, output |
| `app_settings` | Application-wide key/value settings |
| `template_usage` | Template selection counts (popularity tracking) |
| `alert_rules` · `alert_events` | Alert rules engine + fired-events inbox (Phase 6) |
| `notification_channels` | Email/Apprise channels for alert + migration notifications |
| `backup_log` · `metrics_snapshots` | Backup outcomes + per-env CPU/mem/disk history |
| `hosts` | Registered remote hosts (encrypted SSH key, TOFU fingerprint, per-host workspaces dir) — Phase 7 |
| `workspace_host_envs` | Per-(workspace, env) host binding (absent ⇒ local) — Phase 7 |
| `migration_leftovers` | Source-host data left after a migration, awaiting cleanup — Phase 7 |
| `managed_ssh_key` | The single Rigger-managed SSH identity (encrypted private key) — Phase 7 |

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LISTEN_ADDR` | `:8080` | Bind address |
| `TOOLKIT_ROOT` | `/toolkit` | Mounted toolkit root |
| `WORKSPACES_DIR` | `/toolkit/workspaces` | Workspaces directory |
| `TEMPLATES_DIR` | `/toolkit/templates` | Stack templates directory |
| `DATA_DIR` | `/data` | SQLite DB + workspace archives |
| `REMOTE_WORKSPACES_DIR` | = `WORKSPACES_DIR` | Default workspaces path on remote hosts (Phase 7); overridable per host in the Remote Hosts tab |
| `JWT_SECRET` | — | **Required.** `openssl rand -hex 32` (also derives the AES key that encrypts stored SSH keys) |

### Volume mounts

The toolkit logic is baked into the Go binary, so there is no `/toolkit` code mount — only data:

| Mount | Mode | Purpose |
|-------|------|---------|
| `../workspaces` → `/toolkit/workspaces` | `rw` | Workspaces (config, envs, volumes, backups) |
| `../templates` → `/toolkit/templates` | `rw` | Templates (writable so Tools page can save new templates) |
| `/var/run/docker.sock` | `rw` | Docker socket |
| `/` → `/host` | `ro` | Host filesystem for real disk metrics |
| `rigger-data` → `/data` | `rw` | SQLite DB + workspace archives persistence |

### Development mode

```bash
# Terminal 1 — Go backend
cd src/backend
go run ./cmd/server

# Terminal 2 — React dev server
cd src/frontend
npm install && npm run dev
# → http://localhost:5173 (proxies /api to :8080)
```

---

## 22. Multi-Host Support

Rigger can run workspaces — or individual environments of a workspace — on **remote Docker hosts** while you manage everything from one control plane. A single Rigger instance becomes the control plane for a fleet: keep `dev` local, put `stage` and `prod` on beefier remote servers, all from the same UI.

### How it works

Rigger uses an **SSH-exec + file-sync** model (pure-Go SSH — the image ships no `ssh` client):

1. Files (compose, `.env`, build context) are generated **locally** on the control plane and pushed to the host's workspaces directory via **tar-over-SSH**.
2. `docker` / `docker compose` then run **on the remote host** over SSH, so bind mounts and build contexts resolve on the host.
3. A remote host needs only **Docker + an SSH server** — no Rigger binary, no extra agent.

Lifecycle commands (`start`, `stop`, `restart`, `ps`, `logs`, `update`, `backup`, `restore`) are **host-transparent**: the UI/CLI are unchanged; Rigger resolves each environment's host and runs the command in the right place.

### Registering a host

**Settings → Remote Hosts → Add host:**

| Field | Notes |
|-------|-------|
| Display name / Address / SSH user / SSH port | Connection details |
| SSH private key | Paste your own (passphrase-less PEM/OpenSSH), **or** toggle **Use Rigger-managed key** |
| Remote workspaces directory | Absolute path on the host where workspaces live and are pushed (e.g. `/opt/rigger/workspaces`). Blank = the `REMOTE_WORKSPACES_DIR` default |

- **Use Rigger-managed key** — Rigger generates and holds one SSH identity; the form shows its **public** key with a one-click install command (`echo … >> ~/.ssh/authorized_keys`). The private key never leaves Rigger / never transits your browser.
- **Test** — SSH-connects and runs `docker version` on the host (captures the host-key fingerprint on first connect, TOFU).
- **Health** — shows the host's Docker + system stats (version, containers, images, OS, CPU, memory, disk, uptime) over SSH.
- **Scan / Import** — lists workspaces already present in the host's workspaces directory and imports the selected ones (host-badged thereafter).

> If **Scan** finds nothing even though a workspace exists, set the host's **Remote workspaces directory** to the real path — the default points at the control plane's container path, which usually doesn't exist on a bare host.

### Per-environment host binding

Each environment is either **local** (default) or bound to **one remote host**. The binding is per-`(workspace, env)`, so different environments of the same workspace can live on different hosts.

- **New Workspace wizard** — a **Default host** selector on the Project step pre-fills each environment's **Host** dropdown (overridable per env on the Environments step). Binding is recorded at creation; files are pushed and the stack starts on the host the first time you deploy.
- **Edit Workspace → Environment hosts** — a per-env "Move to…" control. Changing a **deployed** env's host migrates its data; an **undeployed** one just repoints (provisions on next deploy).

### Moving / migrating

- **Per environment** — change one env's host from *Edit Workspace → Environment hosts*.
- **Whole workspace** — *Edit Workspace → Move the whole workspace* moves every environment at once; available only when all environments currently share the same host (otherwise use the per-env controls).

A deployed migration: back up on the source → stop the source stack → ship files (incl. `.env` so secrets move) → repoint → start + restore on the target. **The source copy is stopped but its data is left intact.**

Both moves:
- **Warn first** about the **downtime** (the env is down until it's back up on the target) and the **leftover data** left on the source.
- **Run in the background** — you get a notification (in-app alert bell + any configured notification channels) when the move completes, so you can leave the page. Live progress is shown while you stay.

### Cleaning up after a move

A deployed migration deliberately leaves the source host's containers, volumes and files (including `.env` secrets) in place. Wipe them from **Housekeeping → Migration Leftovers** before decommissioning a host — important so the data/secrets can't be recovered by whoever gets the machine next.

### Security

- **SSH keys** are encrypted at rest (AES-256-GCM, key derived from `JWT_SECRET`) and never returned by the API.
- **Host keys** use trust-on-first-use: the fingerprint is captured on first successful connect and verified thereafter; a changed key is refused.
- **`.env` is host-authoritative** — for a remote env, the host's `.env` is never overwritten by a deploy; only the deterministic compose file is pushed.

### Current limitations

- `build` / `promote` are **not yet supported** for remote-bound workspaces (they need the full build context + a remote registry login) and fail with a clear message — build/promote locally, or migrate after building.
- Editing a remote env's variables in the UI writes the **local** cache only (it doesn't push to the host yet).
- The read/connectivity paths (register, test, scan/import, health, status, metrics) and the file-push/exec plumbing are verified; full **deployed-environment** migration and remote backup/restore should be validated against your real hosts before production use.

---

## 23. Maintenance Guide

### Adding a new pre-built template

1. Create `templates/stacks/<name>.json` — see Section 16 for the schema
2. No registration needed — the wizard discovers templates by globbing `*.json`
3. Or use **Tools → Compose → Template** in the UI to generate the JSON from an existing compose file, then save it directly from the browser

### Adding a new stack type (backend/frontend)

1. Add Dockerfiles to `templates/dockerfiles/<name>/`
2. Add Nginx config to `templates/nginx/<name>.conf`
3. Add the choice to the New/Edit Workspace wizard (`src/frontend/src/pages/`)
4. Add the service definition to the Go compose generator (`src/backend/internal/composegen/builders.go`)
5. Add the default image tag to `defaultVersions` in `src/backend/internal/workspace/create.go`

### Adding a new environment to an existing workspace

**Option A — UI (recommended):**
Edit Workspace → Add environment → fill in settings → Save. The new env appears immediately and is bootstrapped on first deploy.

**Option B — CLI:**
```bash
# Add the env block to config.json, then:
rigger myapp <new_env> init
# fill in envs/<new_env>/.env (or let init auto-generate secrets)
rigger myapp <new_env> start
```

> Port collision: ensure `http_port` is unique across all environments on the same host.

### Updating dependency versions

```json
"versions": { "postgres": "16-alpine" }
```
```bash
rigger myapp <env> refresh   # regenerates compose + pulls new image
```

---

## 24. Troubleshooting

### Compose file looks stale or malformed

`docker-compose.yml` is generated natively in Go (`internal/composegen`) from `config.json` — the old class of Bash string-mangling/escape bugs no longer applies. If a workspace's compose file is out of date (e.g. edited config.json directly on disk), run `refresh <env>` to regenerate it cleanly.

### Backup archive download not working

The download endpoint requires authentication (Bearer token). Use the **Download** button in the UI — it uses an authenticated `fetch()` call with a blob URL, not a direct `<a href>` link, which would fail without the token.

### Image update check shows "? digest unknown"

The `latest` tag update check compares local image digest against the remote. If the local image has no `RepoDigest` (common when images are pulled via compose without an explicit `docker pull`, or built locally), the comparison is indeterminate. Run **Update** to pull from the registry and populate the digest, then the check will work on subsequent hourly runs.

### Log viewer shows stuck "Backing up" status

Earlier versions had a path index bug in the backup job polling endpoint. Fixed in the current version — the job status is now polled via React Query with automatic retry and the correct path segment index.

### Port already in use

Each environment must have a unique `http_port`. Check `config.json` and `docker ps -a`. Default assignments: dev=8080, stage=8180, prod=80.

### Remote host: "No workspaces found" on Scan

The scan looks under the host's **Remote workspaces directory**. The default is the control plane's container path (`/toolkit/workspaces`), which usually doesn't exist on a bare remote host. Edit the host (Settings → Remote Hosts) and set its **Remote workspaces directory** to the real path on that machine (e.g. `/opt/rigger/workspaces`), then scan again. The same path is used when pushing files for deploy/migrate.

### Remote env shows as down / no metrics

Status, container lists, and live/historical metrics are gathered **on the env's host over SSH**. If a remote env reads as down or shows no metrics, confirm the host's **Test** is green and its **Remote workspaces directory** is correct, and that the stack was actually deployed there (the per-env 🖥 badge confirms the binding).

### Running behind Cloudflare

Set Cloudflare SSL mode to **Full** (not Flexible). Flexible mode sends plain HTTP to your server, which breaks the Let's Encrypt HTTP-01 challenge that Traefik uses for certificate issuance.
