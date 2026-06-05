package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/actionruns"
	"github.com/mansoor/rigger/ui/internal/alerts"
	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/imagecheck"
	"github.com/mansoor/rigger/ui/internal/shell"
)

// cappedWriter copies into buf until it reaches cap bytes, then drops the rest
// (it always reports a full write so it never breaks an io.MultiWriter).
type cappedWriter struct {
	buf *bytes.Buffer
	cap int
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.buf.Len() < c.cap {
		c.buf.Write(p)
	}
	return len(p), nil
}

// flushWriter flushes the HTTP response after every write so a client (the rigger
// CLI via `curl -N`) sees command output stream in real time.
type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

// ActionHTTP runs a workspace command and streams its output as a chunked plain
// text response. This is the REST counterpart of the WebSocket RunAction, used
// by the thin host-side `rigger` CLI wrapper (Phase 6.5d).
//
// POST /api/workspaces/{name}/envs/{env}/action   body: {"command":"start","extra":[]}
func (h *Handler) ActionHTTP(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env := r.PathValue("env")

	var body struct {
		Command string   `json:"command"`
		Extra   []string `json:"extra"`
	}
	if err := readJSON(r, &body); err != nil || body.Command == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command is required"})
		return
	}

	// Audit (skip read-only/streaming commands, matching the WS handler).
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil &&
		body.Command != "logs" && body.Command != "ps" {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, workspace, command, env, host) VALUES (?,?,?,?,?,?)",
			claims.UserID, claims.Username, name, body.Command, env, h.envHostName(name, env),
		)
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	fw := &flushWriter{w: w, f: flusher}

	// Tee a bounded copy of the streamed output for the recorded history.
	startedAt := time.Now()
	var outBuf bytes.Buffer
	out := io.MultiWriter(fw, &cappedWriter{buf: &outBuf, cap: 128 * 1024})

	runErr := h.bridge.Run(shell.RunOptions{
		Workspace: name,
		Command:   body.Command,
		Env:       env,
		Extra:     body.Extra,
		Stdout:    out,
		Stderr:    out,
	})

	var marker string
	if runErr != nil {
		marker = fmt.Sprintf("\n\033[31m✗ %s failed: %s\033[0m\n", body.Command, runErr.Error())
	} else {
		marker = fmt.Sprintf("\n\033[32m✓ %s %s completed successfully.\033[0m\n", body.Command, env)
		if body.Command == "update" && env != "" {
			h.imgCache.Invalidate(name, env)
			go func() {
				if res := imagecheck.Check(h.workspacesDir, name, env); res != nil {
					h.imgCache.Set(name, env, res)
				}
			}()
		}
	}
	fmt.Fprint(fw, marker)
	outBuf.WriteString(marker)

	// Record in the per-workspace Action-output history (skip read-only streaming).
	if body.Command != "logs" && body.Command != "ps" {
		status := "ok"
		if runErr != nil {
			status = "fail"
		}
		uname := ""
		if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
			uname = claims.Username
		}
		actionruns.Record(h.db, actionruns.Run{ //nolint:errcheck
			Workspace: name, Env: env, Command: body.Command,
			Extra:     strings.Join(body.Extra, " "), Username: uname,
			Status:    status, Output: outBuf.String(),
			StartedAt: startedAt.UnixMilli(), FinishedAt: time.Now().UnixMilli(),
		})
	}

	// Record backup outcomes (same as the WS handler) so backup_failed alerts work.
	if body.Command == "backup" && env != "" {
		status, msg := "ok", ""
		if runErr != nil {
			status, msg = "error", runErr.Error()
		}
		alerts.LogBackup(h.db, name, env, status, msg, 0) //nolint:errcheck
	}
}

// MigrateWorkspace starts an async move of a whole workspace to another host
// (or local). It returns the job immediately (202); progress is pollable via
// GET /api/migration-jobs/{id} and completion fires an alert + notifications.
//
// POST /api/workspaces/{name}/migrate   body: {"target_host_id": 3}
func (h *Handler) MigrateWorkspace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		TargetHostID int64 `json:"target_host_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target_host_id is required"})
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, workspace, command, env, host) VALUES (?,?,?,?,?,?)",
			claims.UserID, claims.Username, name, "migrate", "", h.envHostName(name, ""),
		)
	}

	target := body.TargetHostID
	job := h.migJobs.create("workspace", name, "", h.targetLabel(target))
	go h.runMigration(job, func(out io.Writer) error { return h.bridge.Migrate(name, target, out) })
	writeJSON(w, http.StatusAccepted, job)
}

// SetEnvHost starts an async change of the host one environment runs on. If the
// env is deployed its data is migrated; otherwise it is a plain repoint. Returns
// the job immediately (202).
//
// PUT /api/workspaces/{name}/envs/{env}/host   body: {"host_id": 3}
func (h *Handler) SetEnvHost(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env := r.PathValue("env")
	var body struct {
		TargetHostID int64 `json:"host_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_id is required"})
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, workspace, command, env, host) VALUES (?,?,?,?,?,?)",
			claims.UserID, claims.Username, name, "set-host", env, h.envHostName(name, env),
		)
	}

	target := body.TargetHostID
	job := h.migJobs.create("env", name, env, h.targetLabel(target))
	go h.runMigration(job, func(out io.Writer) error { return h.bridge.MigrateEnv(name, env, target, out) })
	writeJSON(w, http.StatusAccepted, job)
}
