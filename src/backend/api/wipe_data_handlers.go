package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/envorder"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/shell"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// wipeConfirmSentence / deleteConfirmSentence are the exact acknowledgement
// sentences the user must paste to run an irreversible action. Defined once so the
// handler validates precisely what the UI shows (copy-paste, no typing).
func wipeConfirmSentence(project, env string) string {
	return fmt.Sprintf("Wipe data for Project: %s Environment: %s", project, env)
}

func deleteConfirmSentence(project string) string {
	return fmt.Sprintf("Delete Project: %s", project)
}

// wipeAllowedEnvs returns the workspace's data-wipe allowlist (env names). Empty ⇒
// the wipe action is unavailable for every environment in that workspace.
func (h *Handler) wipeAllowedEnvs(ws string) []string {
	vals, err := settings.GetWorkspaceSettings(h.db, ws)
	if err != nil || vals == nil {
		return nil
	}
	return envorder.SplitTierNames(vals["wipe_allowed_envs"])
}

func envAllowedForWipe(allowed []string, env string) bool {
	for _, a := range allowed {
		if strings.EqualFold(strings.TrimSpace(a), env) {
			return true
		}
	}
	return false
}

// WipeEnvData wipes application DATA (named volumes + bind-mount data directories)
// for ONE environment, keeping config/settings, then redeploys it fresh. Guarded:
// workspace-admin (or super-admin) only; the env must be in the workspace's
// wipe_allowed_envs allowlist (which keeps prod un-wipeable unless an admin opts it
// in); the caller must paste the confirm sentence AND re-enter their password.
//
// POST /api/workspaces/{ws}/projects/{name}/envs/{env}/wipe-data
// Body: {confirm, password}. Streams the teardown → clear → redeploy as plain text.
func (h *Handler) WipeEnvData(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")
	claims := h.requireWorkspaceAdmin(w, r, ws)
	if claims == nil {
		return
	}
	if env == "" || strings.ContainsAny(env, "/\\.") || name == "" || strings.ContainsAny(name, "/\\.") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project or environment"})
		return
	}
	var body struct {
		Confirm  string `json:"confirm"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	// The env must be explicitly allowed to be wiped by the workspace (prod excluded
	// by default — it's simply never added to the allowlist).
	if !envAllowedForWipe(h.wipeAllowedEnvs(ws), env) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "data wiping is not enabled for the '" + env + "' environment — a workspace admin can enable it in Manage Workspace → Environments allowed to wipe data",
		})
		return
	}
	// Exact confirmation sentence, then a password re-check (defence in depth).
	if strings.TrimSpace(body.Confirm) != wipeConfirmSentence(name, env) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "confirmation text does not match"})
		return
	}
	if !h.auth.VerifyUserPassword(claims.UserID, body.Password) {
		// 422 (not 401): an in-session re-auth check failing is NOT an expired
		// session — the frontend interceptor logs out on 401, which would kick the
		// user out on a mere password typo. 422 keeps the modal open to retry.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "incorrect password"})
		return
	}
	if _, err := os.Stat(wspath.EnvDir(h.workspacesDir, ws, name, env)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "environment not found"})
		return
	}

	// Audit the destructive action (retained even though most project rows are not).
	h.db.Exec( //nolint:errcheck
		"INSERT INTO audit_log (user_id, username, project, command, env, host) VALUES (?,?,?,?,?,?)",
		claims.UserID, claims.Username, ws+"_"+name, "wipe-data", env, h.envHostName(h.resourcePrefix(ws, name), env),
	)

	// Run the wipe in a BACKGROUND job (reusing the migration job store) and return a
	// job id immediately. A synchronous streaming response tied the whole down →
	// clear → redeploy sequence (which can take a while, esp. waiting on healthchecks)
	// to one long HTTP request: the UI froze on it, a client disconnect orphaned the
	// work, and it competed with other long-lived streams for browser connections.
	// Async instead: the POST returns fast; the UI polls GET /api/migration-jobs/{id}
	// for live progress and can be left/closed safely.
	job := h.migJobs.create("wipe", ws, env, name)
	go func() {
		out := migWriter{store: h.migJobs, id: job.ID}
		err := h.doWipe(ws, name, env, out)
		status, errMsg := "completed", ""
		if err != nil {
			status, errMsg = "failed", err.Error()
		}
		h.migJobs.finish(job.ID, status, errMsg)
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID})
}

// doWipe performs the three-step wipe (down+purge → clear bind data → redeploy),
// writing progress to out. Used by the background job spawned in WipeEnvData.
func (h *Handler) doWipe(ws, name, env string, out io.Writer) error {
	fmt.Fprintf(out, "Wiping application data for %s / %s / %s\n\n", ws, name, env)

	// 1) Stack down + purge named volumes (on the env's deploy host).
	fmt.Fprint(out, "▶ Stopping stack and removing named volumes…\n")
	if err := h.bridge.Run(shell.RunOptions{
		Workspace: ws, Project: name, Command: "down", Env: env,
		PurgeVolumes: true, Stdout: out, Stderr: out,
	}); err != nil {
		return fmt.Errorf("teardown failed: %w", err)
	}

	// 2) Clear bind-mount data directories (keeps compose/.env/config files).
	fmt.Fprint(out, "\n▶ Clearing bind-mount data…\n")
	if err := h.bridge.WipeBindData(ws, name, env, out); err != nil {
		return fmt.Errorf("clearing bind data failed: %w", err)
	}

	// 3) Redeploy fresh (recreates containers + empty volumes/bind dirs).
	fmt.Fprintf(out, "\n▶ Redeploying %s fresh…\n", env)
	if err := h.bridge.Run(shell.RunOptions{
		Workspace: ws, Project: name, Command: "start", Env: env,
		Stdout: out, Stderr: out,
	}); err != nil {
		return fmt.Errorf("redeploy failed (data was wiped — deploy manually to bring it back up): %w", err)
	}
	fmt.Fprintf(out, "\n✓ Data wiped and %s redeployed fresh.\n", env)
	return nil
}

// verifyDeleteConfirm re-checks the delete confirmation sentence + caller password
// for a project deletion. Returns the claims on success, else writes the error and
// returns nil. Shared by DeleteWorkspace.
func (h *Handler) verifyDeleteConfirm(w http.ResponseWriter, r *http.Request, ws, name string) *auth.Claims {
	claims := h.requireWorkspaceAdmin(w, r, ws)
	if claims == nil {
		return nil
	}
	var body struct {
		Confirm  string `json:"confirm"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return nil
	}
	if strings.TrimSpace(body.Confirm) != deleteConfirmSentence(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "confirmation text does not match"})
		return nil
	}
	if !h.auth.VerifyUserPassword(claims.UserID, body.Password) {
		// 422 (not 401) so a password typo doesn't trip the frontend's session-expiry
		// logout on 401 — keeps the confirm dialog open to retry.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "incorrect password"})
		return nil
	}
	return claims
}
