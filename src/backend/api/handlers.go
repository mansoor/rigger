package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/gorilla/websocket"
	"github.com/mansoor/rigger/ui/internal/acme"
	"github.com/mansoor/rigger/ui/internal/actionruns"
	"github.com/mansoor/rigger/ui/internal/alerts"
	"github.com/mansoor/rigger/ui/internal/apikey"
	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/customdomains"
	"github.com/mansoor/rigger/ui/internal/databases"
	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/envgen"
	"github.com/mansoor/rigger/ui/internal/envorder"
	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/gitproviders"
	"github.com/mansoor/rigger/ui/internal/gitsync"
	"github.com/mansoor/rigger/ui/internal/imagecheck"
	"github.com/mansoor/rigger/ui/internal/keygen"
	"github.com/mansoor/rigger/ui/internal/notify"
	"github.com/mansoor/rigger/ui/internal/scaffold"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/shell"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
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
	count   int
	resetAt time.Time
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
	auth                *auth.Service
	db                  *db.DB
	bridge              *shell.Bridge
	workspacesDir       string
	remoteWorkspacesDir string // WORKSPACES_DIR on remote hosts (Phase 7)
	templatesDir        string
	dataDir             string
	imgCache            *imagecheck.Cache
	jobs                *JobStore
	migJobs             *migStore
	alertBroker         *alerts.Broker
	notifier            *notify.Dispatcher
	cryptoKey           []byte       // derived from JWT secret; encrypts host SSH keys (Phase 7)
	acmeIssuer          *acme.Issuer // out-of-band per-email override cert issuer (DNS-01/lego)
	acmeCerts           *acme.Store  // tracked override certs (domain → email) for renewal

	// Live pipeline runs: runID → cancel func, so a Cancel request can kill an
	// in-flight run's docker process. Populated for the lifetime of each run's
	// background goroutine; guarded by runMu.
	runMu      sync.Mutex
	runCancels map[int64]context.CancelFunc

	// apiRL enforces the per-API-key, per-project request rate limit for the /api/v1
	// surface (in-memory, process-local — see apikey.RateLimiter).
	apiRL *apikey.RateLimiter
}

func NewHandler(a *auth.Service, d *db.DB, b *shell.Bridge, workspacesDir, remoteWorkspacesDir, templatesDir, dataDir string, imgCache *imagecheck.Cache, alertBroker *alerts.Broker, notifier *notify.Dispatcher, jwtSecret string) *Handler {
	key, _ := crypto.DeriveKey([]byte(jwtSecret)) // empty only if secret empty (config defaults it)
	return &Handler{
		auth: a, db: d, bridge: b,
		workspacesDir:       workspacesDir,
		remoteWorkspacesDir: remoteWorkspacesDir,
		templatesDir:        templatesDir,
		dataDir:             dataDir,
		imgCache:            imgCache,
		jobs:                newJobStore(),
		migJobs:             newMigStore(),
		alertBroker:         alertBroker,
		notifier:            notifier,
		cryptoKey:           key,
		runCancels:          map[int64]context.CancelFunc{},
		acmeIssuer:          acme.New(nil), // local docker daemon (lego runs on the rigger host)
		acmeCerts:           acme.NewStore(d),
		apiRL:               apikey.NewRateLimiter(),
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
		Email    string `json:"email"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil || len(body.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email and password (min 8 chars) required"})
		return
	}
	uid, err := h.auth.SetupAdmin(body.Email, body.Username, body.Password)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Email verification (pragmatic gate): surface the link if SMTP isn't set up yet.
	resp := map[string]any{"status": "ok"}
	if raw, terr := h.auth.CreateVerifyToken(uid); terr == nil {
		link := h.baseURL(r) + "/verify-email?token=" + raw
		if sent, _ := h.sendUserLink(strings.ToLower(strings.TrimSpace(body.Email)), "Verify your Rigger email", "Welcome to Rigger — please verify your email address.", "Verify your email", link); sent {
			resp["email_sent"] = true
		} else {
			resp["verify_link"] = link
		}
	}
	writeJSON(w, http.StatusCreated, resp)
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
		Email    string `json:"email"`
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"` // optional TOTP code (2FA)
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	identifier := strings.TrimSpace(strings.ToLower(body.Email))
	if identifier == "" {
		identifier = body.Username // legacy clients / emailless accounts
	}

	accessToken, refreshToken, err := h.auth.Login2(identifier, body.Password, body.Code)
	if err != nil {
		// 2FA is on but no code yet → prompt for one (200, not an auth failure).
		if errors.Is(err, auth.ErrTOTPRequired) {
			writeJSON(w, http.StatusOK, map[string]any{"totp_required": true})
			return
		}
		// Wrong code → 400 (NOT 401, so the client's global 401→logout/redirect
		// interceptor doesn't fire) and keep prompting for the code.
		if errors.Is(err, auth.ErrTOTPInvalid) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid authentication code", "totp_required": true})
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid email or password"})
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
		Popular     bool   `json:"popular"`
		PopularRank int    `json:"popular_rank"` // 0-based rank among popular; -1 if not popular
		UseCount    int    `json:"use_count"`
		LastUsedAt  string `json:"last_used_at,omitempty"`
	}

	result := make([]TemplateResponse, 0, len(templates))
	for _, t := range templates {
		rank, isPop := popularSet[t.Name]
		u := usageMap[t.Name]
		result = append(result, TemplateResponse{
			TemplateInfo: t,
			Popular:      isPop,
			PopularRank: func() int {
				if isPop {
					return rank
				}
				return -1
			}(),
			UseCount:   u.UseCount,
			LastUsedAt: u.LastUsedAt,
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
	images, envs, files, err := workspace.LoadTemplate(h.templatesDir, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"images":       images,
		"default_envs": envs,
		"files":        files,
	})
}

// GET /api/templates/{name}/raw — returns the verbatim template JSON file
// (name/label/description/tags/images/default_env_vars) so the Template Manager
// can re-open an existing template for editing without losing metadata.
func (h *Handler) GetTemplateRaw(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// Guard against path traversal: the stored filename is a slug.
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid template name"})
			return
		}
	}
	data, err := os.ReadFile(filepath.Join(h.templatesDir, "stacks", name+".json"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("template %q not found", name)})
		return
	}
	var tpl any
	if err := json.Unmarshal(data, &tpl); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "template file is not valid JSON"})
		return
	}
	writeJSON(w, http.StatusOK, tpl)
}

// WS POST /api/workspaces/create — creates workspace from wizard payload, streams bootstrap output
func (h *Handler) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// gorilla/websocket panics on concurrent writes (and a panic in the spawned
	// output goroutine below isn't recovered by net/http — it would crash the whole
	// process). Serialize ALL writes to this conn through safeWrite.
	var wsMu sync.Mutex
	safeWrite := func(b []byte) {
		wsMu.Lock()
		conn.WriteMessage(websocket.TextMessage, b) //nolint:errcheck
		wsMu.Unlock()
	}

	// First message: { "token": "...", "workspace": { ...CreateRequest... } }
	var msg struct {
		Token     string                  `json:"token"`
		Workspace workspace.CreateRequest `json:"workspace"`
	}
	if err := conn.ReadJSON(&msg); err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("\033[31m✗ Error: invalid request\033[0m\n")) //nolint:errcheck
		return
	}

	claims, err := h.auth.ValidateToken(msg.Token)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("\033[31m✗ Error: unauthorized — please sign in again\033[0m\n")) //nolint:errcheck
		return
	}

	send := func(s string) { safeWrite([]byte(s)) }

	wsName := msg.Workspace.Workspace

	// Phase 5.2b: creating a project requires workspace-admin (or super-admin).
	if h.auth.EffectiveRole(claims.UserID, claims.Role, wsName, "") != auth.RoleAdmin {
		send("\033[31m✗ Error: creating a project requires the admin role in this workspace\033[0m\n")
		return
	}

	// Resolve the project key (folder/URL/Docker identity): validated override, or
	// derived from the display name, collision-free within this workspace.
	min, max := h.keyLengths()
	projKey := keygen.Normalize(msg.Workspace.Key)
	if projKey == "" {
		projKey = keygen.Suggest(msg.Workspace.Name, min, max, h.projectKeyTaken(wsName))
	} else if !keygen.Valid(projKey, min, max) {
		send("\033[31m✗ Error: key must be " + strconv.Itoa(min) + "–" + strconv.Itoa(max) + " lowercase letters/digits\033[0m\n")
		return
	} else if h.projectKeyTaken(wsName)(projKey) {
		send("\033[31m✗ Error: project key " + projKey + " is already in use in this workspace\033[0m\n")
		return
	}
	msg.Workspace.Key = projKey

	pkey := wsName + "_" + projKey // resource_prefix / DB key
	send("Creating project " + msg.Workspace.Name + " (" + projKey + ") in workspace " + wsName + "...\n")

	// For pre-built templates, load images + default env vars and apply smart
	// secret generation BEFORE writing config.json. The generated values are
	// stored in TemplateEnvs and embedded into environments[env].env_vars inside
	// buildConfig() — that is the field env-gen.sh reads when generating .env
	// files during bootstrap. This matches what init_workspace.sh does via CLI.
	if msg.Workspace.Type == "image" {
		if msg.Workspace.Template != "" {
			// Pre-built template: load images + default env vars + seed files from template JSON
			templateImages, defaultEnvs, seedFiles, err := workspace.LoadTemplate(h.templatesDir, msg.Workspace.Template)
			if err != nil {
				send("\033[31m✗ Error loading template: " + err.Error() + "\033[0m\n")
				return
			}
			if len(msg.Workspace.Images) == 0 {
				msg.Workspace.Images = templateImages
			}
			if len(defaultEnvs) > 0 {
				msg.Workspace.TemplateEnvs = workspace.GenerateSmartDefaults(defaultEnvs)
			}
			if len(seedFiles) > 0 {
				msg.Workspace.SeedFiles = seedFiles
			}
		} else if len(msg.Workspace.CustomEnvVars) > 0 {
			// Custom image stack: apply smart defaults to user-supplied env vars
			msg.Workspace.TemplateEnvs = workspace.GenerateSmartDefaults(msg.Workspace.CustomEnvVars)
		}
	}

	// Capture the host-side folder path once, now, so the UI can show where the
	// workspace lives without a runtime docker inspect (persisted in config as
	// project.project_root_dir).
	if hwd := h.hostBindSourceDir(); hwd != "" {
		msg.Workspace.ProjectRootDir = strings.TrimRight(hwd, "/\\") + "/" + wsName + "/projects/" + projKey
	}

	// Adapt the project's source repo URL to its git provider's auth form (e.g. an
	// HTTPS URL entered with an SSH deploy key → git@host:owner/repo.git) so builds —
	// and the scaffold push below — clone with the credential actually applied.
	if msg.Workspace.GitProviderID != 0 && strings.TrimSpace(msg.Workspace.SourceRepo) != "" {
		if p, perr := gitproviders.Get(h.db, h.cryptoKey, msg.Workspace.GitProviderID); perr == nil && p != nil {
			msg.Workspace.SourceRepo = p.NormalizeRepoURL(msg.Workspace.SourceRepo)
		}
	}

	// Laravel scaffold: seed session/cache/queue drivers that need no provisioned
	// backend plus a stable APP_KEY, so the fresh app deploys green out of the box (its
	// .env.example carries the same file-based defaults for local dev). Seeded into the
	// project's initial env vars → env-gen writes them into each environment's .env.
	// Done BEFORE Create so they land in config.json. Existing keys are never overwritten.
	if msg.Workspace.Scaffold && scaffold.TemplateID(msg.Workspace.Services) == "laravel" {
		if msg.Workspace.InitialEnvVars == nil {
			msg.Workspace.InitialEnvVars = map[string]string{}
		}
		laravelDefaults := map[string]string{
			"APP_KEY":          scaffold.NewLaravelAppKey(),
			"SESSION_DRIVER":   "file",
			"CACHE_STORE":      "file",
			"QUEUE_CONNECTION": "sync",
		}
		for k, v := range laravelDefaults {
			if _, ok := msg.Workspace.InitialEnvVars[k]; !ok {
				msg.Workspace.InitialEnvVars[k] = v
			}
		}
	}

	// Write config.json + run.sh (TemplateEnvs embedded in each env's env_vars block)
	if err := workspace.Create(h.workspacesDir, msg.Workspace); err != nil {
		send("\033[31m✗ Error: " + err.Error() + "\033[0m\n")
		return
	}
	send("\033[32m✓\033[0m config.json written\n")

	// Upload-source projects: adopt the staged archive (from POST /api/upload-source)
	// into the project's _source/ before bootstrap, so build extracts it into _src.
	if msg.Workspace.SourceKind == "upload" {
		if err := h.adoptUploadedSource(wsName, projKey, msg.Workspace.SourceToken, msg.Workspace.DBSeedFile); err != nil {
			send("\033[31m✗ Error: " + err.Error() + "\033[0m\n")
			return
		}
		send("\033[32m✓\033[0m uploaded source stored\n")
		if msg.Workspace.DBSeedFile != "" {
			send("\033[32m✓\033[0m database seed stored (" + msg.Workspace.DBSeedFile + ")\n")
		}
	}

	// Blueprint scaffolding (opt-in): generate real starter code for the chosen stack
	// and push it to the project's git repo so the developer can clone and work locally.
	// Non-scaffold blueprints are byte-identical to before. The generated tree also
	// feeds the scaffold.zip download and the post-create clone instructions.
	if msg.Workspace.Scaffold {
		id := scaffold.TemplateID(msg.Workspace.Services)
		scaffDir := wspath.ScaffoldDir(h.workspacesDir, wsName, projKey)
		if ok, gerr := scaffold.Generate(h.templatesDir, id, scaffDir); gerr != nil {
			send("\033[33m⚠ scaffold generation failed: " + gerr.Error() + "\033[0m\n")
		} else if !ok {
			send("\033[33m⚠ no starter available for this stack — skipping scaffold\033[0m\n")
		} else {
			if id == "laravel" {
				_ = scaffold.EnsureLaravelAppKey(scaffDir)
			}
			send("\033[32m✓\033[0m scaffolded " + id + " starter code\n")
			// Push to the project's repo when one was provided (empty repo the user created).
			if repo := strings.TrimSpace(msg.Workspace.SourceRepo); repo != "" {
				branch := strings.TrimSpace(msg.Workspace.SourceBranch)
				if branch == "" {
					branch = "main"
				}
				var auth *gitsync.Auth
				if msg.Workspace.GitProviderID != 0 {
					if p, perr := gitproviders.Get(h.db, h.cryptoKey, msg.Workspace.GitProviderID); perr == nil && p != nil {
						if a, aerr := p.BuildAuth(repo); aerr == nil {
							auth = a
						} else {
							send("\033[33m⚠ git auth: " + aerr.Error() + "\033[0m\n")
						}
					}
				}
				if perr := gitsync.Push(scaffDir, repo, branch, auth, sendWriter{send}); perr != nil {
					send("\033[33m⚠ scaffold push failed: " + perr.Error() + " (code saved — download the ZIP and push manually)\033[0m\n")
				} else {
					send("\033[32m✓\033[0m scaffold pushed to " + repo + "\n")
				}
				if auth != nil && auth.Cleanup != nil {
					auth.Cleanup()
				}
			}
		}
	}

	// Run bootstrap.sh per environment — reads env_vars from config.json
	// and writes the .env file via env-gen.sh (which now has the smart secrets).
	pr, pw := io.Pipe()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := pr.Read(buf)
			if n > 0 {
				safeWrite(buf[:n])
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
		if err := h.bridge.Bootstrap(wsName, projKey, env.Name, pw, pw); err != nil {
			send("\033[31m✗ Bootstrap failed for " + env.Name + ": " + err.Error() + "\033[0m\n")
			allOk = false
		} else {
			send("\033[32m✓ " + env.Name + " bootstrapped\033[0m\n")
			// Write per-environment initial env vars (Phase 8 secret-aware: swarm
			// secrets become Docker secrets, kept out of .env).
			if len(env.Vars) > 0 || len(env.SecretKeys) > 0 {
				if err2 := h.seedEnvVars(wsName, projKey, env, auth.ClaimsFromContext(r.Context()), clientIP(r)); err2 != nil {
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
		if err := settings.SetEnvHost(h.db, pkey, env.Name, env.HostID); err != nil {
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
		"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
		claims.UserID, claims.Username, pkey, "create", "",
	)

	send("\n\033[32m✓ Project " + msg.Workspace.Name + " is ready!\033[0m\n")
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
		"workspaces_dir":         h.workspacesDir,
		"workspaces_dir_entries": names,
		"workspaces_dir_err": func() string {
			if err != nil {
				return err.Error()
			}
			return ""
		}(),
	})
}

// GET /api/workspaces — list the parent-tier workspaces.
func (h *Handler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	wss, err := workspace.ListWorkspaces(h.workspacesDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Phase 5.2b: membership-gated. Super-admins see all; everyone else sees only
	// the workspaces they're a member of, annotated with their effective role.
	claims := auth.ClaimsFromContext(r.Context())
	if claims != nil && !auth.IsSuperadmin(claims.Role) {
		filtered := wss[:0]
		for _, ws := range wss {
			// Show a workspace if the user has any access to it — a membership role
			// or at least one per-project grant. MyRole carries the workspace-level
			// role (empty for a project-only member, who has no workspace powers).
			if h.auth.WorkspaceVisible(claims.UserID, claims.Role, ws.Key) {
				ws.MyRole = h.auth.EffectiveRole(claims.UserID, claims.Role, ws.Key, "")
				filtered = append(filtered, ws)
			}
		}
		wss = filtered
	} else if claims != nil {
		for i := range wss {
			wss[i].MyRole = auth.RoleAdmin
		}
	}
	writeJSON(w, http.StatusOK, wss)
}

// GET /api/workspaces/{workspace}/projects — list the projects within a workspace.
func (h *Handler) ListProjects(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	projects, err := workspace.ListProjects(h.workspacesDir, wsName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.annotateHosts(projects)
	h.applyPrimaryDomainURLs(projects) // env card shows the ★ canonical custom domain
	// Per-project access: annotate each with the caller's effective role and hide
	// the ones they can't see (project override of 'none', or no membership). A
	// super-admin sees everything.
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !auth.IsSuperadmin(claims.Role) {
		visible := projects[:0]
		for _, p := range projects {
			if role := h.auth.EffectiveRole(claims.UserID, claims.Role, wsName, p.Name); role != "" {
				p.MyRole = role
				visible = append(visible, p)
			}
		}
		projects = visible
	} else if claims != nil {
		for i := range projects {
			projects[i].MyRole = auth.RoleAdmin
		}
	}
	writeJSON(w, http.StatusOK, projects)
}

// POST /api/workspaces — create a parent-tier workspace. Body: {"name": "..."}.
func (h *Handler) CreateWorkspaceTier(w http.ResponseWriter, r *http.Request) {
	// Creating a new top-level workspace is a super-admin action (Phase 5.2b).
	if claims := auth.ClaimsFromContext(r.Context()); claims == nil || !auth.IsSuperadmin(claims.Role) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a super-admin can create a workspace"})
		return
	}
	var body struct {
		Name string `json:"name"`
		Key  string `json:"key"` // optional override; derived from name when empty
	}
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	body.Name = strings.TrimSpace(body.Name)

	// Resolve the workspace key: validated override, or derived (collision-free).
	min, max := h.keyLengths()
	key := keygen.Normalize(body.Key)
	if key == "" {
		key = keygen.Suggest(body.Name, min, max, h.workspaceKeyTaken())
	} else if !keygen.Valid(key, min, max) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key must be " + strconv.Itoa(min) + "–" + strconv.Itoa(max) + " lowercase letters/digits"})
		return
	} else if h.workspaceKeyTaken()(key) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace key " + key + " is already in use"})
		return
	}

	if err := workspace.EnsureWorkspace(h.workspacesDir, key, body.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec("INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)", //nolint:errcheck
			claims.UserID, claims.Username, key, "create-workspace", "")
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "key": key, "name": body.Name})
}

// DELETE /api/workspaces/{workspace} — delete a whole workspace tier. Safely
// tears down every project/env stack (removing containers/networks/volumes)
// before deleting the folder, so nothing is orphaned.
func (h *Handler) DeleteWorkspaceTier(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	for _, pk := range workspace.ProjectKeys(h.workspacesDir, wsName) {
		// Capture each project's prefix before the dir is removed so its project-scoped
		// DB rows can be purged (else they leak to a same-key workspace recreated later).
		prefix := h.resourcePrefix(wsName, pk)
		h.teardownProjectStacks(wsName, pk, true)
		h.purgeProjectData(prefix, wsName, pk)
	}
	if err := workspace.DeleteWorkspace(h.workspacesDir, wsName); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Remove resources private to this workspace so they don't dangle (Phase 3).
	if ids, derr := settings.WorkspaceOwnedHostIDs(h.db, wsName); derr == nil {
		for _, id := range ids {
			h.bridge.EvictHost(id)
			_ = settings.DeleteHost(h.db, id) //nolint:errcheck
		}
	}
	if ids, derr := settings.WorkspaceOwnedRegistryIDs(h.db, wsName); derr == nil {
		for _, id := range ids {
			_ = settings.DeleteRegistry(h.db, id) //nolint:errcheck
		}
	}
	if ids, derr := settings.WorkspaceOwnedTargetIDs(h.db, wsName); derr == nil {
		for _, id := range ids {
			_ = settings.DeleteBackupTarget(h.db, id) //nolint:errcheck
		}
	}
	if ids, derr := notify.WorkspaceOwnedChannelIDs(h.db, wsName); derr == nil {
		for _, id := range ids {
			_ = notify.DeleteChannel(h.db, id) //nolint:errcheck
		}
	}
	_ = settings.DeleteWorkspaceSettings(h.db, wsName) //nolint:errcheck
	h.auth.DeleteWorkspaceACL(wsName)                  // Phase 5.2: drop membership/overrides
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec("INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)", //nolint:errcheck
			claims.UserID, claims.Username, wsName, "delete-workspace", "")
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// annotateHosts fills each workspace's per-env host map (EnvHosts) from the
// workspace_host_envs bindings (Phase 7). An explicit (workspace, env) row wins
// over the env=” default. When every environment resolves to the same remote
// host, the workspace-level HostID/HostName are also set as a convenience; a
// mixed or local layout leaves them zero.
func (h *Handler) annotateHosts(wss []workspace.Workspace) {
	for i := range wss {
		bindings, err := settings.EnvHosts(h.db, wss[i].WorkspaceName+"_"+wss[i].Name)
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

// refineRemoteEnvURLs corrects the displayed route URL for envs bound to a remote
// host. workspace.load computes each env's URL with the local app_host (the only
// value it's given), so a remote env in magic-DNS mode would show the wrong host.
// Now that annotateHosts has filled EnvHosts with per-env addresses, recompute
// those envs' URLs using their own host's address. (Base-domain and *.localhost
// URLs don't depend on the host, so this only changes magic-DNS URLs.) Call after
// annotateHosts.
func (h *Handler) refineRemoteEnvURLs(wss []workspace.Workspace) {
	for i := range wss {
		if len(wss[i].EnvHosts) == 0 || len(wss[i].EnvAccess) == 0 {
			continue
		}
		data, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, wss[i].WorkspaceName, wss[i].Name))
		if err != nil {
			continue
		}
		baseDomain := settings.EffectiveBaseDomain(h.db, wss[i].WorkspaceName)
		autoMode := settings.EffectiveAutoURLMode(h.db, wss[i].WorkspaceName)
		for env, hostRef := range wss[i].EnvHosts {
			if hostRef.Address == "" {
				continue
			}
			info, ok := wss[i].EnvAccess[env]
			if !ok {
				continue
			}
			if url, routed := composegen.EnvRouteURL(data, env, baseDomain, autoMode, hostRef.Address); routed {
				info.URL = url
				wss[i].EnvAccess[env] = info
			}
		}
	}
}

// applyPrimaryDomainURLs overrides each routed env's displayed URL with its ★ canonical
// custom domain (verified + primary) when one exists, so the env card / Open-app link
// shows the user's own domain (e.g. weather.example.com) instead of the auto subdomain.
// No-op for envs without a primary custom domain. Call after the EnvAccess URLs are set.
func (h *Handler) applyPrimaryDomainURLs(wss []workspace.Workspace) {
	for i := range wss {
		for env, info := range wss[i].EnvAccess {
			if pd := customdomains.PrimaryDomain(h.db, wss[i].WorkspaceName, wss[i].Name, env); pd != "" {
				info.URL = "https://" + pd
				wss[i].EnvAccess[env] = info
			}
		}
	}
}

// GET /api/workspaces/{name}/envs/{env}/compose  — returns docker-compose.yml content
func (h *Handler) GetCompose(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")
	path := filepath.Join(wspath.EnvDir(h.workspacesDir, wsName, name, env), "docker-compose.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "docker-compose.yml not found for env " + env})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": string(data)})
}

// PUT /api/workspaces/{name}/envs/{env}/compose  — writes docker-compose.yml
func (h *Handler) PutCompose(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")
	var body struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &body); err != nil || body.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content required"})
		return
	}
	path := filepath.Join(wspath.EnvDir(h.workspacesDir, wsName, name, env), "docker-compose.yml")
	if err := os.WriteFile(path, []byte(body.Content), 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	claims := auth.ClaimsFromContext(r.Context())
	if claims != nil {
		h.db.Exec("INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)", //nolint:errcheck
			claims.UserID, claims.Username, wsName+"_"+name, "edit-compose", env)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /api/workspaces/{name}/config  — returns full config.json
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	path := wspath.ConfigPath(h.workspacesDir, wsName, name)
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

// configEnvNames extracts the set of environment names from config.json bytes.
func configEnvNames(data []byte) map[string]bool {
	var c struct {
		Environments map[string]json.RawMessage `json:"environments"`
	}
	json.Unmarshal(data, &c) //nolint:errcheck
	out := make(map[string]bool, len(c.Environments))
	for k := range c.Environments {
		out[k] = true
	}
	return out
}

// configEnvDeployments maps each env to its deployment mode (empty → "compose").
func configEnvDeployments(data []byte) map[string]string {
	var c struct {
		Environments map[string]struct {
			Deployment string `json:"deployment"`
		} `json:"environments"`
	}
	json.Unmarshal(data, &c) //nolint:errcheck
	out := make(map[string]string, len(c.Environments))
	for k, v := range c.Environments {
		dep := v.Deployment
		if dep == "" {
			dep = "compose"
		}
		out[k] = dep
	}
	return out
}

// PUT /api/workspaces/{name}/config  — writes config.json and optionally re-bootstraps
// serviceNameOK reports whether a service name is a safe Docker service/alias
// suffix: 1–30 chars of lowercase letters, digits or hyphens, not starting or
// ending with a hyphen.
func serviceNameOK(s string) bool {
	if len(s) == 0 || len(s) > 30 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

// buildArgKeyOK reports whether s is a valid Docker build-arg name: a C-style
// identifier (letters, digits, underscores; not starting with a digit). This is
// the safe subset that maps cleanly to a Dockerfile ARG.
func buildArgKeyOK(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
		isDigit := c >= '0' && c <= '9'
		if i == 0 && !isLetter {
			return false
		}
		if !isLetter && !isDigit {
			return false
		}
	}
	return true
}

// validateConfigServices checks the unified services[] graph in a config.json
// payload. Returns a user-facing message on the first problem, or "" if all good:
// names dns-safe + unique + not colliding with a managed dependency; exactly one
// source per service; image_from/depends_on must reference a real service (or, for
// depends_on, an enabled managed dependency).
func validateConfigServices(content []byte) string {
	reserved := map[string]bool{"postgres": true, "mysql": true, "mariadb": true, "mongodb": true, "redis": true, "minio": true, "minio_init": true, "storage_console": true, "mailpit": true}
	var doc struct {
		Services []struct {
			Name  string `json:"name"`
			Build *struct {
				Args map[string]string `json:"args"`
			} `json:"build"`
			Image        string   `json:"image"`
			ImageFrom    string   `json:"image_from"`
			DependsOn    []string `json:"depends_on"`
			EnvFileMount string   `json:"env_file_mount"`
			WebRouted    bool     `json:"web_routed"`
			Subdomain    string   `json:"subdomain"`
		} `json:"services"`
		// Managed deps are project-level now; the per-env fields are still read as a
		// fallback for configs written before the move.
		Project struct {
			Database      string `json:"database"`
			Redis         bool   `json:"redis_enabled"`
			StorageMinIO  bool   `json:"storage_minio"`
			ObjectStorage string `json:"object_storage"` // legacy enum (back-compat)
		} `json:"project"`
		Environments map[string]struct {
			Database     string `json:"database"`
			RedisEnabled bool   `json:"redis_enabled"`
		} `json:"environments"`
	}
	if err := json.Unmarshal(content, &doc); err != nil {
		return "" // malformed JSON is reported by the caller's own parse check
	}
	// managedActive reports whether name is an ENABLED managed dependency.
	// composegen only emits a managed-dep service at its bare name when the dep is
	// turned on, so a real service may safely reuse e.g. "postgres" in a
	// self-contained stack (a template that bundles its own DB, no managed DB).
	// Deps are project-level; the per-env fields are also consulted for back-compat.
	managedActive := func(name string) bool {
		switch name {
		case "postgres", "mysql", "mariadb", "mongodb":
			if doc.Project.Database == name {
				return true
			}
		case "redis":
			if doc.Project.Redis {
				return true
			}
		case "minio", "storage_console":
			if doc.Project.StorageMinIO || doc.Project.ObjectStorage == "minio" {
				return true
			}
		}
		for _, ec := range doc.Environments {
			switch name {
			case "postgres", "mysql", "mariadb", "mongodb":
				if ec.Database == name {
					return true
				}
			case "redis":
				if ec.RedisEnabled {
					return true
				}
			}
		}
		return false
	}
	// A managed-dep engine name (postgres/redis/minio/…) only collides with a real
	// service when that managed dep is actually enabled. Rigger-owned auxiliary
	// containers (minio_init/storage_console/mailpit) are always reserved.
	alwaysReserved := map[string]bool{"minio_init": true, "storage_console": true, "mailpit": true}
	reservedActive := func(name string) bool { return alwaysReserved[name] || managedActive(name) }

	names := map[string]bool{}
	for _, s := range doc.Services {
		switch {
		case s.Name == "":
			return "each service needs a name"
		case !serviceNameOK(s.Name):
			return fmt.Sprintf("service name %q must be 1–30 chars of lowercase letters, digits or hyphens", s.Name)
		case reserved[s.Name] && reservedActive(s.Name):
			return fmt.Sprintf("service name %q is reserved for the enabled managed dependency — disable the managed dependency or rename this service", s.Name)
		case names[s.Name]:
			return fmt.Sprintf("duplicate service name %q", s.Name)
		}
		sources := 0
		if s.Build != nil {
			sources++
		}
		if s.Image != "" {
			sources++
		}
		if s.ImageFrom != "" {
			sources++
		}
		if sources != 1 {
			return fmt.Sprintf("service %q must have exactly one source (build, image, or image_from)", s.Name)
		}
		if s.Build != nil {
			for k := range s.Build.Args {
				if !buildArgKeyOK(k) {
					return fmt.Sprintf("service %q build arg %q must be a valid identifier (letters, digits, underscores; not starting with a digit)", s.Name, k)
				}
			}
		}
		if m := s.EnvFileMount; m != "" && !strings.HasPrefix(m, "/") {
			return fmt.Sprintf("service %q .env mount path %q must be absolute (e.g. /var/www/html/.env)", s.Name, m)
		}
		names[s.Name] = true
	}
	for _, s := range doc.Services {
		if s.ImageFrom != "" && !names[s.ImageFrom] {
			return fmt.Sprintf("service %q reuses the image of unknown service %q", s.Name, s.ImageFrom)
		}
		for _, d := range s.DependsOn {
			if d == "" || names[d] || managedActive(d) {
				continue
			}
			return fmt.Sprintf("service %q depends_on unknown service %q", s.Name, d)
		}
	}
	// Web-entry host collisions: two web_routed services with the same subdomain
	// (empty = apex) would emit conflicting Traefik routers, so one silently wins.
	// Multiple web entries are fine as long as each claims a distinct host.
	seenHost := map[string]string{} // subdomain → first service that claimed it
	for _, s := range doc.Services {
		if !s.WebRouted {
			continue
		}
		sub := strings.TrimSpace(s.Subdomain)
		if prev, ok := seenHost[sub]; ok {
			host := "the apex domain"
			if sub != "" {
				host = fmt.Sprintf("subdomain %q", sub)
			}
			return fmt.Sprintf("services %q and %q are both web entries on %s — give one a distinct subdomain", prev, s.Name, host)
		}
		seenHost[sub] = s.Name
	}
	return ""
}

// validateConfigRoutes checks the project-level routing table (Config.Routes). An empty table
// is always valid (legacy single-web-entry behavior). Each route must target a declared
// service; path matches must be absolute; no two routes may claim the same (type, match) tuple;
// and at most one catch-all path is allowed. Returns "" when OK, else a user-facing message.
func validateConfigRoutes(content []byte) string {
	var doc struct {
		Services []struct {
			Name string `json:"name"`
		} `json:"services"`
		Routes []struct {
			Service string `json:"service"`
			Type    string `json:"type"`
			Match   string `json:"match"`
			Target  string `json:"target"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(content, &doc); err != nil {
		return "" // malformed JSON is reported by the caller's own parse check
	}
	if len(doc.Routes) == 0 {
		return ""
	}
	names := map[string]bool{}
	for _, s := range doc.Services {
		names[s.Name] = true
	}
	seen := map[string]string{} // "type\x00match" → first service that claimed it
	catchAlls := 0
	for _, r := range doc.Routes {
		svc := strings.TrimSpace(r.Service)
		if svc == "" || !names[svc] {
			return fmt.Sprintf("routing rule points at unknown service %q", r.Service)
		}
		typ := strings.TrimSpace(r.Type)
		if typ == "" {
			typ = "path"
		}
		if typ != "path" && typ != "subdomain" {
			return fmt.Sprintf("routing rule type %q must be \"path\" or \"subdomain\"", r.Type)
		}
		match := strings.TrimSpace(r.Match)
		if typ == "path" {
			if match == "" || match == "/" {
				catchAlls++
				match = "/" // normalize for duplicate detection
			} else if !strings.HasPrefix(match, "/") {
				return fmt.Sprintf("routing path %q must start with \"/\" (e.g. /api)", r.Match)
			}
			if t := strings.TrimSpace(r.Target); t != "" && !strings.HasPrefix(t, "/") {
				return fmt.Sprintf("routing target %q must start with \"/\" (e.g. /api/v1)", r.Target)
			}
		}
		key := typ + "\x00" + match
		if prev, ok := seen[key]; ok {
			where := fmt.Sprintf("subdomain %q", match)
			if typ == "path" {
				where = fmt.Sprintf("path %q", match)
			}
			return fmt.Sprintf("services %q and %q both claim %s — each path/subdomain can map to only one service", prev, svc, where)
		}
		seen[key] = svc
	}
	if catchAlls > 1 {
		return "only one catch-all route (path \"/\") is allowed"
	}
	return ""
}

// validateManagedEngines checks the managed-engine slots hold engines of the right
// category: Database ∈ database-category, Search ∈ search-category, TSDB ∈ tsdb-category.
// This keeps an auxiliary engine (opensearch/victoriametrics) from being wedged into the
// primary DB slot (or vice-versa) via a hand-edited config.
func validateManagedEngines(content []byte) string {
	var doc struct {
		Project struct {
			Database string `json:"database"`
			Search   string `json:"search"`
			TSDB     string `json:"tsdb"`
			Queue    string `json:"queue"`
		} `json:"project"`
	}
	if err := json.Unmarshal(content, &doc); err != nil {
		return ""
	}
	check := func(id, cat, label string) string {
		if id == "" || id == "none" {
			return ""
		}
		e, ok := databases.Get(id)
		if !ok || e.Category != cat {
			return fmt.Sprintf("invalid %s engine %q", label, id)
		}
		return ""
	}
	if m := check(doc.Project.Database, "database", "database"); m != "" {
		return m
	}
	if m := check(doc.Project.Search, "search", "search"); m != "" {
		return m
	}
	if m := check(doc.Project.TSDB, "tsdb", "TSDB"); m != "" {
		return m
	}
	if m := check(doc.Project.Queue, "queue", "message queue"); m != "" {
		return m
	}
	return ""
}

func (h *Handler) PutConfig(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	var body struct {
		Content   string   `json:"content"`   // raw JSON string
		Bootstrap []string `json:"bootstrap"` // env names to re-bootstrap after save
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
	if msg := validateConfigServices([]byte(body.Content)); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if msg := validateConfigRoutes([]byte(body.Content)); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if msg := validateManagedEngines([]byte(body.Content)); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	path := wspath.ConfigPath(h.workspacesDir, wsName, name)
	// Reject changes to project.resource_prefix: it's the immutable Docker resource
	// prefix (compose project, container, named-volume, network and secret names)
	// — changing it would orphan the running stack and its volume data on the next
	// deploy. The display name (project.name) IS editable; only the prefix is frozen.
	oldEnvs := map[string]bool{}
	oldDeployments := map[string]string{}
	if existing, rerr := os.ReadFile(path); rerr == nil {
		var was, now struct {
			Project struct {
				ResourcePrefix string `json:"resource_prefix"`
				Key            string `json:"key"`
			} `json:"project"`
		}
		json.Unmarshal(existing, &was)             //nolint:errcheck
		json.Unmarshal([]byte(body.Content), &now) //nolint:errcheck
		if was.Project.ResourcePrefix != "" && now.Project.ResourcePrefix != was.Project.ResourcePrefix {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "resource_prefix can't be changed after creation — it's the immutable Docker stack/container/volume/secret prefix.",
			})
			return
		}
		if was.Project.Key != "" && now.Project.Key != was.Project.Key {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "key can't be changed after creation — it's the immutable folder/URL/Docker identifier.",
			})
			return
		}
		oldEnvs = configEnvNames(existing)
		oldDeployments = configEnvDeployments(existing)
	}

	// Environments removed in this edit must be fully cleaned up — otherwise their
	// containers stay running and their envs/<env> dir lingers, which keeps the env
	// visible on the workspace page even though it's gone from config (and so can't
	// be acted on). Tear each one down BEFORE overwriting config.json, while the
	// deploy layer can still resolve it; then delete its directory.
	newEnvs := configEnvNames([]byte(body.Content))
	for env := range oldEnvs {
		if newEnvs[env] {
			continue
		}
		// Removed env: tear it down + delete its directory while the old
		// config.json still resolves it. purgeVolumes=false — a user-driven
		// env delete must NOT destroy its data volumes (122ad60 orphan-cleanup).
		h.removeEnv(wsName, name, env, false)
	}

	// Deployment-mode changes (compose↔swarm) must tear down the OLD deployment
	// first — its containers/networks (e.g. a bridge network) otherwise collide
	// with the new mode's deploy (swarm wants the same name as an overlay). Run
	// `down` while the OLD config.json is still on disk so it resolves the old
	// mode; the user then redeploys cleanly under the new mode.
	newDeployments := configEnvDeployments([]byte(body.Content))
	for env, oldDep := range oldDeployments {
		if !newEnvs[env] || env == "" || strings.ContainsAny(env, "/\\.") {
			continue // removed envs handled above
		}
		if nd := newDeployments[env]; nd == "" || nd == oldDep {
			continue
		}
		var out bytes.Buffer
		if derr := h.bridge.Run(shell.RunOptions{Workspace: wsName, Project: name, Command: "down", Env: env, Stdout: &out, Stderr: &out}); derr != nil {
			fmt.Fprintf(os.Stderr, "PutConfig: tear down for mode change %s/%s (%s→%s): %v\n%s",
				name, env, oldDep, newDeployments[env], derr, out.String())
		}
	}

	if err := os.WriteFile(path, []byte(body.Content), 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// (Removed-env directory cleanup already happened above via removeEnv, which
	// runs `down` + os.RemoveAll while the old config still resolves the env.)
	claims := auth.ClaimsFromContext(r.Context())
	if claims != nil {
		h.db.Exec("INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)", //nolint:errcheck
			claims.UserID, claims.Username, wsName+"_"+name, "edit-config", "")
	}

	// Auto-regenerate docker-compose.yml for every environment in this workspace.
	// compose-gen.sh is fast (<1s) so this is synchronous and non-blocking in practice.
	// Errors are non-fatal — the config was saved successfully even if regen fails.
	go h.regenCompose(wsName, name, body.Content)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// regenCompose regenerates docker-compose.yml for every environment defined in
// configJSON, AND reconciles each env's .env image pointers to the project's current
// registry. Called as a goroutine after PutConfig writes config.json.
func (h *Handler) regenCompose(workspaceName, project, configJSON string) {
	cfg, err := wsconfig.Parse([]byte(configJSON))
	if err != nil {
		return
	}

	wsRoot := wspath.ProjectDir(h.workspacesDir, workspaceName, project)
	baseDomain := settings.WorkspaceBaseDomain(h.db, workspaceName)
	prefix := cfg.Project.Prefix()
	// Effective registry (project → workspace/global system) drives the .env rebase
	// AND the regenerated compose image refs, so a project inheriting a system
	// registry re-prefixes its {SVC}_IMAGE pointers to it (Phase 0: resolves to the
	// project's own value, so identical output).
	registry := settings.EffectiveRegistry(h.db, workspaceName, cfg.Project.Registry)

	for envName := range cfg.Environments {
		envDir := filepath.Join(wsRoot, "envs", envName)
		envPath := filepath.Join(envDir, ".env")
		envContent, _ := os.ReadFile(envPath)

		// Reconcile the .env REGISTRY + {SVC}_IMAGE pointers to the project's current
		// registry, so changing (or clearing) the registry in Edit Project actually
		// takes effect. Without this, a stale "{SVC}_IMAGE=oldregistry/…" kept compose
		// pulling the wrong image (e.g. a denied ghcr pull) even after going local.
		if rebased, changed := envgen.RebaseImageRegistry(envContent, registry, prefix); changed {
			if wErr := os.WriteFile(envPath, rebased, 0o600); wErr != nil {
				fmt.Fprintf(os.Stderr, "envgen: rebase .env for %s/%s: %v\n", workspaceName, envName, wErr)
			} else {
				envContent = rebased
			}
		}

		// Phase 6.5 finish: generate natively in Go — no shell, no fallback. On
		// error, log and skip this env (never write a partial compose file).
		content, err := composegen.GenerateRouted([]byte(configJSON), envName, composegen.RouteOpts{BaseDomain: baseDomain, AutoURLMode: settings.EffectiveAutoURLMode(h.db, workspaceName), AutoURLHost: settings.EffectiveAutoURLHost(h.db, workspaceName), KeepHostPortsUnderTraefik: settings.EffectiveKeepHostPortsUnderTraefik(h.db, workspaceName), Registry: registry, EnvFile: string(envContent)})
		if err != nil {
			fmt.Fprintf(os.Stderr, "composegen: failed for %s/%s: %v\n", workspaceName, envName, err)
			continue
		}
		outPath := filepath.Join(envDir, "docker-compose.yml")
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
		`SELECT project, username, command, env, created_at FROM audit_log
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
		 WHERE project = ? AND command NOT IN ('logs', 'ps')
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
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")

	// Resolve compose project name from config.json
	cfgData, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, wsName, name))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "unknown"})
		return
	}
	var cfg struct {
		Project struct {
			Name           string `json:"name"`
			ResourcePrefix string `json:"resource_prefix"`
		} `json:"project"`
		Environments map[string]struct {
			Deployment string `json:"deployment"`
		} `json:"environments"`
	}
	json.Unmarshal(cfgData, &cfg) //nolint:errcheck

	project := cfg.Project.ResourcePrefix
	if project == "" {
		project = cfg.Project.Name
	}
	project += "_" + env
	envDir := wspath.EnvDir(h.workspacesDir, wsName, name, env)

	// Query the daemon the env actually runs on (local, or its remote host).
	ex, err := h.bridge.ExecForEnv(wsName, name, env)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "unknown"})
		return
	}

	// Swarm envs run as a stack, not compose containers — query stack services.
	if cfg.Environments[env].Deployment == "swarm" {
		out, runErr := ex.DockerOutput(executor.Spec{
			Args: []string{"stack", "services", project, "--format", "json"},
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": parseStackServicesJSON(out, runErr)})
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

// parseReplicas parses a swarm "running/desired" replicas string (tolerating a
// trailing note like "1/1 (max 1 per node)").
func parseReplicas(s string) (run, desired int) {
	slash := strings.IndexByte(s, '/')
	if slash < 0 {
		return 0, 0
	}
	run, _ = strconv.Atoi(strings.TrimSpace(s[:slash]))
	rest := s[slash+1:]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	desired, _ = strconv.Atoi(rest[:j])
	return run, desired
}

// parseStackServicesJSON parses `docker stack services <stack> --format json`
// (NDJSON). Each service's Replicas is "running/desired"; the env is "running"
// when every service is fully replicated, "partial" when some are, else
// "stopped". An errored/empty result for a non-existent stack is "unknown".
func parseStackServicesJSON(out []byte, runErr error) string {
	if runErr != nil && len(bytes.TrimSpace(out)) == 0 {
		return "unknown"
	}
	total, running := 0, 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var s struct {
			Replicas string `json:"Replicas"`
		}
		if json.Unmarshal([]byte(line), &s) != nil {
			continue
		}
		total++
		if run, desired := parseReplicas(s.Replicas); desired > 0 && run == desired {
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

// isOneShotService reports whether a compose service is a synthesized run-once job
// (restart:"no") rather than a long-lived service: the MinIO bucket init ("…_init")
// or a pre-deploy migration gate ("…-migrate"). A clean (exit 0) one-shot is not a
// stopped service, so it must not drag an env's status to "partial".
func isOneShotService(name string) bool {
	return strings.HasSuffix(name, "_init") || strings.HasSuffix(name, "-migrate")
}

// parseComposePsJSON parses docker compose ps --format json (NDJSON) output.
// Each line is a JSON object with at least a "State" field.
func parseComposePsJSON(out []byte, runErr error) string {
	if runErr != nil && len(bytes.TrimSpace(out)) == 0 {
		return "unknown"
	}

	type psRow struct {
		Service  string `json:"Service"`
		State    string `json:"State"`
		Status   string `json:"Status"`
		Health   string `json:"Health"`
		ExitCode int    `json:"ExitCode"`
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
		state := strings.ToLower(row.State + " " + row.Status)
		health := strings.ToLower(row.Health)
		// One-shot synthesized jobs run once and exit 0 — that's success, not a stopped
		// service. Skip them from the tally so a completed job doesn't drag the env to
		// "partial" and disable the URL. A still-running or non-zero-exit job is NOT
		// skipped, so a stuck/failed one correctly surfaces as partial.
		if isOneShotService(row.Service) && strings.Contains(state, "exit") && row.ExitCode == 0 {
			continue
		}
		total++
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
//
//	Up 2 hours, Up (healthy), Exited (0), Exit 1, Created, Restarting
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

// resourcePrefix returns a project's immutable Docker/DB key — its
// resource_prefix from config.json. This is NOT always {workspace}_{name}: a
// transferred project keeps its original prefix, so project-scoped DB rows
// (metrics, secrets, action runs, backup schedules) MUST be keyed by this value,
// never by the current folder path. Falls back to {workspace}_{name} when the
// config can't be read (e.g. a brand-new project).
func (h *Handler) resourcePrefix(wsName, name string) string {
	if data, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, wsName, name)); err == nil {
		var cfg struct {
			Project struct {
				ResourcePrefix string `json:"resource_prefix"`
			} `json:"project"`
		}
		if json.Unmarshal(data, &cfg) == nil && cfg.Project.ResourcePrefix != "" {
			return cfg.Project.ResourcePrefix
		}
	}
	return wsName + "_" + name
}

func (h *Handler) GetWorkspace(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	ws, err := workspace.Get(h.workspacesDir, wsName, name, settings.EffectiveBaseDomain(h.db, wsName), settings.EffectiveAutoURLMode(h.db, wsName), settings.EffectiveAutoURLHost(h.db, wsName))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	// Refine env order with the workspace's configured tier names (workspace.Get
	// used DefaultTiers); explicit project order still wins inside Resolve.
	if wsSettings, _ := settings.GetWorkspaceSettings(h.db, wsName); wsSettings != nil {
		tiers := envorder.SplitTierNames(wsSettings["env_tier_names"])
		ws.Envs = envorder.Resolve(ws.Envs, ws.Config.Project.EnvOrder, tiers)
	}
	wss := []workspace.Workspace{ws}
	h.annotateHosts(wss)
	h.refineRemoteEnvURLs(wss)    // remote envs: show their own host in the route URL
	h.applyPrimaryDomainURLs(wss) // env card shows the ★ canonical custom domain
	out := wss[0]
	// Surface the host-side folder path so the UI can show where the workspace
	// actually lives (not the container's /toolkit path). Prefer the value stamped
	// into config at creation; fall back to resolving the bind-mount source for
	// workspaces created before project_root_dir existed.
	if rd := strings.TrimSpace(out.Config.Project.ProjectRootDir); rd != "" {
		out.HostPath = rd
	} else if hwd := h.hostBindSourceDir(); hwd != "" {
		out.HostPath = strings.TrimRight(hwd, "/\\") + "/" + wsName + "/projects/" + name
	}
	// Caller's effective role for this project (Phase 5.2b) — drives UI gating.
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		out.MyRole = h.auth.EffectiveRole(claims.UserID, claims.Role, wsName, name)
	}
	// Configured host/IP for direct service links — used when an env runs on the
	// local host and the dashboard is reached via a proxy domain (so the browser's
	// hostname is the proxy, not the Docker host).
	out.AppHost = h.appSetting("app_host")
	// Auto-URL context so Edit Project's route preview matches the deploy.
	out.AutoURLMode = settings.EffectiveAutoURLMode(h.db, wsName)
	out.AppsBaseDomain = settings.AppSetting(h.db, "apps_base_domain")
	out.AppsAcmeEmail = settings.AppSetting(h.db, "acme_email")
	writeJSON(w, http.StatusOK, out)
}

// GET /api/workspaces/{name}/envs/{env}/vars  — returns env vars with secret
// flags (secret values masked by default, ?reveal=true for plaintext)
func (h *Handler) GetEnvVars(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")
	reveal := r.URL.Query().Get("reveal") == "true"
	vars, err := workspace.EnvVars(h.workspacesDir, wsName, name, env, reveal)
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
				h.recordSecretEvent(wsName+"_"+name, env, k, "read", claims, ip)
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
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")
	pkey := h.resourcePrefix(wsName, name)
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

	ws, err := workspace.Get(h.workspacesDir, wsName, name, settings.EffectiveBaseDomain(h.db, wsName), settings.EffectiveAutoURLMode(h.db, wsName), settings.EffectiveAutoURLHost(h.db, wsName))
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
		current, _ := workspace.EnvVars(h.workspacesDir, wsName, name, env, true)

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
			if _, serr := h.bridge.EnsureSwarmSecret(wsName, name, env, k, val, ver); serr != nil {
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
					h.bridge.RemoveSwarmSecret(wsName, name, env, k, v) //nolint:errcheck
				}
				delete(versions, k)
			}
		}
		// Deleted keys that were secrets: remove the Docker secret too.
		for _, k := range body.Deletes {
			if prevSecret[k] {
				if v := versions[k]; v > 0 {
					h.bridge.RemoveSwarmSecret(wsName, name, env, k, v) //nolint:errcheck
				}
				delete(versions, k)
			}
		}
	}

	if err := workspace.UpdateEnvVars(h.workspacesDir, wsName, name, env, body.Updates, body.Deletes, skipEnvFile); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Mirror the edits into config.json's env_vars so they survive the next
	// refresh/redeploy (which regenerates .env FROM config.json). Without this, an
	// env edit written only to .env reverts on refresh — and a bundled DB whose
	// password regenerates that way stops matching its initialised data volume.
	// Best-effort: .env is already saved, so a failure here doesn't block the action.
	if cerr := workspace.UpdateConfigEnvVars(h.workspacesDir, wsName, name, env, body.Updates, body.Deletes, skipEnvFile); cerr != nil && warn == "" {
		warn = "saved .env but could not persist to config.json (edits may revert on refresh): " + cerr.Error()
	}
	if err := workspace.SetSecretMeta(h.workspacesDir, wsName, name, env, body.SecretKeys, versions); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "saved .env but failed to persist secret flags: " + err.Error()})
		return
	}
	// Regenerate this env's compose so the secrets wiring (or its removal) is
	// reflected immediately, without waiting for a Refresh.
	if cfgData, rerr := os.ReadFile(wspath.ConfigPath(h.workspacesDir, wsName, name)); rerr == nil {
		envDir := wspath.EnvDir(h.workspacesDir, wsName, name, env)
		envContent, _ := os.ReadFile(filepath.Join(envDir, ".env"))
		reg := ""
		if cfg, perr := wsconfig.Parse(cfgData); perr == nil {
			reg = settings.EffectiveRegistry(h.db, wsName, cfg.Project.Registry)
		}
		ro := composegen.RouteOpts{BaseDomain: settings.EffectiveBaseDomain(h.db, wsName), AutoURLMode: settings.EffectiveAutoURLMode(h.db, wsName), AutoURLHost: settings.EffectiveAutoURLHost(h.db, wsName), Registry: reg, EnvFile: string(envContent)}
		if content, gerr := composegen.GenerateRouted(cfgData, env, ro); gerr == nil {
			outPath := filepath.Join(envDir, "docker-compose.yml")
			os.WriteFile(outPath, content, 0o644) //nolint:errcheck
		}
	}

	// Audit secret writes (newly-flagged or value-changed) and deletes — for both
	// compose and swarm. Key names only, never values.
	for k := range newSecret {
		if _, provided := body.Updates[k]; provided || !prevSecret[k] {
			h.recordSecretEvent(pkey, env, k, "write", claims, ip)
		}
	}
	for _, k := range body.Deletes {
		if prevSecret[k] {
			h.recordSecretEvent(pkey, env, k, "delete", claims, ip)
		}
	}

	// If this env runs on a remote host, push the updated .env to it — the local
	// file is just a cache; the host's copy is what `docker compose` actually
	// reads. This is the one explicit override of the host-authoritative .env.
	pushed, hostName, err := h.bridge.PushEnvFile(wsName, name, env)
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
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, pkey, "env-update", env,
		)
	}
	resp := map[string]string{"status": "ok", "pushed_to_host": pushedTo}
	if warn != "" {
		resp["warning"] = warn
	}
	writeJSON(w, http.StatusOK, resp)
}

// DELETE /api/workspaces/{ws}/projects/{name} — permanently removes a project directory
func (h *Handler) DeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\..") || wsName == "" || strings.ContainsAny(wsName, "/\\..") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project name"})
		return
	}

	wsPath := wspath.ProjectDir(h.workspacesDir, wsName, name)
	if _, err := os.Stat(wsPath); os.IsNotExist(err) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}

	// Capture the resource prefix BEFORE removing the dir — it's read from
	// config.json (a transferred project keeps its original prefix) and is the key
	// for project-scoped DB rows we purge below.
	prefix := h.resourcePrefix(wsName, name)

	// Tear down each env's stack first so containers/networks/volumes aren't orphaned.
	// purge volumes: a permanent project delete must also drop its data volumes, else a
	// later same-key project reuses the stale DB volume and hits password drift (P1000).
	h.teardownProjectStacks(wsName, name, true)

	if err := os.RemoveAll(wsPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete project: " + err.Error()})
		return
	}

	// Purge project-scoped DB rows so a future project reusing the same keys/prefix
	// doesn't inherit stale action output, pipelines, history, alerts, metrics, etc.
	h.purgeProjectData(prefix, wsName, name)

	claims := auth.ClaimsFromContext(r.Context())
	if claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, wsName+"_"+name, "delete", "",
		)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// purgeProjectData removes all project-scoped DB rows on project deletion so a
// future project reusing the same workspace/project keys (and resource prefix)
// doesn't inherit the deleted project's action output, pipelines, deploy history,
// alerts, metrics, host bindings, etc. Best-effort: each delete ignores errors and
// never blocks deletion. `prefix` MUST be captured before the project dir is removed.
//
// Intentionally NOT purged: audit_log (security record, retained) and
// migration_leftovers (tracks data still physically present on a source host).
func (h *Handler) purgeProjectData(prefix, wsName, name string) {
	// Tables keyed by the single resource-prefix `project` column.
	for _, tbl := range []string{
		"action_runs", "alert_rules", "alert_events",
		"backup_log", "backup_syncs", "backup_schedule_runs",
		"secret_events", "metrics_snapshots",
		"workspace_hosts", "workspace_host_envs", "project_build_hosts",
	} {
		h.db.Exec("DELETE FROM "+tbl+" WHERE project=?", prefix) //nolint:errcheck
	}
	// Tables keyed by separate (workspace, project=key) columns.
	for _, tbl := range []string{
		"pipelines", "pipeline_runs", "pipeline_webhooks", "deploy_history",
		"managed_db_users", "api_key_projects",
		"preview_webhooks", "preview_environments", "preview_writeback_tokens",
		"acme_certs", "custom_domains",
	} {
		h.db.Exec("DELETE FROM "+tbl+" WHERE workspace=? AND project=?", wsName, name) //nolint:errcheck
	}
}

// POST /api/workspaces/{name}/action  — runs a run.sh command, streams output via WebSocket
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // CORS handled at server level
}

type actionRequest struct {
	Command  string   `json:"command"`
	Env      string   `json:"env"`
	Extra    []string `json:"extra"`
	Services []string `json:"services"` // manual backup: services to include (empty = all)
}

// WS /api/workspaces/{name}/action
func (h *Handler) RunAction(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	pkey := h.resourcePrefix(wsName, name)

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

	// Enforce RBAC (the WS route bypasses the HTTP gate): read-only streams need
	// viewer; everything else (deploy/restart/stop/backup/…) needs developer+.
	eff := h.auth.EffectiveRole(claims.UserID, claims.Role, wsName, name)
	minRole := auth.RoleDeveloper
	if req.Command == "logs" || req.Command == "ps" {
		minRole = auth.RoleViewer
	}
	if eff == "" || !auth.AtLeast(eff, minRole) {
		conn.WriteMessage(websocket.TextMessage, []byte("\033[31m✗ Error: you don't have permission to run this action on "+name+"\033[0m\n")) //nolint:errcheck
		return
	}

	// Audit log — skip read-only/streaming commands that aren't meaningful as activity
	if req.Command != "logs" && req.Command != "ps" {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, pkey, req.Command, req.Env,
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

	runOpts := shell.RunOptions{
		Workspace: wsName,
		Project:   name,
		Command:   req.Command,
		Env:       req.Env,
		Extra:     req.Extra,
		Stdout:    pw,
		Stderr:    pw, // merged: errors appear inline with output, not silently dropped
	}
	if req.Command == "backup" {
		runOpts.Services = req.Services
		runOpts.ScheduleID = "manual"
		runOpts.ScheduleName = "Manual backup"
		runOpts.Trigger = "manual"
	}
	runErr := h.bridge.Run(runOpts)
	pw.Close()
	<-done // ensure all streamed output is captured before recording

	var marker string
	if runErr != nil {
		marker = "\n\033[31m✗ " + req.Command + " failed: " + runErr.Error() + "\033[0m\n"
	} else {
		marker = "\n\033[32m✓ " + req.Command + " " + req.Env + " completed successfully.\033[0m\n"
		// Record image-changing deploys for rollback (Phase 9e).
		if (req.Command == "start" || req.Command == "update") && req.Env != "" {
			h.recordDeploy(wsName, name, req.Env, claims.Username)
		}
		// After a successful update, invalidate the image-check cache so the next
		// frontend poll triggers a fresh check against the newly pulled image digests.
		if req.Command == "update" && req.Env != "" {
			h.imgCache.Invalidate(wsName, name, req.Env)
			go func() {
				results := imagecheck.Check(h.workspacesDir, wsName, name, req.Env)
				if results != nil {
					h.imgCache.Set(wsName, name, req.Env, results)
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
			Workspace: pkey, Env: req.Env, Command: req.Command,
			Extra: strings.Join(req.Extra, " "), Username: claims.Username,
			Status: status, Output: outBuf.String(),
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
		alerts.LogBackup(h.db, pkey, req.Env, status, msg, 0) //nolint:errcheck
	}
}

// GET /api/workspaces/{name}/action-runs?limit=N — recorded Action-output
// history for a workspace (newest first).
func (h *Handler) GetActionRuns(w http.ResponseWriter, r *http.Request) {
	pkey := r.PathValue("workspace") + "_" + r.PathValue("name")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	runs, err := actionruns.List(h.db, pkey, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// DELETE /api/workspaces/{name}/action-runs — clear recorded history for a workspace.
func (h *Handler) ClearActionRuns(w http.ResponseWriter, r *http.Request) {
	pkey := r.PathValue("workspace") + "_" + r.PathValue("name")
	if err := actionruns.Clear(h.db, pkey); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

// GET /api/stats — dashboard stats (docker info + host metrics + workspace summary)
func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	s := h.bridge.Stats()
	// Phase 5.2b: non-admins only see their workspaces in the per-workspace summary.
	if set, all := h.visibleWorkspaceSet(r); !all {
		filtered := s.Workspaces.Workspaces[:0]
		byType := map[string]int{}
		for _, ws := range s.Workspaces.Workspaces {
			if set[ws.Workspace] {
				filtered = append(filtered, ws)
				byType[ws.Type]++
			}
		}
		s.Workspaces.Workspaces = filtered
		s.Workspaces.Total = len(filtered)
		s.Workspaces.ByType = byType
	}
	writeJSON(w, http.StatusOK, s)
}

// GET /api/live-stats — cheap per-project live stats (cpu/mem/net/running/services)
// for the near-real-time dashboard table. No disk du / docker info, so it's safe
// to poll every few seconds. Keyed by compose project name ({workspace}_{env}).
func (h *Handler) GetLiveStats(w http.ResponseWriter, r *http.Request) {
	live := h.bridge.LiveStats()
	// Keyed by compose project name "{wsKey}_{projKey}_{env}"; drop other workspaces'.
	if set, all := h.visibleWorkspaceSet(r); !all {
		for k := range live {
			if !set[strings.SplitN(k, "_", 2)[0]] {
				delete(live, k)
			}
		}
	}
	writeJSON(w, http.StatusOK, live)
}

// GET /api/workspaces/{name}/envs/{env}/containers — lists containers via docker compose ps
func (h *Handler) GetContainers(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")

	cfgPath := wspath.ConfigPath(h.workspacesDir, wsName, name)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	var cfg struct {
		Project struct {
			Name           string `json:"name"`
			ResourcePrefix string `json:"resource_prefix"`
		} `json:"project"`
		Environments map[string]struct {
			Deployment string `json:"deployment"`
		} `json:"environments"`
	}
	json.Unmarshal(data, &cfg) //nolint:errcheck
	project := cfg.Project.ResourcePrefix
	if project == "" {
		project = cfg.Project.Name
	}
	project += "_" + env

	envDir := wspath.EnvDir(h.workspacesDir, wsName, name, env)

	// Query the daemon the env actually runs on (local, or its remote host).
	ex, exErr := h.bridge.ExecForEnv(wsName, name, env)
	if exErr != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	type Container struct {
		Name    string `json:"Name"`
		Service string `json:"Service"`
		State   string `json:"State"`
		Status  string `json:"Status"`
		Health  string `json:"Health"` // healthy | unhealthy | starting | "" (no healthcheck)
	}

	// Swarm envs run as a stack — map `docker stack services` to the same shape.
	if cfg.Environments[env].Deployment == "swarm" {
		out, _ := ex.DockerOutput(executor.Spec{
			Args: []string{"stack", "services", project, "--format", "json"},
		})
		var containers []Container
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" || line[0] != '{' {
				continue
			}
			var s struct{ Name, Replicas string }
			if json.Unmarshal([]byte(line), &s) != nil {
				continue
			}
			state := "exited"
			if run, desired := parseReplicas(s.Replicas); desired > 0 && run == desired {
				state = "running"
			}
			containers = append(containers, Container{
				Name:    s.Name,
				Service: strings.TrimPrefix(s.Name, project+"_"),
				State:   state,
				Status:  "replicas " + s.Replicas,
			})
		}
		if containers == nil {
			containers = []Container{}
		}
		writeJSON(w, http.StatusOK, containers)
		return
	}

	// --all: include exited/stopped containers so the health panel shows their actual state
	out, err := ex.DockerOutput(executor.Spec{
		Args: []string{"compose", "-p", project, "-f", "docker-compose.yml", "ps", "--all", "--format", "json"},
		Dir:  envDir,
	})

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
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")

	type response struct {
		Updates   []imagecheck.ServiceUpdate `json:"updates"`
		CheckedAt *time.Time                 `json:"checked_at,omitempty"`
		Pending   bool                       `json:"pending"` // true if no cache entry yet
	}

	entry, ok := h.imgCache.Get(wsName, name, env)
	if !ok {
		// Trigger an async check so the next poll will have results
		go func() {
			results := imagecheck.Check(h.workspacesDir, wsName, name, env)
			if results != nil {
				h.imgCache.Set(wsName, name, env, results)
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
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")

	cfgPath := wspath.ConfigPath(h.workspacesDir, wsName, name)
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
		if vars, err := workspace.EnvVars(h.workspacesDir, wsName, name, env, true); err == nil {
			for k, v := range vars {
				if v.Secret || isSecretEnvKey(k) {
					defaultEnvVars[k] = "CHANGE_ME"
				} else {
					defaultEnvVars[k] = v.Value
				}
			}
		}
	}

	// Draft template: name/label/description/tags/categories/website are left for the
	// user to fill in (validated & saved via the Template Manager). images carry over as-is.
	draft := map[string]any{
		"name":             "",
		"label":            "",
		"description":      "",
		"tags":             []string{},
		"categories":       []string{},
		"website":          "",
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
	if err := readJSON(r, &body); err != nil || body.Current == "" || body.New == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "current and new password are required"})
		return
	}
	if err := h.auth.ChangePassword(claims.UserID, body.Current, body.New); err != nil {
		// Distinguish a wrong current password (401) from a policy rejection (400) so
		// the user sees the actual reason instead of a misleading "incorrect password".
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "current password is incorrect"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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
		Project   string       `json:"project"`
		Env       string       `json:"env"`
		Date      string       `json:"date"`
		SizeBytes int64        `json:"size_bytes"`
		Files     []BackupFile `json:"files"`
		Sync      *syncState   `json:"sync,omitempty"`     // 11d: remote-sync state, if any
		Services  []string     `json:"services,omitempty"` // from manifest (empty = all)
		Trigger   string       `json:"trigger,omitempty"`  // scheduled | manual
		Schedule  string       `json:"schedule,omitempty"` // schedule name
	}

	var results []BackupSnapshot
	wsFilter := r.URL.Query().Get("workspace")
	syncStates := h.backupSyncStates()

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
		if wsFilter != "" && wsName != wsFilter {
			continue
		}
		projEntries, perr := os.ReadDir(wspath.ProjectsDir(h.workspacesDir, wsName))
		if perr != nil {
			continue
		}
		for _, projEntry := range projEntries {
			if !projEntry.IsDir() {
				continue
			}
			projName := projEntry.Name()
			pkey := h.resourcePrefix(wsName, projName)
			backupsRoot := wspath.BackupsDir(h.workspacesDir, wsName, projName)

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
						if f.IsDir() || f.Name() == snapshotManifestFile {
							continue // manifest is metadata, not a backup artifact
						}
						info, _ := f.Info()
						size := int64(0)
						if info != nil {
							size = info.Size()
						}
						totalSize += size
						bfiles = append(bfiles, BackupFile{Name: f.Name(), Size: size})
					}
					snapshot := BackupSnapshot{
						Workspace: wsName,
						Project:   projName,
						Env:       envName,
						Date:      snap.Name(),
						SizeBytes: totalSize,
						Files:     bfiles,
					}
					if m := readSnapshotManifest(snapDir); m != nil {
						snapshot.Services = m.Services
						snapshot.Trigger = m.Trigger
						snapshot.Schedule = m.ScheduleName
					}
					if st, ok := syncStates[pkey+"\x00"+envName+"\x00"+snap.Name()]; ok {
						s := st
						snapshot.Sync = &s
					}
					results = append(results, snapshot)
				}
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
	wsName := r.PathValue("workspace")
	project := r.PathValue("name")
	env := r.PathValue("env")
	date := r.PathValue("date")

	if wsName == "" || project == "" || env == "" || date == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace, project, env and date are required"})
		return
	}

	// Validate date looks like a snapshot name (YYYY-MM-DD_HH-MM-SS) to prevent path traversal
	if len(date) != 19 || date[4] != '-' || date[7] != '-' || date[10] != '_' {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid snapshot date format"})
		return
	}

	backupsRoot := wspath.BackupsDir(h.workspacesDir, wsName, project)
	snapDir := filepath.Join(backupsRoot, env, date)
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
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")

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

	claims, err := h.auth.ValidateToken(init.Token)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("\r\nerror: unauthorized\r\n")) //nolint:errcheck
		return
	}
	// Opening a shell is a privileged action — require developer+ on the project.
	if eff := h.auth.EffectiveRole(claims.UserID, claims.Role, wsName, name); eff == "" || !auth.AtLeast(eff, auth.RoleDeveloper) {
		conn.WriteMessage(websocket.TextMessage, []byte("\r\nerror: you don't have permission to open a terminal here\r\n")) //nolint:errcheck
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
	if cols <= 0 {
		cols = 220
	}
	if rows <= 0 {
		rows = 50
	}

	// Resolve the docker exec target. Compose: the service name doubles as the
	// prefixed container_name. Swarm: there's no fixed container name, so resolve
	// the running task's container ID (on the env's own daemon).
	ex, err := h.bridge.ExecForEnv(wsName, name, env)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("\r\nerror: "+err.Error()+"\r\n")) //nolint:errcheck
		return
	}
	target, rerr := h.resolveContainerRef(ex, wsName, name, env, init.Service)
	if rerr != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("\r\nerror: "+rerr.Error()+"\r\n")) //nolint:errcheck
		return
	}

	// Open the PTY on the env's own daemon (local socket or remote SSH).
	de, err := h.bridge.OpenTerminal(wsName, name, env, target, cols, rows)
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
