// Package databases is the catalog (knowledge base) of managed database engines
// Rigger can host: their images, selectable versions, default ports, data-volume
// path, the framework connection driver/env-var family, and which management
// operations they support. It is the single source of truth consumed by the New
// Project wizard, the config model, composegen, envgen and the (future) database
// info + management surfaces — so adding an engine/version is a one-file change.
//
// Defaults intentionally match the previously-hardcoded versions (postgres
// 15-alpine, mysql 8.0) so existing generated compose stays byte-identical until a
// user explicitly picks a newer version.
package databases

// Engine describes one hostable database engine.
type Engine struct {
	ID             string   `json:"id"`              // "postgres" | "mysql" | "mariadb"
	Label          string   `json:"label"`           // "PostgreSQL"
	Image          string   `json:"image"`           // docker image repo (no tag): postgres | mysql | mariadb
	Versions       []string `json:"versions"`        // selectable image tags, newest first
	DefaultVersion string   `json:"default_version"` // tag used when none chosen
	Port           int      `json:"port"`            // standard listen port (5432 / 3306)
	VolumePath     string   `json:"volume_path"`     // container data dir backed by a named volume
	Driver         string   `json:"driver"`          // framework driver family: "postgres" | "mysql"
	EnvPrefix      string   `json:"env_prefix"`      // connection env-var family: "POSTGRES" | "MYSQL"
	Schemas        bool     `json:"schemas"`         // management: list/create schema or database
	Users          bool     `json:"users"`           // management: user / grant administration
	// Category groups the engine by role: "database" (the single primary DB slot),
	// "search" (OpenSearch), "tsdb" (VictoriaMetrics), or "queue" (RabbitMQ). The
	// search/tsdb/queue engines run as opt-in AUXILIARY services ALONGSIDE a primary
	// database, each in its own project slot — not in the DB slot.
	Category string `json:"category"`
}

// catalog is the ordered engine list (drives display order in the picker).
var catalog = []Engine{
	{
		ID: "postgres", Label: "PostgreSQL", Image: "postgres",
		Versions:       []string{"17-alpine", "16-alpine", "15-alpine", "14-alpine", "13-alpine"},
		DefaultVersion: "15-alpine",
		Port:           5432, VolumePath: "/var/lib/postgresql/data",
		Driver: "postgres", EnvPrefix: "POSTGRES", Schemas: true, Users: true, Category: "database",
	},
	{
		ID: "mysql", Label: "MySQL", Image: "mysql",
		Versions:       []string{"8.4", "8.0", "5.7"},
		DefaultVersion: "8.0",
		Port:           3306, VolumePath: "/var/lib/mysql",
		Driver: "mysql", EnvPrefix: "MYSQL", Schemas: true, Users: true, Category: "database",
	},
	{
		ID: "mariadb", Label: "MariaDB", Image: "mariadb",
		Versions:       []string{"11", "10.11", "10.6"},
		DefaultVersion: "11",
		Port:           3306, VolumePath: "/var/lib/mysql",
		Driver: "mysql", EnvPrefix: "MYSQL", Schemas: true, Users: true, Category: "database",
	},
	{
		// MongoDB — document store (NOT SQL). The SQL management surface (Adminer
		// auto-login, schema/database + user administration via psql/mysql) does not
		// apply, so Schemas/Users are false: the console shows connection info only.
		// A web admin UI (mongo-express) is a planned second iteration.
		ID: "mongodb", Label: "MongoDB", Image: "mongo",
		Versions:       []string{"7", "6", "5"},
		DefaultVersion: "7",
		Port:           27017, VolumePath: "/data/db",
		Driver: "mongodb", EnvPrefix: "MONGO", Schemas: false, Users: false, Category: "database",
	},
	{
		// OpenSearch — search/analytics engine (Apache-2.0 fork of ElasticSearch, ES
		// 7.10 API-compatible). NOT SQL: no Adminer/schema/user management (Schemas/
		// Users false → connection info only). Runs single-node with the security
		// plugin ON, so it speaks HTTPS on 9200 with a self-signed demo cert and a
		// fixed `admin` user (password = OPENSEARCH_INITIAL_ADMIN_PASSWORD). Needs RAM
		// (JVM) and the host sysctl vm.max_map_count=262144 — surfaced in the console.
		ID: "opensearch", Label: "OpenSearch", Image: "opensearchproject/opensearch",
		Versions:       []string{"2", "1"},
		DefaultVersion: "2",
		Port:           9200, VolumePath: "/usr/share/opensearch/data",
		Driver: "opensearch", EnvPrefix: "OPENSEARCH", Schemas: false, Users: false, Category: "search",
	},
	{
		// VictoriaMetrics — time-series database (Prometheus-compatible: PromQL +
		// remote-write). NOT SQL (Schemas/Users false → connection info only). Single
		// node, no auth on :8428 by default, so there's no user/password. The image is
		// built FROM scratch (no shell) → it carries NO Docker healthcheck (dependents
		// wait for service_started, like MinIO).
		ID: "victoriametrics", Label: "VictoriaMetrics", Image: "victoriametrics/victoria-metrics",
		Versions:       []string{"v1.102.0", "latest"},
		DefaultVersion: "v1.102.0",
		Port:           8428, VolumePath: "/victoria-metrics-data",
		Driver: "victoriametrics", EnvPrefix: "VICTORIA", Schemas: false, Users: false, Category: "tsdb",
	},
	{
		// RabbitMQ — AMQP message broker (auxiliary "queue" engine, alongside the DB).
		// NOT SQL (Schemas/Users false → connection info only). The `-management` image
		// tag bundles the web UI on :15672. RabbitMQ's default `guest` user is
		// loopback-only, so envgen provisions a real network-reachable user via
		// RABBITMQ_DEFAULT_USER (see envgen). Single-node; rabbitmq-diagnostics healthcheck.
		ID: "rabbitmq", Label: "RabbitMQ", Image: "rabbitmq",
		Versions:       []string{"4-management", "3.13-management", "3.12-management"},
		DefaultVersion: "3.13-management",
		Port:           5672, VolumePath: "/var/lib/rabbitmq",
		Driver: "rabbitmq", EnvPrefix: "RABBITMQ", Schemas: false, Users: false, Category: "queue",
	},
}

// Catalog returns the ordered list of hostable engines.
func Catalog() []Engine {
	out := make([]Engine, len(catalog))
	copy(out, catalog)
	return out
}

// CatalogByCategory returns the engines in one category ("database" | "search" | "tsdb"),
// preserving catalog order. Drives the primary-DB picker (database) and the auxiliary
// Search / Metrics pickers.
func CatalogByCategory(cat string) []Engine {
	out := []Engine{}
	for _, e := range catalog {
		if e.Category == cat {
			out = append(out, e)
		}
	}
	return out
}

// Get returns the engine by id (e.g. "postgres").
func Get(id string) (Engine, bool) {
	for _, e := range catalog {
		if e.ID == id {
			return e, true
		}
	}
	return Engine{}, false
}

// IsKnown reports whether id is a catalog engine.
func IsKnown(id string) bool {
	_, ok := Get(id)
	return ok
}

// DefaultVersion returns the engine's default image tag ("" if unknown).
func DefaultVersion(id string) string {
	e, ok := Get(id)
	if !ok {
		return ""
	}
	return e.DefaultVersion
}

// ResolveVersion returns ver if it is a known tag for the engine, else the
// engine's default. A blank/unknown version is normalised to the default so a
// hand-edited or legacy config still generates a valid image.
func ResolveVersion(id, ver string) string {
	e, ok := Get(id)
	if !ok {
		return ver
	}
	for _, v := range e.Versions {
		if v == ver {
			return ver
		}
	}
	return e.DefaultVersion
}
