package api

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/shell"
	"github.com/mansoor/rigger/ui/internal/workspace"
)

// clientIP extracts the best-effort client IP for audit records.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	return r.RemoteAddr
}

// recordSecretEvent appends a row to the secret audit trail (Phase 8d). The
// value is never recorded — only the key name and action. Best-effort.
func (h *Handler) recordSecretEvent(workspaceName, env, key, action string, claims *auth.Claims, ip string) {
	username := ""
	if claims != nil {
		username = claims.Username
	}
	h.db.Exec( //nolint:errcheck
		"INSERT INTO secret_events (project, env, key, action, username, ip) VALUES (?,?,?,?,?,?)",
		workspaceName, env, key, action, username, ip,
	)
}

// POST /api/workspaces/{name}/envs/{env}/rotate  — body { key, new_value }.
// Replaces a secret's value without exposing the old one. For swarm a new
// immutable secret version is created, the stack is redeployed onto it, and the
// old version is removed; for compose the .env value is updated and affected
// services are restarted.
func (h *Handler) RotateSecret(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")
	pkey := h.resourcePrefix(wsName, name)
	var body struct {
		Key      string `json:"key"`
		NewValue string `json:"new_value"`
	}
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Key) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key and new_value required"})
		return
	}

	ws, err := workspace.Get(h.workspacesDir, wsName, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	envCfg := ws.Config.Environments[env]
	isSecret := false
	for _, k := range envCfg.SecretKeys {
		if k == body.Key {
			isSecret = true
			break
		}
	}
	if !isSecret {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key is not flagged as a secret"})
		return
	}

	claims := auth.ClaimsFromContext(r.Context())
	ip := clientIP(r)
	var out bytes.Buffer

	if envCfg.Deployment == "swarm" {
		versions := map[string]int{}
		for k, v := range envCfg.SecretVersions {
			versions[k] = v
		}
		oldVer := versions[body.Key]
		if oldVer < 1 {
			oldVer = 1
		}
		newVer := oldVer + 1
		// 1) create the new immutable version
		if _, serr := h.bridge.EnsureSwarmSecret(wsName, name, env, body.Key, body.NewValue, newVer); serr != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": serr.Error()})
			return
		}
		// 2) persist the version bump, then 3) redeploy onto the new secret name
		versions[body.Key] = newVer
		if err := workspace.SetSecretMeta(h.workspacesDir, wsName, name, env, envCfg.SecretKeys, versions); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if err := h.bridge.Run(shellRun(wsName, name, env, "refresh", &out)); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "rotated secret created but redeploy failed: " + err.Error() + "\n" + out.String()})
			return
		}
		// 4) drop the now-unreferenced old version (best-effort)
		h.bridge.RemoveSwarmSecret(wsName, name, env, body.Key, oldVer) //nolint:errcheck
	} else {
		// Compose: update the plaintext value and restart affected services.
		if err := workspace.UpdateEnvVars(h.workspacesDir, wsName, name, env, map[string]string{body.Key: body.NewValue}, nil, nil); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if _, _, perr := h.bridge.PushEnvFile(wsName, name, env); perr != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "saved but could not push to host: " + perr.Error()})
			return
		}
		if err := h.bridge.Run(shellRun(wsName, name, env, "restart", &out)); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "value updated but restart failed: " + err.Error() + "\n" + out.String()})
			return
		}
	}

	h.recordSecretEvent(pkey, env, body.Key, "rotate", claims, ip)
	if claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, pkey, "secret-rotate", env,
		)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /api/workspaces/{name}/envs/{env}/secret-events  — recent secret audit entries.
func (h *Handler) GetSecretEvents(w http.ResponseWriter, r *http.Request) {
	pkey := r.PathValue("workspace") + "_" + r.PathValue("name")
	env := r.PathValue("env")
	rows, err := h.db.Query(
		`SELECT key, action, username, ip, created_at FROM secret_events
		 WHERE project = ? AND env = ?
		 ORDER BY created_at DESC LIMIT 200`, pkey, env,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	type entry struct {
		Key       string `json:"key"`
		Action    string `json:"action"`
		Username  string `json:"username"`
		IP        string `json:"ip"`
		CreatedAt string `json:"created_at"`
	}
	out := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.Key, &e.Action, &e.Username, &e.IP, &e.CreatedAt); err == nil {
			out = append(out, e)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// shellRun builds a bridge RunOptions with both streams pointed at one buffer.
func shellRun(wsName, name, env, cmd string, out *bytes.Buffer) shell.RunOptions {
	return shell.RunOptions{Workspace: wsName, Project: name, Command: cmd, Env: env, Stdout: out, Stderr: out}
}

// seedEnvVars writes a new environment's initial vars during workspace creation,
// applying Phase 8 secret handling: for swarm envs, flagged values become Docker
// secrets (encrypted at rest) and are kept out of .env; for compose they stay
// plaintext. The env's secret_keys are already persisted to config.json by
// workspace.Create. A failed Docker-secret creation is returned as a soft warning
// and the value is left in .env as a fallback (never silently dropped).
func (h *Handler) seedEnvVars(wsName, name string, env workspace.EnvRequest, claims *auth.Claims, ip string) error {
	pkey := h.resourcePrefix(wsName, name)
	skip := map[string]bool{}
	versions := map[string]int{}
	var warn error
	if env.Deployment == "swarm" {
		for _, k := range env.SecretKeys {
			val, ok := env.Vars[k]
			if !ok || val == "" {
				continue
			}
			if _, err := h.bridge.EnsureSwarmSecret(wsName, name, env.Name, k, val, 1); err != nil {
				warn = err // keep the value in .env as a fallback
				continue
			}
			versions[k] = 1
			skip[k] = true
		}
	}
	if err := workspace.UpdateEnvVars(h.workspacesDir, wsName, name, env.Name, env.Vars, nil, skip); err != nil {
		return err
	}
	if len(versions) > 0 {
		if err := workspace.SetSecretMeta(h.workspacesDir, wsName, name, env.Name, env.SecretKeys, versions); err != nil {
			return err
		}
	}
	for _, k := range env.SecretKeys {
		if _, ok := env.Vars[k]; ok {
			h.recordSecretEvent(pkey, env.Name, k, "write", claims, ip)
		}
	}
	return warn
}
