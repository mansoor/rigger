package api

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/shell"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// PUT /api/workspaces/{key} — rename a workspace's display name. Body {"name"}.
func (h *Handler) RenameWorkspaceTier(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("workspace")
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := workspace.RenameWorkspace(h.workspacesDir, key, body.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec("INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)", //nolint:errcheck
			claims.UserID, claims.Username, key, "rename-workspace", "")
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "key": key, "name": strings.TrimSpace(body.Name)})
}

// teardownProjectStacks brings down every environment of a project (best-effort)
// so deleting it doesn't orphan running containers/networks/volumes.
//
// purgeVolumes=true also destroys each env's named data volumes (pg_data,
// minio_data, storage, …). Both current callers are PERMANENT deletes (single
// project + whole workspace tier) and pass true: without it the DB data volume
// survives the delete, and a later project reusing the same workspace/project
// keys — hence the same resource_prefix and volume names — reuses that stale
// volume. Postgres then finds an existing data dir, SKIPS init, and keeps the
// old password while the freshly generated .env carries a new one, so every
// networked connection fails scram auth (P1000 / 28P01). A future relocate/
// transfer caller that must KEEP data would pass false.
func (h *Handler) teardownProjectStacks(wsKey, projKey string, purgeVolumes bool) {
	raw, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, wsKey, projKey))
	if err != nil {
		return
	}
	for env := range configEnvNames(raw) {
		var out bytes.Buffer
		if derr := h.bridge.Run(shell.RunOptions{
			Workspace: wsKey, Project: projKey, Command: "down", Env: env,
			PurgeVolumes: purgeVolumes,
			Stdout:       &out, Stderr: &out,
		}); derr != nil {
			fmt.Fprintf(os.Stderr, "teardown %s/%s/%s: %v\n%s", wsKey, projKey, env, derr, out.String())
		}
	}
}

// TransferWorkspace moves projects from one workspace to another, then removes the
// (now-empty) source when all projects were transferred. Projects keep their
// resource_prefix (immutable Docker identity), so running containers are
// untouched — this is a folder relocation, not a redeploy.
//
// POST /api/workspaces/{key}/transfer  Body {"target":"destKey","projects":[...]}
// projects omitted/empty = transfer all.
func (h *Handler) TransferWorkspace(w http.ResponseWriter, r *http.Request) {
	src := r.PathValue("workspace")
	var body struct {
		Target   string   `json:"target"`
		Projects []string `json:"projects"`
	}
	if err := readJSON(r, &body); err != nil || body.Target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target workspace is required"})
		return
	}
	if body.Target == src {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source and target workspaces are the same"})
		return
	}
	// Target must exist.
	if _, err := os.Stat(wspath.WorkspaceMeta(h.workspacesDir, body.Target)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target workspace not found"})
		return
	}
	// Caller must also administer the target workspace (the gate already verified
	// the source). A super-admin satisfies this everywhere.
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil &&
		h.auth.EffectiveRole(claims.UserID, claims.Role, body.Target, "") != auth.RoleAdmin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you must be an admin of the target workspace"})
		return
	}

	all := workspace.ProjectKeys(h.workspacesDir, src)
	toMove := body.Projects
	transferringAll := len(toMove) == 0
	if transferringAll {
		toMove = all
	}

	// Host guard: block transfer of any project whose env runs on a remote host
	// that the target workspace can't see (exclusive to the source). With the
	// current all-global host model nothing is exclusive, so this never blocks;
	// it activates once hosts become workspace-scoped.
	if blocked := h.projectsBlockedByExclusiveHost(src, body.Target, toMove); len(blocked) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "some projects run on a remote host exclusive to this workspace — relocate those environments first",
			"blocked": blocked,
		})
		return
	}

	moved := map[string]string{} // oldKey -> newKey in target
	for _, pk := range toMove {
		newKey, err := workspace.MoveProject(h.workspacesDir, src, pk, body.Target)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": fmt.Sprintf("transfer %q failed: %v", pk, err), "moved": moved,
			})
			return
		}
		moved[pk] = newKey
	}

	// If everything moved, remove the now-empty source workspace.
	removedSource := false
	if transferringAll || len(workspace.ProjectKeys(h.workspacesDir, src)) == 0 {
		if err := workspace.DeleteWorkspace(h.workspacesDir, src); err == nil {
			removedSource = true
		}
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec("INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)", //nolint:errcheck
			claims.UserID, claims.Username, src, "transfer-workspace", body.Target)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "target": body.Target, "moved": moved, "source_removed": removedSource,
	})
}

// projectsBlockedByExclusiveHost returns the projects that cannot transfer because
// an environment runs on a remote host the target workspace cannot use — a host
// private to the source, or a global host not granted to the target. The user must
// repoint those environments (e.g. to Local) before transferring. A host shared
// with the target does not block.
func (h *Handler) projectsBlockedByExclusiveHost(src, target string, projects []string) []string {
	var blocked []string
	for _, pk := range projects {
		prefix := src + "_" + pk
		rows, err := h.db.Query(`SELECT DISTINCT host_id FROM workspace_host_envs WHERE project=? AND host_id<>0`, prefix)
		if err != nil {
			continue
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil { //nolint:errcheck
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			if inPool, _ := settings.HostInWorkspacePool(h.db, target, id); !inPool {
				blocked = append(blocked, pk)
				break
			}
		}
	}
	return blocked
}
