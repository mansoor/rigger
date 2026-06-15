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
	// LocalTLS: when an env auto-routes on *.localhost (no workspace base domain),
	// serve it over Traefik's self-signed cert instead of plain HTTP — for apps
	// that require HTTPS locally (e.g. Vaultwarden). Ignored once a base domain is set.
	LocalTLS bool `json:"local_tls,omitempty"`
	// Managed dependencies are project-level (consistent across envs): the DB engine
	// + version and the Redis/Garage toggles. Only per-env DBExternal stays on Env.
	// Legacy per-env Env.Database/DBVersion/Redis/Garage are read as a fallback so
	// pre-move configs generate identical YAML — see gen.dbEngine/redisOn/garageOn.
	Database  string `json:"database,omitempty"`
	DBVersion string `json:"db_version,omitempty"`
	Redis     bool   `json:"redis_enabled,omitempty"`
	Garage    bool   `json:"garage_enabled,omitempty"`
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
	Domain         string  `json:"domain"`
	HTTPPort       flexStr `json:"http_port"`
	Database       string  `json:"database"` // none | postgres | mysql | mariadb
	DBVersion      string  `json:"db_version,omitempty"`
	DBExternal     bool    `json:"db_external,omitempty"`
	RedisEnabled   bool    `json:"redis_enabled"`
	GarageEnabled  bool    `json:"garage_enabled"`
	TraefikEnabled bool    `json:"traefik_enabled"`
	TraefikNetwork string  `json:"traefik_network"`
	SSLEnabled     bool    `json:"ssl_enabled"`
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
	Name              string             `json:"name"`           // dns-safe, unique
	Role              string             `json:"role,omitempty"` // app | worker | static (informational)
	Build             *BuildSpec         `json:"build,omitempty"`
	Image             string             `json:"image,omitempty"`
	ImageFrom         string             `json:"image_from,omitempty"`
	Tag               string             `json:"tag,omitempty"` // Image source only; build tag derives from version
	Command           string             `json:"command,omitempty"`
	Port              flexStr            `json:"port,omitempty"`      // container port it listens on
	HostPort          flexStr            `json:"host_port,omitempty"` // publish host:container (compose, non-traefik)
	ExtraPorts        []flexStr          `json:"extra_ports,omitempty"`
	WebRouted         bool               `json:"web_routed,omitempty"` // primary HTTP entry (Traefik / host port)
	Subdomain         string             `json:"subdomain,omitempty"`  // "" = {domain}, "app" = app.{domain}
	Healthcheck       string             `json:"healthcheck,omitempty"`
	HealthcheckConfig HealthcheckConfig  `json:"healthcheck_config,omitempty"`
	Volumes           []string           `json:"volumes,omitempty"`
	DependsOn         []string           `json:"depends_on,omitempty"` // short service / managed-dep names
	Restart           string             `json:"restart,omitempty"`
	EnvFile           bool               `json:"env_file,omitempty"`            // inject the env's .env as process env (env_file:)
	EnvFileMount      string             `json:"env_file_mount,omitempty"`      // also bind the env's .env as a physical file at this container path (ro)
	EnvVars           map[string]flexStr `json:"env_vars,omitempty"`
	Links             []ServiceLink      `json:"links,omitempty"` // service→service URL wiring, emitted into environment:
	ExtraCompose      string             `json:"extra_compose,omitempty"`
	Replicas          flexStr            `json:"replicas,omitempty"` // default; per-env override via Swarm.Services
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
	return &c, nil
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
