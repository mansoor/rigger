package api

import (
	"bytes"
	"encoding/json"
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

	cfgPath := wspath.ConfigPath(h.workspacesDir, ws, name)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}

	// Edit config.json as raw JSON so every field on the OTHER envs (and the new
	// one) is preserved verbatim — round-tripping through a typed struct would drop
	// fields the lightweight model doesn't know about.
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config parse: " + err.Error()})
		return
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
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("source environment %q not found", src)})
		return
	}
	if _, exists := envs[newEnv]; exists {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("environment %q already exists", newEnv)})
		return
	}

	// Clone the source block; blank the domain, and optionally reset secret-flagged
	// app env vars so bootstrap generates fresh values for them.
	var envMap map[string]any
	if err := json.Unmarshal(srcRaw, &envMap); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "env parse: " + err.Error()})
		return
	}
	envMap["domain"] = ""
	if body.RegenerateSecrets {
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "env marshal: " + err.Error()})
		return
	}
	envs[newEnv] = cloned

	envsOut, err := json.Marshal(envs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "environments marshal: " + err.Error()})
		return
	}
	root["environments"] = envsOut
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config marshal: " + err.Error()})
		return
	}
	if err := os.WriteFile(cfgPath, out, 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "write config: " + err.Error()})
		return
	}

	// Copy bind-mount config files from the source env dir (e.g. Caddyfile,
	// mosquitto/) so the new env can deploy. Best-effort: bootstrap regenerates the
	// essentials regardless.
	srcDir := wspath.EnvDir(h.workspacesDir, ws, name, src)
	dstDir := wspath.EnvDir(h.workspacesDir, ws, name, newEnv)
	if cerr := copyEnvConfigFiles(srcDir, dstDir); cerr != nil {
		fmt.Fprintf(os.Stderr, "CopyEnvironment: copy config files %s→%s: %v\n", src, newEnv, cerr)
	}

	// Bootstrap the new env: fresh .env (fresh managed-dep secrets), compose, and any
	// generated config (nginx/garage/adminer) for the new env name.
	var bout bytes.Buffer
	if berr := h.bridge.Bootstrap(ws, name, newEnv, &bout, &bout); berr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "bootstrap new env: " + berr.Error() + "\n" + bout.String()})
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, h.resourcePrefix(ws, name), "copy-env:from="+src, newEnv)
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "env": newEnv})
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
