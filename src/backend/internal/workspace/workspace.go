package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mansoor/rigger/ui/internal/wspath"
)

// envOrder ranks environment names by their conventional deployment-pipeline
// position so cards render in a stable, intuitive order (dev → stage → prod)
// rather than the random order of a Go map. Unknown names sort last,
// alphabetically.
func envRank(name string) int {
	switch strings.ToLower(name) {
	case "dev", "develop", "development":
		return 0
	case "test", "testing":
		return 1
	case "qa":
		return 2
	case "stage", "staging":
		return 3
	case "uat":
		return 4
	case "preprod", "pre-prod", "preproduction":
		return 5
	case "prod", "production", "live":
		return 6
	default:
		return 100
	}
}

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
	// ResourcePrefix is the immutable Docker resource prefix ({workspace}_{project}),
	// used to name volumes/networks/containers/stacks/secrets so they stay globally
	// unique even when project display names repeat across workspaces. Set once at
	// creation and never changed. Empty ⇒ fall back to Name (pre-tier configs).
	ResourcePrefix string `json:"resource_prefix,omitempty"`
	// ProjectRootDir is the host-side path of this project's folder, captured once
	// at creation so the UI can show where it lives without a runtime docker
	// inspect. May be empty for projects created before this was added.
	ProjectRootDir string `json:"project_root_dir,omitempty"`
}

// Prefix returns the immutable Docker resource prefix, falling back to the
// display name for configs created before resource_prefix existed.
func (p Project) Prefix() string {
	if p.ResourcePrefix != "" {
		return p.ResourcePrefix
	}
	return p.Name
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
	// SecretKeys are env-var names flagged as secrets (Phase 8). For swarm
	// deployments their values live in Docker Swarm secrets (encrypted at rest),
	// not in .env; for compose they stay in .env and are only masked in the UI.
	SecretKeys []string `json:"secret_keys,omitempty"`
	// SecretVersions tracks the current Docker-secret version per secret key
	// (swarm secrets are immutable, so rotation bumps the version). Absent ⇒ v1.
	SecretVersions map[string]int `json:"secret_versions,omitempty"`
	// BackupSchedules are per-environment automated backup definitions (Phase 11
	// per-env redesign). Each picks which services' data to back up, how often,
	// where to store it, and how many copies to keep.
	BackupSchedules []BackupSchedule `json:"backup_schedules,omitempty"`
}

// BackupSchedule is one automated backup definition for an environment.
type BackupSchedule struct {
	ID            string   `json:"id"`             // stable id (client-generated)
	Name          string   `json:"name"`           // user label
	Services      []string `json:"services"`       // service names to back up; empty = all data services
	IntervalHours int      `json:"interval_hours"` // 2,4,6,12,24,168
	TargetID      *int64   `json:"target_id"`      // nil = local filesystem
	Retention     int      `json:"retention"`      // snapshots to keep for this schedule
	Enabled       bool     `json:"enabled"`
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

// Workspace is the runtime aggregate for ONE project (the type keeps its legacy
// name pending the final Go-type rename). WorkspaceName is the parent tier it
// lives under; Name is the project name (unique only within that workspace).
type Workspace struct {
	WorkspaceName string                 `json:"workspace"` // parent tier
	Name      string                    `json:"name"`      // project name
	Path      string                    `json:"path"`      // path inside the control-plane container
	HostPath  string                    `json:"host_path,omitempty"` // bind-mount source on the host (filled by the API layer)
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

// WorkspaceInfo is one parent-tier workspace (a folder containing projects/).
type WorkspaceInfo struct {
	Key  string `json:"key"`  // dir name = URL segment = Docker prefix part (identity)
	Name string `json:"name"` // free-form display name (from workspace.json)
	Path string `json:"path"`
}

// ListWorkspaces discovers the parent-tier workspaces (dirs carrying a
// workspace.json marker, or — leniently — any dir with a projects/ subdir).
func ListWorkspaces(workspacesDir string) ([]WorkspaceInfo, error) {
	entries, err := os.ReadDir(workspacesDir)
	if err != nil {
		return []WorkspaceInfo{}, fmt.Errorf("read workspaces dir: %w", err)
	}
	out := []WorkspaceInfo{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(workspacesDir, e.Name())
		if !isWorkspaceDir(dir) {
			continue
		}
		out = append(out, WorkspaceInfo{Key: e.Name(), Name: workspaceDisplayName(dir, e.Name()), Path: dir})
	}
	return out, nil
}

// workspaceDisplayName reads the free-form display name from a workspace's
// workspace.json marker, falling back to the key (dir name) when absent.
func workspaceDisplayName(dir, key string) string {
	data, err := os.ReadFile(filepath.Join(dir, "workspace.json"))
	if err != nil {
		return key
	}
	var m struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &m) != nil || m.Name == "" {
		return key
	}
	return m.Name
}

// isWorkspaceDir reports whether dir is a parent-tier workspace.
func isWorkspaceDir(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "workspace.json")); err == nil {
		return true
	}
	if fi, err := os.Stat(filepath.Join(dir, "projects")); err == nil && fi.IsDir() {
		return true
	}
	return false
}

// ListProjects returns the projects within one workspace.
func ListProjects(workspacesDir, workspaceName string) ([]Workspace, error) {
	projDir := filepath.Join(workspacesDir, workspaceName, "projects")
	entries, err := os.ReadDir(projDir)
	if err != nil {
		return []Workspace{}, nil // no projects/ yet ⇒ empty
	}
	out := []Workspace{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ws, err := load(workspacesDir, workspaceName, e.Name())
		if err != nil {
			fmt.Fprintf(os.Stderr, "workspace: skipping %s/%s: %v\n", workspaceName, e.Name(), err)
			continue
		}
		out = append(out, ws)
	}
	return out, nil
}

// List discovers every project across every workspace (flattened). Each
// returned item carries its parent WorkspaceName, so collectors that iterate all
// projects keep working.
func List(workspacesDir string) ([]Workspace, error) {
	wss, err := ListWorkspaces(workspacesDir)
	if err != nil {
		return []Workspace{}, err
	}
	projects := []Workspace{} // never nil — encodes as [] not null
	for _, w := range wss {
		ps, _ := ListProjects(workspacesDir, w.Name)
		projects = append(projects, ps...)
	}
	return projects, nil
}

// Get returns a single project within a workspace.
func Get(workspacesDir, workspaceName, project string) (Workspace, error) {
	return load(workspacesDir, workspaceName, project)
}

func load(workspacesDir, workspaceName, name string) (Workspace, error) {
	wsPath := filepath.Join(workspacesDir, workspaceName, "projects", name)
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
	// Stable, pipeline-style order (dev → stage → prod → others) so cards don't
	// shuffle between loads.
	sort.Slice(envs, func(i, j int) bool {
		ri, rj := envRank(envs[i]), envRank(envs[j])
		if ri != rj {
			return ri < rj
		}
		return envs[i] < envs[j]
	})

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
		WorkspaceName: workspaceName,
		Name:          name,
		Path:          wsPath,
		Config:        cfg,
		Envs:          envs,
		EnvAccess:     envAccess,
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

// EnvVar is one environment variable as returned to the UI: its (possibly
// masked) value plus whether it is flagged as a secret (Phase 8).
type EnvVar struct {
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

const secretMask = "••••••••"

// EnvVars reads the .env file for a workspace+environment and returns each var
// with its secret flag. Non-secret values are always returned in clear; secret
// values are masked unless reveal is true. Keys flagged as secret but absent
// from .env (swarm secrets live in the Docker secret store, write-only) are
// still listed, always masked. Deployment mode comes from config.json.
func EnvVars(workspacesDir, workspaceName, name, env string, reveal bool) (map[string]EnvVar, error) {
	envFile := wspath.DotEnv(workspacesDir, workspaceName, name, env)
	data, err := os.ReadFile(envFile)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read .env: %w", err)
	}

	secret := secretKeySet(workspacesDir, workspaceName, name, env)
	result := make(map[string]EnvVar)
	for _, line := range splitLines(string(data)) {
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		k, v, ok := splitKeyValue(line)
		if !ok {
			continue
		}
		isSecret := secret[k]
		if isSecret && !reveal {
			v = secretMask
		}
		result[k] = EnvVar{Value: v, Secret: isSecret}
	}
	// Surface swarm secrets that are not present in .env (their values live in the
	// Docker secret store and can never be read back).
	for k := range secret {
		if _, ok := result[k]; !ok {
			result[k] = EnvVar{Value: secretMask, Secret: true}
		}
	}
	return result, nil
}

// secretKeySet returns the set of secret-flagged keys for one environment.
func secretKeySet(workspacesDir, workspaceName, name, env string) map[string]bool {
	set := map[string]bool{}
	cfg, err := loadConfig(workspacesDir, workspaceName, name)
	if err != nil {
		return set
	}
	for _, k := range cfg.Environments[env].SecretKeys {
		set[k] = true
	}
	return set
}

// loadConfig reads and parses a workspace's config.json.
func loadConfig(workspacesDir, workspaceName, name string) (*Config, error) {
	data, err := os.ReadFile(wspath.ConfigPath(workspacesDir, workspaceName, name))
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SetSecretMeta persists the secret-key list and versions for one environment
// into config.json, preserving every other field of the file (it operates on
// generic JSON so unknown env fields like replicas/redis_enabled are never
// dropped). Used by the env-vars and rotation handlers.
func SetSecretMeta(workspacesDir, workspaceName, name, env string, secretKeys []string, versions map[string]int) error {
	path := wspath.ConfigPath(workspacesDir, workspaceName, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	var envs map[string]map[string]json.RawMessage
	if raw, ok := root["environments"]; ok {
		if err := json.Unmarshal(raw, &envs); err != nil {
			return err
		}
	}
	if envs == nil {
		envs = map[string]map[string]json.RawMessage{}
	}
	ec := envs[env]
	if ec == nil {
		ec = map[string]json.RawMessage{}
	}
	if len(secretKeys) == 0 {
		delete(ec, "secret_keys")
	} else if b, e := json.Marshal(secretKeys); e == nil {
		ec["secret_keys"] = b
	}
	if len(versions) == 0 {
		delete(ec, "secret_versions")
	} else if b, e := json.Marshal(versions); e == nil {
		ec["secret_versions"] = b
	}
	envs[env] = ec
	encEnvs, err := json.Marshal(envs)
	if err != nil {
		return err
	}
	root["environments"] = encEnvs
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}

// UpdateEnvVars writes changed key=value pairs into the .env file and removes
// any keys listed in deletes. Other lines are preserved unchanged. Keys in
// skipEnvFile are not written to .env (their values live elsewhere — e.g. a
// Docker Swarm secret) and any existing line for them is dropped.
func UpdateEnvVars(workspacesDir, workspaceName, name, env string, updates map[string]string, deletes []string, skipEnvFile map[string]bool) error {
	envFile := wspath.DotEnv(workspacesDir, workspaceName, name, env)
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
	// Keys whose values must not land in .env are treated like deletes for the
	// file, but their values are still applied to the secret store by the caller.
	for k := range skipEnvFile {
		deleteSet[k] = true
		delete(updates, k)
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
