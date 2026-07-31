# Rigger — More Dev, Less Ops

> **Yes, it's called Rigger.** It's the deckhand who lashes your containers to the crane, double-checks every knot, and hoists them into prod without dropping one in the harbor. It remembers exactly which line went where, never fat-fingers a `docker run` at 2 a.m., and quietly judges you for deploying on a Friday. You bring the cargo — Rigger handles the heavy lifting, the rigging, and the part where everything stays afloat.

**Rigger is a self-hosted PaaS for Docker and Docker Swarm.** Point it at a server, and it gives you the parts of a managed platform you actually miss when you self-host: a web UI to create apps, build them from a Git repo, give them a real domain with automatic HTTPS, attach a managed database, back them up on a schedule, and watch them — across dev, staging and production, on one host or a fleet.

The whole runtime is a single ~15 MB Go binary. Your servers need nothing but Docker and SSH.

---

## Contents

**Start here**

- [What you get](#what-you-get)
- [What Rigger is not](#what-rigger-is-not)
- [Requirements](#requirements)
- [Install](#install)
- [Core concepts](#core-concepts)
- [Your first project](#your-first-project)

**Building apps**

- [Creating a project](#creating-a-project)
- [Building from source](#building-from-source)
- [Pre-built stack templates](#pre-built-stack-templates)
- [Managed databases and services](#managed-databases-and-services)

**Configuring**

- [Environment configuration](#environment-configuration)
- [Settings levels](#settings-levels)
- [Secrets](#secrets)

**Deploying and routing**

- [Compose and Swarm](#compose-and-swarm)
- [Build, version and promote](#build-version-and-promote)
- [Domains, TLS and routing](#domains-tls-and-routing)
- [App exposure](#app-exposure)
- [Multiple hosts](#multiple-hosts)

**Operating**

- [Deployment pipelines](#deployment-pipelines)
- [Preview environments](#preview-environments)
- [Backup and restore](#backup-and-restore)
- [Alerting and notifications](#alerting-and-notifications)
- [Metrics](#metrics)
- [Maintenance mode and Danger Zone](#maintenance-mode-and-danger-zone)
- [Housekeeping](#housekeeping)
- [Updating Rigger](#updating-rigger)

**Platform**

- [Users, roles and auth](#users-roles-and-auth)
- [REST API](#rest-api)
- [Proxy service](#proxy-service)
- [Git providers and registries](#git-providers-and-registries)

**Reference**

- [The UI at a glance](#the-ui-at-a-glance)
- [Command reference](#command-reference)
- [Configuration reference](#configuration-reference)
- [Current constraints](#current-constraints)
- [Troubleshooting](#troubleshooting)

---

## What you get

- **A web UI for the whole lifecycle** — create, build, deploy, route, back up, monitor. No YAML by hand.
- **Apps from anywhere** — a Git repo, an uploaded archive, a Docker image, a pasted `docker-compose.yml`, or one of 37 curated one-click stacks.
- **Builds without Dockerfiles** — ten framework templates, or Nixpacks auto-detection for everything else.
- **Real URLs with automatic HTTPS** — Traefik plus Let's Encrypt, including wildcard certs and custom domains. No public DNS? You still get working URLs via magic DNS.
- **Managed dependencies** — PostgreSQL, MySQL, MariaDB, MongoDB, Redis, MinIO, OpenSearch, VictoriaMetrics, RabbitMQ, provisioned and wired into your app's environment automatically.
- **Environments that behave like environments** — dev, stage and prod per project, each with its own data, config and domain, and a promote path that ships the exact image you validated.
- **A fleet from one control plane** — run environments on remote Docker hosts over SSH. Remote hosts need no agent.
- **Operations built in** — scheduled backups with off-site sync, deployment pipelines with approval gates, preview environments per pull request, alerting, metrics, and a rollback that understands what kind of stack it's rolling back.

## What Rigger is not

Being clear up front so you can evaluate it honestly:

- **It targets Docker and Docker Swarm only.** There is no Kubernetes, ECS or Cloud Run support, and none is planned. If you already run Kubernetes, Rigger is not what you want.
- **It is not multi-tenant SaaS.** Workspaces scope and separate teams within one installation, but everyone shares one control plane and its Docker hosts.
- **It is not a managed cloud.** You own the servers, the upgrades and the backups. Rigger automates them; it does not run them for you.
- **It does not build or manage your Swarm cluster.** Rigger deploys stacks to a Swarm you have already initialised and schedules across every node in it, but joining, promoting and draining nodes stay your job, with Docker's own tooling.

---

## Requirements

| Tool | Needed for | Install |
|------|-----------|---------|
| `docker` + Compose v2 plugin | Everything | [docker.com](https://docs.docker.com/get-docker/) or the installer below |
| `curl` | The installer and the host CLI wrapper | Ships with most systems |

The engine runs entirely inside its container as a single Go binary, so hosts need no `bash`, `jq`, `openssl`, `git` or `ssh`. Builds happen inside Docker; remote hosts are reached with a pure-Go SSH client.

**Supported host OS:** Ubuntu/Debian, RHEL/CentOS/AlmaLinux/Fedora, Arch, Alpine, and macOS with Docker Desktop.

> **Planning to use Swarm?** Run `docker swarm init` **before** installing, so the shared `traefik_net` is created as an `overlay --attachable` network from the start and Compose and Swarm projects can coexist. Installing Compose-first and enabling Swarm later works, but needs a one-time network conversion — see [Compose and Swarm](#compose-and-swarm).

---

## Install

One line. It detects your OS, installs dependencies, generates a JWT secret, and starts Rigger:

```bash
curl -sSL https://raw.githubusercontent.com/mansoor/rigger/main/install.sh | bash
```

With overrides:

```bash
curl -sSL https://raw.githubusercontent.com/mansoor/rigger/main/install.sh \
  | RIGGER_DIR=/opt/rigger RIGGER_PORT=9090 ACME_EMAIL=admin@example.com bash
```

| Variable | Default | Description |
|----------|---------|-------------|
| `RIGGER_DIR` | `~/rigger` | Install directory |
| `RIGGER_PORT` | `9999` | UI host port |
| `ACME_EMAIL` | — | Let's Encrypt contact email (required for HTTPS) |
| `SKIP_DOCKER` | `0` | Set to `1` to skip the Docker installation check |

Then open `http://localhost:9999` — the first visit prompts you to create an admin account.

**Manual install:**

```bash
git clone https://github.com/mansoor/rigger.git
cd rigger/src
cp .env.example .env
# Set JWT_SECRET (openssl rand -hex 32) and ACME_EMAIL in .env
docker network create traefik_net    # on a Swarm manager: add -d overlay --attachable
docker compose up -d
```

The image is pulled from GHCR (`ghcr.io/mansoor/rigger`).

**Uninstall:** `./uninstall.sh` in the install directory.

---

## Core concepts

Three levels:

```
Workspace  (a team, tenant or grouping)
  └─ Project  (the deployable unit — an app plus its dependencies)
       └─ Environment  (dev · stage · prod · …)
```

- **Workspace** — a top-level grouping, chosen from the nav's workspace switcher. It owns members, hosts, registries, Git providers, backup targets, notification channels, domains, and its own settings, which fall back to global defaults.
- **Project** — what you build, deploy and operate. Contains one or more environments.
- **Environment** — a named tier of a project, each with its own `.env`, generated `docker-compose.yml`, and data.

Both workspaces and projects have a short lowercase **key** and a free-form display **name**. The key is permanent; the name you can change any time.

Two things worth understanding early:

- **`resource_prefix`** is the immutable Docker naming prefix `{workspaceKey}_{projectKey}`. Every container, volume, network and stack an environment creates is named `{resource_prefix}_{env}_…`. It is fixed at creation, so renaming a project can never orphan running resources.
- **`config.json`** is the single source of truth for a project. Stack, services, environments, managed dependencies, routes, domains and backup schedules all live there. Compose files are **generated** from it, never hand-maintained — edit config (in the UI or the file), then **refresh** the environment to regenerate and redeploy.

API and URL paths mirror the hierarchy: `/api/workspaces/{workspace}/projects/{name}/envs/{env}/…`.

**Design principles**

- **Config-driven.** `config.json` is authoritative; generated compose files are disposable.
- **Self-contained projects.** Everything needed to operate a project lives under `workspaces/<ws>/projects/<name>/` — archivable, movable, restorable.
- **No host toolchain.** Every build step runs inside Docker.
- **Bind mounts by default.** Volume data lives in `envs/<env>/volumes/` on the host — readable, backupable, portable.
- **One control plane, many hosts.** Any project or individual environment can run on a remote host.

### How it fits together

```
┌──────────────────────────────────────────────────────────────────┐
│  rigger        Go server + embedded web UI                        │
│                REST + WebSocket + SSE                             │
├──────────────────────────────────────────────────────────────────┤
│  rigger-traefik        Traefik v3.4 edge, ports 80/443            │
│  rigger-socket-proxy   keeps Traefik off the raw Docker socket    │
│  rigger-fallback       friendly "app starting" catch-all page     │
└───────────────────────────────┬──────────────────────────────────┘
                                │ generates and operates ↓
        ┌───────────────────────▼────────────────────────┐
        │  workspaces/<ws>/projects/<name>/               │
        │    config.json          ← source of truth       │
        │    envs/dev|stage|prod/                         │
        │      .env, docker-compose.yml, volumes/         │
        └─────────────────────────────────────────────────┘
```

Those four containers are the whole control plane, running on a shared `traefik_net` network. Traefik's static configuration is owned by Rigger, so proxy plugins toggle from the UI without editing compose files.

---

## Your first project

1. **Open the UI** at `http://localhost:9999` and create your admin account.
2. **Create a workspace** from the switcher in the nav (or use the default).
3. **New project** → pick a stack type. The quickest first win is a **pre-built template** — pick WordPress or Uptime Kuma, name it, accept the defaults.
4. **Environments step** — keep `dev`. Leave Traefik on.
5. **Review and create.** The final step streams the bootstrap live.
6. **Deploy** from the project page. When the status dot goes green, the env card shows a clickable URL.

No public DNS needed — with no base domain configured you get a working magic-DNS URL like `myws-myapp-dev.192.168.1.50.sslip.io`. See [Domains, TLS and routing](#domains-tls-and-routing).

For automation instead of the UI, see [Command reference](#command-reference).

---

## Creating a project

Projects are created through the **New Project wizard**. Six stack types:

| Type | What it does |
|------|--------------|
| **Pre-built template** | Deploy a curated stack (WordPress, Vaultwarden, Uptime Kuma, …) from the [template library](#pre-built-stack-templates). |
| **Image stack / Docker Compose** | Deploy ready-made images (name, tag, ports, volumes), or paste/fetch a `docker-compose.yml` and let Rigger convert it into a project. |
| **Managed service hosting** | Provision databases, Redis, object storage, search or metrics only — no app code. Useful for a shared database. |
| **From a Git repository** | Scan a repo, review the detected services, build from source. |
| **Start from a stack template** | No repo yet — pick a framework and get a runnable starter scaffolded into a fresh Git repo. |
| **Custom application** | Upload a `.zip`/`.tar.gz`, auto-detect the stack, build. |

The wizard runs seven steps: **Project** (name, registry, default host) → **Stack** → **Environments** (name, domain, Traefik, SSL, deployment mode, host) → **Services** (ports, volumes, healthcheck, web entry, managed dependencies) → **Backup** → **Review** → **Result** (live bootstrap terminal).

---

## Building from source

Rigger builds images from source with one of two backends, chosen per service.

### Framework templates

When a stack is recognised, Rigger generates a Dockerfile for it. Ten frameworks ship today:

| Backend | Frontend |
|---------|----------|
| `laravel` (PHP-FPM + Composer + Nginx) · `nodejs` · `django` · `go` · `rails` · `spring` / `spring-gradle` · `dotnet` | `nextjs` (standalone) · `react` (Vite → Nginx) |

Each framework carries an environment contract, so a detected app wires up to its managed dependencies with no hand-mapping. Laravel gets discrete `DB_*` and `AWS_*` variables; Node, Next, Django and Go get `DATABASE_URL` and `REDIS_URL`; Rails gets a `mysql2://` URL; Spring gets `SPRING_DATASOURCE_*`; .NET gets `ConnectionStrings__*`.

Stack detection never executes your code. It reads the filesystem in signal order: an existing `docker-compose.yml`, then Dockerfiles (monorepo-aware), then language manifests, then `Procfile` for workers, then dependency and env hints for managed dependencies.

### Nixpacks (no Dockerfile)

Any build service can switch to **Nixpacks** with **Build method: Nixpacks** on the service card. Nixpacks auto-detects and builds without a Dockerfile — the escape hatch when a generic template doesn't fit (native dependencies, monorepos, unusual runtimes), and the fallback for languages Rigger doesn't template.

When detection can't identify a framework at all, the scanner seeds a single web-routed `app` service that builds with Nixpacks, so unrecognised languages are still deployable. Nixpacks produces a normal OCI image, so routing, managed dependencies, image pointers, rollback and pipelines all work unchanged.

The Nixpacks CLI is bundled in the Rigger image for local builds. For remote build hosts, install it with one click from **Settings → Remote Hosts**.

### Starter scaffolding

Choosing **Start from a stack template** with the scaffold option generates a minimal runnable app, commits it, and pushes it to a fresh Git repo you name (or offers a ZIP). Starters ship for **django, go, laravel, nodejs and react**. After that first push, every push rebuilds and deploys.

### Importing an existing app

- **From Git** — scan a repo, adding a [Git provider](#git-providers-and-registries) for private ones. Handles monorepos and nested subdirectories, multi-file compose overlays, profile-gated services, bundled database seeds, and per-service pre-deploy commands. It offers to swap a database the repo runs itself for a Rigger-managed one. Cookiecutter, Copier and Yeoman *template* repositories are detected and refused — they aren't apps.
- **From an archive** — drop a `.zip` or `.tar.gz`. It is extracted safely (hardened against zip-slip, symlink and bomb attacks, never executed), detected, and reviewed through the same flow. A Dockerfile is generated if the archive has none.

---

## Pre-built stack templates

**37 templates** ship ready to deploy:

```
activepieces · actual-budget · adguard-home · authentik · bookstack · code-server
cyberchef · dockhand · flowise · ghost · gitea · grafana · homepage · immich
it-tools · mealie · metabase · minio · n8n · nextcloud · nginx-proxy-manager
nocodb · pairdrop · paperless-ngx · plausible · pocketbase · privatebin · sftpgo
sonarqube · syncthing · teslamate · transmission · umami · uptime-kuma
vaultwarden · wg-easy · wordpress
```

Each declares its images, ports, volumes, healthchecks, default environment variables, any seed config files, and — for multi-service stacks — which service the domain routes to.

**Secrets are generated for you.** A placeholder secret in a template (any `*PASSWORD*`, `*SECRET*`, `*TOKEN*`, `*KEY*` or `*SALT*` key) is replaced with a generated value at create time, written to `.env`, and pinned so it survives regeneration and keeps matching the initialised data volume. Non-secret placeholders like ports and hostnames are left for you to fill in.

**Template Manager** (Tools) lets you create, edit, validate and save templates in the browser: convert a `docker-compose.yml`, generate one from an existing project (secrets masked), or upload a template file. Saving writes it live — no rebuild.

> **Templates bring their own database.** A pre-built stack bundles its own version-pinned database service so it stays self-contained, and backups cover it. That is separate from the managed-dependency catalog below; Rigger will not inject a managed database into a template that ships one.

---

## Managed databases and services

Rigger provisions dependencies from a catalog. They are **project-level** — shared across a project's environments — and configured in the wizard's Services step and under Edit Project → Services.

### Databases

One **primary database** per project:

| Engine | Default | Port | Notes |
|--------|---------|------|-------|
| **PostgreSQL** | `15-alpine` | 5432 | Schema and user management |
| **MySQL** | `8.0` | 3306 | Schema and user management |
| **MariaDB** | `11` | 3306 | Uses the MySQL contract |
| **MongoDB** | `7` | 27017 | Connection info only — no schema/user management |

Plus **auxiliary engines** that run alongside a primary database:

| Engine | Category | Port | Notes |
|--------|----------|------|-------|
| **OpenSearch** | search | 9200 | HTTPS with the security plugin; needs `vm.max_map_count=262144` on the host |
| **VictoriaMetrics** | time-series | 8428 | Prometheus-compatible |
| **RabbitMQ** | queue | 5672 | AMQP; the `-management` image adds a web UI on 15672 |

The connection contract is written into each service's `.env`, and framework templates translate it to framework-native keys. Passwords are generated once and pinned, so they survive regeneration — see [Secrets](#secrets).

Only the **external port** is per-environment, which is what lets you expose a database on dev and keep it closed on prod. Everything else is project-level.

### Object storage, Redis and cache

- **Object storage** — a local persistent volume, or **MinIO** for a managed S3. MinIO emits the standard S3 environment contract (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_BUCKET`, `AWS_ENDPOINT`, …) and creates the per-environment bucket on first start.
- **Redis** — a project-level toggle that emits `REDIS_HOST`, `REDIS_PORT` and `REDIS_URL`.

### Consoles and per-environment sidecars

Several helper UIs are per-environment toggles (on, off, or inherit the project default):

- **Adminer** for SQL and **mongo-express** for Mongo, with one-click signed auto-login.
- **MinIO console** for the object store.
- **Mailpit** — a catch-all test SMTP server. When on, the app's mail is wired to it and the inbox UI is served.

The **Service Console** on each env card gives a tabbed view of every enabled service — database, Redis, S3, storage, Mailpit, search, time-series, queue — with a single operator-gated "reveal credentials" toggle and links to open each UI.

### Database users and schemas

**Manage Database** lists and creates schemas and dedicated per-schema users, with passwords encrypted at rest. It runs the database client *inside* the container, so no port needs exposing. It is deliberately list-and-create only, not a free-form SQL console. SQL engines only.

---

## Environment configuration

Everything lives in `config.json`. Edit it in the UI or on disk, then refresh the environment.

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

`http_port` matters only for custom stacks with Traefik **off** — it's the host port Nginx binds. Under Traefik it isn't needed.

### Service fields

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

- **`link_ports`** — which host ports appear as clickable links on the env card.
- **`extra_compose`** — raw YAML appended to the service block (memory limits, CPU, logging). Per-environment overrides go under `environments.<env>.service_overrides.<service>.extra_compose`.
- **Processes** — worker and scheduler services that reuse the app image with a different command and no ports.
- **Pre-deploy command** — a build service's pre-deploy step becomes a one-shot migration container that must succeed before the app starts. Compose only.

### Secret environment variables

Any variable can be flagged as a secret in the editor. On Compose it stays in `.env`; on Swarm it becomes a native Docker secret. See [Secrets](#secrets).

> **`NODE_ENV` is always `production`** in deployed containers, whatever the environment tier is called. The tier means "which data and config", not the Node runtime mode — a pruned production image running with `NODE_ENV=development` will crash-loop. Override it in project variables if you genuinely want dev mode.

---

## Settings levels

Settings resolve most-specific-first across four tiers:

```
User  →  Project (config.json)  →  Workspace  →  Global (admin)
```

- **Global** (Admin → Settings) — ACME email, base domain, app host/IP, auto-URL mode, DNS provider and token, destructive-action confirmation defaults, password policy, default appearance.
- **Workspace** (Manage Workspace) — overrides most global keys, plus workspace defaults for registry, host, backup target and build target, environment tier names, and the wipe-allowed environment list.
- **Project** — everything in `config.json`.
- **User** — appearance and confirmation preferences, where the tiers above allow an override.

---

## Secrets

Two separate mechanisms.

### Flagged secrets

Variables you mark as secrets are stored plainly in `.env` for **Compose** deployments, and as **native Docker Swarm secrets** for **Swarm** deployments, mounted at `/run/secrets/<KEY>` (databases use the `<KEY>_FILE` convention). Rigger runs no custom cryptography — Swarm encrypts secrets at rest in its Raft log. Because Swarm secrets are immutable, rotation creates a new version, redeploys, and removes the old one. An audit trail records the key and the action, never the value.

### Managed-secret pinning

Passwords Rigger generates for you — database, app, MinIO — are **pinned** in `config.json`, captured on first generation and re-captured on every deploy. The pin records what the data volume was actually initialised with, so it **wins over `.env`**: if `.env` ever drifts, a regeneration heals it back to the pinned value rather than locking your app out of its own database. This is what prevents the classic PostgreSQL `P1000` authentication failure after a config regeneration. It runs automatically and has no UI.

---

## Compose and Swarm

### Docker Compose (default)

```json
"deployment": "compose"
```

Best for development and single-host staging or production.

### Docker Swarm

```json
"deployment": "swarm",
"replicas": { "backend": 2, "frontend": 1 }
```

Enables replica scaling. Per-environment Swarm settings — replica counts, restart policy, update and rollback config, placement — are emitted into the compose `deploy:` block. Requires `docker swarm init`.

The shared `traefik_net` must be **overlay and attachable** for Swarm stacks. The easiest path is running `docker swarm init` before installing Rigger. To convert an existing Compose install:

```bash
docker network rm traefik_net && docker network create -d overlay --attachable traefik_net
```

**Switching an environment between modes** automatically tears down the old deployment first — while the old config can still resolve it — so its network and containers can't collide with the new one of the same name.

---

## Build, version and promote

### Build

After a build, each service's image pointer in `.env` advances to the new tag **only if it was tracking** the previous one. A pointer set to anything else is treated as a deliberate pin (from a rollback or a hand edit) and left alone.

**Rebuild policy** — `if-changed` skips the build when the source signature is unchanged; `force` always rebuilds. Build arguments expand `${ENV}`, `${VERSION}` and `${ROUTE_URL}`.

### Version

| Command | Result |
|---------|--------|
| `version bump` | `1.2.3-build.41` → `1.2.3-build.42` |
| `version bump patch` | → `1.2.4-build.0` |
| `version bump minor` | → `1.3.0-build.0` |
| `version bump major` | → `2.0.0-build.0` |
| `version set 3.0.0-build.0` | exact version |

Image tags follow `registry/project-service:2.1.0-build.47-stage`.

### Promote

Promotion retags the exact image from one environment to the next — pull, tag, push, **no rebuild** — then deploys. You ship byte-for-byte what you validated.

**Recommended flow:** develop on `dev` → build and push to `stage` → validate → promote `stage` → `prod`.

### Image update detection

For image-based stacks Rigger checks the registry hourly:

- **Update available** — a newer digest for the *same* tag. Shows an amber badge; one-click Update clears it.
- **Newer version available** — a newer stable tag exists. Informational, with its own alert.
- **Digest unknown** — the local image has no registry digest (pulled without an explicit `docker pull`, or built locally), so the comparison can't be made.

---

## Domains, TLS and routing

### How an environment gets its URL

Three layers, most specific first:

1. **Base domain** — set on the workspace or globally. Each environment gets `{ws}-{project}-{env}.{base}` over **HTTPS with Let's Encrypt**.
2. **Magic DNS** — with no base domain, pick `sslip`, `nip` or `traefikme` to get `{ws}-{project}-{env}.<host-ip>.sslip.io` over HTTP. No DNS configuration at all. This is what makes a fresh install immediately usable.
3. **localhost** — the fallback.

### App host / IP

The **app host** is the address users reach this server's apps at. It builds the direct `host:port` links and the magic-DNS hostname for locally deployed environments; remote environments use their own host's address.

- Seeded at install from the detected host IP. Change it only if the host's address changes.
- The server runs in a container and can't see the host's LAN IP by itself. **Detect** works on native Linux; on Docker Desktop it returns the VM address, so enter it manually there.
- **It must be an IP** for sslip and nip URLs. With a base domain, the app host is irrelevant.
- **Changing it warns you.** The magic-DNS hostname is baked into each running app's routing labels, so changing the IP updates displayed URLs but running containers keep the old one until redeployed. As you edit the field, Rigger lists which environments would break and offers to refresh and redeploy them on save — redeploying running environments in place and only regenerating config for stopped ones. Reverting the value dismisses the warning.

### Certificates

Traefik carries two certificate resolvers: **HTTP-01** per host (the default) and **DNS-01** via Cloudflare. With a Cloudflare token configured, base-domain environments request a single `*.{base}` **wildcard** certificate; otherwise each hostname gets its own.

The ACME email resolves per environment → workspace → global. Per-environment and per-workspace overrides are issued out-of-band and hot-reloaded by Traefik without a restart, with a twice-daily renewal check.

### Custom domains

Add external domains to an environment alongside its automatic subdomain. Prove ownership by **TXT record**, **CNAME**, or an **HTTP file**, and Rigger issues a certificate and adds an HTTPS router with an HTTP redirect. One domain can be marked primary, which makes it canonical for the app's URL.

### Routing table

A project's **Routing** tab maps **path prefixes** or **subdomains** on the environment's domain to specific services. Path routes support prefix rewriting, which is how you serve version aliases. The catch-all `/` route owns the wildcard certificate and any custom domains.

### Traefik versus direct ports

With Traefik **off**, a custom stack's Nginx binds `http_port` and image stacks map each host port directly. With Traefik **on**, routing is by domain and the web service's host port is redundant, so it's stripped by default (configurable per workspace and per environment). Extra ports and non-web services always publish.

---

## App exposure

Per environment, `expose_mode` controls reachability and `auth_gate` adds an access gate:

| `expose_mode` | Behaviour |
|---------------|-----------|
| **`traefik`** (default) | Public Traefik router, as above. |
| **`cloudflare_tunnel`** | No public router — a `cloudflared` sidecar makes an outbound-only tunnel. Good for exposing an app without opening a firewall port. |
| **`none`** | Internal only, with an optional explicit host port and optional attachment to an external Docker network. |

| `auth_gate` | Behaviour |
|-------------|-----------|
| **`none`** (default) | No gate. |
| **`basic`** | Traefik basic authentication from an htpasswd value in `.env`. |

Admin sidecars such as Adminer have their own separate basic-auth switch.

---

## Multiple hosts

Rigger runs projects — or individual environments — on **remote Docker hosts** while you manage everything from one control plane. Keep `dev` local and put `stage` and `prod` on real servers, all from one UI.

### How it works

Files are generated locally and pushed over SSH; `docker` and `docker compose` then run **on the remote host**, so bind mounts and build contexts resolve there. A remote host needs only **Docker and an SSH server** — no Rigger binary, no agent.

Lifecycle commands are host-transparent: Rigger resolves each environment's host and runs the command in the right place. On first deploy it provisions the Traefik edge and network on the remote host automatically.

### Registering a host

| Field | Notes |
|-------|-------|
| Name, address, SSH user and port | Connection details |
| SSH private key | Paste a passphrase-less key, or use a **Rigger-managed key** — Rigger holds one identity and shows you a one-liner to install its public key. The private key never passes through your browser. |
| Remote workspaces directory | Absolute path where projects live on that host, e.g. `/opt/rigger/workspaces` |

**Test** connects and runs `docker version`, capturing the host key on first use. **Health** shows the host's Docker and system stats. **Scan** lists projects already on the host so you can import them.

### Public versus private hosts

Each host is flagged for how it's reached, which decides how its apps get a public URL:

- **Public (direct)** — the host is its own front door with its own address. Its apps route directly at the host, which issues its own certificates. With a base domain and Cloudflare token, Rigger can create the DNS record pointing at it.
- **Private (behind gateway)** — only the control plane is exposed, and it fronts the host's apps by proxying to the host's edge. Use this for LAN and homelab machines with no public IP.

Each environment is either local or bound to one remote host, so different environments of a project can live on different machines.

### Moving between hosts

Migrate a single environment or a whole project. A migration backs up the source, ships the files including `.env`, repoints the environment, and restores on the target. **The source copy is stopped but its data is left intact** — clear it from [Housekeeping](#housekeeping) once you're satisfied. Migrations warn about downtime, run in the background, and notify you when they finish.

---

## Deployment pipelines

Pipelines are ordered stage lists — lightweight CI/CD scoped to a project.

- **Stage types:** deploy · refresh · update · build · restart · backup · test · script · version · promote · **gate**.
- **Runs** happen in the background, with live per-stage progress and logs you can close and reopen. The log viewer has search, per-stage anchors, line numbers and download. History keeps recent runs; runs can be cancelled.
- **Approval gates** pause a run until someone approves or rejects it.
- **Webhooks** trigger a pipeline on push, with a hashed URL token and optional HMAC secret.
- **Notifications** — per-pipeline started, succeeded and failed events fan out to your notification channels, naming the failing stage.
- **Generate from environments** drafts a release pipeline (build and bump → deploy → promote chain, with an optional gate) or a hotfix pipeline from your environment topology. The draft opens in the editor; nothing is saved automatically.

### Rollback

Every deploy snapshots its resolved image references. **Source-built stacks** roll back by pinning a previous entry's images and redeploying. **Image stacks**, which have no per-environment image override, roll back by restoring a backup instead. The env card's Rollback button picks the right method.

---

## Preview environments

Opt in per project. A signed webhook from **GitHub or Gitea** spins up an ephemeral `pr{n}` environment cloned from a template environment when a pull request opens, redeploys it on every push, and tears it down when the PR closes.

Configurable: template environment, provider, branch filter, fork policy, maximum concurrent previews, TTL, database strategy (isolated empty, isolated with seed, or cloned), authentication gate, and PR write-back. Write-back posts a commit status and comment on the pull request.

A reaper runs every 30 minutes and tears down previews past their TTL, in case a "closed" webhook is ever missed.

---

## Backup and restore

### Environment snapshots

Snapshots write to `backups/{env}/{timestamp}/` with a manifest. SQL databases get a compressed logical dump taken *inside* the database container, so no client is needed on the host. Non-SQL engines and file volumes are archived at the volume level. Restoring stops the stack, restores the database, restores volumes, and starts it again.

- **Backup targets** — S3-compatible object storage and SFTP destinations, with a connectivity test.
- **Sync** — push a snapshot to a target, automatically after archive backups.
- **Schedules** — per environment, every 2/4/6/12 hours, daily or weekly. The scheduler survives restarts, and count-based retention keeps the newest N.
- **Health** — the dashboard shows per-environment backup coverage with a verdict: current, stale, never, or disabled.
- **Verify** — a non-destructive dry run checks every file in a snapshot is present, non-empty and intact before you commit to restoring.

### Whole-project artifacts

Two portable files, from Tools → Backup & Restore:

- **`.rps` (Snapshot)** — configuration only: `config.json`, each environment's `.env`, and a portable database bundle. Fast, and rolling back overwrites configuration in place without touching data.
- **`.rpb` (Backup)** — the full project: the whole directory, the latest data snapshot per environment, and the database bundle.

Both embed pipelines and their webhooks, alert rules, custom domains, maintenance schedules and host bindings, and both re-import them on restore. They exclude deploy history, access grants, raw database-user secrets and logs.

### Migrating versus copying an environment

- **Migrate data** moves *data* between two existing environments: it backs up the target for safety, backs up the source, then restores the source into the target. The source is untouched; the target is overwritten. Requires the operator role and a typed confirmation.
- **Copy environment** clones *configuration* — the environment block, config files and a fresh `.env`, with the domain blanked and optional secret regeneration. It starts with **empty volumes; no data is copied**.

---

## Alerting and notifications

Rules are re-evaluated every 60 seconds, with an open/resolve state machine that clears an alert when the condition lifts, and a per-rule cooldown (15 minutes by default).

| Category | Rules |
|----------|-------|
| Containers | container down, container unhealthy, stack partially up, OOM killed, restart count above a threshold |
| Resources | CPU above a percentage, memory above a percentage, host disk above a percentage |
| Backups | backup failed, backup stale beyond N hours |
| Images | update available, newer version available |

Severities are info, warning and critical.

**Inbox** — alerts stream live to the bell in the nav, with an unread badge and a slide-out inbox. The dashboard marks affected projects.

**Notification channels** — email over SMTP, or Apprise URLs covering Slack, Discord, Telegram, generic webhooks and more. Channels are global with per-workspace grants, or private to a workspace. Pipeline and migration events use the same channels.

---

## Metrics

A collector samples every environment every 30 seconds by default — CPU, memory, disk and network — and stores the series locally.

- **Downsampling** — full resolution for 24 hours, then one sample per minute out to five days, then one per five minutes.
- **Retention** — 90 days.
- **Long-term history** — optionally dual-write to a managed VictoriaMetrics instance, exposing Prometheus-compatible series for Grafana.
- **In the UI** — env cards show CPU, memory, disk and network sparklines; the dashboard aggregates live usage across the control plane and every remote host.

---

## Maintenance mode and Danger Zone

### Maintenance mode

Per environment, Rigger serves a maintenance page, either immediately or on a schedule. Because the route points at Rigger itself rather than your app, **it works even when the environment is stopped**.

### Danger Zone

Irreversible actions, restricted to workspace admins and super-admins, each gated by a copy-paste acknowledgement sentence **and your password**. A wrong password simply re-prompts — it never logs you out.

- **Wipe application data** resets one environment to empty: it removes the named volumes and clears bind-mounted data directories, keeps `docker-compose.yml` and `.env` so settings and secrets survive, and redeploys fresh. It is offered **only** for environments a workspace admin has allow-listed under Manage Workspace → Environments. Keep production off that list and it can never be wiped. It runs as a background job with live progress. It's also the clean recovery path for a drifted database password, since it re-initialises the volume against the current `.env`.
- **Delete project** permanently removes the project directory — configuration, environment files and backups. Running containers are not stopped automatically.

---

## Housekeeping

An admin page with four tabs:

- **Dashboard** — Docker storage breakdown, a health verdict, and safe one-click actions like pruning networks and dangling images.
- **Safety Center** — approval-gated destructive cleanup for unused images, stopped containers, volumes, build cache and old kernels. Guards exclude Rigger-managed stacks and lock the active kernel.
- **Migration leftovers** — after moving an environment, wipe the containers, volumes and files left on the source host.
- **Automation and logs** — a daily 03:00 UTC job runs the two safe prune tasks, plus manual host-OS tools and a task history.

> Host-OS actions assume a Linux host. The Docker prune actions work anywhere Docker runs.

---

## Updating Rigger

Admin → Settings → **Updates**:

- **Rigger** — check for updates against published releases with a changelog, update with one click, and roll back to the previous version. One-click updates need `RIGGER_HOST_DIR` set, which the installer does for you.
- **Docker Engine** — shows the Docker version for the local daemon and every registered host against the latest release, with an in-place update over SSH (Linux, with sudo).

---

## Users, roles and auth

Two role systems:

- **Global role** — `superadmin` or `user`. A super-admin manages global settings and is an admin everywhere.
- **Workspace roles** — `viewer < developer < operator < admin`, granted through workspace membership, with optional per-project overrides that take precedence.

Manage users under Admin → Users; grant workspace membership and project access under Manage Workspace → Members.

| Mechanism | Detail |
|-----------|--------|
| Password storage | bcrypt, cost 12 |
| Access token | JWT, 15-minute expiry, held in memory only |
| Refresh token | httpOnly cookie, 7-day rolling |
| Password policy | Minimum length, complexity and maximum age; an expired password forces a change |
| Invites | Invited accounts can't sign in until they complete registration |
| Email verification | Banner and resend; falls back to showing the link when no SMTP is configured |
| Forgot password | Public self-service flow |
| Two-factor | Opt-in TOTP with QR enrollment and recovery codes |
| Access requests | Self-service request with an admin approval inbox |
| Audit log | Every action recorded — user, project, command, environment, host |

---

## REST API

A versioned external API at `/api/v1`, authenticated with **API keys** (`rgk_…`, shown once at creation, only the hash stored). Keys carry granular scopes, an optional per-key rate limit, and project access that can be limited to a list or confined to one workspace. Manage them under Admin → API Keys or per workspace.

```
GET  /api/v1/openapi.json                          # OpenAPI spec (public)
GET  /api/v1/docs                                  # API documentation (public)
GET  /api/v1/workspaces/{ws}/projects              # list projects
GET  .../projects/{project}/envs/{env}/services    # list services
GET  .../services/{service}/logs?tail=N            # log snapshot
POST .../envs/{env}/actions/{action}               # start|stop|restart|refresh|inactivate|backup
GET  .../projects/{project}/pipelines              # list pipelines
POST .../pipelines/{id}/runs                       # trigger a run
GET  .../pipelines/{id}/runs/{runId}               # run status and stage logs
POST .../pipelines/{id}/runs/{runId}/cancel        # cancel a run
```

Authenticate with `Authorization: Bearer rgk_…` or `X-API-Key`. The OpenAPI spec is generated from the same scope catalog the keys are checked against, so the documentation can't drift from what's actually permitted.

---

## Proxy service

A standalone reverse-proxy manager at `/proxy` for routing public hostnames to **any** upstream — inside Rigger, on your LAN, or on a remote host — independently of projects.

- **Routes** — hostnames to upstreams, with load balancing across multiple upstreams, custom locations, path rewriting, redirects and catch-all statuses. Per-route TLS: Let's Encrypt over HTTP-01 or DNS-01, an uploaded certificate (encrypted at rest), an existing certificate by SNI, or a per-route ACME email.
- **Access lists** — reusable bundles of basic-auth users, IP allow rules and **GeoIP country policy**, attachable to proxy routes and to project environments. Workspace access lists are strictly isolated from global ones.
- **Plugins** — **WAF** (Coraza with the OWASP core rule set) and **GeoIP blocking** using an offline database you download from the UI. Enabling one briefly restarts Traefik.
- **Backup** — export and import the whole proxy configuration as a `.rpx` bundle.

> Response caching is not available. The Traefik cache plugin crashes the proxy under its interpreter, so it is disabled deliberately; use a CDN or a caching sidecar instead.

---

## Git providers and registries

### Git providers

Workspace-scoped and encrypted at rest:

- **Token (HTTPS)** — presets for **GitHub**, **GitLab**, **Bitbucket** and **Gitea/Forgejo**. Works against any HTTPS host, including self-hosted instances on plain HTTP or non-standard ports.
- **SSH deploy key** — Rigger generates an ed25519 keypair; add the public key as a read-only deploy key.
- **GitHub App** — one-click connect and install, cloning with short-lived tokens that are never persisted.

Pick a provider on a project's source, or create one inline. **Test** verifies it against the real repository.

### Container registries

- **Managed registry** — one click stands up a registry on the Rigger host. With a base domain it's served over HTTPS at `registry.{base}` so a cluster can pull from it; without one it's a local HTTP port suitable for a single node.
- **Registry picker** — a project uses the managed registry, a registry you configure, or one entered manually. Swarm and remote deployments need a real registry and are blocked until one exists.
- **Build hosts** — a host can be marked build-only, making it a dedicated builder that's excluded from deploy targets. Rigger logs in to the project's registry automatically before building and pushing.

---

## The UI at a glance

**Top nav** — Dashboard, Housekeeping, Tools, Proxy Service, Admin, plus a help-text toggle, theme toggle, alert bell and user menu. A workspace switcher selects the active workspace, and **⌘/Ctrl-K** opens a command palette that jumps to projects, navigation, and an on-demand index of variables, routes, domains and pipelines.

**Left sidebar** — the workspace's projects with live status dots, a New Project button, and slide-out panels for recent activity, backup history and the version log.

**Dashboard** — stat cards for alerts, workspaces, projects, running containers, images and networks; a projects table with per-environment status, host, service counts and live resource usage; Docker engine and host system panels, aggregated across the control plane and every remote host.

**Project page** — one card per environment with status, deployment mode, host, access links, image-update badge, a `> bash` terminal, and Deploy / Restart / Update / Maintenance / Rollback controls. Below: streaming action output, a multi-container log viewer with per-service colour, filtering and download, a read-only compose viewer, metrics tiles, and the Service Console.

**Manage Workspace** — General, Preferences, Members, Access Requests, API Keys, Domains & TLS, Remote Hosts, Registries, Git, Backup Targets, Notifications, Access Lists, Alert Rules, Danger Zone.

**Edit Project** — Services, Environments, Routing, Backup, Pipelines, Preview Environments, Notes and Danger Zone. Notes is a small per-project wiki of Markdown documents, rendered and sanitised server-side.

**Tools** — Template Manager, project Backup & Restore, and environment-to-environment data migration.

**Theming** — light and dark, following the system or pinned, plus font, density and log-viewer preferences. A global toggle hides inline help text once you know your way around.

---

## Command reference

Everything issues from the web UI, the `rigger` CLI wrapper, or the REST endpoint — all reach the same runtime.

```
POST /api/workspaces/{ws}/projects/{name}/envs/{env}/action
     { "command": "...", "extra": [...] }
```

The host wrapper (`rigger.sh`, `rigger.ps1`, `rigger.bat`) is a thin convenience over that endpoint:

```bash
./rigger.sh login                 # stores a session in ~/.rigger
./rigger.sh list                  # list projects
./rigger.sh myapp dev start
./rigger.sh myapp dev ps
```

**Available commands:**

```
Lifecycle:   start · stop · down · restart · update · refresh · regen
Build:       build · promote · version
Operations:  ps · logs · backup · restore · migrate · init · test · script
```

- **`refresh`** regenerates the compose file and redeploys. **`regen`** regenerates it *without* starting anything — useful for correcting a stopped environment's routing while leaving it stopped.
- **`down`** removes containers but keeps volumes. **`stop`** leaves containers in place.
- **`build`** and **`promote`** apply to source-built stacks.
- Container shell access is available from the env card terminal in the UI.

For headless creation, the binary's `init-workspace` subcommand takes a prepared `config.json` and scaffolds every environment:

```bash
docker exec rigger rigger init-workspace -name myapp -config /toolkit/workspaces/myapp.config.json
```

---

## Configuration reference

### Server environment

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_SECRET` | — | **Required.** `openssl rand -hex 32`. Also derives the key that encrypts SSH keys, tokens and database passwords. |
| `RIGGER_PORT` | `9999` | UI host port |
| `RIGGER_APP_HOST` | — | Host IP used for app links and magic DNS |
| `ACME_EMAIL` | `admin@example.com` | Let's Encrypt account email |
| `CF_DNS_API_TOKEN` | — | Cloudflare token for DNS-01 and wildcard certificates |
| `RIGGER_HOST_DIR` | — | Host path to the install directory; enables one-click self-update |
| `RIGGER_IMAGE_TAG` | `latest` | Image tag this instance runs |
| `REMOTE_WORKSPACES_DIR` | same as local | Default projects path on remote hosts |
| `TZ` | `UTC` | Timezone for rendered timestamps |
| `METRICS_INTERVAL_SECONDS` | `30` | Metrics sampling interval |
| `APPRISE_URL` | — | Optional external Apprise API |

### Volume mounts

| Mount | Mode | Purpose |
|-------|------|---------|
| `../workspaces → /toolkit/workspaces` | rw | Projects: config, environments, volumes, backups |
| `../templates → /toolkit/templates` | rw | Templates, writable so you can save new ones |
| `/var/run/docker.sock` | rw | Docker control |
| `/ → /host` | ro | Host filesystem, for real disk metrics |
| `rigger-data → /data` | rw | Database and archives |

### Project layout

```
workspaces/<workspace>/projects/<name>/
├── config.json                 # Edit this, then refresh
├── notes/                      # Wiki documents
├── backups/<env>/<timestamp>/  # Snapshots
└── envs/
    └── dev/
        ├── .env                # Live secrets — never commit
        ├── .env.example        # Redacted — safe to commit
        ├── docker-compose.yml  # Generated
        └── volumes/            # Data
```

**Commit** `config.json`, `.env.example`, the generated compose file, and any generated Dockerfiles. **Don't commit** `.env` or `volumes/`.

---

## Current constraints

Worth knowing before you plan around them:

- **Remote-host builds** need a build context and a reachable registry. Build and promote locally, or migrate the environment after building.
- **Editing a remote environment's variables** updates the control plane's copy; redeploy to push it to the host.
- **Backup sync to S3 and SFTP** covers locally deployed environments.
- **Preview environments** support GitHub and Gitea webhooks; GitLab is not supported.
- **Response caching** in the proxy service is unavailable — see [Proxy service](#proxy-service).
- **Host-OS housekeeping** actions require a Linux host.
- **Swarm data is node-local.** A multi-node Swarm schedules stateless services across the cluster freely, but named volumes use Docker's `local` driver and bind mounts exist only on the node Rigger synced files to. Anything holding data — or bind-mounting a config file — should carry a placement constraint pinning it to one node, or a rescheduled task will come up against an empty volume. Rigger already runs its managed databases single-instance for this reason, and honours per-service placement constraints, but it does not yet add that pin for you or show you the cluster's other nodes.

---

## Troubleshooting

### The compose file looks out of date

It's generated from `config.json`. If you edited config on disk, run **refresh** on the environment.

### Database authentication fails (`P1000`, "password authentication failed")

A database applies its password only on the **first** start with an empty data directory; later starts ignore it. If `.env` diverges from what the volume was initialised with, the app fails and the environment shows as partial. Rigger normally prevents this by [pinning](#secrets) each generated secret. If a volume still mismatches, either use **Danger Zone → Wipe data** to re-initialise against the current `.env` (destroys that environment's data), or change the password in the database itself to match `.env` (keeps data).

### An app URL updated but doesn't load after changing the host IP

The magic-DNS hostname is baked into running containers' routing labels, so changing the app host updates the *displayed* URL but not the live routing. Rigger offers to refresh affected environments when you save — accept it, or run **refresh** manually.

### A container can't start: "Could not attach to network … not found"

A network was deleted and recreated (by a `docker network prune`, a Docker Desktop reset, enabling Swarm, or restoring onto a fresh host), so containers still reference a network ID that no longer exists. Start, Deploy and Restart can't fix this because they reuse the existing containers. Run **Refresh**, which recreates the affected containers against the current network. Data volumes are kept.

### A non-root image crash-loops on a bind mount

An image running as a fixed non-root user can't write to a Docker-created, root-owned directory. Rigger adds a one-shot init container that fixes ownership before the app starts, so redeploying resolves it.

### "Digest unknown" on an image update check

The local image has no registry digest — it was pulled without an explicit `docker pull`, or built locally — so the comparison is indeterminate. Run **Update** to populate it.

### Remote host: "No workspaces found" when scanning

Scan looks in the host's **Remote workspaces directory**. The default is the control plane's own container path, which doesn't exist on a bare host. Set it to the real path, for example `/opt/rigger/workspaces`.

### A deploy fails complaining about a missing config file

A service bind-mounts a *file* that doesn't exist. Docker would silently create it as a directory and break the mount, so Rigger stops first. Create the file in `envs/<env>/`, or ship it as a seed file in the template.

### Running behind Cloudflare

Set Cloudflare's SSL mode to **Full**, not Flexible. Flexible sends plain HTTP to your server, which breaks the Let's Encrypt HTTP-01 challenge.

---

## License and contributing

Building from source, running the development servers, and extending Rigger with new templates, frameworks or database engines are covered in **[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)**.
