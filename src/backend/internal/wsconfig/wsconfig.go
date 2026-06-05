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
	Versions     map[string]Str `json:"versions"`
	Environments map[string]Env `json:"environments"`
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

// Env is one environment's config.
type Env struct {
	Domain          string         `json:"domain"`
	HTTPPort        Str            `json:"http_port"`
	HTTPSPort       Str            `json:"https_port"`
	Backend         string         `json:"backend"`
	FrontendEnabled bool           `json:"frontend_enabled"`
	Frontend        string         `json:"frontend"`
	Database        string         `json:"database"`
	RedisEnabled    bool           `json:"redis_enabled"`
	GarageEnabled   bool           `json:"garage_enabled"`
	TraefikEnabled  bool           `json:"traefik_enabled"`
	Deployment      string         `json:"deployment"`
	Replicas        Replicas       `json:"replicas"`
	EnvVars         map[string]Str `json:"env_vars"`
}

type Replicas struct {
	Backend  Str `json:"backend"`
	Frontend Str `json:"frontend"`
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
// "{registry}/{project}-{service}:{version}-{env}".
func (c *Config) ImageTag(service, env string) string {
	return fmt.Sprintf("%s/%s-%s:%s-%s",
		c.Project.Registry, c.Project.Name, service, c.VersionString(), env)
}

// StackName reproduces lib.sh stack_name(): "{project}_{env}". This is also the
// compose project name / service prefix.
func (c *Config) StackName(env string) string {
	return c.Project.Name + "_" + env
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
