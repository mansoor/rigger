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
	ServiceOverrides map[string]ServiceOverride `json:"service_overrides"`
}

type Replicas struct {
	Backend  flexStr `json:"backend"`
	Frontend flexStr `json:"frontend"`
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
