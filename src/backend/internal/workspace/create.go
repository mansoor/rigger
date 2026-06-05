package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]{0,62}$`)

// CreateRequest is the payload sent from the wizard.
type CreateRequest struct {
	Name         string            `json:"name"`
	Registry     string            `json:"registry"`
	Type         string            `json:"type"`         // "image" or "custom"
	Template     string            `json:"template"`     // pre-built template name (image type)
	Images       []ImageDef        `json:"images"`       // populated from template or manual entry
	Backend      string            `json:"backend"`      // laravel | nodejs (custom type)
	Frontend     string            `json:"frontend"`     // none | nextjs | react (custom type)
	Database     string            `json:"database"`     // postgres | mysql | none (custom type)
	Redis        bool              `json:"redis"`
	Garage       bool              `json:"garage"`
	Envs         []EnvRequest      `json:"environments"`
	Versions     map[string]string `json:"versions"`
	CustomEnvVars  map[string]string `json:"custom_env_vars"`  // user-supplied env vars for image stacks (Step 2)
	InitialEnvVars map[string]string `json:"initial_env_vars"` // global initial env vars (Step 4)
	NamedVolumes   []NamedVolume     `json:"named_volumes"`    // additional named volumes (Step 4)
	Backup         *BackupCfg        `json:"backup"`           // backup configuration (Step 5)
	TemplateEnvs   map[string]string `json:"-"`                // resolved env vars (post-smart-defaults); set server-side
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
	Port       int               `json:"port"`
	HostPort   string            `json:"host_port"`
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
	HostID     int64             `json:"host_id,omitempty"`  // per-env remote host (0 = local); bound after create
}

// defaultVersions are the fallback image tags for custom stacks.
var defaultVersions = map[string]string{
	"postgres":     "15-alpine",
	"mysql":        "8.0",
	"redis":        "7-alpine",
	"garage":       "v1.0.1",
	"garage_webui": "latest",
	"nginx":        "1.25-alpine",
	"node":         "20-alpine",
	"php":          "8.3-fpm-alpine",
	"composer":     "2.7",
}

// Create validates the request, writes the workspace directory, config.json,
// and run.sh. It does NOT run bootstrap — the caller does that to stream output.
func Create(workspacesDir string, req CreateRequest) error {
	if !validName.MatchString(req.Name) {
		return fmt.Errorf("invalid workspace name %q: use lowercase letters, numbers, hyphens only", req.Name)
	}

	wsPath := filepath.Join(workspacesDir, req.Name)
	if _, err := os.Stat(wsPath); err == nil {
		return fmt.Errorf("workspace %q already exists", req.Name)
	}

	if err := os.MkdirAll(wsPath, 0755); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
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
		bePeers := e.BEReplicas
		if bePeers < 1 {
			bePeers = 1
		}
		fePeers := e.FEReplicas
		if fePeers < 1 {
			fePeers = 1
		}
		traefik := e.TraefikNet
		if traefik == "" {
			traefik = "traefik_net"
		}
		deployment := e.Deployment
		if deployment == "" {
			deployment = "compose"
		}
		frontend := req.Frontend
		if frontend == "" {
			frontend = "none"
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

		// Custom stacks carry source-build fields; image stacks don't need them.
		if req.Type == "custom" {
			envBlock["backend"] = req.Backend
			envBlock["frontend_enabled"] = frontend != "none"
			envBlock["frontend"] = frontend
			envBlock["database"] = database
			envBlock["redis_enabled"] = req.Redis
			envBlock["garage_enabled"] = req.Garage
			envBlock["replicas"] = map[string]any{
				"backend":  bePeers,
				"frontend": fePeers,
			}
			envBlock["git"].(map[string]any)["backend_path"] = "./src/backend"
			envBlock["git"].(map[string]any)["frontend_path"] = "./src/frontend"
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

		environments[envName] = envBlock
	}

	cfg := map[string]any{
		"project": map[string]any{
			"name":     req.Name,
			"type":     req.Type,
			"registry": req.Registry,
			"version": map[string]any{
				"major": 1, "minor": 0, "patch": 0, "build": 0,
			},
		},
		"versions":     versions,
		"environments": environments,
	}

	// Image stacks include the images array
	if req.Type == "image" && len(req.Images) > 0 {
		cfg["images"] = req.Images
	}

	// Additional named volumes declared in the wizard
	if len(req.NamedVolumes) > 0 {
		cfg["named_volumes"] = req.NamedVolumes
	}

	// Backup configuration
	if req.Backup != nil {
		retention := req.Backup.Retention
		if retention <= 0 {
			retention = 7
		}
		schedule := req.Backup.Schedule
		if schedule == "" {
			schedule = "daily"
		}
		cfg["backup"] = map[string]any{
			"enabled":     req.Backup.Enabled,
			"target_id":   req.Backup.TargetID,
			"target_name": req.Backup.TargetName,
			"schedule":    schedule,
			"retention":   retention,
		}
	} else {
		// Always write a default backup config so scripts can rely on it
		cfg["backup"] = map[string]any{
			"enabled":     true,
			"target_id":   nil,
			"target_name": "local",
			"schedule":    "daily",
			"retention":   7,
		}
	}

	return cfg, nil
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
