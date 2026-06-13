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
}

// catalog is the ordered engine list (drives display order in the picker).
var catalog = []Engine{
	{
		ID: "postgres", Label: "PostgreSQL", Image: "postgres",
		Versions:       []string{"17-alpine", "16-alpine", "15-alpine", "14-alpine", "13-alpine"},
		DefaultVersion: "15-alpine",
		Port:           5432, VolumePath: "/var/lib/postgresql/data",
		Driver: "postgres", EnvPrefix: "POSTGRES", Schemas: true, Users: true,
	},
	{
		ID: "mysql", Label: "MySQL", Image: "mysql",
		Versions:       []string{"8.4", "8.0", "5.7"},
		DefaultVersion: "8.0",
		Port:           3306, VolumePath: "/var/lib/mysql",
		Driver: "mysql", EnvPrefix: "MYSQL", Schemas: true, Users: true,
	},
	{
		ID: "mariadb", Label: "MariaDB", Image: "mariadb",
		Versions:       []string{"11", "10.11", "10.6"},
		DefaultVersion: "11",
		Port:           3306, VolumePath: "/var/lib/mysql",
		Driver: "mysql", EnvPrefix: "MYSQL", Schemas: true, Users: true,
	},
}

// Catalog returns the ordered list of hostable engines.
func Catalog() []Engine {
	out := make([]Engine, len(catalog))
	copy(out, catalog)
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
