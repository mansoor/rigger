package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mansoor/rigger/ui/internal/actionruns"
	"github.com/mansoor/rigger/ui/internal/alerts"
	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/imagecheck"
	"github.com/mansoor/rigger/ui/internal/notify"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/shell"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/gorilla/websocket"
)

// ── JSON helpers ──────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// ── Rate limiter (simple in-memory, per IP) ───────────────────────────────────

type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rlEntry
}

type rlEntry struct {
	count     int
	resetAt   time.Time
}

var loginLimiter = &rateLimiter{entries: make(map[string]*rlEntry)}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	e, ok := rl.entries[ip]
	if !ok || time.Now().After(e.resetAt) {
		rl.entries[ip] = &rlEntry{count: 1, resetAt: time.Now().Add(15 * time.Minute)}
		return true
	}
	if e.count >= 5 {
		return false
	}
	e.count++
	return true
}

// ── Handlers ──────────────────────────────────────────────────────────────────

type Handler struct {
	auth          *auth.Service
	db            *db.DB
	bridge        *shell.Bridge
	workspacesDir string
	remoteWorkspacesDir string // WORKSPACES_DIR on remote hosts (Phase 7)
	templatesDir  string
	dataDir       string
	imgCache      *imagecheck.Cache
	jobs          *JobStore
	migJobs       *migStore
	alertBroker   *alerts.Broker
	notifier      *notify.Dispatcher
	cryptoKey     []byte // derived from JWT secret; encrypts host SSH keys (Phase 7)
}

func NewHandler(a *auth.Service, d *db.DB, b *shell.Bridge, workspacesDir, remoteWorkspacesDir, templatesDir, dataDir string, imgCache *imagecheck.Cache, alertBroker *alerts.Broker, notifier *notify.Dispatcher, jwtSecret string) *Handler {
	key, _ := crypto.DeriveKey([]byte(jwtSecret)) // empty only if secret empty (config defaults it)
	return &Handler{
		auth: a, db: d, bridge: b,
		workspacesDir: workspacesDir,
		remoteWorkspacesDir: remoteWorkspacesDir,
		templatesDir:  templatesDir,
		dataDir:       dataDir,
		imgCache:      imgCache,
		jobs:          newJobStore(),
		migJobs:       newMigStore(),
		alertBroker:   alertBroker,
		notifier:      notifier,
		cryptoKey:     key,
	}
}

// POST /api/setup  — first-run admin account creation
func (h *Handler) Setup(w http.ResponseWriter, r *http.Request) {
	required, err := h.db.IsSetupRequired()
	if err != nil || !required {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "setup already complete"})
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil || body.Username == "" || len(body.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username and password (min 8 chars) required"})
		return
	}
	if err := h.auth.CreateUser(body.Username, body.Password, "admin"); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}

// GET /api/setup/status  — is setup required?
func (h *Handler) SetupStatus(w http.ResponseWriter, r *http.Request) {
	required, err := h.db.IsSetupRequired()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"setup_required": required})
}

// POST /api/auth/login
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip = strings.Split(xff, ",")[0]
	}
	if !loginLimiter.allow(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts, try again in 15 minutes"})
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	accessToken, refreshToken, err := h.auth.Login2(body.Username, body.Password)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
		return
	}

	setRefreshCookie(w, r, refreshToken)
	writeJSON(w, http.StatusOK, map[string]string{"token": accessToken})
}

func setRefreshCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    token,
		Path:     "/api/auth/refresh",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   7 * 24 * 3600,
	})
}

// POST /api/auth/refresh — exchange refresh cookie for a new access token (rolling session)
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("refresh_token")
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no refresh token"})
		return
	}
	accessToken, newRefresh, err := h.auth.RefreshAccessToken(cookie.Value)
	if err != nil {
		// Clear invalid cookie
		http.SetCookie(w, &http.Cookie{Name: "refresh_token", Value: "", Path: "/api/auth/refresh", MaxAge: -1})
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session expired, please log in again"})
		return
	}
	setRefreshCookie(w, r, newRefresh)
	writeJSON(w, http.StatusOK, map[string]string{"token": accessToken})
}

// POST /api/auth/logout
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		Path:     "/api/auth/refresh",
		HttpOnly: true,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// popularTemplates is the fixed set of 4 templates shown on the main step.
// Order matters — they appear left-to-right in the UI.
var popularTemplates = []string{"vaultwarden", "wordpress", "uptime-kuma", "nextcloud"}

// GET /api/templates  — lists available pre-built stack templates with popularity + usage metadata
func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	templates, err := workspace.ListTemplates(h.templatesDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Build popular set for O(1) lookup
	popularSet := map[string]int{}
	for i, n := range popularTemplates {
		popularSet[n] = i
	}

	// Load usage data from DB
	type usageRow struct {
		UseCount   int    `json:"use_count"`
		LastUsedAt string `json:"last_used_at"`
	}
	usageMap := map[string]usageRow{}
	rows, _ := h.db.Query(`SELECT name, use_count, last_used_at FROM template_usage`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			var u usageRow
			rows.Scan(&name, &u.UseCount, &u.LastUsedAt) //nolint:errcheck
			usageMap[name] = u
		}
	}

	type TemplateResponse struct {
		workspace.TemplateInfo
		Popular    bool   `json:"popular"`
		PopularRank int   `json:"popular_rank"` // 0-based rank among popular; -1 if not popular
		UseCount   int    `json:"use_count"`
		LastUsedAt string `json:"last_used_at,omitempty"`
	}

	result := make([]TemplateResponse, 0, len(templates))
	for _, t := range templates {
		rank, isPop := popularSet[t.Name]
		u := usageMap[t.Name]
		result = append(result, TemplateResponse{
			TemplateInfo: t,
			Popular:      isPop,
			PopularRank:  func() int { if isPop { return rank }; return -1 }(),
			UseCount:     u.UseCount,
			LastUsedAt:   u.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

// POST /api/templates/{name}/use — records that a template was selected in the wizard
func (h *Handler) RecordTemplateUse(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	h.db.Exec(`INSERT INTO template_usage (name, use_count, last_used_at)
		VALUES (?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET
			use_count    = use_count + 1,
			last_used_at = CURRENT_TIMESTAMP`, name) //nolint:errcheck
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /api/templates/{name}  — returns full template (images + default env vars)
func (h *Handler) GetTemplate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	images, envs, err := workspace.LoadTemplate(h.templatesDir, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"images":       images,
		"default_envs": envs,
	})
}

// WS POST /api/workspaces/create — creates workspace from wizard payload, streams bootstrap output
func (h *Handler) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// First message: { "token": "...", "workspace": { ...CreateRequest... } }
	var msg struct {
		Token string                  `json:"token"`
		Workspace workspace.CreateRequest `json:"workspace"`
	}
	if err := conn.ReadJSON(&msg); err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("error: invalid request\n")) //nolint:errcheck
		return
	}

	claims, err := h.auth.ValidateToken(msg.Token)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("error: unauthorized\n")) //nolint:errcheck
		return
	}

	send := func(s string) { conn.WriteMessage(websocket.TextMessage, []byte(s)) } //nolint:errcheck

	send("Creating workspace " + msg.Workspace.Name + "...\n")

	// For pre-built templates, load images + default env vars and apply smart
	// secret generation BEFORE writing config.json. The generated values are
	// stored in TemplateEnvs and embedded into environments[env].env_vars inside
	// buildConfig() — that is the field env-gen.sh reads when generating .env
	// files during bootstrap. This matches what init_workspace.sh does via CLI.
	if msg.Workspace.Type == "image" {
		if msg.Workspace.Template != "" {
			// Pre-built template: load images + default env vars from template JSON
			templateImages, defaultEnvs, err := workspace.LoadTemplate(h.templatesDir, msg.Workspace.Template)
			if err != nil {
				send("\033[31mError loading template: " + err.Error() + "\033[0m\n")
				return
			}
			if len(msg.Workspace.Images) == 0 {
				msg.Workspace.Images = templateImages
			}
			if len(defaultEnvs) > 0 {
				msg.Workspace.TemplateEnvs = workspace.GenerateSmartDefaults(defaultEnvs)
			}
		} else if len(msg.Workspace.CustomEnvVars) > 0 {
			// Custom image stack: apply smart defaults to user-supplied env vars
			msg.Workspace.TemplateEnvs = workspace.GenerateSmartDefaults(msg.Workspace.CustomEnvVars)
		}
	}

	// Capture the host-side folder path once, now, so the UI can show where the
	// workspace lives without a runtime docker inspect (persisted in config as
	// project.workspace_root_dir).
	if hwd := h.hostBindSourceDir(); hwd != "" {
		msg.Workspace.WorkspaceRootDir = strings.TrimRight(hwd, "/\\") + "/" + msg.Workspace.Name
	}

	// Write config.json + run.sh (TemplateEnvs embedded in each env's env_vars block)
	if err := workspace.Create(h.workspacesDir, msg.Workspace); err != nil {
		send("\033[31mError: " + err.Error() + "\033[0m\n")
		return
	}
	send("\033[32m✓\033[0m config.json written\n")

	// Run bootstrap.sh per environment — reads env_vars from config.json
	// and writes the .env file via env-gen.sh (which now has the smart secrets).
	pr, pw := io.Pipe()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := pr.Read(buf)
			if n > 0 {
				conn.WriteMessage(websocket.TextMessage, buf[:n]) //nolint:errcheck
			}
			if readErr != nil {
				break
			}
		}
	}()

	allOk := true
	for _, env := range msg.Workspace.Envs {
		if env.Name == "" {
			continue
		}
		send("\n\033[2mBootstrapping environment: " + env.Name + "\033[0m\n")
		if err := h.bridge.Bootstrap(msg.Workspace.Name, env.Name, pw, pw); err != nil {
			send("\033[31m✗ Bootstrap failed for " + env.Name + ": " + err.Error() + "\033[0m\n")
			allOk = false
		} else {
			send("\033[32m✓ " + env.Name + " bootstrapped\033[0m\n")
			// Write per-environment initial env vars if provided
			if len(env.Vars) > 0 {
				if err2 := workspace.UpdateEnvVars(h.workspacesDir, msg.Workspace.Name, env.Name, env.Vars, nil, nil); err2 != nil {
					send("\033[33m⚠ env vars for " + env.Name + ": " + err2.Error() + "\033[0m\n")
				} else {
					send("\033[32m✓ " + env.Name + " env vars written\033[0m\n")
				}
			}
		}
	}
	pw.Close()

	// Phase 7: bind environments to their chosen remote host. This is metadata
	// only — files are generated, pushed, and started on the host the first time
	// the env is deployed.
	for _, env := range msg.Workspace.Envs {
		if env.Name == "" || env.HostID == 0 {
			continue
		}
		if err := settings.SetEnvHost(h.db, msg.Workspace.Name, env.Name, env.HostID); err != nil {
			send("\033[33m⚠ host binding for " + env.Name + ": " + err.Error() + "\033[0m\n")
		} else {
			send("\033[32m✓ " + env.Name + " bound to remote host\033[0m\n")
		}
	}

	if !allOk {
		send("\n\033[33mWorkspace created with errors — check output above.\033[0m\n")
	}

	// Audit log
	h.db.Exec( //nolint:errcheck
		"INSERT INTO audit_log (user_id, username, workspace, command, env) VALUES (?,?,?,?,?)",
		claims.UserID, claims.Username, msg.Workspace.Name, "create", "",
	)

	send("\n\033[32m✓ Workspace " + msg.Workspace.Name + " is ready!\033[0m\n")
}

// GET /api/events — SSE stream of Docker container events (auth via ?token= query param
// because the browser EventSource API does not support custom request headers).
func (h *Handler) StreamEvents(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if _, err := h.auth.ValidateToken(token); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering if behind a proxy

	// Send a heartbeat comment every 25s to keep the connection alive through proxies
	ctx := r.Context()

	// Run docker events, scoped to container lifecycle events only
	cmd := exec.CommandContext(ctx, "docker", "events", //nolint:gosec
		"--format", "{{json .}}",
		"--filter", "type=container",
		"--filter", "event=start",
		"--filter", "event=die",
		"--filter", "event=stop",
		"--filter", "event=kill",
		"--filter", "event=pause",
		"--filter", "event=unpause",
		"--filter", "event=health_status",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer cmd.Process.Kill() //nolint:errcheck

	// Heartbeat ticker
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	// Subscribe to alert broadcasts (Phase 6) so fired/resolved/dismissed alerts
	// push over this same SSE connection alongside Docker container events.
	var alertSub chan []byte
	if h.alertBroker != nil {
		alertSub = h.alertBroker.Subscribe()
		defer h.alertBroker.Unsubscribe(alertSub)
	}

	// Send initial ping so the client knows it's connected
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()

		case msg := <-alertSub:
			if msg == nil {
				continue
			}
			fmt.Fprintf(w, "event: alert\ndata: %s\n\n", msg)
			flusher.Flush()

		case line, ok := <-lines:
			if !ok {
				return
			}
			var event struct {
				Action string `json:"Action"`
				Actor  struct {
					Attributes map[string]string `json:"Attributes"`
				} `json:"Actor"`
			}
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				continue
			}

			// com.docker.compose.project = "{workspace}_{env}" (set by compose-gen.sh)
			project := event.Actor.Attributes["com.docker.compose.project"]
			container := event.Actor.Attributes["name"]

			// Only forward events from managed compose stacks (project label present)
			if project == "" {
				continue
			}

			payload, _ := json.Marshal(map[string]string{
				"action":    event.Action,
				"project":   project,
				"container": container,
			})
			fmt.Fprintf(w, "event: container\ndata: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

// GET /api/debug/paths — shows resolved paths and workspace dir contents (auth required)
func (h *Handler) DebugPaths(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(h.workspacesDir)
	var names []string
	if err == nil {
		for _, e := range entries {
			names = append(names, e.Name())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspaces_dir":      h.workspacesDir,
		"workspaces_dir_entries": names,
		"workspaces_dir_err": func() string {
			if err != nil {
				return err.Error()
			}
			return ""
		}(),
	})
}

// GET /api/workspaces
func (h *Handler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	workspaces, err := workspace.List(h.workspacesDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.annotateHosts(workspaces)
	writeJSON(w, http.StatusOK, workspaces)
}

// annotateHosts fills each workspace's per-env host map (EnvHosts) from the
// workspace_host_envs bindings (Phase 7). An explicit (workspace, env) row wins
// over the env='' default. When every environment resolves to the same remote
// host, the workspace-level HostID/HostName are also set as a convenience; a
// mixed or local layout leaves them zero.
func (h *Handler) annotateHosts(wss []workspace.Workspace) {
	for i := range wss {
		bindings, err := settings.EnvHosts(h.db, wss[i].Name)
		if err != nil || len(bindings) == 0 {
			continue
		}
		byEnv := make(map[string]settings.EnvHostBinding, len(bindings))
		for _, b := range bindings {
			byEnv[b.Env] = b
		}
		def, hasDef := byEnv[""]

		eh := map[string]workspace.EnvHostRef{}
		common := int64(-1)
		uniform := true
		for _, env := range wss[i].Envs {
			b, ok := byEnv[env]
			if !ok && hasDef {
				b, ok = def, true
			}
			id := int64(0)
			if ok {
				eh[env] = workspace.EnvHostRef{HostID: b.HostID, HostName: b.HostName, Address: b.Address}
				id = b.HostID
			}
			if common == -1 {
				common = id
			} else if id != common {
				uniform = false
			}
		}
		if len(eh) > 0 {
			wss[i].EnvHosts = eh
		}
		if uniform && common > 0 {
			wss[i].HostID = common
			for _, b := range bindings {
				if b.HostID == common {
					wss[i].HostName = b.HostName
					break
				}
			}
		}
	}
}

// GET /api/workspaces/{name}/envs/{env}/compose  — returns docker-compose.yml content
func (h *Handler) GetCompose(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env := r.PathValue("env")
	path := filepath.Join(h.workspacesDir, name, "envs", env, "docker-compose.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "docker-compose.yml not found for env " + env})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": string(data)})
}

// PUT /api/workspaces/{name}/envs/{env}/compose  — writes docker-compose.yml
func (h *Handler) PutCompose(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env := r.PathValue("env")
	var body struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &body); err != nil || body.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content required"})
		return
	}
	path := filepath.Join(h.workspacesDir, name, "envs", env, "docker-compose.yml")
	if err := os.WriteFile(path, []byte(body.Content), 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	claims := auth.ClaimsFromContext(r.Context())
	if claims != nil {
		h.db.Exec("INSERT INTO audit_log (user_id, username, workspace, command, env) VALUES (?,?,?,?,?)", //nolint:errcheck
			claims.UserID, claims.Username, name, "edit-compose", env)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /api/workspaces/{name}/config  — returns full config.json
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(h.workspacesDir, name, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "config.json not found"})
		return
	}
	// Return raw JSON so the client can parse it directly
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

// PUT /api/workspaces/{name}/config  — writes config.json and optionally re-bootstraps
func (h *Handler) PutConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Content   string   `json:"content"`    // raw JSON string
		Bootstrap []string `json:"bootstrap"`  // env names to re-bootstrap after save
	}
	if err := readJSON(r, &body); err != nil || body.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content required"})
		return
	}
	// Validate it's parseable JSON before writing
	var check any
	if err := json.Unmarshal([]byte(body.Content), &check); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	path := filepath.Join(h.workspacesDir, name, "config.json")
	// Reject changes to project.name: it's the Docker resource prefix (compose
	// project, container, named-volume and network names) and the workspace folder
	// is never renamed — so changing it would orphan the running stack and its
	// volume data on the next deploy. The folder name is the stable identity.
	if existing, rerr := os.ReadFile(path); rerr == nil {
		var was, now struct {
			Project struct {
				Name string `json:"name"`
			} `json:"project"`
		}
		json.Unmarshal(existing, &was)             //nolint:errcheck
		json.Unmarshal([]byte(body.Content), &now) //nolint:errcheck
		if was.Project.Name != "" && now.Project.Name != was.Project.Name {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Project name can't be changed after creation — it's the Docker stack/container/volume prefix and the workspace folder isn't renamed.",
			})
			return
		}
	}
	if err := os.WriteFile(path, []byte(body.Content), 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	claims := auth.ClaimsFromContext(r.Context())
	if claims != nil {
		h.db.Exec("INSERT INTO audit_log (user_id, username, workspace, command, env) VALUES (?,?,?,?,?)", //nolint:errcheck
			claims.UserID, claims.Username, name, "edit-config", "")
	}

	// Auto-regenerate docker-compose.yml for every environment in this workspace.
	// compose-gen.sh is fast (<1s) so this is synchronous and non-blocking in practice.
	// Errors are non-fatal — the config was saved successfully even if regen fails.
	go h.regenCompose(name, body.Content)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// regenCompose runs compose-gen.sh for every environment defined in configJSON.
// Called as a goroutine after PutConfig writes config.json.
func (h *Handler) regenCompose(workspaceName, configJSON string) {
	// Parse environment names from the saved config
	var cfg struct {
		Environments map[string]json.RawMessage `json:"environments"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return
	}

	wsRoot := filepath.Join(h.workspacesDir, workspaceName)

	for envName := range cfg.Environments {
		outPath := filepath.Join(wsRoot, "envs", envName, "docker-compose.yml")

		// Phase 6.5 finish: generate natively in Go — no shell, no fallback. On
		// error, log and skip this env (never write a partial compose file).
		content, err := composegen.Generate([]byte(configJSON), envName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "composegen: failed for %s/%s: %v\n", workspaceName, envName, err)
			continue
		}
		if mkErr := os.MkdirAll(filepath.Dir(outPath), 0o755); mkErr != nil {
			fmt.Fprintf(os.Stderr, "composegen: mkdir for %s/%s: %v\n", workspaceName, envName, mkErr)
			continue
		}
		if wErr := os.WriteFile(outPath, content, 0o644); wErr != nil {
			fmt.Fprintf(os.Stderr, "composegen: write for %s/%s: %v\n", workspaceName, envName, wErr)
		}
	}
}

// GET /api/activity  — recent audit log entries across ALL workspaces (dashboard / slide-out)
func (h *Handler) GetAllActivity(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(
		`SELECT workspace, username, command, env, created_at FROM audit_log
		 WHERE command NOT IN ('logs', 'ps')
		 ORDER BY created_at DESC LIMIT 200`,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	type entry struct {
		Workspace string `json:"workspace"`
		Username  string `json:"username"`
		Command   string `json:"command"`
		Env       string `json:"env"`
		CreatedAt string `json:"created_at"`
	}
	var entries []entry
	for rows.Next() {
		var e entry
		rows.Scan(&e.Workspace, &e.Username, &e.Command, &e.Env, &e.CreatedAt) //nolint:errcheck
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []entry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// GET /api/workspaces/{name}/activity  — recent audit log entries for this workspace
func (h *Handler) GetActivity(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rows, err := h.db.Query(
		`SELECT username, command, env, created_at FROM audit_log
		 WHERE workspace = ? AND command NOT IN ('logs', 'ps')
		 ORDER BY created_at DESC LIMIT 20`, name,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	type entry struct {
		Username  string `json:"username"`
		Command   string `json:"command"`
		Env       string `json:"env"`
		CreatedAt string `json:"created_at"`
	}
	var entries []entry
	for rows.Next() {
		var e entry
		rows.Scan(&e.Username, &e.Command, &e.Env, &e.CreatedAt) //nolint:errcheck
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []entry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// GET /api/workspaces/{name}/envs/{env}/status  — docker compose ps (direct, no run.sh)
// Does NOT go through run.sh ps because that also invokes image-check.sh for image stacks,
// whose output ("up to date", "healthy") falsely triggers the "running" detection logic.
func (h *Handler) GetEnvStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env  := r.PathValue("env")

	// Resolve compose project name from config.json
	cfgData, err := os.ReadFile(filepath.Join(h.workspacesDir, name, "config.json"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "unknown"})
		return
	}
	var cfg struct{ Project struct{ Name string } `json:"project"` }
	json.Unmarshal(cfgData, &cfg) //nolint:errcheck

	project := cfg.Project.Name + "_" + env
	envDir  := filepath.Join(h.workspacesDir, name, "envs", env)

	// Query the daemon the env actually runs on (local, or its remote host).
	ex, err := h.bridge.ExecForEnv(name, env)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "unknown"})
		return
	}

	// docker compose ps --all --format json → NDJSON (one object per line)
	// --all is required: without it, Compose v2 only lists running containers,
	// so exited/stopped containers are silently excluded and status is always "running".
	out, runErr := ex.DockerOutput(executor.Spec{
		Args: []string{"compose", "-p", project, "-f", "docker-compose.yml", "ps", "--all", "--format", "json"},
		Dir:  envDir,
	})

	status := parseComposePsJSON(out, runErr)
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

// parseComposePsJSON parses docker compose ps --format json (NDJSON) output.
// Each line is a JSON object with at least a "State" field.
func parseComposePsJSON(out []byte, runErr error) string {
	if runErr != nil && len(bytes.TrimSpace(out)) == 0 {
		return "unknown"
	}

	type psRow struct {
		State  string `json:"State"`
		Status string `json:"Status"`
		Health string `json:"Health"`
	}

	total, running := 0, 0
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var row psRow
		if err := json.Unmarshal(line, &row); err != nil {
			continue
		}
		total++
		state  := strings.ToLower(row.State + " " + row.Status)
		health := strings.ToLower(row.Health)
		// A container counts as "running" only when it is up AND not actively unhealthy.
		// "starting" is still acceptable — the healthcheck hasn't had a chance to pass yet.
		isUp := strings.Contains(state, "running") || strings.Contains(state, "up")
		if isUp && health != "unhealthy" {
			running++
		}
	}

	switch {
	case total == 0:
		return "stopped"
	case running == total:
		return "running"
	case running > 0:
		return "partial"
	default:
		return "stopped"
	}
}

// parseComposeStatus inspects docker compose ps output and returns a status string.
// docker compose ps table format has a STATUS column with values like:
//   Up 2 hours, Up (healthy), Exited (0), Exit 1, Created, Restarting
func parseComposeStatus(output string, runErr error) string {
	if runErr != nil && !strings.Contains(output, "NAME") {
		// Command failed completely — compose file may not exist yet
		return "unknown"
	}

	lines := strings.Split(output, "\n")
	total, running := 0, 0
	for _, line := range lines {
		// Skip header lines and empty lines
		if line == "" || strings.HasPrefix(line, "NAME") || strings.HasPrefix(line, "─") ||
			strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		// Any non-header line with content is a container row
		lower := strings.ToLower(line)
		if strings.Contains(lower, "up") || strings.Contains(lower, "running") ||
			strings.Contains(lower, "healthy") || strings.Contains(lower, "exit") ||
			strings.Contains(lower, "created") || strings.Contains(lower, "restarting") {
			total++
			if strings.Contains(lower, "up") || strings.Contains(lower, "running") ||
				strings.Contains(lower, "healthy") {
				running++
			}
		}
	}

	switch {
	case total == 0:
		return "stopped"
	case running == total:
		return "running"
	case running > 0:
		return "partial"
	default:
		return "stopped"
	}
}

// GET /api/workspaces/{name}
var (
	hostWsDirOnce  sync.Once
	hostWsDirValue string
)

// hostBindSourceDir returns the workspaces directory path ON THE HOST (the
// bind-mount source), as opposed to h.workspacesDir which is the path inside
// this container (e.g. /toolkit/workspaces). Prefers the HOST_WORKSPACES_DIR
// override; otherwise inspects this container's own mounts for the workspaces
// destination. Cached for the process; returns "" if it can't be determined.
func (h *Handler) hostBindSourceDir() string {
	hostWsDirOnce.Do(func() {
		if v := strings.TrimSpace(os.Getenv("HOST_WORKSPACES_DIR")); v != "" {
			hostWsDirValue = v
			return
		}
		host, _ := os.Hostname() // inside a container this is the container ID
		if host == "" {
			return
		}
		format := fmt.Sprintf(`{{range .Mounts}}{{if eq .Destination "%s"}}{{.Source}}{{end}}{{end}}`, h.workspacesDir)
		if out, err := dockerRun("inspect", host, "--format", format); err == nil {
			hostWsDirValue = strings.TrimSpace(out)
		}
	})
	return hostWsDirValue
}

func (h *Handler) GetWorkspace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ws, err := workspace.Get(h.workspacesDir, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	wss := []workspace.Workspace{ws}
	h.annotateHosts(wss)
	out := wss[0]
	// Surface the host-side folder path so the UI can show where the workspace
	// actually lives (not the container's /toolkit path). Prefer the value stamped
	// into config at creation; fall back to resolving the bind-mount source for
	// workspaces created before workspace_root_dir existed.
	if rd := strings.TrimSpace(out.Config.Project.WorkspaceRootDir); rd != "" {
		out.HostPath = rd
	} else if hwd := h.hostBindSourceDir(); hwd != "" {
		out.HostPath = strings.TrimRight(hwd, "/\\") + "/" + name
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/workspaces/{name}/envs/{env}/vars  — returns env vars with secret
// flags (secret values masked by default, ?reveal=true for plaintext)
func (h *Handler) GetEnvVars(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env := r.PathValue("env")
	reveal := r.URL.Query().Get("reveal") == "true"
	vars, err := workspace.EnvVars(h.workspacesDir, name, env, reveal)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	// Revealing a secret-flagged value is an auditable read (Phase 8d).
	if reveal {
		claims := auth.ClaimsFromContext(r.Context())
		ip := clientIP(r)
		for k, v := range vars {
			if v.Secret {
				h.recordSecretEvent(name, env, k, "read", claims, ip)
			}
		}
	}
	writeJSON(w, http.StatusOK, vars)
}

// PATCH /api/workspaces/{name}/envs/{env}/vars  — updates and/or deletes env vars.
// Body: { updates, deletes, secret_keys }. secret_keys is the full desired set of
// secret-flagged keys for this env. For swarm deployments, flagged values are
// stored as Docker Swarm secrets (encrypted at rest) and kept out of .env; for
// compose they stay in .env and are only masked in the UI.
func (h *Handler) UpdateEnvVars(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env := r.PathValue("env")
	var body struct {
		Updates    map[string]string `json:"updates"`
		Deletes    []string          `json:"deletes"`
		SecretKeys []string          `json:"secret_keys"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if body.Updates == nil {
		body.Updates = map[string]string{}
	}

	ws, err := workspace.Get(h.workspacesDir, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	envCfg := ws.Config.Environments[env]
	swarm := envCfg.Deployment == "swarm"
	versions := map[string]int{}
	for k, v := range envCfg.SecretVersions {
		versions[k] = v
	}
	prevSecret := map[string]bool{}
	for _, k := range envCfg.SecretKeys {
		prevSecret[k] = true
	}
	newSecret := map[string]bool{}
	for _, k := range body.SecretKeys {
		newSecret[k] = true
	}

	claims := auth.ClaimsFromContext(r.Context())
	ip := clientIP(r)
	var warn string

	// Keys whose values must not be written to .env — only those actually secured
	// as Docker secrets, so a creation failure never silently drops the value.
	skipEnvFile := map[string]bool{}

	if swarm {
		// Current plaintext values, so flagging an existing var as secret can move
		// its value into a Docker secret without the user re-typing it.
		current, _ := workspace.EnvVars(h.workspacesDir, name, env, true)

		for k := range newSecret {
			val, provided := body.Updates[k]
			if !provided {
				if cur, ok := current[k]; ok && !cur.Secret {
					val, provided = cur.Value, true // reuse the existing plaintext
				}
			}
			if !provided {
				// Already a secret with no new value → its Docker secret already
				// exists; keep it out of .env.
				if prevSecret[k] {
					skipEnvFile[k] = true
				}
				continue
			}
			ver := versions[k]
			if ver < 1 {
				ver = 1
			}
			if _, serr := h.bridge.EnsureSwarmSecret(name, env, k, val, ver); serr != nil {
				warn = serr.Error() // leave the value in .env as a fallback
			} else {
				versions[k] = ver
				skipEnvFile[k] = true
			}
		}
		// Keys unflagged this save: drop their Docker secret (best-effort). A new
		// plaintext value, if provided, falls through to .env below.
		for k := range prevSecret {
			if !newSecret[k] {
				if v := versions[k]; v > 0 {
					h.bridge.RemoveSwarmSecret(name, env, k, v) //nolint:errcheck
				}
				delete(versions, k)
			}
		}
		// Deleted keys that were secrets: remove the Docker secret too.
		for _, k := range body.Deletes {
			if prevSecret[k] {
				if v := versions[k]; v > 0 {
					h.bridge.RemoveSwarmSecret(name, env, k, v) //nolint:errcheck
				}
				delete(versions, k)
			}
		}
	}

	if err := workspace.UpdateEnvVars(h.workspacesDir, name, env, body.Updates, body.Deletes, skipEnvFile); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := workspace.SetSecretMeta(h.workspacesDir, name, env, body.SecretKeys, versions); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "saved .env but failed to persist secret flags: " + err.Error()})
		return
	}
	// Regenerate this env's compose so the secrets wiring (or its removal) is
	// reflected immediately, without waiting for a Refresh.
	if cfgData, rerr := os.ReadFile(filepath.Join(h.workspacesDir, name, "config.json")); rerr == nil {
		if content, gerr := composegen.Generate(cfgData, env); gerr == nil {
			outPath := filepath.Join(h.workspacesDir, name, "envs", env, "docker-compose.yml")
			os.WriteFile(outPath, content, 0o644) //nolint:errcheck
		}
	}

	// Audit secret writes (newly-flagged or value-changed) and deletes — for both
	// compose and swarm. Key names only, never values.
	for k := range newSecret {
		if _, provided := body.Updates[k]; provided || !prevSecret[k] {
			h.recordSecretEvent(name, env, k, "write", claims, ip)
		}
	}
	for _, k := range body.Deletes {
		if prevSecret[k] {
			h.recordSecretEvent(name, env, k, "delete", claims, ip)
		}
	}

	// If this env runs on a remote host, push the updated .env to it — the local
	// file is just a cache; the host's copy is what `docker compose` actually
	// reads. This is the one explicit override of the host-authoritative .env.
	pushed, hostName, err := h.bridge.PushEnvFile(name, env)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "saved locally but could not push to host " + hostName + ": " + err.Error(),
		})
		return
	}
	pushedTo := ""
	if pushed {
		pushedTo = hostName
	}

	// Audit log
	if claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, workspace, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, name, "env-update", env,
		)
	}
	resp := map[string]string{"status": "ok", "pushed_to_host": pushedTo}
	if warn != "" {
		resp["warning"] = warn
	}
	writeJSON(w, http.StatusOK, resp)
}

// DELETE /api/workspaces/{name} — permanently removes a workspace directory
func (h *Handler) DeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\..") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid workspace name"})
		return
	}

	wsPath := filepath.Join(h.workspacesDir, name)
	if _, err := os.Stat(wsPath); os.IsNotExist(err) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}

	if err := os.RemoveAll(wsPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete workspace: " + err.Error()})
		return
	}

	claims := auth.ClaimsFromContext(r.Context())
	if claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, workspace, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, name, "delete", "",
		)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// POST /api/workspaces/{name}/action  — runs a run.sh command, streams output via WebSocket
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // CORS handled at server level
}

type actionRequest struct {
	Command string   `json:"command"`
	Env     string   `json:"env"`
	Extra   []string `json:"extra"`
}

// WS /api/workspaces/{name}/action
func (h *Handler) RunAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Expect first message: { "command": "start", "env": "prod", "token": "<jwt>" }
	var req struct {
		actionRequest
		Token string `json:"token"`
	}
	if err := conn.ReadJSON(&req); err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("error: invalid request\n")) //nolint:errcheck
		return
	}

	// Validate token from first WS message (no Authorization header over WS)
	claims, err := h.auth.ValidateToken(req.Token)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("error: unauthorized\n")) //nolint:errcheck
		return
	}

	// Audit log — skip read-only/streaming commands that aren't meaningful as activity
	if req.Command != "logs" && req.Command != "ps" {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, workspace, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, name, req.Command, req.Env,
		)
	}

	// Pipe stdout+stderr → WebSocket text frames, capturing a bounded copy for the
	// recorded Action-output history. Both streams share one writer so output
	// appears in order.
	startedAt := time.Now()
	var outBuf bytes.Buffer
	const outCap = 128 * 1024
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, readErr := pr.Read(buf)
			if n > 0 {
				conn.WriteMessage(websocket.TextMessage, buf[:n]) //nolint:errcheck
				if outBuf.Len() < outCap {
					outBuf.Write(buf[:n])
				}
			}
			if readErr != nil {
				break
			}
		}
	}()

	runErr := h.bridge.Run(shell.RunOptions{
		Workspace: name,
		Command:   req.Command,
		Env:       req.Env,
		Extra:     req.Extra,
		Stdout:    pw,
		Stderr:    pw, // merged: errors appear inline with output, not silently dropped
	})
	pw.Close()
	<-done // ensure all streamed output is captured before recording

	var marker string
	if runErr != nil {
		marker = "\n\033[31m✗ " + req.Command + " failed: " + runErr.Error() + "\033[0m\n"
	} else {
		marker = "\n\033[32m✓ " + req.Command + " " + req.Env + " completed successfully.\033[0m\n"
		// After a successful update, invalidate the image-check cache so the next
		// frontend poll triggers a fresh check against the newly pulled image digests.
		if req.Command == "update" && req.Env != "" {
			h.imgCache.Invalidate(name, req.Env)
			go func() {
				results := imagecheck.Check(h.workspacesDir, name, req.Env)
				if results != nil {
					h.imgCache.Set(name, req.Env, results)
				}
			}()
		}
	}
	conn.WriteMessage(websocket.TextMessage, []byte(marker)) //nolint:errcheck
	outBuf.WriteString(marker)

	// Record the run in the per-workspace Action-output history (skip read-only
	// streaming commands, matching the audit-log policy).
	if req.Command != "logs" && req.Command != "ps" {
		status := "ok"
		if runErr != nil {
			status = "fail"
		}
		actionruns.Record(h.db, actionruns.Run{ //nolint:errcheck
			Workspace: name, Env: req.Env, Command: req.Command,
			Extra:     strings.Join(req.Extra, " "), Username: claims.Username,
			Status:    status, Output: outBuf.String(),
			StartedAt: startedAt.UnixMilli(), FinishedAt: time.Now().UnixMilli(),
		})
	}

	// Record backup outcomes so the backup_failed alert condition has a source
	// (audit_log only records that a backup ran, not whether it succeeded).
	if req.Command == "backup" && req.Env != "" {
		status, msg := "ok", ""
		if runErr != nil {
			status, msg = "error", runErr.Error()
		}
		alerts.LogBackup(h.db, name, req.Env, status, msg, 0) //nolint:errcheck
	}
}

// GET /api/workspaces/{name}/action-runs?limit=N — recorded Action-output
// history for a workspace (newest first).
func (h *Handler) GetActionRuns(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	runs, err := actionruns.List(h.db, name, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// DELETE /api/workspaces/{name}/action-runs — clear recorded history for a workspace.
func (h *Handler) ClearActionRuns(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := actionruns.Clear(h.db, name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

// GET /api/stats — dashboard stats (docker info + host metrics + workspace summary)
func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.bridge.Stats())
}

// GET /api/live-stats — cheap per-project live stats (cpu/mem/net/running/services)
// for the near-real-time dashboard table. No disk du / docker info, so it's safe
// to poll every few seconds. Keyed by compose project name ({workspace}_{env}).
func (h *Handler) GetLiveStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.bridge.LiveStats())
}

// GET /api/workspaces/{name}/envs/{env}/containers — lists containers via docker compose ps
func (h *Handler) GetContainers(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env  := r.PathValue("env")

	cfgPath := filepath.Join(h.workspacesDir, name, "config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	var cfg struct {
		Project struct{ Name string } `json:"project"`
	}
	json.Unmarshal(data, &cfg) //nolint:errcheck
	project := cfg.Project.Name + "_" + env

	envDir := filepath.Join(h.workspacesDir, name, "envs", env)

	// Query the daemon the env actually runs on (local, or its remote host).
	ex, exErr := h.bridge.ExecForEnv(name, env)
	if exErr != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	// --all: include exited/stopped containers so the health panel shows their actual state
	out, err := ex.DockerOutput(executor.Spec{
		Args: []string{"compose", "-p", project, "-f", "docker-compose.yml", "ps", "--all", "--format", "json"},
		Dir:  envDir,
	})

	type Container struct {
		Name    string `json:"Name"`
		Service string `json:"Service"`
		State   string `json:"State"`
		Status  string `json:"Status"`
		Health  string `json:"Health"` // healthy | unhealthy | starting | "" (no healthcheck)
	}

	var containers []Container
	if err == nil {
		// docker compose ps --format json outputs one JSON object per line (NDJSON)
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			var c Container
			if json.Unmarshal([]byte(line), &c) == nil {
				containers = append(containers, c)
			}
		}
	}
	if containers == nil {
		containers = []Container{}
	}
	writeJSON(w, http.StatusOK, containers)
}

// GET /api/workspaces/{name}/envs/{env}/image-updates — returns cached image update check results
func (h *Handler) GetImageUpdates(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env := r.PathValue("env")

	type response struct {
		Updates   []imagecheck.ServiceUpdate `json:"updates"`
		CheckedAt *time.Time                 `json:"checked_at,omitempty"`
		Pending   bool                       `json:"pending"` // true if no cache entry yet
	}

	entry, ok := h.imgCache.Get(name, env)
	if !ok {
		// Trigger an async check so the next poll will have results
		go func() {
			results := imagecheck.Check(h.workspacesDir, name, env)
			if results != nil {
				h.imgCache.Set(name, env, results)
			}
		}()
		writeJSON(w, http.StatusOK, response{Pending: true})
		return
	}
	writeJSON(w, http.StatusOK, response{Updates: entry.Results, CheckedAt: &entry.CheckedAt})
}

// isSecretEnvKey reports whether an env-var name looks like a secret, so its
// value is masked (→ CHANGE_ME) when generating a template draft.
func isSecretEnvKey(k string) bool {
	ku := strings.ToUpper(k)
	return strings.Contains(ku, "PASSWORD") || strings.Contains(ku, "SECRET") ||
		strings.Contains(ku, "TOKEN") || strings.Contains(ku, "KEY") || strings.Contains(ku, "SALT")
}

// GET /api/workspaces/{name}/template-draft?env=<env>
// Generates a prebuilt-template JSON draft from an image workspace and RETURNS
// it (no file is written) so the Template Manager can load it into its editor
// for review, validation and save. Secret env-var values are masked here so
// they never reach the browser.
func (h *Handler) GenerateTemplateDraft(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfgPath := filepath.Join(h.workspacesDir, name, "config.json")
	cfgData, err := os.ReadFile(cfgPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	var cfg map[string]any
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "invalid config.json"})
		return
	}

	projectType, _ := cfg["project"].(map[string]any)["type"].(string)
	if projectType != "image" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "only image stacks can be turned into templates"})
		return
	}

	// Choose the env to read default values from (query param, else first env).
	env := r.URL.Query().Get("env")
	if env == "" {
		if envs, ok := cfg["environments"].(map[string]any); ok {
			for k := range envs {
				env = k
				break
			}
		}
	}

	// Default env-var values come from the env's decrypted .env file (the config's
	// image env_vars are only ${VAR} references). Secrets are masked to CHANGE_ME.
	defaultEnvVars := map[string]string{}
	if env != "" {
		if vars, err := workspace.EnvVars(h.workspacesDir, name, env, true); err == nil {
			for k, v := range vars {
				if v.Secret || isSecretEnvKey(k) {
					defaultEnvVars[k] = "CHANGE_ME"
				} else {
					defaultEnvVars[k] = v.Value
				}
			}
		}
	}

	// Draft template: name/label/description/tags are left for the user to fill in
	// (validated & saved via the Template Manager). images carry over as-is.
	draft := map[string]any{
		"name":             "",
		"label":            "",
		"description":      "",
		"tags":             []string{},
		"images":           cfg["images"],
		"default_env_vars": defaultEnvVars,
	}
	writeJSON(w, http.StatusOK, draft)
}

// POST /api/tools/save-template — save a converter-generated template JSON to templates/stacks/
func (h *Handler) SaveTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string          `json:"name"`    // filename slug (no extension)
		Content json.RawMessage `json:"content"` // raw template JSON
		Force   bool            `json:"force"`   // overwrite if exists
	}
	if err := readJSON(r, &body); err != nil || body.Name == "" || len(body.Content) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and content required"})
		return
	}

	// Sanitise name — allow only lowercase letters, digits, hyphens
	for _, c := range body.Name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be lowercase letters, digits, hyphens only"})
			return
		}
	}

	// Validate it's parseable JSON
	var check any
	if err := json.Unmarshal(body.Content, &check); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content is not valid JSON"})
		return
	}

	stacksDir := filepath.Join(h.templatesDir, "stacks")
	if err := os.MkdirAll(stacksDir, 0755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create stacks directory"})
		return
	}

	destPath := filepath.Join(stacksDir, body.Name+".json")
	if !body.Force {
		if _, err := os.Stat(destPath); err == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "template already exists", "name": body.Name})
			return
		}
	}

	// Pretty-print the JSON before saving
	var pretty any
	json.Unmarshal(body.Content, &pretty) //nolint:errcheck
	out, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to format JSON"})
		return
	}

	if err := os.WriteFile(destPath, out, 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to write template: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "name": body.Name, "path": destPath})
}

// POST /api/auth/password — change current user's password
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := readJSON(r, &body); err != nil || body.Current == "" || len(body.New) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "current_password and new_password (min 8 chars) required"})
		return
	}
	if err := h.auth.ChangePassword(claims.UserID, body.Current, body.New); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "current password is incorrect"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /api/backups — list all backup snapshots across all workspaces
func (h *Handler) ListBackups(w http.ResponseWriter, r *http.Request) {
	type BackupFile struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	type BackupSnapshot struct {
		Workspace string       `json:"workspace"`
		Env       string       `json:"env"`
		Date      string       `json:"date"`
		SizeBytes int64        `json:"size_bytes"`
		Files     []BackupFile `json:"files"`
	}

	var results []BackupSnapshot

	wsEntries, err := os.ReadDir(h.workspacesDir)
	if err != nil {
		writeJSON(w, http.StatusOK, results)
		return
	}

	for _, wsEntry := range wsEntries {
		if !wsEntry.IsDir() {
			continue
		}
		wsName := wsEntry.Name()
		backupsRoot := filepath.Join(h.workspacesDir, wsName, "backups")

		envEntries, err := os.ReadDir(backupsRoot)
		if err != nil {
			continue // no backups dir
		}

		for _, envEntry := range envEntries {
			if !envEntry.IsDir() {
				continue
			}
			envName := envEntry.Name()
			snapshots, err := os.ReadDir(filepath.Join(backupsRoot, envName))
			if err != nil {
				continue
			}

			for _, snap := range snapshots {
				if !snap.IsDir() {
					continue
				}
				snapDir := filepath.Join(backupsRoot, envName, snap.Name())
				files, _ := os.ReadDir(snapDir)

				var bfiles []BackupFile
				var totalSize int64
				for _, f := range files {
					if f.IsDir() {
						continue
					}
					info, _ := f.Info()
					size := int64(0)
					if info != nil {
						size = info.Size()
					}
					totalSize += size
					bfiles = append(bfiles, BackupFile{Name: f.Name(), Size: size})
				}
				results = append(results, BackupSnapshot{
					Workspace: wsName,
					Env:       envName,
					Date:      snap.Name(),
					SizeBytes: totalSize,
					Files:     bfiles,
				})
			}
		}
	}

	// Sort newest first (snapshot dirs are named YYYY-MM-DD_HH-MM-SS so lexicographic desc works)
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}

	writeJSON(w, http.StatusOK, results)
}

// DELETE /api/backups/{workspace}/{env}/{date} — removes a single backup snapshot directory
func (h *Handler) DeleteBackup(w http.ResponseWriter, r *http.Request) {
	workspace := r.PathValue("workspace")
	env       := r.PathValue("env")
	date      := r.PathValue("date")

	if workspace == "" || env == "" || date == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace, env and date are required"})
		return
	}

	// Validate date looks like a snapshot name (YYYY-MM-DD_HH-MM-SS) to prevent path traversal
	if len(date) != 19 || date[4] != '-' || date[7] != '-' || date[10] != '_' {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid snapshot date format"})
		return
	}

	snapDir := filepath.Join(h.workspacesDir, workspace, "backups", env, date)

	// Verify it's inside the expected backups directory (belt-and-suspenders)
	backupsRoot := filepath.Join(h.workspacesDir, workspace, "backups")
	if !strings.HasPrefix(snapDir, backupsRoot+string(filepath.Separator)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid snapshot path"})
		return
	}

	if err := os.RemoveAll(snapDir); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// WS /api/workspaces/{name}/envs/{env}/terminal — interactive shell into a container.
// The session is opened on whichever daemon the environment runs on: locally via
// the Docker socket (the daemon allocates a PTY in the container, avoiding the
// "input device is not a TTY" error), or — for an env bound to a remote host —
// via `docker exec -it` over an SSH-allocated PTY (Wave C, cross-host terminal).
func (h *Handler) Terminal(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env  := r.PathValue("env")

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// First message: {"token":"...","service":"app","cols":120,"rows":40}
	var init struct {
		Token   string `json:"token"`
		Service string `json:"service"`
		Cols    int    `json:"cols"`
		Rows    int    `json:"rows"`
	}
	if err := conn.ReadJSON(&init); err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("\r\nerror: invalid handshake\r\n")) //nolint:errcheck
		return
	}

	if _, err := h.auth.ValidateToken(init.Token); err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("\r\nerror: unauthorized\r\n")) //nolint:errcheck
		return
	}
	if init.Service == "" {
		conn.WriteMessage(websocket.TextMessage, []byte("\r\nerror: service name required\r\n")) //nolint:errcheck
		return
	}
	// The service name is interpolated into a docker command (and, for remote
	// hosts, a remote shell line) — enforce the strict container-name pattern.
	if !safeContainerName.MatchString(init.Service) {
		conn.WriteMessage(websocket.TextMessage, []byte("\r\nerror: invalid container name\r\n")) //nolint:errcheck
		return
	}

	cols, rows := init.Cols, init.Rows
	if cols <= 0 { cols = 220 }
	if rows <= 0 { rows = 50 }

	// Open the PTY on the env's own daemon (local socket or remote SSH). In Rigger
	// the compose service name is the prefixed container name, so it doubles as
	// the docker exec target — no compose lookup needed, and the same call works
	// for both local and remote.
	de, err := h.bridge.OpenTerminal(name, env, init.Service, cols, rows)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, //nolint:errcheck
			[]byte("\r\nerror: "+err.Error()+"\r\n"))
		return
	}
	defer de.Close()

	conn.WriteMessage(websocket.TextMessage, //nolint:errcheck
		[]byte(fmt.Sprintf("\r\n\x1b[32mConnected to %s/%s — type 'exit' to disconnect\x1b[0m\r\n", env, init.Service)))

	done := make(chan struct{})

	// PTY output → WebSocket
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := de.Read(buf)
			if n > 0 {
				conn.WriteMessage(websocket.BinaryMessage, buf[:n]) //nolint:errcheck
			}
			if err != nil {
				break
			}
		}
		close(done)
	}()

	// WebSocket → PTY input (or resize control messages)
	go func() {
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			// Text frames may be resize control messages: {"type":"resize","rows":r,"cols":c}
			if mt == websocket.TextMessage {
				var ctrl struct {
					Type string `json:"type"`
					Rows int    `json:"rows"`
					Cols int    `json:"cols"`
				}
				if json.Unmarshal(msg, &ctrl) == nil && ctrl.Type == "resize" {
					de.Resize(ctrl.Rows, ctrl.Cols)
					continue
				}
			}
			de.Write(msg) //nolint:errcheck
		}
		de.Close()
	}()

	<-done
}
