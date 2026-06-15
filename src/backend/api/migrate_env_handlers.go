package api

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/shell"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// MigrateEnvData copies one environment's DATA into another within the same
// project (e.g. refresh staging from prod). It's a thin wrapper over the existing
// backup→restore engine: safety-backup the target, back up the source, restore the
// source snapshot into the target's resources. The source is never modified; the
// target's data is OVERWRITTEN (a typed confirm + a default safety backup guard it).
//
// POST /api/workspaces/{workspace}/projects/{name}/migrate-data
// body: { source_env, target_env, confirm, services?, skip_target_backup? }
// Returns a job id; poll GET /api/tools/backup-jobs/{id}.
func (h *Handler) MigrateEnvData(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var body struct {
		SourceEnv        string   `json:"source_env"`
		TargetEnv        string   `json:"target_env"`
		Confirm          string   `json:"confirm"`
		Services         []string `json:"services"`
		SkipTargetBackup bool     `json:"skip_target_backup"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	src := strings.TrimSpace(body.SourceEnv)
	tgt := strings.TrimSpace(body.TargetEnv)
	if src == "" || tgt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source_env and target_env are required"})
		return
	}
	if src == tgt {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source and target must be different environments"})
		return
	}
	// Typed confirmation must match the target env (it's being overwritten).
	if strings.TrimSpace(body.Confirm) != tgt {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type the target environment name to confirm"})
		return
	}

	// Both envs must exist in this project's config.
	data, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	envs := configEnvNames(data)
	if !envs[src] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source environment not found: " + src})
		return
	}
	if !envs[tgt] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target environment not found: " + tgt})
		return
	}

	job := h.jobs.create(ws, name)

	username := ""
	uid := int64(0)
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		username = claims.Username
		uid = claims.UserID
	}
	pkey := h.resourcePrefix(ws, name)

	go func() {
		var buf bytes.Buffer
		var mu sync.Mutex
		out := &lockedWriter{w: &buf, mu: &mu}
		err := h.bridge.Run(shell.RunOptions{
			Workspace:        ws,
			Project:          name,
			Command:          "migrate",
			Env:              tgt, // target — data written here
			SourceEnv:        src, // source — data read from here
			Services:         body.Services,
			SkipTargetBackup: body.SkipTargetBackup,
			Stdout:           out,
			Stderr:           out,
		})
		mu.Lock()
		log := buf.String()
		mu.Unlock()
		h.jobs.update(job.ID, func(j *BackupJob) {
			j.Log = log
			now := time.Now()
			j.DoneAt = &now
			if err != nil {
				j.Status = "failed"
				j.Error = err.Error()
			} else {
				j.Status = "completed"
			}
		})
		// Audit the outcome.
		status := "ok"
		if err != nil {
			status = "failed"
		}
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			uid, username, pkey, "migrate-data:from="+src+":"+status, tgt)
	}()

	writeJSON(w, http.StatusOK, map[string]any{"id": job.ID, "status": "running"})
}

// lockedWriter serializes writes to an underlying buffer from the bridge's
// stdout/stderr (same goroutine here, but kept safe for any fan-out).
type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
