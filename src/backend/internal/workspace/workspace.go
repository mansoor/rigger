package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Version struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
	Patch int `json:"patch"`
	Build int `json:"build"`
}

type Project struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Registry string  `json:"registry"`
	Version  Version `json:"version"`
}

// ServiceOverride holds environment-specific YAML appended to a service definition.
// Keyed by service name in EnvConfig.ServiceOverrides.
type ServiceOverride struct {
	ExtraCompose string `json:"extra_compose"`
}

type EnvConfig struct {
	Domain          string                      `json:"domain"`
	HTTPPort        any                         `json:"http_port"`
	HTTPSPort       any                         `json:"https_port"`
	Deployment      string                      `json:"deployment"`
	TraefikEnabled  bool                        `json:"traefik_enabled"`
	SSLEnabled      bool                        `json:"ssl_enabled"`
	FrontendEnabled bool                        `json:"frontend_enabled"`
	Backend         string                      `json:"backend"`
	Frontend        string                      `json:"frontend"`
	Database        string                      `json:"database"`
	ServiceOverrides map[string]ServiceOverride `json:"service_overrides,omitempty"`
}

type Config struct {
	Project      Project              `json:"project"`
	Environments map[string]EnvConfig `json:"environments"`
	Images       []ConfigImage        `json:"images"`
}

// ConfigImage is the read-only view of an image service as stored in config.json.
// Only the fields needed by the frontend (port links, names) are parsed here.
type ConfigImage struct {
	Name       string   `json:"name"`
	Image      string   `json:"image"`
	Tag        string   `json:"tag"`
	Port       int      `json:"port"`
	HostPort   string   `json:"host_port"`
	ExtraPorts []string `json:"extra_ports"`
	LinkPorts  []string `json:"link_ports"`
}

// EnvAccessInfo holds resolved (${VAR}-substituted) access values for one environment.
// Computed server-side from config.json + .env so the frontend never has to parse raw strings.
type EnvAccessInfo struct {
	Domain    string            `json:"domain"`     // resolved domain, empty if not configured
	HTTPPort  string            `json:"http_port"`  // resolved http_port for custom stacks
	Images    []ImageAccessInfo `json:"images"`     // per-service resolved ports
}

// ImageAccessInfo holds the resolved host port and link ports for one image service.
type ImageAccessInfo struct {
	Name      string   `json:"name"`
	HostPort  string   `json:"host_port"`  // resolved host_port
	LinkPorts []string `json:"link_ports"` // resolved link_ports (may be empty)
}

type Workspace struct {
	Name      string                    `json:"name"`
	Path      string                    `json:"path"`
	Config    Config                    `json:"config"`
	Envs      []string                  `json:"envs"`
	EnvAccess map[string]EnvAccessInfo  `json:"env_access"` // keyed by env name
	// HostID/HostName are filled by the API layer when every environment shares
	// one remote host (a convenience for the whole-workspace view); 0/"" means
	// local or a mixed (per-env) layout. EnvHosts carries each environment's
	// effective host (Phase 7 per-environment binding).
	HostID   int64                 `json:"host_id,omitempty"`
	HostName string                `json:"host_name,omitempty"`
	EnvHosts map[string]EnvHostRef `json:"env_hosts,omitempty"` // env name → host
}

// EnvHostRef is the host an environment runs on (omitted ⇒ local).
type EnvHostRef struct {
	HostID   int64  `json:"host_id"`
	HostName string `json:"host_name"`
	Address  string `json:"host_address"` // for building direct host:port URLs
}

// List discovers all workspaces under the given root directory.
func List(workspacesDir string) ([]Workspace, error) {
	entries, err := os.ReadDir(workspacesDir)
	if err != nil {
		return []Workspace{}, fmt.Errorf("read workspaces dir: %w", err)
	}

	workspaces := []Workspace{} // never nil — encodes as [] not null
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ws, err := load(workspacesDir, e.Name())
		if err != nil {
			fmt.Fprintf(os.Stderr, "workspace: skipping %q: %v\n", e.Name(), err)
			continue
		}
		workspaces = append(workspaces, ws)
	}
	return workspaces, nil
}

// Get returns a single workspace by name.
func Get(workspacesDir, name string) (Workspace, error) {
	return load(workspacesDir, name)
}

func load(workspacesDir, name string) (Workspace, error) {
	wsPath := filepath.Join(workspacesDir, name)
	cfgPath := filepath.Join(wsPath, "config.json")

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return Workspace{}, fmt.Errorf("read config.json: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Workspace{}, fmt.Errorf("parse config.json: %w", err)
	}

	// Collect environment names: union of envs/ subdirectories on disk AND
	// keys in config.json environments. This ensures a newly-added environment
	// (saved to config.json but not yet bootstrapped/Init'd) is visible on
	// the workspace page immediately after save.
	envSet := map[string]bool{}
	envsDir := filepath.Join(wsPath, "envs")
	if entries, err := os.ReadDir(envsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				envSet[e.Name()] = true
			}
		}
	}
	for envName := range cfg.Environments {
		envSet[envName] = true
	}
	var envs []string
	for k := range envSet {
		envs = append(envs, k)
	}

	// Build per-environment resolved access info.
	// Best-effort: missing .env files result in empty/raw values, never an error.
	envAccess := make(map[string]EnvAccessInfo, len(envSet))
	for envName := range envSet {
		dotenv := readDotEnv(filepath.Join(wsPath, "envs", envName, ".env"))
		resolve := func(s string) string { return resolveEnvRefs(s, dotenv) }

		info := EnvAccessInfo{}

		if ec, ok := cfg.Environments[envName]; ok {
			info.Domain   = resolve(ec.Domain)
			info.HTTPPort = resolve(fmt.Sprintf("%v", ec.HTTPPort))
		}

		for _, img := range cfg.Images {
			ia := ImageAccessInfo{
				Name:     img.Name,
				HostPort: resolve(img.HostPort),
			}
			for _, lp := range img.LinkPorts {
				ia.LinkPorts = append(ia.LinkPorts, resolve(lp))
			}
			info.Images = append(info.Images, ia)
		}

		envAccess[envName] = info
	}

	return Workspace{
		Name:      name,
		Path:      wsPath,
		Config:    cfg,
		Envs:      envs,
		EnvAccess: envAccess,
	}, nil
}

// readDotEnv parses a .env file into a key→value map. Missing file returns empty map.
func readDotEnv(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	m := map[string]string{}
	for _, line := range splitLines(string(data)) {
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		k, v, ok := splitKeyValue(line)
		if ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

var envRefRe = regexp.MustCompile(`\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// resolveEnvRefs replaces ${VAR} and $VAR references using the provided map.
// Unresolved references are left as-is so callers can detect them.
func resolveEnvRefs(s string, vars map[string]string) string {
	return envRefRe.ReplaceAllStringFunc(s, func(match string) string {
		// Extract variable name from ${VAR} or $VAR form
		name := envRefRe.FindStringSubmatch(match)
		key := name[1]
		if key == "" {
			key = name[2]
		}
		if val, ok := vars[key]; ok {
			return val
		}
		return match // leave unresolved refs intact
	})
}

// EnvVars reads .env file for a workspace+environment.
// When reveal is false, secret-looking values are masked as "••••••••".
func EnvVars(workspacesDir, name, env string, reveal bool) (map[string]string, error) {
	envFile := filepath.Join(workspacesDir, name, "envs", env, ".env")
	data, err := os.ReadFile(envFile)
	if err != nil {
		return nil, fmt.Errorf("read .env: %w", err)
	}

	result := make(map[string]string)
	for _, line := range splitLines(string(data)) {
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		k, v, ok := splitKeyValue(line)
		if !ok {
			continue
		}
		if reveal {
			result[k] = v
		} else {
			result[k] = "••••••••"
		}
	}
	return result, nil
}

// UpdateEnvVars writes changed key=value pairs into the .env file and removes
// any keys listed in deletes. Other lines are preserved unchanged.
func UpdateEnvVars(workspacesDir, name, env string, updates map[string]string, deletes []string) error {
	envFile := filepath.Join(workspacesDir, name, "envs", env, ".env")
	data, err := os.ReadFile(envFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		// .env doesn't exist yet (new environment not yet bootstrapped).
		// Create the directory and an empty file so updates can be applied.
		if mkErr := os.MkdirAll(filepath.Dir(envFile), 0755); mkErr != nil {
			return mkErr
		}
		data = []byte{}
	}

	deleteSet := make(map[string]bool, len(deletes))
	for _, k := range deletes {
		deleteSet[k] = true
	}

	lines := splitLines(string(data))
	out := make([]string, 0, len(lines))
	written := make(map[string]bool)

	for _, line := range lines {
		if len(line) == 0 || line[0] == '#' {
			out = append(out, line)
			continue
		}
		k, _, ok := splitKeyValue(line)
		if ok {
			if deleteSet[k] {
				continue // drop the line
			}
			if newVal, changed := updates[k]; changed {
				out = append(out, k+"="+newVal)
				written[k] = true
				continue
			}
		}
		out = append(out, line)
	}

	// Append any new keys not already in the file
	for k, v := range updates {
		if !written[k] && !deleteSet[k] {
			out = append(out, k+"="+v)
		}
	}

	content := ""
	for _, l := range out {
		content += l + "\n"
	}
	return os.WriteFile(envFile, []byte(content), 0600)
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func splitKeyValue(line string) (string, string, bool) {
	for i := 0; i < len(line); i++ {
		if line[i] == '=' {
			return line[:i], line[i+1:], true
		}
	}
	return "", "", false
}
