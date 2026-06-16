// Package wsconfig is the shared, lightweight reader for a workspace's
// config.json used by the non-compose runtime operations (version, build,
// promote, bootstrap, envgen). It ports the config accessors and version/tag
// helpers that lived in scripts/lib.sh (cfg_get, cfg_env_get, version_string,
// image_tag, stack_name, validate_env).
//
// composegen intentionally keeps its own richer Config view (so image env_vars
// never leak to the UI); this package is the smaller view the other operations
// need, including env-level env_vars and fields composegen omits (https_port).
package wsconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
)

// Config is the subset of config.json the non-compose operations read.
type Config struct {
	Project      Project        `json:"project"`
	Services     []Service      `json:"services"`
	Versions     map[string]Str `json:"versions"`
	Environments map[string]Env `json:"environments"`
}

// Service is the read view of a unified services[] entry — enough for build,
// bootstrap, envgen and deploy-history to know which services build, from what
// context/template, and how to reuse images.
type Service struct {
	Name           string `json:"name"`
	Build          *Build `json:"build,omitempty"`
	Image          string `json:"image,omitempty"`
	ImageFrom      string `json:"image_from,omitempty"`
	Tag            string `json:"tag,omitempty"`
	ConfigTemplate string `json:"config_template,omitempty"` // bootstrap renders templates/nginx/<x>.conf → nginx.conf
}

// Build describes how a build service's image is produced.
type Build struct {
	Context    string            `json:"context,omitempty"`    // subdir under envs/<env>/, default = service name
	Dockerfile string            `json:"dockerfile,omitempty"` // default "Dockerfile"
	Template   string            `json:"template,omitempty"`   // templates/dockerfiles/<template> to scaffold
	Target     string            `json:"target,omitempty"`
	Args       map[string]string `json:"args,omitempty"` // --build-arg KEY=VALUE; values may use ${ENV}/${VERSION}/${ROUTE_URL}
}

// BuildServices returns the services that build from source (Build != nil).
func (c *Config) BuildServices() []Service {
	var out []Service
	for _, s := range c.Services {
		if s.Build != nil {
			out = append(out, s)
		}
	}
	return out
}

// ContextDir returns the build context subdir for a build service (default = name).
func (s Service) ContextDir() string {
	if s.Build != nil && s.Build.Context != "" {
		return s.Build.Context
	}
	return s.Name
}

type Project struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Registry string  `json:"registry"`
	Version  Version `json:"version"`
	// ResourcePrefix is the immutable Docker resource prefix ({workspace}_{project});
	// empty ⇒ fall back to Name. See workspace.Project.Prefix.
	ResourcePrefix string `json:"resource_prefix,omitempty"`
	// GitRepo / GitBranch are the project's single source repository (one repo per
	// project; each build service's build.context is a subdir). Cloned into
	// envs/{env}/_src before build. Empty ⇒ build services use scaffolded Dockerfiles.
	GitRepo   string `json:"git_repo,omitempty"`
	GitBranch string `json:"git_branch,omitempty"`
	// SourceKind selects where build services get their source. "" / "git" → the
	// GitRepo above (cloned into _src). "upload" → an uploaded archive stored under
	// the project (_source/), extracted into _src at build time (see internal/srcarchive).
	SourceKind string `json:"source_kind,omitempty"`
	// EnvOrder is the explicit deploy-tier order of this project's environments
	// (low→high, e.g. ["dev","staging","prod"]). Empty ⇒ order is auto-guessed
	// from env names. Drives the release pipeline and the project-page env strip.
	// See internal/envorder.
	EnvOrder []string `json:"env_order,omitempty"`
	// Managed dependencies are PROJECT-level (consistent across all environments):
	// the database engine (none|postgres|mysql|mariadb), its catalog version, and
	// the Redis / Garage toggles. Only the per-env DBExternal (host-port exposure)
	// stays on Env. These supersede the legacy per-env Env.Database/DBVersion/
	// RedisEnabled/GarageEnabled, which are still read as a fallback (see Eff*).
	Database  string `json:"database,omitempty"`
	DBVersion string `json:"db_version,omitempty"`
	Redis     bool   `json:"redis_enabled,omitempty"`
	Garage    bool   `json:"garage_enabled,omitempty"`
	// WebSQL adds an Adminer web-SQL client to the project (the unified flag, like
	// Redis/Garage). composegen synthesizes the service from it for any stack with a
	// database. Legacy projects instead carry a literal "adminer" service — HasAdminer
	// treats both as present. See [[adminer]] in composegen.buildAdminer.
	WebSQL bool `json:"web_sql,omitempty"`
	// GarageWebUI adds the optional Garage web admin UI sidecar (when Garage is on).
	GarageWebUI bool `json:"garage_web_ui,omitempty"`
	// DBSeed, when set, points at a bundled SQL dump (stored at _source/seed.sql,
	// found by the detector in an uploaded marketplace app) to import into the managed
	// database. Auto=true imports it automatically on first deploy of an empty DB;
	// either way it can be imported manually. See the v3 DB-seed hook.
	DBSeed *DBSeed `json:"db_seed,omitempty"`
}

// DBSeed describes a project's database-seed dump.
type DBSeed struct {
	File string `json:"file"` // stored relative name under _source (always "seed.sql")
	Auto bool   `json:"auto"` // import automatically on first deploy when the DB is empty
}

// SourceRepo returns the project's source repository URL ("" if none).
func (c *Config) SourceRepo() string { return c.Project.GitRepo }

// SourceKind reports how build services obtain their source: "upload" when source
// was uploaded as an archive, else "git" (the default — GitRepo, or none).
func (c *Config) SourceKind() string {
	if c.Project.SourceKind == "upload" {
		return "upload"
	}
	return "git"
}

// SeedSpec returns the project's configured DB-seed spec, or nil when none.
func (c *Config) SeedSpec() *DBSeed { return c.Project.DBSeed }

// Branch returns the git branch to build env from: the env override, else the
// project default, else "main".
func (c *Config) Branch(env string) string {
	if e, ok := c.Environments[env]; ok && e.Git.Branch != "" {
		return e.Git.Branch
	}
	if c.Project.GitBranch != "" {
		return c.Project.GitBranch
	}
	return "main"
}

// Prefix returns the immutable Docker resource prefix, falling back to Name.
func (p Project) Prefix() string {
	if p.ResourcePrefix != "" {
		return p.ResourcePrefix
	}
	return p.Name
}

// Managed dependencies moved from per-env to project-level. The Eff* helpers
// return the project value when set, else fall back to the given env's legacy
// value — so configs written before the move keep generating identical output
// (no migration pass needed) while new configs are driven project-wide.

// EffDatabase returns the project's database engine, falling back to the env's
// legacy Database. "none" and "" both mean no managed DB.
func (c *Config) EffDatabase(e Env) string {
	if c.Project.Database != "" {
		return c.Project.Database
	}
	return e.Database
}

// EffDBVersion returns the project's chosen DB version, falling back to the env's.
func (c *Config) EffDBVersion(e Env) string {
	if c.Project.DBVersion != "" {
		return c.Project.DBVersion
	}
	return e.DBVersion
}

// EffRedis reports whether Redis is enabled (project-level OR legacy per-env).
func (c *Config) EffRedis(e Env) bool { return c.Project.Redis || e.RedisEnabled }

// EffGarage reports whether Garage is enabled (project-level OR legacy per-env).
func (c *Config) EffGarage(e Env) bool { return c.Project.Garage || e.GarageEnabled }

// HasAdminer reports whether the project exposes the Adminer web-SQL client: the
// project-level web_sql flag (unified), or a legacy literal "adminer" service in
// services[]. Gates adminer-login.php generation + the ADMINER_LOGIN_SECRET in .env.
func (c *Config) HasAdminer() bool {
	if c.Project.WebSQL {
		return true
	}
	for _, s := range c.Services {
		if s.Name == "adminer" {
			return true
		}
	}
	return false
}

type Version struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
	Patch int `json:"patch"`
	Build int `json:"build"`
}

// Env is one environment's config. App services are project-level (Config.Services);
// database/redis/garage stay as managed-dependency toggles.
type Env struct {
	Domain    string `json:"domain"`
	HTTPPort  Str    `json:"http_port"`
	HTTPSPort Str    `json:"https_port"`
	// Managed database (catalog-driven, one per env). Database is the engine id
	// (none|postgres|mysql|mariadb); DBVersion is the chosen image tag ("" → the
	// catalog default); DBExternal publishes the DB port on the host so external
	// clients can connect. Older configs only have Database (string) — DBVersion/
	// DBExternal default to "" / false, preserving prior behaviour.
	Database   string `json:"database"` // none | postgres | mysql | mariadb
	DBVersion  string `json:"db_version,omitempty"`
	DBExternal bool   `json:"db_external,omitempty"`
	// ProtectAdminUIs gates the admin sidecars (Adminer / Garage UI) behind Traefik
	// basic-auth for this env; envgen generates the credential into .env.
	ProtectAdminUIs bool           `json:"protect_admin_uis,omitempty"`
	RedisEnabled    bool           `json:"redis_enabled"`
	GarageEnabled   bool           `json:"garage_enabled"`
	TraefikEnabled  bool           `json:"traefik_enabled"`
	Deployment      string         `json:"deployment"`
	Git             EnvGit         `json:"git"`
	EnvVars         map[string]Str `json:"env_vars"`
}

// EnvGit is the per-env git override (branch). The repo is project-level.
type EnvGit struct {
	Branch string `json:"branch"`
}

// Load reads and parses a config.json from disk.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse unmarshals config.json bytes into a Config.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ProjectType returns the project type, defaulting to "custom" (lib.sh
// '.project.type // "custom"').
func (c *Config) ProjectType() string {
	if c.Project.Type == "" {
		return "custom"
	}
	return c.Project.Type
}

// VersionString reproduces lib.sh version_string():
// "{major}.{minor}.{patch}-build.{build}".
func (c *Config) VersionString() string {
	v := c.Project.Version
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." +
		strconv.Itoa(v.Patch) + "-build." + strconv.Itoa(v.Build)
}

// Version reads a versions.<key> with a fallback (lib.sh cfg_version).
func (c *Config) Version(key, def string) string {
	if v, ok := c.Versions[key]; ok && string(v) != "" {
		return string(v)
	}
	return def
}

// ImageTag reproduces lib.sh image_tag():
// "{registry}/{project}-{service}:{version}-{env}". With no registry (a local-only
// build, e.g. a scanned repo) the "{registry}/" prefix is omitted — a leading slash
// is an invalid Docker reference (`docker build -t /foo:bar` errors).
func (c *Config) ImageTag(service, env string) string {
	name := fmt.Sprintf("%s-%s:%s-%s", c.Project.Prefix(), service, c.VersionString(), env)
	if c.Project.Registry == "" {
		return name
	}
	return c.Project.Registry + "/" + name
}

// StackName reproduces lib.sh stack_name(): "{project}_{env}". This is also the
// compose project name / service prefix.
func (c *Config) StackName(env string) string {
	return c.Project.Prefix() + "_" + env
}

// EnvNames returns the configured environment names, sorted (lib.sh cfg_envs
// used `jq keys[]`, which sorts alphabetically).
func (c *Config) EnvNames() []string {
	names := make([]string, 0, len(c.Environments))
	for name := range c.Environments {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HasEnv reports whether env is configured.
func (c *Config) HasEnv(env string) bool {
	_, ok := c.Environments[env]
	return ok
}

// ValidateEnv reproduces lib.sh validate_env(): error if env is not configured,
// listing the valid environments.
func (c *Config) ValidateEnv(env string) error {
	if c.HasEnv(env) {
		return nil
	}
	return fmt.Errorf("unknown environment %q (configured: %v)", env, c.EnvNames())
}

// ── Str ─────────────────────────────────────────────────────────────────────────
// A string that also unmarshals from a JSON number, rendering integers without a
// decimal point — matching `jq -r` (e.g. 80 → "80", "8090" → "8090"). Mirrors
// composegen.flexStr; duplicated to keep the packages independent.
type Str string

func (s *Str) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*s = ""
		return nil
	}
	if b[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*s = Str(str)
		return nil
	}
	raw := string(b)
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		if n == math.Trunc(n) && !math.IsInf(n, 0) {
			*s = Str(strconv.FormatInt(int64(n), 10))
		} else {
			*s = Str(strconv.FormatFloat(n, 'f', -1, 64))
		}
		return nil
	}
	*s = Str(raw)
	return nil
}

func (s Str) String() string { return string(s) }
