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
	"github.com/mansoor/rigger/ui/internal/settings"
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
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")
	pkey := h.resourcePrefix(wsName, name) // resource-prefix key for project-scoped DB rows

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
			"INSERT INTO audit_log (user_id, username, project, command, env, host) VALUES (?,?,?,?,?,?)",
			claims.UserID, claims.Username, pkey, body.Command, env, h.envHostName(pkey, env),
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
		Workspace: wsName,
		Project:   name,
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
		if (body.Command == "start" || body.Command == "update") && env != "" {
			uname := ""
			if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
				uname = claims.Username
			}
			h.recordDeploy(wsName, name, env, uname)
			// Auto-import the bundled DB seed on a fresh deploy (empty DB + opt-in).
			// Best-effort, streamed to the same output; never affects deploy status.
			h.maybeAutoSeed(wsName, name, env, out)
			// Issue/refresh a per-email override cert (out-of-band, file provider) when
			// the env's effective ACME email differs from the global. Best-effort.
			h.maybeIssueOverrideCert(wsName, name, env, out)
			// Issue/refresh the workspace's own Cloudflare wildcard (*.{wsBase}) when it
			// overrides the base domain with its own DNS token. Shared across the
			// workspace's base-domain envs; idempotent (skipped while valid). Best-effort.
			h.maybeIssueWorkspaceWildcard(wsName, out)
		}
		if body.Command == "update" && env != "" {
			h.imgCache.Invalidate(wsName, name, env)
			go func() {
				if res := imagecheck.Check(h.workspacesDir, wsName, name, env); res != nil {
					h.imgCache.Set(wsName, name, env, res)
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
			Workspace: pkey, Env: env, Command: body.Command,
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
		alerts.LogBackup(h.db, pkey, env, status, msg, 0) //nolint:errcheck
	}
}

// MigrateWorkspace starts an async move of a whole workspace to another host
// (or local). It returns the job immediately (202); progress is pollable via
// GET /api/migration-jobs/{id} and completion fires an alert + notifications.
//
// POST /api/workspaces/{name}/migrate   body: {"target_host_id": 3}
func (h *Handler) MigrateWorkspace(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	pkey := h.resourcePrefix(wsName, name)
	var body struct {
		TargetHostID int64 `json:"target_host_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target_host_id is required"})
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env, host) VALUES (?,?,?,?,?,?)",
			claims.UserID, claims.Username, pkey, "migrate", "", h.envHostName(pkey, ""),
		)
	}

	target := body.TargetHostID
	job := h.migJobs.create("workspace", name, "", h.targetLabel(target))
	go h.runMigration(job, func(out io.Writer) error { return h.bridge.Migrate(wsName, name, target, out) })
	writeJSON(w, http.StatusAccepted, job)
}

// SetEnvHost starts an async change of the host one environment runs on. If the
// env is deployed its data is migrated; otherwise it is a plain repoint. Returns
// the job immediately (202).
//
// PUT /api/workspaces/{name}/envs/{env}/host   body: {"host_id": 3}
func (h *Handler) SetEnvHost(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")
	pkey := h.resourcePrefix(wsName, name)
	var body struct {
		TargetHostID int64 `json:"host_id"`
		BindOnly     bool  `json:"bind_only"` // record the binding without a migration (for new, not-yet-deployed envs)
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_id is required"})
		return
	}

	// Pool guard (Phase 3): an env may only be bound to a host this workspace can
	// use — its own host or a global host granted to it. host_id 0 = move to local.
	if body.TargetHostID != 0 {
		if inPool, _ := settings.HostInWorkspacePool(h.db, wsName, body.TargetHostID); !inPool {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "that host is not available to this workspace"})
			return
		}
		// A build-only host is a dedicated builder, not a deploy target (Phase 5).
		if hb, _ := settings.GetHost(h.db, body.TargetHostID); hb != nil && hb.BuildOnly {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that host is marked build-only and can't be a deploy target — pick another host or clear its build-only flag in Settings"})
			return
		}
	}

	// Bind-only: just record the (env → host) binding, no migration. Used when an
	// environment is first created in Edit Project — there's nothing deployed to
	// migrate yet; the stack starts on this host the first time it's deployed.
	if body.BindOnly {
		if err := settings.SetEnvHost(h.db, pkey, env, body.TargetHostID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "bound"})
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env, host) VALUES (?,?,?,?,?,?)",
			claims.UserID, claims.Username, pkey, "set-host", env, h.envHostName(pkey, env),
		)
	}

	target := body.TargetHostID
	job := h.migJobs.create("env", name, env, h.targetLabel(target))
	go h.runMigration(job, func(out io.Writer) error { return h.bridge.MigrateEnv(wsName, name, env, target, out) })
	writeJSON(w, http.StatusAccepted, job)
}
