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
	"strings"

	"github.com/mansoor/rigger/ui/internal/databases"
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
	Port           int    `json:"port,omitempty"`        // container port the app listens on (Traefik target for web_routed)
	WebRouted      bool   `json:"web_routed,omitempty"`  // this service is the HTTP entry routed by Traefik
	HostPort       string `json:"host_port,omitempty"`   // published host port (older port-exposed stacks); maps host→Port
	ConfigTemplate string `json:"config_template,omitempty"` // bootstrap renders templates/nginx/<x>.conf → nginx.conf
	EnvFileMount   string `json:"env_file_mount,omitempty"`   // container path the env's .env is delivered at
	EnvFileWritable bool  `json:"env_file_writable,omitempty"` // deliver .env writable (app owns it) — see composegen
}

// UnmarshalJSON tolerates a string OR numeric `port` in config.json. Detected/scan
// services persist the container port as a string (compose ports are strings, the
// detector keeps them as such), while wizard-created services write a number — both
// must read back into Port (int). Composegen's own view already uses flexStr for
// the same reason; this mirrors that on the read side.
func (s *Service) UnmarshalJSON(b []byte) error {
	type alias Service // avoid infinite recursion
	aux := &struct {
		Port json.RawMessage `json:"port"`
		*alias
	}{alias: (*alias)(s)}
	if err := json.Unmarshal(b, aux); err != nil {
		return err
	}
	s.Port = flexInt(aux.Port)
	return nil
}

// flexInt parses a JSON value that may be a number (9000) or a numeric string
// ("9000") into an int. Empty/null/non-numeric (ranges, ${VAR}) → 0.
func flexInt(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		n, _ = strconv.Atoi(str)
		return n
	}
	return 0
}

// WebPort returns the container port of the project's web/app service — the port
// composegen routes (Traefik) or publishes (host_port) to, and therefore the port
// an app honoring $PORT must listen on. It checks, in order: the Traefik web entry,
// then the single host-port-published service (older port-exposed stacks). Returns
// 0 when neither is unambiguous, so the caller can pick its own default.
//
// Because composegen targets the same `port` field, setting $PORT to it keeps a
// $PORT-honoring app's listen port aligned with where traffic is sent.
func (c *Config) WebPort() int {
	for _, s := range c.Services {
		if s.WebRouted && s.Port > 0 {
			return s.Port
		}
	}
	// No Traefik entry: the lone service publishing a host port is the app.
	pub, n := 0, 0
	for _, s := range c.Services {
		if s.HostPort != "" && s.Port > 0 {
			pub, n = s.Port, n+1
		}
	}
	if n == 1 {
		return pub
	}
	return 0
}

// HasWritableEnvFile reports whether any service owns its .env at runtime (writable
// bind + suppressed process env). Such an env's .env is regenerated in MERGE mode:
// managed keys are re-asserted but the app's own writes are preserved.
func (c *Config) HasWritableEnvFile() bool {
	for _, s := range c.Services {
		if s.EnvFileWritable && s.EnvFileMount != "" {
			return true
		}
	}
	return false
}

// Build describes how a build service's image is produced.
type Build struct {
	Context    string            `json:"context,omitempty"`    // subdir under envs/<env>/, default = service name
	Dockerfile string            `json:"dockerfile,omitempty"` // default "Dockerfile"
	Template   string            `json:"template,omitempty"`   // templates/dockerfiles/<template> to scaffold
	Method     string            `json:"method,omitempty"`     // "" / "dockerfile" (default) | "nixpacks" — the build backend
	Target     string            `json:"target,omitempty"`
	Args       map[string]string `json:"args,omitempty"` // --build-arg KEY=VALUE; values may use ${ENV}/${VERSION}/${ROUTE_URL}
}

// IsNixpacks reports whether this build service builds via Nixpacks (no Dockerfile)
// rather than the default Dockerfile backend.
func (b *Build) IsNixpacks() bool {
	return b != nil && strings.EqualFold(b.Method, "nixpacks")
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
	// GitProviderID references a stored git_providers row (Phase 12) supplying
	// credentials to clone a PRIVATE GitRepo. 0 ⇒ public repo (no credentials).
	GitProviderID int64 `json:"git_provider_id,omitempty"`
	// SourceKind selects where build services get their source. "" / "git" → the
	// GitRepo above (cloned into _src). "upload" → an uploaded archive stored under
	// the project (_source/), extracted into _src at build time (see internal/srcarchive).
	SourceKind string `json:"source_kind,omitempty"`
	// EnvOrder is the explicit deploy-tier order of this project's environments
	// (low→high, e.g. ["dev","staging","prod"]). Empty ⇒ order is auto-guessed
	// from env names. Drives the release pipeline and the project-page env strip.
	// See internal/envorder.
	EnvOrder []string `json:"env_order,omitempty"`
	// SeedFiles are static config files (relative path → contents) a template ships for
	// apps that bind-mount a config file — e.g. prometheus.yml. Bootstrap writes each into
	// the env dir ONLY when absent, so they survive restarts (they live on the host bind)
	// and a user's later edits are never clobbered on refresh. Carried in config.json so a
	// project stays self-contained even if the source template later changes or is removed.
	SeedFiles map[string]MultilineString `json:"seed_files,omitempty"`
	// Managed dependencies are PROJECT-level (consistent across all environments):
	// the database engine (none|postgres|mysql|mariadb), its catalog version, and
	// the Redis toggle. Only the per-env DBExternal (host-port exposure) stays on
	// Env. These supersede the legacy per-env Env.Database/DBVersion/RedisEnabled,
	// which are still read as a fallback (see Eff*).
	Database  string `json:"database,omitempty"`
	DBVersion string `json:"db_version,omitempty"`
	Redis     bool   `json:"redis_enabled,omitempty"`
	// Search / TSDB / Queue are opt-in AUXILIARY managed engines that run ALONGSIDE the
	// primary Database (not in its slot): Search = "" | "opensearch"; TSDB = "" |
	// "victoriametrics"; Queue = "" | "rabbitmq" (a message broker). Internal-only in v1
	// (no external host-port exposure). See EffSearch / EffTSDB / EffQueue and
	// databases.CatalogByCategory.
	Search        string `json:"search,omitempty"`
	SearchVersion string `json:"search_version,omitempty"`
	TSDB          string `json:"tsdb,omitempty"`
	TSDBVersion   string `json:"tsdb_version,omitempty"`
	Queue         string `json:"queue,omitempty"`
	QueueVersion  string `json:"queue_version,omitempty"`
	// QueueConsole routes the managed broker's built-in web UI (RabbitMQ's management
	// plugin on :15672) through Traefik on a "rabbitmq" subdomain. The UI has its own
	// login (the managed user + password), so no edge basic-auth is layered on.
	QueueConsole  bool   `json:"queue_console,omitempty"`
	// WebSQL adds an Adminer web-SQL client to the project (the unified flag, like
	// Redis). composegen synthesizes the service from it for any stack with a
	// database. Legacy projects instead carry a literal "adminer" service — HasAdminer
	// treats both as present. See [[adminer]] in composegen.buildAdminer.
	WebSQL bool `json:"web_sql,omitempty"`
	// Mailpit is the project-level DEFAULT for the Mailpit test-SMTP sidecar (Tier-2
	// dev/admin tool, per-env overridable). See EffMailpit.
	Mailpit bool `json:"mailpit,omitempty"`
	// Object/file storage is project-level; the two backends are INDEPENDENT (local,
	// MinIO, both, or neither). StorageLocal → FILESYSTEM_DISK=local + a persistent
	// volume at StoragePath; StorageMinIO → managed MinIO S3 (+ mc bucket-init) via AWS_*.
	// When both are on, FILESYSTEM_DISK defaults to s3. Legacy ObjectStorage enum is
	// still read as a fallback. See MinIOOn / LocalStorageOn / StorageDefaultDisk.
	StorageLocal  bool   `json:"storage_local,omitempty"`
	StorageMinIO  bool   `json:"storage_minio,omitempty"`
	ObjectStorage string `json:"object_storage,omitempty"` // legacy enum: ""/none|local|minio (back-compat)
	// StorageBucket overrides the auto-derived MinIO bucket base ({prefix}); the env
	// name is always appended ({base}-{env}). Blank → derived. minio only.
	StorageBucket string `json:"storage_bucket,omitempty"`
	// StoragePath is the container path the local persistent volume mounts at
	// (default /var/www/html/storage). local only.
	StoragePath string `json:"storage_path,omitempty"`
	// StorageUI adds the opens3/console admin sidecar when ObjectStorage=minio.
	StorageUI bool `json:"storage_ui,omitempty"`
	// ExposeMode / AuthGate are the project-level DEFAULTS for the app-exposure model
	// (per-env overridable). "" = the baseline (traefik / none). See EffExposeMode.
	ExposeMode string `json:"expose_mode,omitempty"` // "" => traefik
	AuthGate   string `json:"auth_gate,omitempty"`   // "" => none
	// Deprecated: legacy Garage toggles — kept only so old config.json unmarshals.
	// Garage generation is retired; these are ignored (read as no-op). See EffObjectStorage.
	Garage      bool `json:"garage_enabled,omitempty"`
	GarageWebUI bool `json:"garage_web_ui,omitempty"`
	// DBSeed, when set, points at a bundled SQL dump (stored at _source/seed.sql,
	// found by the detector in an uploaded marketplace app) to import into the managed
	// database. Auto=true imports it automatically on first deploy of an empty DB;
	// either way it can be imported manually. See the v3 DB-seed hook.
	DBSeed *DBSeed `json:"db_seed,omitempty"`
	// Preview configures PR/preview environments for this project (opt-in). nil ⇒
	// the feature is off. See internal/previews and docs/design/preview-environments.md.
	Preview *PreviewConfig `json:"preview,omitempty"`
}

// PreviewConfig holds a project's PR/preview-environment settings. A preview is a
// short-lived env keyed pr{n}, cloned from TemplateEnv with the git branch
// overridden to the PR head, torn down on PR close. Opt-in (Enabled) and driven by
// a signed per-project webhook. See docs/design/preview-environments.md.
type PreviewConfig struct {
	Enabled         bool   `json:"enabled,omitempty"`
	TemplateEnv     string `json:"template_env,omitempty"`      // env to clone (e.g. "dev")
	Provider        string `json:"provider,omitempty"`          // "github"|"gitlab"|"gitea"
	BranchFilter    string `json:"branch_filter,omitempty"`     // optional glob, e.g. "feature/*"; "" = all
	MaxConcurrent   int    `json:"max_concurrent,omitempty"`    // cap active previews (0 = unlimited)
	TTLHours        int    `json:"ttl_hours,omitempty"`         // auto-reap after inactivity (0 = until PR closes)
	ProtectAuth     bool   `json:"protect_auth,omitempty"`      // basic-auth the preview URLs
	AutoDeployForks string `json:"auto_deploy_forks,omitempty"` // "off"|"approved"|"on" (security gate)
	WriteBack       bool   `json:"write_back,omitempty"`        // post URL/status back to the PR
	DBStrategy      string `json:"db_strategy,omitempty"`       // "isolated-empty"|"isolated-seed"|"clone-from"|"shared"
	DBSource        string `json:"db_source,omitempty"`         // env to clone-from / share (for those two modes)
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

// EffSearch / EffTSDB / EffQueue return the project's auxiliary search / time-series /
// message-queue engine ("" = none). The Env param is kept for signature parity with the
// other Eff* helpers (and a possible future per-env override); today they're project-level.
func (c *Config) EffSearch(_ Env) string        { return c.Project.Search }
func (c *Config) EffSearchVersion(_ Env) string { return c.Project.SearchVersion }
func (c *Config) EffTSDB(_ Env) string          { return c.Project.TSDB }
func (c *Config) EffTSDBVersion(_ Env) string   { return c.Project.TSDBVersion }
func (c *Config) EffQueue(_ Env) string          { return c.Project.Queue }
func (c *Config) EffQueueVersion(_ Env) string   { return c.Project.QueueVersion }

// EffQueueConsole reports whether the managed broker's web console is routed. Only
// meaningful when a queue engine is selected.
func (c *Config) EffQueueConsole(_ Env) bool { return c.Project.QueueConsole }

// MinIOOn / LocalStorageOn report the active object-storage backends (project-level,
// independent — both may be on). New flags OR the legacy ObjectStorage enum. The Env
// arg is accepted for symmetry with the other Eff* helpers (storage is project-level).
func (c *Config) MinIOOn(_ Env) bool {
	return c.Project.StorageMinIO || c.Project.ObjectStorage == "minio"
}
func (c *Config) LocalStorageOn(_ Env) bool {
	return c.Project.StorageLocal || c.Project.ObjectStorage == "local"
}

// StorageDefaultDisk is the framework's default filesystem disk: "s3" when MinIO is on
// (S3 wins as primary when both are enabled), else "local" when only local, else "".
func (c *Config) StorageDefaultDisk(e Env) string {
	if c.MinIOOn(e) {
		return "s3"
	}
	if c.LocalStorageOn(e) {
		return "local"
	}
	return ""
}

// EffGarage is retired: garage generation is removed and legacy flags are ignored.
// Kept as a no-op (always false) so any not-yet-migrated caller compiles. Remove once
// all call sites move to EffObjectStorage / MinIOOn / LocalStorageOn.
func (c *Config) EffGarage(_ Env) bool { return false }

// HasAdminer reports whether the project exposes the Adminer web-SQL client: the
// project-level web_sql flag (unified), or a legacy literal "adminer" service in
// services[]. Gates adminer-login.php generation.
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

// EffWebSQL / EffStorageUI resolve the per-env TRI-STATE override of the project-level
// dev/admin sidecar defaults: env override wins when set (non-nil), else project default.
func (c *Config) EffWebSQL(e Env) bool {
	if e.WebSQL != nil {
		return *e.WebSQL
	}
	return c.Project.WebSQL
}
func (c *Config) EffStorageUI(e Env) bool {
	if e.StorageUI != nil {
		return *e.StorageUI
	}
	return c.Project.StorageUI
}
func (c *Config) EffMailpit(e Env) bool {
	if e.Mailpit != nil {
		return *e.Mailpit
	}
	return c.Project.Mailpit
}

// EffExposeMode / EffAuthGate resolve the app-exposure model (docs/EXPOSURE_AND_
// REMOTE_ACCESS.md): how an env's web entry is reachable, and who gets in. Both are
// string overrides — per-env value wins when non-empty, else the project default,
// else the safe baseline ("traefik" / "none") that reproduces today's behaviour
// (so golden output is byte-identical when neither is set).
//
//	expose_mode: "traefik" (default) | "cloudflare_tunnel" | "none"
//	auth_gate:   "none" (default)    | "basic"             | "forward_auth"
func (c *Config) EffExposeMode(e Env) string {
	if e.ExposeMode != "" {
		return e.ExposeMode
	}
	if c.Project.ExposeMode != "" {
		return c.Project.ExposeMode
	}
	return "traefik"
}
func (c *Config) EffAuthGate(e Env) string {
	if e.AuthGate != "" {
		return e.AuthGate
	}
	if c.Project.AuthGate != "" {
		return c.Project.AuthGate
	}
	return "none"
}

// HasAdminerEnv reports whether THIS env exposes Adminer (effective web_sql, or a legacy
// literal "adminer" service). Per-env variant of HasAdminer — gates the ADMINER_LOGIN_SECRET
// and the admin-UI protection credential for the env.
func (c *Config) HasAdminerEnv(e Env) bool {
	if c.EffWebSQL(e) {
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
	// AcmeEmail is the per-env Let's Encrypt email override (blank = inherit workspace,
	// then global). See EffAcmeEmail (Phase-2 out-of-band issuer reads it).
	AcmeEmail string `json:"acme_email,omitempty"`
	// Managed database (catalog-driven, one per env). Database is the engine id
	// (none|postgres|mysql|mariadb); DBVersion is the chosen image tag ("" → the
	// catalog default); DBExternal publishes the DB port on the host so external
	// clients can connect. Older configs only have Database (string) — DBVersion/
	// DBExternal default to "" / false, preserving prior behaviour.
	Database   string `json:"database"` // none | postgres | mysql | mariadb
	DBVersion  string `json:"db_version,omitempty"`
	DBExternal bool   `json:"db_external,omitempty"`
	// ProtectAdminUIs gates the admin sidecars (Adminer / MinIO console) behind Traefik
	// basic-auth for this env; envgen generates the credential into .env.
	ProtectAdminUIs bool           `json:"protect_admin_uis,omitempty"`
	// WebSQL / StorageUI / Mailpit are per-env TRI-STATE overrides of the project-level
	// sidecar defaults (nil = inherit project, &true/&false = force). See Eff* helpers.
	WebSQL          *bool          `json:"web_sql,omitempty"`
	StorageUI       *bool          `json:"storage_ui,omitempty"`
	Mailpit         *bool          `json:"mailpit,omitempty"`
	// ExposeMode / AuthGate are per-env overrides of the project-level exposure
	// defaults ("" = inherit project, then the traefik/none baseline). See
	// EffExposeMode / EffAuthGate and docs/EXPOSURE_AND_REMOTE_ACCESS.md.
	ExposeMode      string         `json:"expose_mode,omitempty"`
	AuthGate        string         `json:"auth_gate,omitempty"`
	// AttachNetwork joins this env's web service(s) to an existing (external) Docker
	// network so another container — e.g. the user's own reverse proxy — can reach the
	// app in-network without publishing a host port. The network must already exist on
	// the host. Mainly used with expose_mode=none (Internal only). See composegen.
	AttachNetwork   string         `json:"attach_network,omitempty"`
	RedisEnabled    bool           `json:"redis_enabled"`
	GarageEnabled   bool           `json:"garage_enabled"`
	TraefikEnabled  bool           `json:"traefik_enabled"`
	SSLEnabled      bool           `json:"ssl_enabled"`
	// SSLSelfSigned serves this env over HTTPS with Traefik's self-signed cert — the
	// per-env "local HTTPS" opt-in for URLs that can't get a Let's Encrypt cert
	// (localhost / IP / magic-DNS). Supersedes the project-level local_tls default.
	SSLSelfSigned   bool           `json:"ssl_self_signed,omitempty"`
	Deployment      string         `json:"deployment"`
	Git             EnvGit         `json:"git"`
	EnvVars         map[string]Str `json:"env_vars"`
	// Secrets are Rigger-managed generated secrets (DB/app/MinIO passwords) pinned on
	// first .env generation so they survive a lost/regenerated .env (envgen reads them as
	// a preservation fallback). A SEPARATE channel from EnvVars: a repo's .env.example
	// values must never override these. Written by bootstrap; not user-editable.
	Secrets map[string]string `json:"secrets,omitempty"`
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
	c.Normalize()
	return &c, nil
}

// Normalize self-heals a freshly-parsed config. The search/tsdb engines (opensearch,
// victoriametrics) were briefly selectable in the single Database slot; relocate any such
// selection into the dedicated Search / TSDB slots (carrying its version) so it runs as an
// auxiliary service alongside a primary DB and frees the DB slot. Idempotent; a no-op for
// configs that never used them, so compose/env output stays byte-identical.
func (c *Config) Normalize() {
	if c.Project.Database == "" {
		return
	}
	eng, ok := databases.Get(c.Project.Database)
	if !ok {
		return
	}
	switch eng.Category {
	case "search":
		if c.Project.Search == "" {
			c.Project.Search, c.Project.SearchVersion = c.Project.Database, c.Project.DBVersion
		}
		c.Project.Database, c.Project.DBVersion = "", ""
	case "tsdb":
		if c.Project.TSDB == "" {
			c.Project.TSDB, c.Project.TSDBVersion = c.Project.Database, c.Project.DBVersion
		}
		c.Project.Database, c.Project.DBVersion = "", ""
	case "queue":
		if c.Project.Queue == "" {
			c.Project.Queue, c.Project.QueueVersion = c.Project.Database, c.Project.DBVersion
		}
		c.Project.Database, c.Project.DBVersion = "", ""
	}
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

// ── MultilineString ──────────────────────────────────────────────────────────────
// A seed-file body that unmarshals from EITHER a JSON string OR an array of strings,
// in which case the lines are joined with "\n" (plus a trailing newline, so the file
// ends cleanly). The array form lets a template author a multi-line config file as a
// readable line list instead of one long "\n"-escaped string — avoiding the easy
// mistake of a malformed escape breaking the whole JSON. It marshals back as a plain
// string (the default for a string type), so config.json always stores the joined form.
type MultilineString string

func (m *MultilineString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*m = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*m = MultilineString(s)
		return nil
	}
	if b[0] == '[' {
		var lines []string
		if err := json.Unmarshal(b, &lines); err != nil {
			return fmt.Errorf("seed file content must be a string or array of strings: %w", err)
		}
		*m = MultilineString(strings.Join(lines, "\n") + "\n")
		return nil
	}
	return fmt.Errorf("seed file content must be a string or array of strings, got %s", b)
}

func (m MultilineString) String() string { return string(m) }
