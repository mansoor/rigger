package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mansoor/rigger/ui/internal/wspath"
)

// validKey: the lowercase short identifier used for folders/URLs/Docker (the
// effective length is enforced separately against the configured min/max).
var validKey = regexp.MustCompile(`^[a-z0-9]{1,12}$`)

// validDisplayName: the free-form label — 1..32 chars, must start alphanumeric,
// then letters/digits/space/dash/underscore.
var validDisplayName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,31}$`)

// CreateRequest is the payload sent from the wizard.
type CreateRequest struct {
	Workspace    string            `json:"workspace"` // parent tier KEY the project is created under
	Name         string            `json:"name"`      // free-form display name
	Key          string            `json:"key"`       // project key (folder/URL/Docker identity); derived if empty
	Registry     string            `json:"registry"`
	SourceRepo   string            `json:"source_repo"`   // project-level git repo (one per project)
	SourceBranch string            `json:"source_branch"` // default branch (per-env override via env.git.branch)
	SourceKind   string            `json:"source_kind"`   // "upload" → build source came from an uploaded archive (see SourceToken)
	SourceToken  string            `json:"source_token"`  // staging token from POST /api/upload-source; archive moved into the project on create
	DBSeedFile   string            `json:"db_seed_file"`  // chosen bundled SQL dump (path relative to source); copied to _source/seed.sql on create
	DBSeedAuto   bool              `json:"db_seed_auto"`  // auto-import the dump on first deploy of an empty DB
	Services     []map[string]any  `json:"services"`      // unified services[] (repo-scan path); else seeded from legacy fields
	Type         string            `json:"type"`         // "image" or "custom"
	Template     string            `json:"template"`     // pre-built template name (image type)
	Images       []ImageDef        `json:"images"`       // populated from template or manual entry
	Backend      string            `json:"backend"`      // laravel | nodejs (custom type)
	Frontend     string            `json:"frontend"`     // none | nextjs | react (custom type)
	Database     string            `json:"database"`     // none | postgres | mysql | mariadb (custom type)
	DBVersion    string            `json:"db_version"`   // chosen DB image tag ("" → catalog default)
	DBExternal   bool              `json:"db_external"`  // publish the DB port on the host
	WebSQL       bool              `json:"web_sql"`      // database stack: add an Adminer web SQL client (becomes the web entry)
	Cloudbeaver  bool              `json:"cloudbeaver"`  // legacy alias for WebSQL (older clients)
	Redis         bool             `json:"redis"`
	StorageLocal  bool             `json:"storage_local"`  // local volume backend
	StorageMinIO  bool             `json:"storage_minio"`  // MinIO S3 backend (independent of local)
	StorageBucket string           `json:"storage_bucket"` // minio: bucket-name override (env auto-suffixed)
	StoragePath   string           `json:"storage_path"`   // local: container mount path (default /var/www/html/storage)
	StorageUI     bool             `json:"storage_ui"`     // minio: opens3/console admin sidecar
	Envs         []EnvRequest      `json:"environments"`
	Versions     map[string]string `json:"versions"`
	CustomEnvVars  map[string]string `json:"custom_env_vars"`  // user-supplied env vars for image stacks (Step 2)
	InitialEnvVars map[string]string `json:"initial_env_vars"` // global initial env vars (Step 4)
	NamedVolumes   []NamedVolume     `json:"named_volumes"`    // additional named volumes (Step 4)
	Backup         *BackupCfg        `json:"backup"`           // backup configuration (Step 5)
	TemplateEnvs   map[string]string `json:"-"`                // resolved env vars (post-smart-defaults); set server-side
	ProjectRootDir string            `json:"-"`                // host-side folder path; set server-side at creation
}

// NamedVolume is an additional named Docker volume to declare in compose.
type NamedVolume struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"` // informational; used by compose-gen
}

// BackupCfg stores the backup target and schedule chosen in the wizard.
type BackupCfg struct {
	Enabled   bool   `json:"enabled"`
	TargetID  *int64 `json:"target_id"`   // nil = local filesystem
	TargetName string `json:"target_name"` // "local" or display name
	Schedule  string `json:"schedule"`    // "daily" | "weekly" | "manual"
	Retention int    `json:"retention"`   // number of backups to keep
}

type ImageDef struct {
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Tag        string            `json:"tag"`
	Command    string            `json:"command,omitempty"` // override the image's default CMD
	Port       int               `json:"port"`
	HostPort   string            `json:"host_port"`
	WebRouted  bool              `json:"web_routed"` // primary HTTP entry (Traefik / host-port routing)
	Subdomain  string            `json:"subdomain,omitempty"`
	Volumes    []string          `json:"volumes"`
	EnvVars    map[string]string `json:"env_vars"`
	DependsOn  []string          `json:"depends_on"`
	ExtraPorts []string          `json:"extra_ports"`
	LinkPorts  []string          `json:"link_ports,omitempty"`    // host ports shown as links on env card
	Healthcheck string           `json:"healthcheck"`
	HealthcheckConfig map[string]string `json:"healthcheck_config"`
	Restart      string           `json:"restart,omitempty"`       // unless-stopped | always | on-failure | no
	ExtraCompose string           `json:"extra_compose,omitempty"` // raw YAML appended to service
}

type EnvRequest struct {
	Name       string            `json:"name"`
	Domain     string            `json:"domain"`
	HTTPPort   int               `json:"http_port"`
	HTTPSPort  int               `json:"https_port"`
	Traefik    bool              `json:"traefik"`
	TraefikNet string            `json:"traefik_network"`
	SSLEnabled bool              `json:"ssl_enabled"`
	Deployment string            `json:"deployment"`
	BEReplicas int               `json:"backend_replicas"`
	FEReplicas int               `json:"frontend_replicas"`
	GitEnabled bool              `json:"git_enabled"`
	GitRepo    string            `json:"git_repo"`
	GitBranch  string            `json:"git_branch"`
	Vars       map[string]string `json:"vars"`               // per-environment initial env vars
	SecretKeys []string          `json:"secret_keys,omitempty"` // env-var names flagged as secrets (Phase 8)
	HostID     int64             `json:"host_id,omitempty"`  // per-env remote host (0 = local); bound after create
	BackupSchedules []BackupSchedule `json:"backup_schedules,omitempty"` // per-env backup schedules (Phase 11)
}

// defaultVersions are the fallback image tags for custom stacks.
var defaultVersions = map[string]string{
	"postgres":     "15-alpine",
	"mysql":        "8.0",
	"redis":        "7-alpine",
	"minio":           "latest",
	"minio_mc":        "latest",
	"storage_console": "latest",
	"nginx":        "1.25-alpine",
	"node":         "20-alpine",
	"php":          "8.3-fpm-alpine",
	"composer":     "2.7",
}

// Create validates the request, writes the workspace directory, config.json,
// and run.sh. It does NOT run bootstrap — the caller does that to stream output.
func Create(workspacesDir string, req CreateRequest) error {
	if !validKey.MatchString(req.Workspace) {
		return fmt.Errorf("invalid workspace key %q", req.Workspace)
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validDisplayName.MatchString(req.Name) {
		return fmt.Errorf("invalid project name %q: 1–32 chars, letters/digits/space/dash/underscore", req.Name)
	}
	if !validKey.MatchString(req.Key) {
		return fmt.Errorf("invalid project key %q: lowercase letters/digits only", req.Key)
	}

	// Ensure the parent workspace exists (idempotent; display name already set by
	// CreateWorkspaceTier).
	if err := EnsureWorkspace(workspacesDir, req.Workspace, ""); err != nil {
		return err
	}

	// The folder/identity is the KEY, not the display name.
	wsPath := wspath.ProjectDir(workspacesDir, req.Workspace, req.Key)
	if _, err := os.Stat(wsPath); err == nil {
		return fmt.Errorf("project key %q already exists in workspace %q", req.Key, req.Workspace)
	}

	if err := os.MkdirAll(wsPath, 0755); err != nil {
		return fmt.Errorf("create project dir: %w", err)
	}

	cfg, err := buildConfig(req)
	if err != nil {
		os.RemoveAll(wsPath) //nolint:errcheck
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		os.RemoveAll(wsPath) //nolint:errcheck
		return err
	}

	if err := os.WriteFile(filepath.Join(wsPath, "config.json"), data, 0644); err != nil {
		os.RemoveAll(wsPath) //nolint:errcheck
		return err
	}

	// Phase 6.5 finish: no run.sh is generated — all commands run natively in Go
	// via the shell bridge (dockerops/backup/builder/version/bootstrap).
	return nil
}

// EnsureWorkspace creates the parent-tier workspace directory (and its projects/
// subdir + workspace.json marker) if it does not yet exist. Idempotent.
// EnsureWorkspace creates the parent-tier workspace folder (named by its key) +
// projects/ subdir + a workspace.json marker carrying the free-form display name.
// Idempotent: if the marker already exists its display name is left untouched.
func EnsureWorkspace(workspacesDir, key, displayName string) error {
	if !validKey.MatchString(key) {
		return fmt.Errorf("invalid workspace key %q", key)
	}
	if err := os.MkdirAll(wspath.ProjectsDir(workspacesDir, key), 0755); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}
	marker := wspath.WorkspaceMeta(workspacesDir, key)
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		name := strings.TrimSpace(displayName)
		if name == "" {
			name = key
		}
		meta, _ := json.MarshalIndent(map[string]any{"name": name, "key": key}, "", "  ")
		if err := os.WriteFile(marker, meta, 0644); err != nil {
			return fmt.Errorf("write workspace.json: %w", err)
		}
	}
	return nil
}

// DeleteWorkspace removes an entire parent-tier workspace directory (all its
// projects). Caller is responsible for tearing down running stacks first.
func DeleteWorkspace(workspacesDir, key string) error {
	if !validKey.MatchString(key) {
		return fmt.Errorf("invalid workspace key %q", key)
	}
	return os.RemoveAll(wspath.WorkspaceDir(workspacesDir, key))
}

func buildConfig(req CreateRequest) (map[string]any, error) {
	// Merge versions
	versions := make(map[string]string)
	for k, v := range defaultVersions {
		versions[k] = v
	}
	for k, v := range req.Versions {
		if v != "" {
			versions[k] = v
		}
	}

	// Build environments map
	if len(req.Envs) == 0 {
		return nil, fmt.Errorf("at least one environment is required")
	}
	environments := map[string]any{}
	for _, e := range req.Envs {
		envName := e.Name
		if envName == "" {
			continue
		}
		traefik := e.TraefikNet
		if traefik == "" {
			traefik = "traefik_net"
		}
		deployment := e.Deployment
		if deployment == "" {
			deployment = "compose"
		}
		database := req.Database
		if database == "" {
			database = "none"
		}
		httpPort := e.HTTPPort
		if httpPort == 0 {
			httpPort = 8080
		}
		httpsPort := e.HTTPSPort
		if httpsPort == 0 {
			httpsPort = 8443
		}

		envBlock := map[string]any{
			"domain":          e.Domain,
			"http_port":       httpPort,
			"https_port":      httpsPort,
			"deployment":      deployment,
			"traefik_enabled": e.Traefik,
			"traefik_network": traefik,
			"ssl_enabled":     e.SSLEnabled,
			"git": map[string]any{
				"enabled": e.GitEnabled,
				"repo":    e.GitRepo,
				"branch":  e.GitBranch,
			},
		}

		// Managed dependencies are PROJECT-level now (engine/version/redis/garage are
		// consistent across envs — written into the project block below). Only the
		// per-env DBExternal (host-port exposure) lives on the env. Image stacks bring
		// their own data services as images.
		if (req.Type == "custom" || req.Type == "database") && database != "none" && req.DBExternal {
			envBlock["db_external"] = true
		}

		// Merge env vars: template/smart-defaults first, then user's initial vars on top,
		// then per-image custom vars. Later values win so user choices always take precedence.
		mergedEnvVars := make(map[string]string)
		for k, v := range req.TemplateEnvs {
			mergedEnvVars[k] = v
		}
		for k, v := range req.InitialEnvVars {
			mergedEnvVars[k] = v
		}
		// For image stacks, CustomEnvVars from Step 2 also merge in
		if req.Type == "image" {
			for k, v := range req.CustomEnvVars {
				mergedEnvVars[k] = v
			}
		}
		if len(mergedEnvVars) > 0 {
			envBlock["env_vars"] = mergedEnvVars
		}
		if len(e.SecretKeys) > 0 {
			envBlock["secret_keys"] = e.SecretKeys
		}
		if len(e.BackupSchedules) > 0 {
			envBlock["backup_schedules"] = e.BackupSchedules
		}

		environments[envName] = envBlock
	}

	project := map[string]any{
		"name":     req.Name, // free-form display name
		"key":      req.Key,  // immutable project key (folder/URL identity)
		"type":     req.Type,
		"registry": req.Registry,
		// Immutable Docker resource prefix = {workspaceKey}_{projectKey}; globally
		// unique and short, even when project display names repeat across workspaces.
		"resource_prefix": req.Workspace + "_" + req.Key,
		"version": map[string]any{
			"major": 1, "minor": 0, "patch": 0, "build": 0,
		},
	}
	// Managed dependencies are PROJECT-level (consistent across all envs); only the
	// per-env DBExternal stays on the env block. Image stacks bring their own data
	// services as images, so they get no managed deps.
	if req.Type == "custom" || req.Type == "database" {
		pdb := req.Database
		if pdb == "" {
			pdb = "none"
		}
		if pdb != "none" {
			project["database"] = pdb
			if req.DBVersion != "" {
				project["db_version"] = req.DBVersion
			}
			// Adminer is a project-level flag (like redis/garage); composegen
			// synthesizes the service for any stack with a database. Accept the legacy
			// `cloudbeaver` alias from older clients.
			if req.WebSQL || req.Cloudbeaver {
				project["web_sql"] = true
			}
		}
		if req.Redis {
			project["redis_enabled"] = true
		}
		// Object storage backends are independent — both may be selected.
		if req.StorageMinIO {
			project["storage_minio"] = true
			if req.StorageBucket != "" {
				project["storage_bucket"] = req.StorageBucket
			}
			if req.StorageUI {
				project["storage_ui"] = true
			}
		}
		if req.StorageLocal {
			project["storage_local"] = true
			if req.StoragePath != "" {
				project["storage_path"] = req.StoragePath
			}
		}
	}
	// Project-level source repo (one repo per project). Prefer the explicit field;
	// fall back to the wizard's per-env git fields (first env with a repo) so the
	// existing UI drives source until the dedicated project field lands.
	srcRepo, srcBranch := req.SourceRepo, req.SourceBranch
	for _, e := range req.Envs {
		if srcRepo == "" && e.GitRepo != "" {
			srcRepo = e.GitRepo
			if srcBranch == "" {
				srcBranch = e.GitBranch
			}
		}
	}
	if srcRepo != "" {
		project["git_repo"] = srcRepo
		if srcBranch == "" {
			srcBranch = "main"
		}
		project["git_branch"] = srcBranch
	}
	// Uploaded-source projects record source_kind; the archive itself is moved into
	// the project's _source/ by the create handler (using SourceToken) before bootstrap.
	if req.SourceKind == "upload" {
		project["source_kind"] = "upload"
	}
	// A chosen bundled SQL dump (the create handler copies it to _source/seed.sql).
	// Stored as the fixed relative name; Auto drives auto-import on first deploy.
	if req.DBSeedFile != "" {
		project["db_seed"] = map[string]any{"file": "seed.sql", "auto": req.DBSeedAuto}
	}
	// Host-side folder path, resolved once by the API layer at creation.
	if req.ProjectRootDir != "" {
		project["project_root_dir"] = req.ProjectRootDir
	}
	// Services come straight from the wizard when the repo scanner (or manual
	// editor) produced them; otherwise seed from the legacy wizard fields.
	services := req.Services
	if len(services) == 0 {
		services = seedServices(req)
	}
	cfg := map[string]any{
		"project":      project,
		"services":     services,
		"versions":     versions,
		"environments": environments,
	}

	// Additional named volumes declared in the wizard
	if len(req.NamedVolumes) > 0 {
		cfg["named_volumes"] = req.NamedVolumes
	}

	// Backup schedules are per-environment (Phase 11) — written into each
	// envBlock above. No workspace-level backup config.

	return cfg, nil
}

// seedServices builds the unified services[] from the wizard request. Image stacks
// map each image to a pull service; custom stacks seed a build "backend" fronted by
// nginx (+ an optional build "frontend") from the chosen language. This is a
// transitional mapping of the legacy wizard fields until the blueprint picker
// (Phase 2a-2b) seeds richer, language-agnostic graphs directly.
// adminerService returns the Adminer web SQL client as a web-routed image service
// that depends on the managed database. As the only web service in a database-hosting
// project it becomes the web entry (the env's route / HTTP port). A Rigger-generated
// auto-login index.php is bind-mounted over Adminer's own (via ${RIGGER_BIND_ROOT}, so
// the host daemon resolves it): on its own the URL shows the normal login page, and it
// auto-logs-in only when the Manage Database UI hands it credentials. env_file injects
// the DB creds + ADMINER_LOGIN_SECRET (used to verify those Rigger-issued links). The
// image is PINNED — Adminer 5.x changed the plugin/namespacing surface this PHP targets.
func adminerService(engine string) map[string]any {
	return map[string]any{
		"name":       "adminer",
		"image":      "adminer",
		"tag":        "4.8.1",
		"port":       "8080", // Adminer's native HTTP port (Traefik/healthcheck target)
		"web_routed": true,
		// Publish on 8978 rather than the env's HTTP port (8080), which would collide
		// with Rigger itself on a single host. Editable in Edit Project → Services.
		"host_port":  "8978",
		"env_file":   true, // inject the DB creds + ADMINER_LOGIN_SECRET from the env's .env
		// Drop the auto-login plugin into Adminer's auto-loaded plugins-enabled/ dir
		// (the stock image globs plugins-enabled/*.php). Bind resolves on the host
		// daemon via ${RIGGER_BIND_ROOT}.
		"volumes":    []string{"${RIGGER_BIND_ROOT:-.}/adminer-login.php:/var/www/html/plugins-enabled/01-rigger-autologin.php:ro"},
		"depends_on": []string{engine},
	}
}

func seedServices(req CreateRequest) []map[string]any {
	// Database-hosting projects have no application services — just the managed DB
	// (emitted from the env's database/db_version by composegen). Optionally an
	// Adminer web SQL client is added as the sole web-routed service → web entry.
	if req.Type == "database" {
		if (req.WebSQL || req.Cloudbeaver) && req.Database != "" && req.Database != "none" {
			return []map[string]any{adminerService(req.Database)}
		}
		return nil
	}
	if req.Type == "image" {
		out := make([]map[string]any, 0, len(req.Images))
		for _, im := range req.Images {
			tag := im.Tag
			if tag == "" {
				tag = "latest"
			}
			s := map[string]any{"name": im.Name, "image": im.Image, "tag": tag, "env_file": true}
			if im.Command != "" {
				s["command"] = im.Command
			}
			if im.Port != 0 {
				s["port"] = im.Port
			}
			if im.HostPort != "" {
				s["host_port"] = im.HostPort
			}
			if im.WebRouted {
				s["web_routed"] = true
			}
			if im.Subdomain != "" {
				s["subdomain"] = im.Subdomain
			}
			if len(im.ExtraPorts) > 0 {
				s["extra_ports"] = im.ExtraPorts
			}
			if len(im.Volumes) > 0 {
				s["volumes"] = im.Volumes
			}
			if len(im.DependsOn) > 0 {
				s["depends_on"] = im.DependsOn
			}
			if im.Healthcheck != "" {
				s["healthcheck"] = im.Healthcheck
			}
			if im.Restart != "" {
				s["restart"] = im.Restart
			}
			if im.ExtraCompose != "" {
				s["extra_compose"] = im.ExtraCompose
			}
			if len(im.EnvVars) > 0 {
				s["env_vars"] = im.EnvVars
			}
			out = append(out, s)
		}
		return out
	}

	// Custom: a build "backend" fronted by nginx, plus an optional build "frontend".
	be := req.Backend
	if be == "" {
		be = "laravel"
	}
	port, health := backendDefaults(be)
	backend := map[string]any{
		"name":        "backend",
		"role":        "app",
		"build":       map[string]any{"template": be, "context": "backend"},
		"env_file":    true,
		"port":        port,
		"healthcheck": health,
		"volumes":     []string{"uploads:/app/storage/uploads"},
	}
	if req.Database == "postgres" || req.Database == "mysql" || req.Database == "mariadb" {
		backend["depends_on"] = []string{req.Database}
	}
	nginx := map[string]any{
		"name":            "nginx",
		"image":           "nginx",
		"tag":             "1.25-alpine",
		"web_routed":      true,
		"port":            "80",
		"depends_on":      []string{"backend"},
		"config_template": be,
		"volumes":         []string{"./nginx.conf:/etc/nginx/conf.d/default.conf:ro", "uploads:/var/www/uploads:ro"},
		"healthcheck":     "curl -sf http://localhost/ -o /dev/null || exit 1",
	}
	out := []map[string]any{backend, nginx}
	if req.Frontend != "" && req.Frontend != "none" {
		out = append(out, map[string]any{
			"name":       "frontend",
			"role":       "app",
			"build":      map[string]any{"template": req.Frontend, "context": "frontend"},
			"env_file":   true,
			"port":       "3000",
			"web_routed": true,
			"subdomain":  "app",
		})
	}
	return out
}

// backendDefaults returns the listen port + healthcheck for a legacy backend type.
func backendDefaults(backend string) (port, health string) {
	switch backend {
	case "nodejs":
		return "3000", "wget -qO- http://localhost:3000/health >/dev/null 2>&1 || curl -sf http://localhost:3000/health >/dev/null 2>&1 || exit 1"
	default: // laravel / php-fpm
		return "9000", "php -r 'exit(0);' 2>/dev/null || exit 1"
	}
}

// TemplateInfo is a summary of a stack template for the API.
type TemplateInfo struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	ImageCount  int      `json:"image_count"`
}

// ListTemplates reads all JSON files in templates/stacks/ and returns summaries.
func ListTemplates(templatesDir string) ([]TemplateInfo, error) {
	stacksDir := filepath.Join(templatesDir, "stacks")
	entries, err := os.ReadDir(stacksDir)
	if err != nil {
		return []TemplateInfo{}, nil // no templates dir is not fatal
	}

	var templates []TemplateInfo
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(stacksDir, e.Name()))
		if err != nil {
			continue
		}
		var raw struct {
			Name        string   `json:"name"`
			Label       string   `json:"label"`
			Description string   `json:"description"`
			Tags        []string `json:"tags"`
			Images      []any    `json:"images"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}
		templates = append(templates, TemplateInfo{
			Name:        raw.Name,
			Label:       raw.Label,
			Description: raw.Description,
			Tags:        raw.Tags,
			ImageCount:  len(raw.Images),
		})
	}
	return templates, nil
}

// LoadTemplate reads a template JSON and returns its images and default env vars.
func LoadTemplate(templatesDir, name string) ([]ImageDef, map[string]string, error) {
	path := filepath.Join(templatesDir, "stacks", name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("template %q not found", name)
	}

	var raw struct {
		Images      []ImageDef        `json:"images"`
		DefaultEnvs map[string]string `json:"default_env_vars"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, err
	}
	return raw.Images, raw.DefaultEnvs, nil
}
