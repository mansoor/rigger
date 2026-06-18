# Rigger REST API (`/api/v1`)

External, machine-facing REST API for listing and operating projects. It is separate
from the web UI's session auth — clients authenticate with an **API key**.

- **Base path:** `/api/v1` (same host/port as the Rigger UI).
- **Machine-readable spec:** `GET /api/v1/openapi.json` (OpenAPI 3).
- **Rendered docs:** `GET /api/v1/docs`.
- **Docs are public**; every other endpoint requires a key.

## Authentication

Create a key in **Admin → API Keys** (global-admin only). The raw key (`rgk_…`) is shown
**once** at creation and stored only as a hash — copy it then. Send it on every request:

```
Authorization: Bearer rgk_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

(`X-API-Key: rgk_…` is also accepted.)

A key carries:
- **Scopes** — the operations it may perform (see below).
- **Project access** — `all` projects, or a specific allow-list of `{workspace, project}`.
- **Rate limit** — max requests per minute **per project** (0 = unlimited).
- **Expiry** — optional; after it, the key is rejected.

## Scopes

Scopes are granular operation ids, grouped in the UI as **Read**, **Operate**, and
**Pipeline** (you can select a whole group or override individual operations).

| Group | Scope id | Allows |
|---|---|---|
| Read | `projects.list` | List projects |
| Read | `services.list` | List a project's services |
| Read | `logs.read` | Read a service's recent logs |
| Operate | `env.start` | Start an environment |
| Operate | `env.stop` | Stop an environment |
| Operate | `env.restart` | Restart an environment |
| Operate | `env.refresh` | Regenerate compose + redeploy |
| Operate | `env.inactivate` | Tear down (compose `down`) |
| Operate | `env.backup` | Back up an environment |
| Pipeline | `pipeline.run` | Trigger a pipeline run |

## Endpoints

### List projects
```
GET /api/v1/projects
```
Returns the projects the key may access, each with its environments.
```bash
curl -H "Authorization: Bearer $RIGGER_KEY" https://rigger.example.com/api/v1/projects
```
```json
[{ "workspace": "mcl", "project": "ahb", "type": "custom", "envs": ["prod"] }]
```

### List services
```
GET /api/v1/projects/{workspace}/{project}/envs/{env}/services
```
```json
{ "workspace": "mcl", "project": "ahb",
  "services": [{ "name": "laravel-test", "kind": "build", "web_routed": true, "port": "80" }] }
```

### Read service logs
```
GET /api/v1/projects/{workspace}/{project}/envs/{env}/services/{service}/logs?tail=200
```
`tail` defaults to 200, capped at 2000. Returns a bounded, non-streaming snapshot.
```json
{ "service": "laravel-test", "tail": 200, "logs": "…recent log lines…" }
```

### Lifecycle actions
```
POST /api/v1/projects/{workspace}/{project}/envs/{env}/actions/{action}
```
`{action}` ∈ `start`, `stop`, `restart`, `refresh`, `inactivate`, `backup`. Runs
synchronously and returns the captured output.
```bash
curl -X POST -H "Authorization: Bearer $RIGGER_KEY" \
  https://rigger.example.com/api/v1/projects/mcl/ahb/envs/prod/actions/restart
```
```json
{ "status": "ok", "action": "restart", "env": "prod", "output": "…" }
```

### Trigger a pipeline
```
POST /api/v1/projects/{workspace}/{project}/pipelines/{id}/run
```
Starts the run in the background and returns its id (`202 Accepted`).
```json
{ "status": "started", "pipeline": "release", "run_id": 42 }
```

## Errors

| Status | Meaning |
|---|---|
| `401` | Missing / invalid / disabled / expired key |
| `403` | Key lacks the required scope, or has no access to the project |
| `404` | Unknown project / pipeline / action |
| `429` | Rate limit exceeded for this key + project (see `Retry-After`) |
| `502` | The underlying docker/compose operation failed (body includes `output`) |

All errors return `{ "error": "…" }`.

## Notes

- The rate limiter is in-memory and per-instance (Rigger runs single-instance); a
  multi-replica deployment would count per replica.
- `inactivate` maps to compose `down` (stop **and** remove); `stop` leaves containers
  in place.
- This v1 surface is read + operate only — there are no project create/edit endpoints.
