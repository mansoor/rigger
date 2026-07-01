# Template deploy reliability — web entry, seed files, env-gen, bind-mount guard

**Status: BUILT + RELEASED (v0.1.23, develop + pushed).** Five related fixes/features that
make prebuilt and image-stack templates deploy correctly out of the box, surfaced while
testing the bundled templates (Gitea, Grafana, Immich, …). All unit-tested; Grafana and
Immich live-verified end-to-end on Docker Desktop.

Backend: `internal/composegen` (web-entry fallback), `api/secret_handlers.go` +
`internal/envgen` (secret resolution, port-leak), `internal/dockerops` (bind-mount guard),
`internal/wsconfig` + `internal/workspace` + `api/handlers.go` (seed files). Frontend:
`pages/NewProjectPage.jsx` (web-entry picker), `pages/ToolsPage.jsx` + `components/DropZone.jsx`
(Static Files editor).

---

## 1. Web entry for multi-service stacks

### Problem
A multi-service image stack (an app + its database) under Traefik with **no service marked
`web_routed`** generated no router, so the env 404'd on its domain while every container
looked healthy (Traefik silently drops an empty/failed router). composegen's
`applyWebEntryFallback` deliberately bailed on multi-service stacks ("pick a web entry
explicitly") — but **10 of the bundled templates ship no `web_routed` marker at all**
(Gitea, Ghost, Grafana, Immich, n8n, Nextcloud, Nginx-Proxy-Manager, Plausible, TeslaMate,
WordPress), so they were all affected.

### Design
- **`applyWebEntryFallback` is now datastore-aware** (`internal/composegen/generator.go`).
  When Traefik resolves a domain and nothing is `web_routed` (and the project isn't driving
  ingress from the routing table), it promotes:
  - a single service → that service;
  - a multi-service stack → the **first non-datastore service**, so an app+db stack routes to
    the app, never the database. `isDatastoreImage` matches the final image path segment
    against a known set (`postgres`, `mysql`, `mariadb`, `mongo`, `redis`, `clickhouse`, …)
    plus substrings for engine forks (`clickhouse-server`, `pgvecto-rs`, `pgvector`, `postgis`,
    `timescale`). Bails when **every** service looks like a datastore (no router invented).
- **Templates are explicitly marked.** The heuristic can't disambiguate every case (e.g.
  Grafana+Prometheus — neither is a "datastore", but the web entry is Grafana; Immich's DB is
  `tensorchord/pgvecto-rs`). So all 10 templates got an explicit `web_routed` on the correct
  service. The fallback remains the safety net for hand-built / scanned / unknown stacks.
- **Wizard picker** (`NewProjectPage.jsx`, Services step). For a multi-service image/prebuilt
  stack a "Web entry" radio group lets the user pick/change the routed service; the
  datastore-aware default is pre-selected and datastore services are labelled. Mirrors the
  same `isDatastoreImage`/`DATASTORE_SUBSTRINGS` logic in JS.

### Why a heuristic AND explicit marks
The generate-time fallback fixes *any* stack (wizard, REST, repo-scan, compose-import, live
config edits) at one chokepoint and is unit-testable. Explicit template marks are
authoritative for curated data where the heuristic alone would mis-route (3 of the 10).

Tests: `composegen/webentry_fallback_test.go` (app vs db, all-datastore bail, single-service
promote, `isDatastoreImage` incl. forks).

---

## 2. Env-gen: secret-placeholder resolution + port leak

### Problem A — `CHANGE_ME` secrets / password drift
Two env-var pipelines disagreed at create. `TemplateEnvs` (→ `config.json`) ran through
`GenerateSmartDefaults` and got the **resolved** secret; but `seedEnvVars` then wrote the
wizard's **raw** per-env vars (a template's `DB_PASSWORD=CHANGE_ME`) to `.env` *after*
bootstrap, and `UpdateEnvVars` writes verbatim — **clobbering** the resolved secret. Result:
`.env` held `CHANGE_ME` while `config.json` held the real value. That's both an **insecure
default** and the recurring **refresh-time password drift** (a regenerated `.env` no longer
matches an already-initialized DB volume → auth fail).

### Fix A
`api/secret_handlers.go` — `seedEnvVars` now resolves each incoming var via
`envgen.ResolveImageValue` **against the `.env` bootstrap just wrote**, reusing the
already-generated secret instead of overwriting it. `.env` and `config.json` converge on the
resolved value: no `CHANGE_ME`, no drift. (`ResolveImageValue`: placeholder + existing → reuse;
placeholder + absent → generate; real value / skip-key → keep.)

### Problem B — env-var leak into containers
`env_file: .env` injects the **whole** `.env` into **every** container, so Rigger's own
metadata (`HTTP_PORT`, `HTTPS_PORT`, `ENV`, `DOMAIN`, `DEPLOYMENT`, `TRAEFIK_ENABLED`,
`PROJECT_NAME`, `REGISTRY`, `IMAGE_TAG`) could collide with a var an app reads under a generic
name — Gitea bound to a bare `HTTP_PORT`; Vaultwarden and others read `DOMAIN`.

There are two sub-classes, with different fixes:

**Rigger metadata → namespaced.** `internal/envgen/envgen.go` emits all of Rigger's own
platform vars **`RIGGER_`-prefixed** (`RIGGER_ENV`, `RIGGER_DOMAIN`, `RIGGER_HTTP_PORT`, …) so
they can never collide with an app's vars, while the values stay available for tooling. Two keep
their names by design: `COMPOSE_PROJECT_NAME` (docker-compose reserved; no app collides with it)
and the `{SVC}_IMAGE` build pointers (compose interpolates `image: ${SVC_IMAGE}`). None of the
renamed keys are read by the backend, the builder, or any template's `${…}` interpolation
(verified), so the rename is transparent. The frontend env-var classifier treats the whole
`RIGGER_` prefix as System (`lib/envVarGroups.jsx`). `MAIL_PORT` is deliberately left alone — a
legitimate app var for custom Laravel stacks.

**App/template config → declared per-service.** A template var that means different things to
different services (Immich's `IMMICH_PORT`: 2283 for the server, 3003 for `immich-machine-
learning`) can't be namespaced — it's the app's own name. The fix is for the service to declare
its own value in its `env_vars`, which composegen emits as an `environment:` entry that
**overrides** `env_file`. Done for Immich (`ml` → `IMMICH_PORT=3003`) and Gitea
(`GITEA__server__HTTP_PORT`). This is correct config, not a workaround, and is the pattern for
authoring any multi-service template where two services share a config-var name.

> Why not scope `env_file` per-service instead: apps rely on `env_file` injecting top-level
> config they don't re-declare (Immich reads `UPLOAD_LOCATION`), and it would touch the
> deploy → refresh → remote-host file-sync path. The `RIGGER_` namespacing removes the
> collision risk for Rigger's own vars without that blast radius.

Tests: `envgen_test.go` — `ResolveImageValue` contract; `RIGGER_`-prefixed keys present and the
bare names (`HTTP_PORT`/`ENV`/`DOMAIN`/…) absent. Live-verified: Immich (incl. ML), Grafana,
Gitea all deploy healthy with the prefixed `.env`, and no bare metadata reaches the containers.

---

## 3. Layer 1 — pre-deploy bind-mount guard

### Problem
A bind mount whose host source is a missing **file** is silently created by Docker as a
**directory**, so a container expecting a file fails with a cryptic OCI "not a directory"
error (or mounts an empty dir). The bundled Grafana (`prometheus.yml`) and Immich
(`hwaccel.transcoding.yml`) templates both hit this.

### Design
`internal/dockerops/bindcheck.go` — before any `compose up` (start / deploy / refresh /
restart-fallback / update, gated in `compose()`), scan the generated compose for env-dir-
relative bind mounts (`${RIGGER_BIND_ROOT:-.}/X` or `./X`) whose source is **absent** AND
**looks like a file** (final segment has a short extension), and fail fast with an actionable
message listing each. Data-dir mounts (no extension) and named volumes are ignored, so it
never false-blocks. Checks the local env dir, which the control plane stages before any local
run or remote sync.

Tests: `dockerops/bindcheck_test.go` (detection, all-present, no-compose).

---

## 4. Layer 2 — template seed files

### Problem
Some apps require a host-provided **config file** (no usable image default) — Layer 1 stops the
deploy, but the file still has to come from somewhere, and a file created inside the container
at runtime wouldn't survive a recreate.

### Design
A template ships static config files in a top-level **`files`** map (relative path → contents).
- **Persistence.** At create, `LoadTemplate` returns `files`; the handler sets
  `CreateRequest.SeedFiles`; create writes them to `config.json` → `project.seed_files`. The
  project stays self-contained even if the source template later changes or is removed.
- **Materialization.** `bootstrap.writeSeedFiles` writes each into the env dir **write-if-
  absent** — they live on the host bind (survive restarts) and a user's later edits / per-env
  changes are never clobbered on refresh. Nested dirs are created; absolute and parent-escape
  paths are rejected so a template can't write outside the env.
- **Line-array bodies.** `wsconfig.MultilineString` unmarshals a file body from a JSON **string
  OR an array of lines** (joined with `\n` + trailing newline), so a multi-line config is
  authored as a readable list instead of one `\n`-escaped string. Normalized to a joined string
  in `config.json`.
- **`GetTemplate`** returns `files`; Grafana ships a real `prometheus.yml` (scrapes itself +
  Grafana) as the worked example.

### Static Files editor (Template Manager)
`pages/ToolsPage.jsx` — a collapsible **Static files** section:
- per-file **path input + content `<textarea>`** (type real newlines; `JSON.stringify` handles
  all escaping; multi-line content round-trips into the JSON as a line-array);
- a **drop zone** (shared `DropZone`, extended with an opt-in `multiple` prop) that accepts
  text/config files (`.json` `.yml` `.yaml` `.conf` `.cfg` `.toml` `.ini` `.env` `.xml` `.txt`)
  and pre-fills name + content; re-dropping a known filename updates it; 1 MB cap; non-text
  rejected with a message;
- auto-pull on expand, **Add file** / **Reload from JSON**;
- the **Validate** step warns (non-blocking) when a service bind-mounts a file with no `files`
  entry — turning the Layer-1 deploy failure into an author-time hint.

### Phasing (as built)
- Phase 1 — line-array support (`MultilineString`).
- Phase 2 — Validate-time bind-mount-vs-`files` cross-check.
- Phase 3 — Static Files form + drop zone.

Tests: `workspace/seedfiles_test.go` (`writeSeedFiles` write-if-absent / nested / unsafe-path;
`LoadTemplate` string and line-array bodies).

### Deferred: folder-based files
A sibling-folder convention (`templates/stacks/<name>.files/`) of real files was considered for
**large/binary** seed files (TLS cert, GeoIP DB). Deferred — it breaks the "template is one
self-contained JSON" property and complicates distribution (marketplace/upload/save). The
embed-in-JSON + line-array + textarea path covers text configs without that cost; revisit only
when a large/binary case appears.

---

## 5. Template fixes applied (Grafana + Immich)

Concrete deploy blockers fixed in the two templates while validating the above:
- **Grafana**: `prometheus.yml` shipped as a seed file (was an unsded mount); `user: "0:0"` on
  prometheus + grafana (they run as nobody/472 and couldn't write host-owned bind data dirs).
- **Immich**: quote the postgres `search_path` group so Compose's shlex keeps it one arg (was
  split → `FATAL search_path`); drop the optional `hwaccel.transcoding.yml` mount; healthcheck
  → `/api/server/ping` (v2.x); wire `IMMICH_MACHINE_LEARNING_URL=http://ml:3003` (service is
  named `ml`) and drop the ml `curl` healthcheck (curl absent; the image self-checks).

> Cross-cutting note: any non-root image that writes a **bind-mounted** data dir needs
> `user: "0:0"` (or a writable dir) — Postgres/MySQL/Redis self-chown via their root entrypoint,
> but Prometheus/Grafana don't. And a credential an app persists on **first init**
> (Postgres password, Grafana admin) does **not** re-apply on restart from its env var — it must
> be reset in place (`ALTER ROLE`, `grafana-cli admin reset-admin-password`).
