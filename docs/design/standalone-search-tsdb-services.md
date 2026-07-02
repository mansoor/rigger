# Standalone Search & TSDB managed services (OpenSearch + VictoriaMetrics as auxiliary deps)

**Status: IMPLEMENTED (2026-07-02).** OpenSearch (search) and VictoriaMetrics (tsdb) now run
as opt-in auxiliary managed services ALONGSIDE a primary DB, each with its own console tab.
Catalog `Category`; `Project.Search/TSDB` (+versions); `Config.Normalize()` (wsconfig) +
`normalizeAux()` (composegen) relocate a legacy `Database: opensearch|victoriametrics` into the
new slots; composegen/envgen/backup re-gated off `Search/TSDB`; PutConfig validates by category;
`ManagedServices` "Search & metrics" group + category-filtered `DatabaseSelect`; Search/Metrics
console tabs. Internal-only v1. Verified via unit tests + a throwaway postgres+opensearch+
victoriametrics deploy. Out-of-scope items below remain deferred.
**Decision taken: promote OpenSearch (Search) and VictoriaMetrics (Metrics/TSDB) from
single-slot catalog database engines to opt-in AUXILIARY managed services that run
ALONGSIDE a primary database + Redis + object storage + Mailpit, each with its own
dedicated console tab.**

Prereq context: [search-and-timeseries-databases.md](search-and-timeseries-databases.md)
(Parts A/B/C — the engines themselves + Rigger's internal metrics sink, already BUILT).

---

## Problem

The managed-database catalog (`internal/databases`) is a single source of truth, and a
project picks **exactly one** engine via `Project.Database` (a single string,
[wsconfig.go:180](../../src/backend/internal/wsconfig/wsconfig.go)). OpenSearch and
VictoriaMetrics were added into that same catalog pool, so today they are **mutually
exclusive with a real primary database** — choosing OpenSearch as "the database" means you
cannot also have Postgres. They render only inside the generic **Database** tab of the
per-env Managed Service Console (with engine-specific notes added in Part A/B).

A dedicated **Search** / **Metrics** console tab is only meaningful if these can run
*next to* a primary DB — which is their real-world role (a search index and a metrics store
beside the app's Postgres). That requires decoupling them from the single `Database` slot.

Every generation path currently keys off a single engine:
- composegen `buildManagedDeps` gates the blocks `if engine == "opensearch"` /
  `if engine == "victoriametrics"` ([builders.go:848,875](../../src/backend/internal/composegen/builders.go)).
- envgen emits `OPENSEARCH_*`/`VICTORIA_*` inside the `switch engine` on `EffDatabase`
  ([envgen.go:528,539](../../src/backend/internal/envgen/envgen.go)).
- backup archives the volume off `EffDatabase`
  ([backup.go:135](../../src/backend/internal/backup/backup.go)).
- `managedDBName` includes them in its switch
  ([generator.go:269](../../src/backend/internal/composegen/generator.go)).

## Goal

A project can enable, independently and concurrently: a primary DB (postgres/mysql/
mariadb/mongodb) **and** OpenSearch (search) **and** VictoriaMetrics (TSDB) **and** Redis
**and** object storage **and** Mailpit — each surfaced as its own service, wired via env
vars, backed up, and shown as its own console tab.

---

## Implementation plan (file-level)

### Phase 1 — Model & catalog

1. **`internal/databases/databases.go`** — add `Category string` to `Engine`
   (`"database" | "search" | "tsdb"`). Tag: postgres/mysql/mariadb/mongodb → `database`,
   opensearch → `search`, victoriametrics → `tsdb`. Add `CatalogByCategory(cat string) []Engine`.
   Leave `Catalog()`, `Get()`, `DefaultVersion()`, `ResolveVersion()` unchanged (so all
   existing `Get("opensearch")` lookups keep working).

2. **`internal/wsconfig/wsconfig.go`** — add project-level fields next to `Database`:
   ```go
   Search        string `json:"search,omitempty"`         // "" | "opensearch"
   SearchVersion string `json:"search_version,omitempty"`
   TSDB          string `json:"tsdb,omitempty"`            // "" | "victoriametrics"
   TSDBVersion   string `json:"tsdb_version,omitempty"`
   ```
   Add effective getters (project-level; `Env` param kept for signature parity / future
   per-env override): `EffSearch(e) string`, `EffSearchVersion(e) string`,
   `EffTSDB(e) string`, `EffTSDBVersion(e) string`.

   Add `(c *Config) Normalize()` called at the end of `Load`: if `Database` is
   `"opensearch"`/`"victoriametrics"`, relocate it to `Search`/`TSDB` (carry `DBVersion`
   into `SearchVersion`/`TSDBVersion`) and blank `Database`/`DBVersion`. This self-heals
   the few-hours-old engines that may have been picked as the DB. Add a unit test.

3. **`internal/workspace/workspace.go`** — `managedDepServices` (line ~167): push a
   `{name: search, kind: search}` row when `Search != ""` and a `{name: tsdb, kind: tsdb}`
   row when `TSDB != ""`. Thread `Search/SearchVersion/TSDB/TSDBVersion` from the wizard
   create payload into `cfg.Project.*`. Extend `create_dbstack_test.go`.

### Phase 2 — Generation

4. **`internal/composegen/builders.go`**
   - Add `g.searchEngine()` / `g.tsdbEngine()` (read `EffSearch`/`EffTSDB`).
   - Re-gate the existing OpenSearch block on `g.searchEngine() == "opensearch"` and the
     VictoriaMetrics block on `g.tsdbEngine() == "victoriametrics"` (they already exist as
     separate `if` blocks — just change the gate variable so they emit *in addition* to the
     primary DB block).
   - **Remove the `g.dbExternalPorts(eng)` call from both aux blocks** — aux services are
     internal-only in v1 (external exposure deferred; `DBExternal` is the DB's toggle and
     must not leak the aux ports).
   - `namedVolumes` (lines ~210-215): add the opensearch/vm data volumes when
     `searchEngine`/`tsdbEngine` are set (not when `engine` is them).
   - `depHasHealthcheck` (line ~572) and `managedDepPort` (line ~1048) are keyed by service
     **name** ("opensearch"/"victoriametrics"), not engine — no change needed.

5. **`internal/composegen/generator.go`** — `managedDBName` (line ~263): remove
   `opensearch`/`victoriametrics` from the switch so the app's auto `depends_on` targets
   only the real DB. Aux `depends_on` stays available via the existing per-service
   `DependsOn` mechanism / `enabledDependsOnTargets`.

6. **`internal/envgen/envgen.go`**
   - Pull the `case "opensearch"` / `case "victoriametrics"` bodies (lines ~528-545) OUT of
     the `switch engine` (on `EffDatabase`) and into independent `if EffSearch(e) == "opensearch"`
     / `if EffTSDB(e) == "victoriametrics"` blocks so they write alongside the DB vars.
   - `managedContractKeys` (lines ~154-165): move `OPENSEARCH_*` under `if EffSearch` and
     `VICTORIA_*` under `if EffTSDB` (currently both are under the `EffDatabase` guard).
   - `outputKeys` per-engine switch (lines ~553-557): emit the OPENSEARCH/VICTORIA output
     keys under the new flags.
   - Keep `osPassword := getOut(strongPw(r), "OPENSEARCH_PASSWORD")` but only write it when
     search is on. `OPENSEARCH_PASSWORD` stays in `ManagedSecretKeys` (preserved across
     regen when present).
   - `databaseURL` already returns "" for opensearch/vm — no change.

7. **`internal/backup/backup.go`** (line ~135): archive the opensearch/vm data volumes off
   `EffSearch`/`EffTSDB` instead of the `EffDatabase` switch. `archiveManagedDBVolume(engine, …)`
   already resolves the `{prefix}_{engine}_data` volume by engine id — call it for the aux
   engines when their flags are set.

8. **PutConfig validation** (wherever `Database` is validated) — validate `Search` ∈
   {"", search-category ids} and `TSDB` ∈ {"", tsdb-category ids} via `CatalogByCategory`.

### Phase 3 — Console API

9. **`api/service_console_handlers.go`** `GetServiceConsole` — after the Mailpit section,
   add:
   - **Search (OpenSearch)** when `cfg.EffSearch(ec) == "opensearch"`: a `consoleService{
     Kind:"search", Label:"Search (OpenSearch)"}` with rows Host / Port / User (`admin`) /
     URL / Password(`Secret:true`), `EnvKeys` = the `OPENSEARCH_*` family (password masked
     via the existing `mask`), `SecretKeys:["OPENSEARCH_PASSWORD"]`, and a note about HTTPS +
     self-signed cert + `vm.max_map_count`. No `Subdomain` (Dashboards deferred).
   - **Metrics (VictoriaMetrics)** when `cfg.EffTSDB(ec) == "victoriametrics"`: a
     `consoleService{Kind:"tsdb", Label:"Metrics (VictoriaMetrics)"}` with rows Host / Port /
     URL, `EnvKeys` = `VICTORIA_*`, no secret, note about no-auth + remote-write/PromQL paths.

   These reuse the existing generic `consoleService` shape, so the frontend renders them
   with no panel changes.

### Phase 4 — Frontend

10. **`components/DatabaseSelect.jsx`** — filter the fetched catalog to
    `category === 'database'` so search/tsdb are no longer offered as the primary DB.
    (Confirm where it fetches: `fetchDatabases` / `GET /api/databases`. Either filter
    client-side or pass `?category=database`.)

11. **`components/ManagedServices.jsx`** — add a **"Search & metrics"** `Group` with two
    selectors: Search engine (None / OpenSearch) + version, and Metrics engine
    (None / VictoriaMetrics) + version (a small `CatalogSelect` filtered by category, or two
    `DatabaseSelect`-style components). Extend `SERVICE_META` (opensearch, victoriametrics),
    `managedServiceList` (push search/tsdb rows), and `enabledDependsOnTargets` (include them).

12. **`pages/NewProjectPage.jsx` + `pages/EditProjectPage.jsx`** — map
    `search/searchVersion/tsdb/tsdbVersion` ⇄ `Project.*` in both the create payload and the
    edit load/save (mirror how `database/dbVersion/redis` are mapped).

13. **`components/ServiceConsoleModal.jsx`** — add `KIND_ICON` entries `search: '🔍'`,
    `tsdb: '📈'`. Tabs are otherwise built automatically from the console `services` array,
    and the generic `ServicePanel` already renders rows/env_keys/note — no other change.

14. **`lib/api.js`** — only if `/api/databases` gains a `category` param; otherwise no change.

### Phase 5 — Verify & document

15. Tests:
    - composegen golden/focused test: a config with **postgres + opensearch +
      victoriametrics + redis** emits all four blocks correctly (and the aux blocks carry no
      external ports).
    - envgen test: `OPENSEARCH_*` and `VICTORIA_*` are written alongside `POSTGRES_*`.
    - `wsconfig.Normalize` test (Database=opensearch → Search).
    - `workspace.managedDepServices` test: search/tsdb rows present.
16. Build gates (containerized `go build/vet/test` + frontend `npm run build`), rebuild
    rigger, then live-verify: a project with Postgres **and** OpenSearch **and**
    VictoriaMetrics deploys; all tabs (Database / Search / Metrics / Redis / …) render with
    correct masked secrets. Update [search-and-timeseries-databases.md](search-and-timeseries-databases.md)
    and memory.

---

## Back-compat / migration

- `Normalize()` on load relocates any existing `Database: opensearch|victoriametrics` to the
  new `Search`/`TSDB` slots in-memory (and on next save), so prior selections keep working
  and stop occupying the DB slot. The engines were added the same day, so real-world
  exposure is expected to be ~nil.
- Compose output for projects that do NOT use search/tsdb stays byte-identical (the new
  blocks are gated off the new flags, which default empty).

## Out of scope (deferred; note in code)

- **External host-port exposure** for the aux services (internal-only in v1; reuse the
  `DBExternal`/`DB_EXTERNAL_PORT` pattern later if needed, with per-service toggles).
- **Web UI sidecars**: OpenSearch Dashboards (`search.{domain}`), Grafana / VMUI for
  VictoriaMetrics.
- Multiple engines of the same category; ElasticSearch / InfluxDB / TimescaleDB-as-Postgres.
- Logical/snapshot backups for the aux engines (still volume-restore only).

## Touched files (summary)

Backend: `internal/databases/databases.go`, `internal/wsconfig/wsconfig.go`,
`internal/workspace/workspace.go`, `internal/composegen/builders.go`,
`internal/composegen/generator.go`, `internal/envgen/envgen.go`,
`internal/backup/backup.go`, `api/service_console_handlers.go`, the PutConfig validation
site (+ tests).
Frontend: `components/DatabaseSelect.jsx`, `components/ManagedServices.jsx`,
`components/ServiceConsoleModal.jsx`, `pages/NewProjectPage.jsx`,
`pages/EditProjectPage.jsx`, possibly `lib/api.js`.
