package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/envorder"
	"github.com/mansoor/rigger/ui/internal/wspath"
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
	// ResourcePrefix is the immutable Docker resource prefix ({workspace}_{project}),
	// used to name volumes/networks/containers/stacks/secrets so they stay globally
	// unique even when project display names repeat across workspaces. Set once at
	// creation and never changed. Empty ⇒ fall back to Name (pre-tier configs).
	ResourcePrefix string `json:"resource_prefix,omitempty"`
	// ProjectRootDir is the host-side path of this project's folder, captured once
	// at creation so the UI can show where it lives without a runtime docker
	// inspect. May be empty for projects created before this was added.
	ProjectRootDir string `json:"project_root_dir,omitempty"`
	// EnvOrder is the explicit deploy-tier order of this project's environments
	// (low→high). Empty ⇒ order auto-guessed from env names. See internal/envorder.
	EnvOrder []string `json:"env_order,omitempty"`
	// BuildPipelineID, when >0, makes the project's Build button run that pipeline
	// instead of a plain build (lets ops override the default build behaviour).
	BuildPipelineID int64 `json:"build_pipeline_id,omitempty"`
	// Managed dependencies are project-level (consistent across envs). The frontend
	// reads these to render the dependency picker + derived service rows. Only the
	// per-env DBExternal (host-port exposure) lives on EnvConfig.
	Database  string `json:"database,omitempty"`
	DBVersion string `json:"db_version,omitempty"`
	Redis     bool   `json:"redis_enabled,omitempty"`
	// WebSQL adds an Adminer web-SQL client (composegen synthesizes it). The UI reads
	// this to render the Adminer toggle + Manage-DB connect links.
	WebSQL bool `json:"web_sql,omitempty"`
	// Mailpit is the project-level default for the Mailpit test-SMTP sidecar (per-env
	// overridable). The UI renders the toggle; composegen synthesizes the service per env.
	Mailpit bool `json:"mailpit,omitempty"`
	// Object/file storage is project-level; the two backends are INDEPENDENT (local,
	// MinIO, both, or neither). StorageLocal → FILESYSTEM_DISK=local + persistent volume
	// at StoragePath; StorageMinIO → managed MinIO S3 + mc bucket-init. Legacy
	// ObjectStorage enum is still read as a fallback. The UI renders checkboxes + derived
	// service rows from these.
	StorageLocal  bool   `json:"storage_local,omitempty"`
	StorageMinIO  bool   `json:"storage_minio,omitempty"`
	ObjectStorage string `json:"object_storage,omitempty"` // legacy enum: ""/none|local|minio
	StorageBucket string `json:"storage_bucket,omitempty"` // minio: bucket-name override (env auto-suffixed)
	StoragePath   string `json:"storage_path,omitempty"`   // local: container mount path (default /var/www/html/storage)
	StorageUI     bool   `json:"storage_ui,omitempty"`     // minio: opens3/console admin sidecar
	// Deprecated: legacy Garage toggles — kept so old config.json unmarshals; ignored.
	Garage      bool `json:"garage_enabled,omitempty"`
	GarageWebUI bool `json:"garage_web_ui,omitempty"`
	// SourceKind is "upload" when build source came from an uploaded archive (else
	// "git"/empty). The UI reads it to show the source origin + "Replace source".
	SourceKind string `json:"source_kind,omitempty"`
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
	// DBExternal is the per-env toggle that publishes the managed DB's port on the
	// host (expose on dev, keep prod private). The engine/version live project-level.
	DBExternal      bool                        `json:"db_external,omitempty"`
	// RedisEnabled/GarageEnabled are the legacy per-env managed-dep toggles, kept so
	// pre-move configs still resolve the derived service rows (fallback).
	RedisEnabled    bool                        `json:"redis_enabled,omitempty"`
	GarageEnabled   bool                        `json:"garage_enabled,omitempty"`
	// Per-env sidecar overrides (web_sql / storage_ui, tri-state) live in config.json and
	// are round-tripped raw by the editor + read by composegen/envgen; they don't need a
	// typed field in this read view.
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
	Services     []ConfigService      `json:"services"`
}

// managedDepServices returns synthetic (managed) service rows for the project's
// active managed dependencies, so the DB/Redis/object-storage appear in the Services
// list. Engine/redis are project-level; for configs written before the move they fall
// back to any environment's legacy per-env value. Rows are skipped when a real service
// of the same name already exists (e.g. an image-stack postgres).
func managedDepServices(c *Config) []ConfigService {
	have := map[string]bool{}
	for _, s := range c.Services {
		have[s.Name] = true
	}
	engine := c.Project.Database
	redis := c.Project.Redis
	for _, ec := range c.Environments { // legacy per-env fallback
		if engine == "" && ec.Database != "" && ec.Database != "none" {
			engine = ec.Database
		}
		redis = redis || ec.RedisEnabled
	}
	var out []ConfigService
	add := func(name, kind string) {
		if name == "" || name == "none" || have[name] {
			return
		}
		have[name] = true
		out = append(out, ConfigService{Name: name, Managed: true, Engine: kind})
	}
	if engine != "" && engine != "none" {
		add(engine, engine) // postgres | mysql | mariadb
	}
	if redis {
		add("redis", "redis")
	}
	// Object storage backends are independent — both may be on. (The MinIO console is
	// per-env tooling now, surfaced in the env's Managed Service Console — not here.)
	if c.Project.StorageMinIO || c.Project.ObjectStorage == "minio" {
		add("minio", "minio")
	}
	if c.Project.StorageLocal || c.Project.ObjectStorage == "local" {
		add("storage", "local") // a persistent local volume (no container)
	}
	return out
}

// ConfigService is the read view of a unified services[] entry needed for
// env-access resolution: which service is the web entry and its published port.
type ConfigService struct {
	Name      string `json:"name"`
	WebRouted bool   `json:"web_routed"`
	Subdomain string `json:"subdomain"`
	HostPort  string `json:"host_port"`
	// Build is the raw build block (passed through) so the UI can tell which
	// services build images — drives whether Build / Release-pipeline show. Absent
	// for pull-only (image / database) services.
	Build json.RawMessage `json:"build,omitempty"`
	// Managed marks a synthetic row derived from a project-level managed dependency
	// (database/redis/object-storage). The UI lists these alongside real services but
	// hides the delete affordance — they're removed by unchecking the dependency. Until
	// the full service-graph fold, these are NOT persisted to config.json services[].
	Managed bool   `json:"managed,omitempty"`
	Engine  string `json:"engine,omitempty"` // managed rows: the dependency kind (postgres|redis|minio|local|…)
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
	Domain   string            `json:"domain"`        // resolved domain, empty if not configured
	URL      string            `json:"url,omitempty"` // full Traefik route URL (incl. auto-derived); empty when host-bound
	HTTPPort string            `json:"http_port"`     // resolved http_port for custom stacks
	Images   []ImageAccessInfo `json:"images"`        // per-service resolved ports
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
	MyRole   string                `json:"my_role,omitempty"`   // caller's effective role (Phase 5.2b); set by the API layer
	AppHost  string                `json:"app_host,omitempty"`  // configured host/IP for direct service links (local envs); set by the API layer
	// Auto-URL context for the route preview (set by the API layer) so Edit Project
	// shows the same URL the deploy will use: the magic-DNS mode + the global apps
	// base domain (workspace `domain` overrides it, like EffectiveBaseDomain).
	AutoURLMode    string `json:"auto_url_mode,omitempty"`
	AppsBaseDomain string `json:"apps_base_domain,omitempty"`
}

// EnvHostRef is the host an environment runs on (omitted ⇒ local).
type EnvHostRef struct {
	HostID   int64  `json:"host_id"`
	HostName string `json:"host_name"`
	Address  string `json:"host_address"` // for building direct host:port URLs
}

// WorkspaceInfo is one parent-tier workspace (a folder containing projects/).
type WorkspaceInfo struct {
	Key    string `json:"key"`  // dir name = URL segment = Docker prefix part (identity)
	Name   string `json:"name"` // free-form display name (from workspace.json)
	Path   string `json:"path"`
	MyRole string `json:"my_role,omitempty"` // caller's effective role (Phase 5.2b); set by the handler
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
		ws, err := load(workspacesDir, workspaceName, e.Name(), "", "", "")
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
		// Folder identity is the workspace KEY, not the (renameable) display name.
		ps, _ := ListProjects(workspacesDir, w.Key)
		projects = append(projects, ps...)
	}
	return projects, nil
}

// Get returns a single project within a workspace.
// Get loads a project's workspace detail. baseDomain (the workspace apps base
// domain) is used to resolve each env's Traefik route URL for display; pass ""
// when routing URLs aren't needed.
func Get(workspacesDir, workspaceName, project, baseDomain, autoMode, autoHost string) (Workspace, error) {
	return load(workspacesDir, workspaceName, project, baseDomain, autoMode, autoHost)
}

func load(workspacesDir, workspaceName, name, baseDomain, autoMode, autoHost string) (Workspace, error) {
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
	// Surface project-level managed dependencies as synthetic (managed) service rows
	// so the DB/Redis/Garage appear in the Services list. They are NOT written back to
	// config.json — removal is done by unchecking the dependency. Until the full
	// service-graph fold (backlog), this derived view is how deps "are" services.
	cfg.Services = append(cfg.Services, managedDepServices(&cfg)...)

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
	var rawEnvs []string
	for k := range envSet {
		rawEnvs = append(rawEnvs, k)
	}
	// Effective deploy-tier order: explicit project order, else auto-guess from
	// env names (DefaultTiers here; GetWorkspace refines with the workspace's
	// env_tier_names setting). Keeps cards from shuffling between loads.
	envs := envorder.Resolve(rawEnvs, cfg.Project.EnvOrder, nil)

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
			// An apex web service that sets its own host_port publishes on THAT port
			// (composegen prefers it over the env HTTP port), so the access URL must
			// use it too. Mirrors emitServicePorts' "host_port wins" rule.
			apexRouted, hasAdminerSvc := false, false
			for _, svc := range cfg.Services {
				if svc.Name == "adminer" {
					hasAdminerSvc = true
				}
				if svc.WebRouted && svc.Subdomain == "" {
					apexRouted = true
					if svc.HostPort != "" {
						info.HTTPPort = resolve(svc.HostPort)
					}
				}
			}
			// A flag-synthesized Adminer (web_sql, no services[] entry) owns the apex
			// web entry when no app service routes there — it publishes on 8978.
			if !apexRouted && cfg.Project.WebSQL && !hasAdminerSvc {
				info.HTTPPort = "8978"
			}
			// Full Traefik route URL — incl. the auto-derived {prefix}-{env}.
			// {base|localhost} when Traefik is on and no explicit domain is set.
			if url, routed := composegen.EnvRouteURL(data, envName, baseDomain, autoMode, autoHost); routed {
				info.URL = url
			}
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
