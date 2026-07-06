// Package composegen generates docker-compose.yml from a workspace config,
// natively in Go. It is a byte-for-byte replacement for the legacy
// scripts/compose-gen.sh (Phase 6.5a) — the source of most historical YAML
// bugs — so the bash script can be retired from the runtime path.
package composegen

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"

	"github.com/mansoor/rigger/ui/internal/databases"
)

// Config is the complete view of a workspace config.json that the generator
// needs. It is intentionally separate from workspace.Config (the API/frontend
// view) so generation stays decoupled and image env_vars never leak to the UI.
type Config struct {
	Project      Project            `json:"project"`
	Services     []Service          `json:"services"`
	Versions     map[string]flexStr `json:"versions"`
	NamedVolumes []NamedVolume      `json:"named_volumes"`
	Environments map[string]Env     `json:"environments"`
	// Routes is the project-level public-ingress table: each row maps a path prefix or a
	// subdomain on the env's domain to a target service. EMPTY ⇒ legacy behavior (the single
	// web_routed service catches all traffic via Host(domain) — see emitServicePorts); the
	// presence of rows (NOT the service count) switches composegen to route-driven Traefik
	// labels (emitRouteLabels). Host comes from each environment's domain, so one table
	// applies across all envs. The schema leaves room for future per-row weight / rewrite /
	// rate-limit / IP columns (traffic split, canary, version aliasing) — not built yet.
	Routes []Route `json:"routes,omitempty"`
}

// Route is one public-ingress rule: it sends a path prefix or a subdomain (on the env's
// domain) to a target service. See Config.Routes.
type Route struct {
	Service string `json:"service"` // target service short name (must exist in Services)
	Type    string `json:"type"`    // "path" | "subdomain"
	// Match is the path prefix ("/api"; "" or "/" = catch-all, type=path) OR the subdomain
	// label ("app" → app.{domain}; "" = apex, type=subdomain).
	Match string `json:"match"`
	// Target (path routes only) rewrites the matched prefix the backend sees: it receives
	// Target + (incoming path − Match), with the remainder sub-path and query string preserved.
	// "" or == Match ⇒ passthrough (no rewrite); "/" ⇒ strip the prefix (/api/x → /x); "/api/v1"
	// ⇒ strip then re-prefix (/api/x → /api/v1/x). Enables version aliasing by editing one field.
	Target string `json:"target,omitempty"`
}

// routesFor returns the routes targeting service name (nil when none / Routes empty).
func (c *Config) routesFor(name string) []Route {
	var out []Route
	for _, r := range c.Routes {
		if r.Service == name {
			out = append(out, r)
		}
	}
	return out
}

type Project struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Registry string  `json:"registry"`
	Version  Version `json:"version"`
	// ResourcePrefix is the immutable Docker resource prefix ({workspace}_{project});
	// empty ⇒ fall back to Name. See workspace.Project.Prefix.
	ResourcePrefix string `json:"resource_prefix,omitempty"`
	// GitRepo is the project's source repository ("" ⇒ none). When set, the env's
	// source is checked out to envs/{env}/_src, so repo-relative bind-mount sources
	// (e.g. ./mosquitto/mosquitto.conf) are re-rooted there. See gen.bindSource.
	GitRepo string `json:"git_repo,omitempty"`
	// SourceKind "upload" means source came from an uploaded archive (extracted into
	// _src at build time, like a clone) — so bind sources re-root under _src too.
	SourceKind string `json:"source_kind,omitempty"`
	// LocalTLS: when an env auto-routes on *.localhost (no workspace base domain),
	// serve it over Traefik's self-signed cert instead of plain HTTP — for apps
	// that require HTTPS locally (e.g. Vaultwarden). Ignored once a base domain is set.
	LocalTLS bool `json:"local_tls,omitempty"`
	// Managed dependencies are project-level (consistent across envs): the DB engine
	// + version and the Redis toggle. Only per-env DBExternal stays on Env.
	// Legacy per-env Env.Database/DBVersion/Redis are read as a fallback so
	// pre-move configs generate identical YAML — see gen.dbEngine/redisOn.
	Database  string `json:"database,omitempty"`
	DBVersion string `json:"db_version,omitempty"`
	Redis     bool   `json:"redis_enabled,omitempty"`
	// Search / TSDB / Queue are opt-in auxiliary engines (opensearch / victoriametrics /
	// rabbitmq) emitted ALONGSIDE the primary DB (internal-only in v1). See
	// gen.searchEngine/tsdbEngine/queueEngine.
	Search        string `json:"search,omitempty"`
	SearchVersion string `json:"search_version,omitempty"`
	TSDB          string `json:"tsdb,omitempty"`
	TSDBVersion   string `json:"tsdb_version,omitempty"`
	Queue         string `json:"queue,omitempty"`
	QueueVersion  string `json:"queue_version,omitempty"`
	QueueConsole  bool   `json:"queue_console,omitempty"` // route RabbitMQ's :15672 mgmt UI
	// WebSQL synthesizes an Adminer web-SQL service (see buildAdminer) — the unified
	// flag, like Redis. Legacy projects carry a literal "adminer" service in
	// Services instead; buildAdminer skips synthesis when one already exists.
	WebSQL bool `json:"web_sql,omitempty"`
	// Mailpit is the project-level DEFAULT for the Mailpit test-SMTP sidecar (a Tier-2
	// dev/admin tool, per-env overridable like WebSQL/StorageUI). See gen.mailpitOn.
	Mailpit bool `json:"mailpit,omitempty"`
	// Object/file storage is project-level and the two backends are INDEPENDENT — a
	// project can have local, MinIO, both, or neither:
	//   StorageLocal → FILESYSTEM_DISK=local + a persistent named volume at StoragePath
	//   StorageMinIO → managed MinIO S3 (server + one-shot mc bucket-init) wired via AWS_*
	// When both are on, the local volume is mounted AND MinIO runs; FILESYSTEM_DISK
	// defaults to s3. Legacy ObjectStorage ("local"/"minio") is still read as a fallback
	// (see gen.localStorageOn/minioOn). Garage is retired (fields below kept for unmarshal).
	StorageLocal  bool   `json:"storage_local,omitempty"`
	StorageMinIO  bool   `json:"storage_minio,omitempty"`
	ObjectStorage string `json:"object_storage,omitempty"` // legacy enum: ""/none|local|minio (back-compat)
	// StorageBucket overrides the auto-derived MinIO bucket base ({prefix}); the env
	// name is always appended ({base}-{env}). Blank → derived. minio only.
	StorageBucket string `json:"storage_bucket,omitempty"`
	// StoragePath is the container path the local persistent volume mounts at (default
	// /var/www/html/storage). local only.
	StoragePath string `json:"storage_path,omitempty"`
	// StorageUI adds the opens3/console admin sidecar (routed on the "storage"
	// subdomain) when ObjectStorage=minio. Off by default (MinIO alone = S3 only).
	StorageUI bool `json:"storage_ui,omitempty"`
	// Deprecated: legacy Garage toggles — read only for back-compat unmarshal; ignored.
	Garage      bool `json:"garage_enabled,omitempty"`
	GarageWebUI bool `json:"garage_web_ui,omitempty"`
	// ExposeMode / AuthGate are the project-level DEFAULTS for the app-exposure model
	// (per-env overridable). "" = the baseline (traefik / none). See gen.exposeMode.
	ExposeMode string `json:"expose_mode,omitempty"` // "" => traefik
	AuthGate   string `json:"auth_gate,omitempty"`   // "" => none
}

type Version struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
	Patch int `json:"patch"`
	Build int `json:"build"`
}

type NamedVolume struct {
	Name string `json:"name"`
}

// Env is one environment's config. The app services live in Config.Services
// (project level); per-env knobs live here. Database/Redis/Garage stay as simple
// managed-dependency toggles until Phase 3 folds them into the service graph.
type Env struct {
	Domain     string  `json:"domain"`
	HTTPPort   flexStr `json:"http_port"`
	Database   string  `json:"database"` // none | postgres | mysql | mariadb
	DBVersion  string  `json:"db_version,omitempty"`
	DBExternal bool    `json:"db_external,omitempty"`
	// ProtectAdminUIs gates the synthesized admin sidecars (Adminer / MinIO console)
	// behind Traefik HTTP basic-auth for THIS environment (creds generated into .env by
	// envgen). Only enforceable under Traefik routing; a no-Traefik host-port env can't apply it.
	ProtectAdminUIs bool `json:"protect_admin_uis,omitempty"`
	// WebSQL / StorageUI are per-env TRI-STATE overrides of the project-level defaults for
	// the dev/admin sidecars (Adminer / MinIO console): nil = inherit the project value,
	// &true / &false = force on/off for this env. Lets tooling run in dev/stage but not
	// prod. See gen.webSQLOn / storageUIOn.
	WebSQL    *bool `json:"web_sql,omitempty"`
	StorageUI *bool `json:"storage_ui,omitempty"`
	Mailpit   *bool `json:"mailpit,omitempty"` // per-env tri-state override of Project.Mailpit
	// ExposeMode / AuthGate are per-env overrides of the project exposure defaults
	// ("" = inherit project, then the traefik/none baseline). See gen.exposeMode.
	ExposeMode string `json:"expose_mode,omitempty"`
	AuthGate   string `json:"auth_gate,omitempty"`
	// TraefikHostPorts is a per-env override of whether a WEB-ROUTED service still
	// publishes its primary host port when Traefik is enabled: "" inherit the workspace
	// default (RouteOpts.KeepHostPortsUnderTraefik; default = strip), "strip" force off,
	// "keep" force on. Under Traefik the app is reached by domain, so the host port is
	// redundant and the main source of host-port conflicts — stripping it is the default.
	// Only the web entry's primary port is affected; extra_ports and non-web-routed
	// services (a deliberately-exposed DB / SSH port) always publish. See gen.keepHostPort.
	TraefikHostPorts string `json:"traefik_host_ports,omitempty"`
	// AttachNetwork joins web service(s) to an existing external Docker network so a
	// user-run proxy / another stack can reach the app in-network. Must already exist.
	AttachNetwork string `json:"attach_network,omitempty"`
	// CustomDomains are VERIFIED external domains routed to this env's apex web service
	// in addition to its auto subdomain (Render-style). Not persisted in config.json —
	// the bridge injects them from the DB onto RouteOpts at generation time.
	CustomDomains  []string `json:"-"`
	// RouterMiddlewares: extra Traefik file-provider middleware refs (workspace access list +
	// WAF/cache plugins) for this env's APP routers. Not persisted here — the API/bridge
	// resolves them from the DB onto RouteOpts at generation time (the per-env attach inputs
	// access_list_id/waf/cache live in config.json and survive verbatim via PutConfig). See
	// docs/design/workspace-plugins-and-access-lists.md.
	RouterMiddlewares []string `json:"-"`
	RedisEnabled   bool     `json:"redis_enabled"`
	GarageEnabled  bool     `json:"garage_enabled"`
	TraefikEnabled bool     `json:"traefik_enabled"`
	TraefikNetwork string   `json:"traefik_network"`
	SSLEnabled     bool     `json:"ssl_enabled"`
	// AcmeEmail is the per-env Let's Encrypt account email override (blank ⇒ inherit
	// the workspace, then global ACME email). Consumed by the Phase-2 out-of-band
	// issuer; carried here so it round-trips through generation.
	AcmeEmail string `json:"acme_email,omitempty"`
	// SSLSelfSigned routes HTTPS through Traefik's default (self-signed) cert
	// instead of Let's Encrypt — used for local *.localhost envs that need HTTPS
	// (e.g. Vaultwarden) but can't get a public cert. Ignored unless SSLEnabled.
	SSLSelfSigned bool   `json:"ssl_self_signed,omitempty"`
	Deployment    string `json:"deployment"`
	// ServiceOverrides appends per-service YAML for THIS env (keyed by service name).
	ServiceOverrides map[string]ServiceOverride `json:"service_overrides"`
	// Swarm holds per-env Docker Swarm scheduling: env-level rolling-update/restart
	// policy plus per-service replicas/placement overrides. Only consulted for swarm
	// deployments; ignored for compose.
	Swarm SwarmConfig `json:"swarm"`
	// SecretKeys / SecretVersions drive Docker Swarm secret wiring (Phase 8).
	// Only consulted for swarm deployments; ignored for compose.
	SecretKeys     []string       `json:"secret_keys"`
	SecretVersions map[string]int `json:"secret_versions"`

	// ── Transient routing state (set by resolveRoute, never parsed from config) ──
	// certResolver is the Traefik ACME resolver an SSL router should use:
	// "letsencrypt" (per-host HTTP-01) or "dns" (DNS-01, enables wildcards).
	certResolver string
	// wildcardBase, when set, makes the apex web router request a wildcard cert
	// (tls.domains main={base} sans=*.{base}) so all base-domain apps share one cert.
	wildcardBase string
	// useFileCert routes an SSL env's TLS to Traefik's FILE-PROVIDER cert (issued
	// out-of-band under a per-env/per-workspace ACME email) instead of a Traefik ACME
	// resolver: the router emits tls=true with NO certresolver, so Traefik serves the
	// matching file cert by SNI. Set from RouteOpts.OverrideCert.
	useFileCert bool
	// keepHostPort is the resolved decision (per-env override → workspace default) for
	// whether a web-routed service publishes its primary host port under Traefik. false
	// (default) ⇒ Traefik-only (strip the redundant host port). Set in generate().
	keepHostPort bool
}

// Service is one entry in config.json services[] — the unified app-service model
// that replaces the old custom backend/frontend enums, the image-stack images[],
// and the Phase 1 processes[]. Exactly one source is set:
//
//	Build     — build from a context dir (envs/{env}/{name}/Dockerfile)
//	Image     — pull a prebuilt image ("nginx:1.25")
//	ImageFrom — reuse another (Build) service's image (workers/scheduler)
//
// Language-specific opinions (port, healthcheck, whether an nginx fronting
// service is needed) live in the seed blueprint, NOT the generator.
type Service struct {
	Name      string     `json:"name"`           // dns-safe, unique
	Role      string     `json:"role,omitempty"` // app | worker | static | predeploy (informational; "predeploy" marks a synthesized one-shot migrate service)
	Build     *BuildSpec `json:"build,omitempty"`
	Image     string     `json:"image,omitempty"`
	ImageFrom string     `json:"image_from,omitempty"`
	Tag       string     `json:"tag,omitempty"` // Image source only; build tag derives from version
	Command   string     `json:"command,omitempty"`
	// PreDeploy is a Render-style release/migrate command set on a BUILD service. When
	// non-empty, composegen synthesizes a one-shot "{name}-migrate" service (reusing this
	// service's image) that runs the command and exits, and gates this service (and any
	// sibling reusing its image) on it via depends_on service_completed_successfully — so
	// migrations run, once, before the app starts; a non-zero exit aborts the deploy. The
	// previously-running app keeps serving on failure (it's not recreated). Compose only —
	// docker stack deploy ignores depends_on conditions, so synthesis is skipped on Swarm.
	PreDeploy         string            `json:"pre_deploy,omitempty"`
	Port              flexStr           `json:"port,omitempty"`      // container port it listens on
	HostPort          flexStr           `json:"host_port,omitempty"` // publish host:container (compose, non-traefik)
	ExtraPorts        []flexStr         `json:"extra_ports,omitempty"`
	WebRouted         bool              `json:"web_routed,omitempty"` // primary HTTP entry (Traefik / host port)
	Subdomain         string            `json:"subdomain,omitempty"`  // "" = {domain}, "app" = app.{domain}
	Healthcheck       string            `json:"healthcheck,omitempty"`
	HealthcheckConfig HealthcheckConfig `json:"healthcheck_config,omitempty"`
	Volumes           []string          `json:"volumes,omitempty"`
	DependsOn         []string          `json:"depends_on,omitempty"` // short service / managed-dep names
	// DependsOnConditions overrides the per-dependency compose `condition:` keyed by the
	// dependency's short name. Used to (a) emit service_completed_successfully for the
	// synthesized pre-deploy gate and (b) round-trip an imported compose's own conditions
	// faithfully (the detector captures the long form). When a dep has no entry here,
	// composegen derives the condition (one-shot predeploy ⇒ completed_successfully; has a
	// healthcheck ⇒ service_healthy; else service_started). Compose only (swarm ignores it).
	DependsOnConditions map[string]string `json:"depends_on_conditions,omitempty"`
	Restart             string            `json:"restart,omitempty"`
	EnvFile             bool              `json:"env_file,omitempty"`       // inject the env's .env as process env (env_file:)
	EnvFileMount        string            `json:"env_file_mount,omitempty"` // also bind the env's .env as a physical file at this container path
	// EnvFileWritable changes env_file_mount delivery from a read-only inline config
	// to a WRITABLE bind of the env's real .env (rooted at ${RIGGER_BIND_ROOT}), and
	// suppresses process-env (env_file:) injection for this service. For apps that own
	// their .env at runtime — e.g. CodeCanyon web installers that write INSTALLED=true
	// to .env: process env would otherwise override that write, so it could never
	// "stick". The bind lives in the env dir, so writes persist across recreate and
	// migrate with the env on a host move (a named volume would not). Requires
	// EnvFileMount. Rigger re-asserts managed-infra keys on regen but preserves the
	// app's own keys (see envgen merge mode).
	EnvFileWritable bool               `json:"env_file_writable,omitempty"`
	EnvVars         map[string]flexStr `json:"env_vars,omitempty"`
	Links           []ServiceLink      `json:"links,omitempty"` // service→service URL wiring, emitted into environment:
	// AuthProtect marks a synthesized admin sidecar (Adminer / Garage UI) as eligible
	// for the per-env basic-auth middleware. Not persisted on real services — set only
	// by buildAdminer/buildGarageWebUI; gated further on Env.ProtectAdminUIs + Traefik.
	AuthProtect  bool    `json:"-"`
	ExtraCompose string  `json:"extra_compose,omitempty"`
	Replicas     flexStr `json:"replicas,omitempty"` // default; per-env override via Swarm.Services
}

// ServiceLink declares that THIS service needs another service's in-network URL,
// injected as an environment variable. Rigger emits
// {EnvVar}={Scheme}://{prefix}_{Service}:{Port}{Path} into the service's
// environment: block (which overrides env_file), so a multi-service app can reach a
// sibling without the user hand-computing the in-network host. Manual env vars still
// work — links are an opt-in convenience, detector-proposed but never forced.
type ServiceLink struct {
	Service string `json:"service"`          // target service short name (app or managed dep)
	EnvVar  string `json:"env_var"`          // env key injected into THIS service
	Port    string `json:"port,omitempty"`   // default = target's port (managed-dep default if managed)
	Path    string `json:"path,omitempty"`   // optional suffix, e.g. "/api"
	Scheme  string `json:"scheme,omitempty"` // default "http"
}

// BuildSpec describes how a Build service's image is built.
type BuildSpec struct {
	Context    string            `json:"context,omitempty"`    // subdir, default = service name
	Dockerfile string            `json:"dockerfile,omitempty"` // default "Dockerfile"
	Target     string            `json:"target,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// SwarmConfig is the per-env Docker Swarm deploy tuning. Empty fields fall back to
// Rigger's defaults (which match the historical hardcoded output), so existing
// configs and compose envs are unaffected.
type SwarmConfig struct {
	RestartPolicy  *RestartPolicy          `json:"restart_policy,omitempty"`
	UpdateConfig   *UpdateConfig           `json:"update_config,omitempty"`
	RollbackConfig *UpdateConfig           `json:"rollback_config,omitempty"`
	Services       map[string]SwarmService `json:"services,omitempty"` // short svc name → overrides
}

// RestartPolicy maps to compose deploy.restart_policy.
type RestartPolicy struct {
	Condition   string  `json:"condition"` // any | on-failure | none
	Delay       string  `json:"delay"`
	MaxAttempts flexStr `json:"max_attempts"`
	Window      string  `json:"window"`
}

// UpdateConfig maps to compose deploy.update_config (also reused for rollback_config).
type UpdateConfig struct {
	Parallelism   flexStr `json:"parallelism"`
	Delay         string  `json:"delay"`
	Order         string  `json:"order"`          // stop-first | start-first
	FailureAction string  `json:"failure_action"` // pause | continue | rollback
}

// SwarmService is a per-service override: replica count and placement constraints.
type SwarmService struct {
	Replicas  flexStr  `json:"replicas"`
	Placement []string `json:"placement"` // constraints, e.g. "node.role==manager"
}

type ServiceOverride struct {
	ExtraCompose string `json:"extra_compose"`
}

type HealthcheckConfig struct {
	Interval      flexStr `json:"interval"`
	Timeout       flexStr `json:"timeout"`
	Retries       flexStr `json:"retries"`
	StartPeriod   flexStr `json:"start_period"`
	StartInterval flexStr `json:"start_interval"`
}

// parseConfig unmarshals config.json bytes into Config.
func parseConfig(data []byte) (*Config, error) {
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	c.normalizeAux()
	return &c, nil
}

// normalizeAux relocates an auxiliary engine (opensearch / victoriametrics) that a legacy
// config selected in the single Database slot into the Search / TSDB slots, so it emits as
// an auxiliary service alongside the primary DB. composegen parses raw config.json bytes
// (it doesn't go through wsconfig.Normalize), so it self-heals here. No-op otherwise.
func (c *Config) normalizeAux() {
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

// versionString reproduces lib.sh version_string(): "{major}.{minor}.{patch}-build.{build}".
func (c *Config) versionString() string {
	v := c.Project.Version
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." +
		strconv.Itoa(v.Patch) + "-build." + strconv.Itoa(v.Build)
}

// version reads a versions.<key> with a fallback (lib.sh cfg_version).
func (c *Config) version(key, def string) string {
	if v, ok := c.Versions[key]; ok && string(v) != "" {
		return string(v)
	}
	return def
}

// resourcePrefix returns the immutable Docker resource prefix, falling back to
// the project display name for configs created before resource_prefix existed.
func (c *Config) resourcePrefix() string {
	if c.Project.ResourcePrefix != "" {
		return c.Project.ResourcePrefix
	}
	return c.Project.Name
}

// ── flexStr ─────────────────────────────────────────────────────────────────────
// A string that also unmarshals from a JSON number, rendering integers without a
// decimal point — matching `jq -r` (e.g. 80 → "80", "8090" → "8090").
type flexStr string

func (f *flexStr) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexStr(s)
		return nil
	}
	s := string(b)
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		if n == math.Trunc(n) && !math.IsInf(n, 0) {
			*f = flexStr(strconv.FormatInt(int64(n), 10))
		} else {
			*f = flexStr(strconv.FormatFloat(n, 'f', -1, 64))
		}
		return nil
	}
	*f = flexStr(s)
	return nil
}

func (f flexStr) String() string { return string(f) }
