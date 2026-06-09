package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/deployhistory"
	"github.com/mansoor/rigger/ui/internal/shell"
	"github.com/mansoor/rigger/ui/internal/workspace"
)

// Phase 9e — Rollback. Each deploy records the env's resolved image refs
// (deploy_history); a custom stack can be rolled back by pinning a prior entry's
// images in the env .env and redeploying. Image stacks have no per-env image
// override, so they roll back via the existing Backup restore instead.

// recordDeploy snapshots the env's current image refs after a successful deploy.
// Best-effort: failures are swallowed (history is non-critical).
func (h *Handler) recordDeploy(ws, project, env, username string) {
	e := deployhistory.Resolve(h.workspacesDir, ws, project, env)
	e.Username = username
	e.CreatedAt = time.Now().UnixMilli()
	deployhistory.Record(h.db, e) //nolint:errcheck
}

// GET /api/workspaces/{workspace}/projects/{name}/envs/{env}/deploy-history
func (h *Handler) ListDeployHistory(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	list, err := deployhistory.List(h.db, ws, name, env, 30)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// POST /api/workspaces/{workspace}/projects/{name}/envs/{env}/rollback {to_id}
// Custom stacks only: pin the target entry's images in .env and redeploy.
func (h *Handler) RollbackEnv(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var body struct {
		ToID int64 `json:"to_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	entry, err := deployhistory.Get(h.db, body.ToID)
	if err != nil || entry == nil || entry.Workspace != ws || entry.Project != name || entry.Env != env {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "deploy entry not found"})
		return
	}
	if entry.Ptype != "custom" || len(entry.Images) == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "image-stack environments roll back via Backup restore, not image rollback",
		})
		return
	}

	// Pin the prior images in the env .env via the compose override vars.
	updates := map[string]string{}
	for svc, ref := range entry.Images {
		updates[deployhistory.OverrideKey(svc)] = ref
	}
	if err := workspace.UpdateEnvVars(h.workspacesDir, ws, name, env, updates, nil, nil); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "pin images: " + err.Error()})
		return
	}

	pkey := h.resourcePrefix(ws, name)
	username := ""
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		username = claims.Username
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, pkey, "rollback", env,
		)
	}

	var out strings.Builder
	if err := h.bridge.Run(shell.RunOptions{
		Workspace: ws, Project: name, Command: "start", Env: env,
		Stdout: &out, Stderr: &out,
	}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "redeploy failed: " + err.Error() + "\n" + out.String()})
		return
	}
	// Record the rollback itself as a new deploy entry (keeps history linear).
	h.recordDeploy(ws, name, env, username+" (rollback)")
	writeJSON(w, http.StatusOK, map[string]string{"status": "rolled back", "version": entry.Version})
}
