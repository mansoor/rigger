package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/envgen"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Sentinel errors from cloneEnv so HTTP callers can map them to status codes
// (the preview controller treats them as ordinary errors).
var (
	errCloneSrcNotFound = errors.New("source environment not found")
	errCloneDstExists   = errors.New("environment already exists")
)

// copyEnvNameRe validates a new environment name: lowercase letter first, then
// lowercase letters / digits / hyphens. Keeps it a safe single path segment.
var copyEnvNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,29}$`)

// copyEnvSecretKey mirrors envgen.isSecretKey (unexported there) so we can reset
// secret-flagged env vars to a placeholder and have bootstrap regenerate them.
func copyEnvSecretKey(key string) bool {
	ku := strings.ToUpper(key)
	if strings.Contains(ku, "PASSWORD") || strings.Contains(ku, "PASSWD") ||
		strings.Contains(ku, "SECRET") || strings.Contains(ku, "TOKEN") ||
		strings.Contains(ku, "SALT") {
		return true
	}
	return strings.Contains(ku, "KEY") && !strings.Contains(ku, "_ID")
}

// CopyEnvironment clones an existing environment into a new one (Phase 1: config +
// regenerated .env/compose + bind-mount config files; FRESH empty volumes). It is
// strictly additive — the source environment is never modified. The new env's
// domain is blanked (two envs can't claim the same host/URL; a Traefik env then
// auto-routes to a unique {prefix}-{env} host). Managed-dependency secrets are
// generated fresh for the new env automatically (new env dir ⇒ no existing .env);
// with regenerate_secrets, secret-flagged app env vars are reset so they too
// regenerate. Volume DATA is NOT copied (Phase 2).
//
// POST /api/workspaces/{workspace}/projects/{name}/envs/{env}/copy
func (h *Handler) CopyEnvironment(w http.ResponseWriter, r *http.Request) {
	ws, name, src := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var body struct {
		NewEnv            string `json:"new_env"`
		RegenerateSecrets bool   `json:"regenerate_secrets"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	newEnv := strings.TrimSpace(body.NewEnv)
	if !copyEnvNameRe.MatchString(newEnv) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "new environment name must start with a letter and contain only lowercase letters, digits and hyphens"})
		return
	}
	if newEnv == src {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "new environment name must differ from the source"})
		return
	}

	if err := h.cloneEnv(ws, name, src, newEnv, body.RegenerateSecrets, ""); err != nil {
		switch {
		case errors.Is(err, errCloneSrcNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, errCloneDstExists):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, h.resourcePrefix(ws, name), "copy-env:from="+src, newEnv)
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "env": newEnv})
}

// cloneEnv clones src env → dst within a project's config.json and bootstraps the
// new env. It is additive — src is never modified. The new env's domain is blanked
// (so a Traefik env auto-routes to a unique {prefix}-{env} host); when branch != ""
// the per-env git override (git.branch) is set so build pulls that ref; with
// regenSecrets, secret-flagged app env vars are reset to a placeholder so bootstrap
// regenerates them (previews never inherit prod secrets). Source-env bind-mount
// config files are copied best-effort. Returns errCloneSrcNotFound / errCloneDstExists
// so HTTP callers can map status codes; the preview controller uses it directly.
func (h *Handler) cloneEnv(ws, name, src, dst string, regenSecrets bool, branch string) error {
	cfgPath := wspath.ConfigPath(h.workspacesDir, ws, name)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("project not found: %w", err)
	}
	// Edit config.json as raw JSON so every field on the OTHER envs (and the new
	// one) is preserved verbatim — round-tripping through a typed struct would drop
	// fields the lightweight model doesn't know about.
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("config parse: %w", err)
	}
	var envs map[string]json.RawMessage
	if len(root["environments"]) > 0 {
		json.Unmarshal(root["environments"], &envs) //nolint:errcheck
	}
	if envs == nil {
		envs = map[string]json.RawMessage{}
	}
	srcRaw, ok := envs[src]
	if !ok {
		return fmt.Errorf("%w: %q", errCloneSrcNotFound, src)
	}
	if _, exists := envs[dst]; exists {
		return fmt.Errorf("%w: %q", errCloneDstExists, dst)
	}

	var envMap map[string]any
	if err := json.Unmarshal(srcRaw, &envMap); err != nil {
		return fmt.Errorf("env parse: %w", err)
	}
	envMap["domain"] = ""
	if branch != "" {
		envMap["git"] = map[string]any{"branch": branch}
	}
	if regenSecrets {
		if vars, ok := envMap["env_vars"].(map[string]any); ok {
			for k, v := range vars {
				if s, ok := v.(string); ok && copyEnvSecretKey(k) && !envgen.IsPlaceholder(s) {
					vars[k] = "CHANGE_ME"
				}
			}
		}
	}
	cloned, err := json.Marshal(envMap)
	if err != nil {
		return fmt.Errorf("env marshal: %w", err)
	}
	envs[dst] = cloned

	envsOut, err := json.Marshal(envs)
	if err != nil {
		return fmt.Errorf("environments marshal: %w", err)
	}
	root["environments"] = envsOut
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("config marshal: %w", err)
	}
	if err := os.WriteFile(cfgPath, out, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Copy bind-mount config files from the source env dir (e.g. Caddyfile,
	// mosquitto/) so the new env can deploy. Best-effort: bootstrap regenerates the
	// essentials regardless.
	srcDir := wspath.EnvDir(h.workspacesDir, ws, name, src)
	dstDir := wspath.EnvDir(h.workspacesDir, ws, name, dst)
	if cerr := copyEnvConfigFiles(srcDir, dstDir); cerr != nil {
		fmt.Fprintf(os.Stderr, "cloneEnv: copy config files %s→%s: %v\n", src, dst, cerr)
	}

	// Bootstrap the new env: fresh .env (fresh managed-dep secrets), compose, and any
	// generated config (nginx/adminer/etc.) for the new env name.
	var bout bytes.Buffer
	if berr := h.bridge.Bootstrap(ws, name, dst, &bout, &bout); berr != nil {
		return fmt.Errorf("bootstrap %s: %v\n%s", dst, berr, bout.String())
	}
	return nil
}

// copyEnvConfigFiles copies the source env dir into the new env dir, skipping the
// regenerated essentials (.env/.env.example/docker-compose.yml) and the ephemeral
// source checkout (_src, re-cloned at build time).
func copyEnvConfigFiles(srcDir, dstDir string) error {
	info, err := os.Stat(srcDir)
	if err != nil || !info.IsDir() {
		return err
	}
	skipTop := map[string]bool{
		".env": true, ".env.example": true, "docker-compose.yml": true, "_src": true,
	}
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(srcDir, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return os.MkdirAll(dstDir, 0o755)
		}
		// Skip the regenerated/ephemeral top-level entries (and _src's whole subtree).
		top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if skipTop[top] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		dst := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		mode := fs.FileMode(0o644)
		if fi, ierr := d.Info(); ierr == nil {
			mode = fi.Mode()
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return copyFile(path, dst, mode) // shared helper in backup_handlers.go
	})
}
