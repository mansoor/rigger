# Development guide

Everything needed to build, run and extend Rigger itself. For using Rigger, see the [README](../README.md).

---

## Repository layout

```
rigger/
├── install.sh / uninstall.sh              # OS-aware installer
├── rigger.sh / rigger.ps1 / rigger.bat    # Thin host CLI wrappers (HTTP → API)
├── templates/                             # Baked into the image as a seed
│   ├── dockerfiles/    # django dotnet go laravel nextjs nodejs rails react spring spring-gradle
│   ├── scaffold/       # django go laravel nodejs react (starter apps)
│   └── stacks/         # 37 pre-built stack templates (*.json)
└── src/
    ├── Dockerfile                         # node → golang → alpine; bakes templates/ and nixpacks
    ├── docker-compose.yml                 # rigger + Traefik v3.4 + socket-proxy + fallback
    ├── .env.example
    ├── backend/
    │   ├── cmd/server/                    # server + `init-workspace` subcommand
    │   ├── api/                           # HTTP handlers
    │   └── internal/                      # composegen, envgen, dockerops, builder, detect,
    │       #   blueprints, scaffold, databases, managedregistry, gitproviders, remotehost,
    │       #   executor, acme, customdomains, gateway, clouddns, proxyroutes, pipelines,
    │       #   previews, backup, backupsync, alerts, notify, metrics, managedmetrics,
    │       #   maintenance, apikey, auth, crypto, settings, shell, wsconfig, workspace,
    │       #   imagecheck, version, …
    └── frontend/src/                      # React SPA (pages, components, hooks, lib)
```

The frontend is compiled at image build time and embedded into the Go binary via `embed.FS`, so the runtime is a single binary plus the baked `templates/` tree. `src/backend/cmd/server/dist/` is generated and gitignored.

---

## Running locally

```bash
# Terminal 1 — Go backend
cd src/backend && go run ./cmd/server

# Terminal 2 — React dev server
cd src/frontend && npm install && npm run dev   # :5173, proxies /api to :8080
```

To run the full containerized stack instead:

```bash
docker compose -f src/docker-compose.yml up -d --build rigger
```

---

## Build gates

Both gates run in containers so results match CI regardless of the host toolchain. On Windows, prefix Docker commands with `MSYS_NO_PATHCONV=1` so Git Bash doesn't rewrite container paths.

**Backend:**

```bash
docker run --rm -v "$PWD/src/backend:/app" \
  -v rigger-gomod:/go/pkg/mod -v rigger-gobuild:/root/.cache/go-build \
  -w /app golang:1.25 sh -c "go build ./... && go vet ./... && go test ./..."
```

**Frontend:**

```bash
docker run --rm -v "$PWD/src/frontend:/app" -w /app node:20 sh -c "npm run build"
```

`npm run build` is `eslint . --quiet && vite build` — the lint step is part of the gate and catches references Vite alone would not.

### Opt-in live tests

A few tests exercise a real Docker daemon and are skipped unless their environment variables are set. Compile the test binary and run it where a daemon and a real stack exist:

```bash
docker run --rm -v "$PWD/src/backend:/app" -w /app \
  -e CGO_ENABLED=0 -e GOOS=linux golang:1.25 \
  sh -c "go test -c -o /app/x.test ./internal/dockerops"
docker cp src/backend/x.test rigger:/tmp/x.test
docker exec -e RIGGER_LIVE_STACK=<stack> -e RIGGER_LIVE_ENVDIR=<env dir> \
  rigger /tmp/x.test -test.v
```

`RIGGER_LIVE_MUTATE=1` additionally allows the tests that remove containers. Always clean up the compiled binary afterwards — it must not be committed.

---

## Releasing

Rigger's version comes from the Git tag; there is no version constant to bump.

```bash
git push origin develop
git tag -a vX.Y.Z -F -   # annotated, with the changelog as the message
git push origin vX.Y.Z
```

A `v*` tag triggers the release workflow, which builds and pushes the image to GHCR and publishes a GitHub release from the tag message. Verify with `gh run watch` and `gh release view vX.Y.Z`.

---

## Extending Rigger

### Add a pre-built stack template

Create `templates/stacks/<name>.json`. There is no registration step — the wizard globs `*.json`. You can also build one in **Tools → Template Manager** and save it from the browser, which writes the file live without a rebuild.

Mark exactly one service `"web_routed": true` in a multi-service stack. If none is marked, the first non-datastore service is promoted. If a service bind-mounts a *config file*, ship it in the template's top-level `files` map — a bind source that doesn't exist is silently created by Docker as a directory and breaks the mount.

### Add a source-build framework

1. Add Dockerfiles to `templates/dockerfiles/<id>/`, and optionally a starter app to `templates/scaffold/<id>/`.
2. Register the blueprint in `internal/blueprints/blueprints.go` — language, port, healthcheck, template id, and the managed-dependency env contract.
3. Teach `internal/detect` to recognise the framework's manifest.
4. Add the default image tag in `internal/workspace/create.go`.

The `id` is the wiring key: `identify()`, the blueprint registry, `templates/dockerfiles/<id>/` and `envgen` all join on it.

### Add a managed database engine

Add an `Engine` entry to `internal/databases/databases.go` — id, image, versions, port, volume, env prefix and capability flags. The catalog, wizard and env contract pick it up from there. An *auxiliary* engine (one that runs alongside a primary database) also needs a composegen service, an envgen contract and secret, detection folding, and a frontend toggle.

---

## Frontend conventions

All form controls come from `src/frontend/src/components/ui.jsx`. Import from there rather than writing Tailwind classes at the call site — the kit exists because seven files had each grown their own `Label`/`Input`/`Select`/`Toggle`, and buttons had drifted to 291 distinct class strings.

| Primitive | Use for |
|-----------|---------|
| `Btn` | Any button. Pick a `variant` and `size`; never restate padding or colour. |
| `IconBtn` / `CloseBtn` / `LinkBtn` | Icon-only actions, modal dismiss, inline text actions. |
| `Input` / `Select` / `Textarea` / `Checkbox` / `RadioGroup` / `Toggle` | Form controls. |
| `CONTROL` / `CONTROL_SM` | The control class strings, for the raw `<input>`s whose event wiring can't use the kit component. |
| `Field` + `FormRow` | Form rows. `FormRow` is a shared 4-column track — give each `Field` a `span` so consecutive rows line up. |
| `ReadOnly` | A derived or fixed value in a control-shaped box. Matches `Input`'s box model exactly. |
| `Hint` | Field help. Gated by the user's help-text preference, so it may not render. |
| `LabelSpacer` + `CONTROL_H` | Aligning a non-input control (a toggle, a badge) with an `Input` beside it. |

Two rules worth internalising:

- **Colours come from semantic tokens**, defined per theme in `src/styles/themes.css` and mapped in `tailwind.config.js`. Use `text-accent-text`, not `text-brand-400` — the raw palette doesn't flip between light and dark, and bright brand colours fail contrast on the light theme. Solid button fills are the deliberate exception.
- **Never use `items-end` to align a control with a neighbouring field.** It bottom-aligns against the tallest cell, which changes when a `Hint` is hidden by the help-text preference — the control visibly jumps. Use `LabelSpacer` + `CONTROL_H` instead.

Adding a colour scheme is one block in `themes.css` and one entry in `themes.js`; no component changes.

---

## Architecture notes

- **`config.json` is authoritative.** Compose files are generated by `internal/composegen` and are disposable. Anything that changes deployment shape belongs in config, not in a hand-edited compose file.
- **No shell.** Every runtime operation is native Go. The `shell` package is a command *bridge* with a strict allowlist, not a shell.
- **All Docker calls go through `internal/executor`**, which abstracts local versus remote (SSH) execution. Adding a call site anywhere else breaks remote-host support. Cancellable commands run in their own process group, because `docker compose` spawns the compose plugin as a child that would otherwise survive a kill.
- **The database is single-connection** (`SetMaxOpenConns(1)`, modernc SQLite). Nested queries while iterating an open `*sql.Rows` deadlock — read fully, then act.
