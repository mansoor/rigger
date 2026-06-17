package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// P4 — per-env Managed Service Console. The env card's database button opens a
// tabbed console; this endpoint feeds the NON-database tabs (Redis, object storage,
// Mailpit) with the same connection-info + reveal-secret model as GetDatabaseInfo
// (the database tab keeps its own richer endpoints). Secrets are masked unless
// ?reveal=true AND the caller is operator+. Plus a small MinIO bucket surface
// (list + create) so the "first bucket" can be made from the console.

type consoleRow struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// consoleService is one tab in the console: a managed service enabled for this env,
// its connection rows (masked), the raw env keys, an optional web-UI subdomain (the
// frontend builds the full URL from the env's apex), and whether bucket management
// applies (S3+MinIO only).
type consoleService struct {
	Kind      string            `json:"kind"`                // redis | s3 | storage_local | mailpit
	Label     string            `json:"label"`               //
	Subdomain string            `json:"subdomain,omitempty"` // web-UI subdomain ("storage"/"mail"); "" = no UI
	Note      string            `json:"note,omitempty"`      //
	Rows      []consoleRow      `json:"rows"`                //
	EnvKeys   map[string]string `json:"env_keys"`            // connection env-var names → values (secrets masked unless revealed)
	Buckets   bool              `json:"buckets,omitempty"`   // S3+MinIO → bucket list/create available
}

type serviceConsoleResponse struct {
	Revealed bool             `json:"revealed"`
	Services []consoleService `json:"services"`
}

// GetServiceConsole — GET …/envs/{env}/services[?reveal=true]. Connection info for the
// env's managed Redis / object-storage / Mailpit sidecars. viewer+ to see (secrets
// masked); operator+ with reveal=true to unmask.
func (h *Handler) GetServiceConsole(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	role := h.pipelineRole(r, ws, name)
	if !auth.AtLeast(role, auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	ec, ok := cfg.Environments[env]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "environment not found"})
		return
	}
	reveal := r.URL.Query().Get("reveal") == "true" && auth.AtLeast(role, auth.RoleOperator)
	dotenv := readEnvMap(wspath.DotEnv(h.workspacesDir, ws, name, env))
	mask := func(v string) string {
		if v == "" || reveal {
			return v
		}
		return dbMask
	}
	prefix := cfg.Project.Prefix()
	resp := serviceConsoleResponse{Revealed: reveal, Services: []consoleService{}}

	// ── Redis ──────────────────────────────────────────────────────────────────
	if cfg.EffRedis(ec) {
		host := dotenv["REDIS_HOST"]
		if host == "" {
			host = prefix + "_redis"
		}
		port := dotenv["REDIS_PORT"]
		if port == "" {
			port = "6379"
		}
		pass := dotenv["REDIS_PASSWORD"]
		svc := consoleService{
			Kind: "redis", Label: "Redis",
			Note:    "In-network cache / queue backend. No web UI — connect from app services or a redis client.",
			Rows:    []consoleRow{{Label: "Host", Value: host}, {Label: "Port", Value: port}},
			EnvKeys: map[string]string{"REDIS_HOST": host, "REDIS_PORT": port},
		}
		if pass != "" {
			svc.Rows = append(svc.Rows, consoleRow{Label: "Password", Value: mask(pass)})
			svc.EnvKeys["REDIS_PASSWORD"] = mask(pass)
		} else {
			svc.Rows = append(svc.Rows, consoleRow{Label: "Password", Value: "(none)"})
		}
		if u := dotenv["REDIS_URL"]; u != "" {
			svc.EnvKeys["REDIS_URL"] = u
		}
		resp.Services = append(resp.Services, svc)
	}

	// ── Object storage ───────────────────────────────────────────────────────────
	// MinIO (S3) gets the rich tab; a local-only volume shows a disk note instead.
	switch {
	case cfg.MinIOOn(ec):
		endpoint := dotenv["MINIO_ENDPOINT"]
		if endpoint == "" {
			endpoint = "http://minio:9000"
		}
		ak, sk := dotenv["MINIO_ROOT_USER"], dotenv["MINIO_ROOT_PASSWORD"]
		bucket := dotenv["MINIO_BUCKET"]
		region := dotenv["MINIO_REGION"]
		if region == "" {
			region = "us-east-1"
		}
		svc := consoleService{
			Kind: "s3", Label: "Object storage (MinIO)", Buckets: true,
			Rows: []consoleRow{
				{Label: "Endpoint (in-network)", Value: endpoint},
				{Label: "Access key", Value: ak},
				{Label: "Secret key", Value: mask(sk)},
				{Label: "Default bucket", Value: bucket},
				{Label: "Region", Value: region},
			},
			EnvKeys: map[string]string{
				"MINIO_ENDPOINT": endpoint, "MINIO_ROOT_USER": ak,
				"MINIO_ROOT_PASSWORD": mask(sk), "MINIO_BUCKET": bucket, "MINIO_REGION": region,
			},
		}
		// Surface the AWS_* mapping too when the app uses the S3 SDK / Laravel.
		for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_BUCKET", "AWS_DEFAULT_REGION", "AWS_ENDPOINT", "AWS_URL", "AWS_USE_PATH_STYLE_ENDPOINT"} {
			if v, ok := dotenv[k]; ok {
				if strings.Contains(k, "SECRET") {
					v = mask(v)
				}
				svc.EnvKeys[k] = v
			}
		}
		if cfg.EffStorageUI(ec) {
			svc.Subdomain = "storage" // MinIO console sidecar (frontend builds the URL from apex)
		}
		resp.Services = append(resp.Services, svc)
	case cfg.LocalStorageOn(ec):
		disk := dotenv["FILESYSTEM_DISK"]
		if disk == "" {
			disk = "local"
		}
		path := cfg.Project.StoragePath
		if path == "" {
			path = "/var/www/html/storage"
		}
		resp.Services = append(resp.Services, consoleService{
			Kind: "storage_local", Label: "Object storage (local volume)",
			Note: "Files persist in the " + prefix + "_storage volume, mounted at " + path + ". No S3 endpoint or web UI.",
			Rows: []consoleRow{
				{Label: "Filesystem disk", Value: disk},
				{Label: "Mount path", Value: path},
				{Label: "Volume", Value: prefix + "_storage"},
			},
			EnvKeys: map[string]string{"FILESYSTEM_DISK": disk},
		})
	}

	// ── Mailpit ────────────────────────────────────────────────────────────────
	if cfg.EffMailpit(ec) {
		resp.Services = append(resp.Services, consoleService{
			Kind: "mailpit", Label: "Mailpit (test SMTP)", Subdomain: "mail",
			Note: "Catch-all test mailbox: the app sends to it and every message lands in the web inbox — nothing is delivered externally.",
			Rows: []consoleRow{
				{Label: "SMTP host (in-network)", Value: "mailpit"},
				{Label: "SMTP port", Value: "1025"},
				{Label: "Encryption", Value: "none"},
			},
			EnvKeys: map[string]string{"MAIL_HOST": "mailpit", "MAIL_PORT": "1025"},
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// bucketNameRe enforces S3 bucket naming (a safe subset): 3–63 chars, lowercase
// alphanumerics plus dots and hyphens, starting and ending alphanumeric.
var bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// minioMC runs a one-off `minio/mc` command against the env's MinIO. The transient
// container SHARES the minio container's network namespace (--network container:…),
// so the S3 API is reachable at 127.0.0.1:9000 with no need to resolve the compose
// network name. Returns combined stdout+stderr (stdout carries --json output on
// success; stderr carries mc's error text on failure).
func (h *Handler) minioMC(workspace, project, env string, mcArgs ...string) ([]byte, error) {
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, workspace, project))
	if err != nil {
		return nil, fmt.Errorf("project not found")
	}
	ec, ok := cfg.Environments[env]
	if !ok {
		return nil, fmt.Errorf("environment not found")
	}
	if !cfg.MinIOOn(ec) {
		return nil, fmt.Errorf("MinIO is not enabled for this project")
	}
	dotenv := readEnvMap(wspath.DotEnv(h.workspacesDir, workspace, project, env))
	user, pass := dotenv["MINIO_ROOT_USER"], dotenv["MINIO_ROOT_PASSWORD"]
	if user == "" || pass == "" {
		return nil, fmt.Errorf("MinIO credentials not found — deploy this environment first")
	}
	ex, err := h.bridge.ExecForEnv(workspace, project, env)
	if err != nil {
		return nil, fmt.Errorf("reach environment host: %w", err)
	}
	ref, err := h.resolveContainerRef(ex, workspace, project, env, "minio")
	if err != nil {
		return nil, err
	}
	if ref == "minio" { // resolver fell back to the service name → no container found
		return nil, fmt.Errorf("MinIO container is not running — deploy this environment first")
	}
	args := []string{
		"run", "--rm", "--network", "container:" + ref,
		"-e", "MC_HOST_r=http://" + user + ":" + pass + "@127.0.0.1:9000",
		"minio/mc:" + cfg.Version("minio_mc", "latest"),
	}
	args = append(args, mcArgs...)
	var buf bytes.Buffer
	rerr := ex.Docker(executor.Spec{Args: args, Stdout: &buf, Stderr: &buf})
	return buf.Bytes(), rerr
}

// ListStorageBuckets — GET …/envs/{env}/storage/buckets. Lists MinIO buckets. viewer+.
func (h *Handler) ListStorageBuckets(w http.ResponseWriter, r *http.Request) {
	workspace, project, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, workspace, project), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	out, err := h.minioMC(workspace, project, env, "ls", "--json", "r")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "list failed: " + strings.TrimSpace(string(out)+" "+err.Error())})
		return
	}
	buckets := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			Key string `json:"key"`
		}
		if json.Unmarshal([]byte(line), &row) == nil && row.Key != "" {
			buckets = append(buckets, strings.TrimSuffix(row.Key, "/"))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": buckets})
}

// CreateStorageBucket — POST …/envs/{env}/storage/buckets {name}. Creates a MinIO
// bucket (idempotent). operator+.
func (h *Handler) CreateStorageBucket(w http.ResponseWriter, r *http.Request) {
	workspace, project, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, workspace, project), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = readJSON(r, &body)
	name := strings.ToLower(strings.TrimSpace(body.Name))
	if !bucketNameRe.MatchString(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bucket name: 3–63 chars, lowercase letters, digits, dots and hyphens (S3 naming)"})
		return
	}
	out, err := h.minioMC(workspace, project, env, "mb", "--ignore-existing", "r/"+name)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "create failed: " + strings.TrimSpace(string(out)+" "+err.Error())})
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, h.resourcePrefix(workspace, project), "s3-create-bucket:"+name, env,
		)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "created", "name": name})
}
