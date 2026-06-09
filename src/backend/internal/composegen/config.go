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
	Project      Project           `json:"project"`
	Images       []Image           `json:"images"`
	Versions     map[string]flexStr `json:"versions"`
	NamedVolumes []NamedVolume     `json:"named_volumes"`
	Environments map[string]Env    `json:"environments"`
}

type Project struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Registry string  `json:"registry"`
	Version  Version `json:"version"`
	// ResourcePrefix is the immutable Docker resource prefix ({workspace}_{project});
	// empty ⇒ fall back to Name. See workspace.Project.Prefix.
	ResourcePrefix string `json:"resource_prefix,omitempty"`
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

// Env is one environment's config.
type Env struct {
	Domain           string                     `json:"domain"`
	HTTPPort         flexStr                    `json:"http_port"`
	Backend          string                     `json:"backend"`
	FrontendEnabled  bool                       `json:"frontend_enabled"`
	Frontend         string                     `json:"frontend"`
	Database         string                     `json:"database"`
	RedisEnabled     bool                       `json:"redis_enabled"`
	GarageEnabled    bool                       `json:"garage_enabled"`
	TraefikEnabled   bool                       `json:"traefik_enabled"`
	TraefikNetwork   string                     `json:"traefik_network"`
	SSLEnabled       bool                       `json:"ssl_enabled"`
	Deployment       string                     `json:"deployment"`
	Replicas         Replicas                   `json:"replicas"`
	// Processes are extra long-running containers for custom apps (queue workers,
	// scheduler, job processors) that reuse a built image with a custom command.
	// Empty ⇒ no extra services, so existing configs/output are unchanged.
	Processes        []Process                  `json:"processes,omitempty"`
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

type Replicas struct {
	Backend  flexStr `json:"backend"`
	Frontend flexStr `json:"frontend"`
}

// Process is an extra long-running container that reuses a built image (the
// backend or frontend) with a custom command — e.g. a queue worker, scheduler,
// or job processor. It has no published ports and is not web-routed.
type Process struct {
	Name     string  `json:"name"`               // dns-safe; unique within the env
	Command  string  `json:"command"`            // e.g. "php artisan queue:work --tries=3"
	Source   string  `json:"source,omitempty"`   // "backend" (default) | "frontend"
	Replicas flexStr `json:"replicas,omitempty"` // swarm only (mirrors backend/frontend today)
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

// Image is one entry in config.json images[] (image-stack projects).
type Image struct {
	Name              string             `json:"name"`
	Image             string             `json:"image"`
	Tag               string             `json:"tag"`
	Port              flexStr            `json:"port"`
	HostPort          flexStr            `json:"host_port"`
	Healthcheck       string             `json:"healthcheck"`
	Command           string             `json:"command"`
	Volumes           []string           `json:"volumes"`
	ExtraPorts        []flexStr          `json:"extra_ports"`
	Restart           string             `json:"restart"`
	HealthcheckConfig HealthcheckConfig  `json:"healthcheck_config"`
	DependsOn         []string           `json:"depends_on"`
	EnvVars           map[string]flexStr `json:"env_vars"`
	ExtraCompose      string             `json:"extra_compose"`
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

// projectType returns the project type, defaulting to "custom".
func (c *Config) projectType() string {
	if c.Project.Type == "" {
		return "custom"
	}
	return c.Project.Type
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
