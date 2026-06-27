# Search & time-series managed databases + Rigger's own metrics store

**Status: Parts A + B + C BUILT (develop, 2026-06-26); web-UI sidecars deferred.**
Part A (OpenSearch) and Part B (VictoriaMetrics) ship as managed engines, connection-info
only. **Part C (Rigger's own container-stats store) is now BUILT**: a pluggable
`metrics.Sink` (`SQLiteSink` default + `TSDBSink`) dual-writes to a Rigger-managed
VictoriaMetrics sidecar (`internal/managedmetrics`, mirrors `internal/managedregistry`) when
the admin toggle is on — SQLite stays the env-card source; VM adds durable long-retention +
PromQL/Grafana-ready storage. Internal-only (on traefik_net, reached by container name; no
host port / Traefik route). Admin card under Settings → General → Metrics storage. The
optional web-UI sidecars (OpenSearch Dashboards / Grafana / VMUI) remain design-only, as does
switching the in-app dashboards to read VM (the tiered "after ~15d serve old windows from VM"
idea) and **VictoriaLogs/Loki for pipeline & action logs** (a possible future "Part D").

> **Planned next:** promote OpenSearch + VictoriaMetrics from single-slot catalog DB
> engines to opt-in **auxiliary** services that run alongside a primary database, each with
> its own console tab. Detailed plan: [standalone-search-tsdb-services.md](standalone-search-tsdb-services.md)
> (deferred — revisit after more testing).

**Decisions taken:** OpenSearch (not ElasticSearch — licensing); VictoriaMetrics (not
InfluxDB/TimescaleDB). Both slot into the `internal/databases` catalog as non-SQL engines
(`Schemas`/`Users` false → connection info only), exactly like MongoDB.

**Built-engine specifics:** OpenSearch runs single-node with the security plugin ON →
HTTPS on 9200 (self-signed demo cert), fixed `admin` user, `OPENSEARCH_INITIAL_ADMIN_PASSWORD`
from a complexity-meeting strong password (OpenSearch 2.12+ enforces a zxcvbn score — random
generation passes; sequences/words fail); needs RAM + host `vm.max_map_count=262144`.
VictoriaMetrics runs single-node, no auth on :8428; its image is FROM scratch (no shell) so it
carries NO Docker healthcheck (dependents use `service_started`, like MinIO). Both back up at
the volume level (no logical dump; restore is volume-restore).

## Context

Today the managed-database catalog (`internal/databases`) is relational-only —
PostgreSQL, MySQL, MariaDB, and (new, minimal) **MongoDB**. The catalog is the single
source of truth: an `Engine` entry flows into the wizard, composegen (the managed
service block), envgen (the `*_HOST/PORT/...` family), and the connection-info console.
MongoDB proved the catalog cleanly absorbs a non-SQL engine when the SQL-only surfaces
(Adminer auto-login, schema/user management) are gated off (`Schemas`/`Users` = false,
`Driver` ≠ postgres/mysql).

Search engines and TSDBs are the same shape of problem: managed containers with their
own connection contract and **no SQL console** — so they slot into the catalog the same
way MongoDB does, with engine-specific connection info and (optionally) their own web UI
sidecar.

## Part A — Search: ElasticSearch / OpenSearch as a managed engine

**Recommendation: OpenSearch**, not ElasticSearch. ElasticSearch moved off Apache-2.0
(SSPL/Elastic License); OpenSearch is the Apache-2.0 fork (AWS-led), API-compatible with
the ES 7.10 line, and avoids the licensing constraint for an open-core product. Offer ES
later only if a user explicitly needs it.

Catalog entry (mirrors the MongoDB minimal cut):
- `ID: opensearch`, `Label: OpenSearch`, `Image: opensearchproject/opensearch`,
  `Port: 9200`, `VolumePath: /usr/share/opensearch/data`, `Driver: opensearch`,
  `EnvPrefix: OPENSEARCH`, `Schemas: false`, `Users: false`.
- composegen managed block: single-node (`discovery.type=single-node`), the security
  demo config or a set initial admin password (`OPENSEARCH_INITIAL_ADMIN_PASSWORD`), a
  data volume, and a healthcheck (`curl -k -u admin:$pw https://localhost:9200/_cluster/health`).
  ⚠ memory: OpenSearch wants a heap floor; set `OPENSEARCH_JAVA_OPTS=-Xms512m -Xmx512m`
  and document the `vm.max_map_count` host sysctl requirement.
- envgen: `OPENSEARCH_HOST/PORT/USER/PASSWORD` + a ready `OPENSEARCH_URL`. Password
  preserved across regen (add to `getOut` chain + `ManagedSecretKeys`), like Mongo.
- Console: connection-info tab only. Optional **OpenSearch Dashboards** sidecar
  (`opensearchproject/opensearch-dashboards`) on a `search` subdomain — the search
  analogue of the MinIO console / planned mongo-express (gate behind a project flag like
  `storage_ui`).

## Part B — Time-series database as a managed engine

Three credible options; pick by the dominant use case:

| Engine | Model | Best when | Notes |
|---|---|---|---|
| **VictoriaMetrics** | Prometheus-compatible (PromQL) | metrics/monitoring; **also Rigger's own stats** (Part C) | tiny footprint, single binary, fast, Apache-2.0; remote-write + PromQL |
| **InfluxDB (2.x/3)** | push, Flux/InfluxQL | app event/IoT time-series, bundled UI | heavier; v2/v3 API churn |
| **TimescaleDB** | Postgres extension (SQL!) | teams who want SQL + relational alongside | it IS Postgres — could be a `postgres` variant with the extension rather than a new engine |

**Recommendation: VictoriaMetrics** as the managed TSDB engine — smallest footprint,
Prometheus ecosystem compatibility, and it doubles as Rigger's own metrics backend
(Part C), so one integration serves both. TimescaleDB is attractive precisely because
it's SQL — if chosen, model it as a Postgres image variant (reuse the entire SQL
console) rather than a separate non-SQL engine.

Catalog/composegen/envgen: same pattern as OpenSearch (connection-info only,
`Driver: victoriametrics`, `EnvPrefix: VICTORIA`/`VM`, data volume, healthcheck on
`/health`, optional Grafana/VMUI sidecar on a subdomain).

## Part C — Rigger's own container-stats store (internal architecture)

Distinct from offering a TSDB to *projects*: Rigger currently records container metrics
in **SQLite** (`metrics_snapshots`, written by `internal/metrics`). SQLite is fine for
short-window dashboards but is the wrong shape for long-retention, high-cardinality
time-series (per-container CPU/mem at intervals across many envs).

Proposed: make the metrics sink **pluggable**, mirroring the 8e secret-backend pattern:
- `internal/metrics` gains a `Sink` interface — `SQLiteSink` (default, today) and
  `TSDBSink` (writes via Prometheus remote-write / InfluxDB line protocol).
- A **Rigger-managed VictoriaMetrics container** (one, instance-wide — reuse the
  `internal/managedregistry` managed-sidecar lifecycle pattern), enabled by an admin
  toggle "Store metrics in a time-series database". Internal-only by default.
- The metrics dashboard reads from whichever sink is active; long-retention + PromQL
  queries become possible; SQLite `metrics_snapshots` stays the zero-config default.
- This is also a natural place to later expose **Grafana** as an optional admin sidecar.

Relationship to 8e: same "pluggable backend + optional Rigger-managed sidecar" shape as
the [vault secrets backend](vault-secrets-backend.md) — worth keeping the two consistent
(an `internal/<x>.Backend` interface + a `managed<x>` lifecycle package).

## Cross-cutting: backups

The relational engines back up via logical dump (pg_dump/mysqldump) in
`internal/backup`. Search/TSDB/Mongo don't fit that path — they're covered by the
**volume-level** backup (the data volume is tarred) which already runs for any managed
dep. Logical/snapshot backup (mongodump, OpenSearch snapshots, VM snapshots) is a later
enhancement; document that restore for these engines is volume-restore, not logical.

## Phasing (when greenlit)

1. **OpenSearch** managed engine (catalog + composegen + envgen + connection console) —
   smallest, proves the search shape. Optional Dashboards sidecar.
2. **VictoriaMetrics** managed engine (same pattern).
3. **Rigger internal metrics → pluggable Sink** + managed VictoriaMetrics + admin toggle
   (Part C). Reuses #2's container work.
4. Optional: TimescaleDB as a Postgres variant (full SQL console reuse); ElasticSearch
   proper; Grafana/Dashboards sidecars; logical backups per engine.

## Decisions needed before building

- Search: OpenSearch only, or also ElasticSearch? (rec: OpenSearch only)
- TSDB: VictoriaMetrics vs InfluxDB vs TimescaleDB-as-Postgres? (rec: VictoriaMetrics)
- Part C priority: is the internal-metrics-store its own milestone, or bundled with the
  managed-TSDB engine work?
- Footprint: OpenSearch + a JVM is heavy for small hosts — gate behind a clear "this
  needs RAM" note and keep it opt-in.

## Related

- [vault-secrets-backend.md](vault-secrets-backend.md) — same pluggable-backend + managed-sidecar shape.
- Catalog: `internal/databases/databases.go` (MongoDB is the reference non-SQL minimal cut).
- `internal/metrics` (current SQLite `metrics_snapshots` writer) for Part C.
