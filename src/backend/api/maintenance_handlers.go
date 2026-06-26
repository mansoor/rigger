package api

import (
	"fmt"
	"html"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/customdomains"
	"github.com/mansoor/rigger/ui/internal/maintenance"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// maintenanceHosts resolves the public Host() value(s) an env is reachable at (base-
// domain route + verified custom domains) and whether it serves HTTPS. Mirrors the
// deploy path (composegen.EnvRouteURL + settings.Effective* + customdomains) so the
// maintenance router matches exactly the hosts Traefik already routes for the app.
func (h *Handler) maintenanceHosts(ws, name, env string) (hosts []string, ssl bool) {
	data, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err == nil {
		if url, ok := composegen.EnvRouteURL(data, env,
			settings.EffectiveBaseDomain(h.db, ws),
			settings.EffectiveAutoURLMode(h.db, ws),
			settings.EffectiveAutoURLHost(h.db, ws)); ok {
			ssl = strings.HasPrefix(url, "https://")
			host := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
			if host != "" {
				hosts = append(hosts, host)
			}
		}
	}
	hosts = append(hosts, customdomains.VerifiedDomains(h.db, ws, name, env)...)
	return hosts, ssl
}

// StartMaintenanceScheduler launches the background reconcile loop (mirrors
// StartBackupScheduler). It supplies the host resolver from this handler so the
// maintenance package needn't import settings/composegen/customdomains.
func (h *Handler) StartMaintenanceScheduler() {
	maintenance.NewScheduler(maintenance.NewStore(h.db), maintenance.DynDir(),
		func(ws, project, env string) ([]string, bool) { return h.maintenanceHosts(ws, project, env) },
	).Run()
}

// GetMaintenance — GET .../envs/{env}/maintenance. Returns the env's maintenance
// state (viewer+).
func (h *Handler) GetMaintenance(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	rec, _, err := maintenance.NewStore(h.db).Get(ws, name, env)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, maintenanceView(rec))
}

// PutMaintenance — PUT .../envs/{env}/maintenance. Sets the env's maintenance state
// (operator+). enabling/active now applies the Traefik fragment immediately; disabling
// clears the schedule and removes the fragment.
func (h *Handler) PutMaintenance(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	var body struct {
		Enabled     bool   `json:"enabled"`
		WindowStart int64  `json:"window_start"`
		WindowEnd   int64  `json:"window_end"`
		Title       string `json:"title"`
		Message     string `json:"message"`
		RetryAfter  int64  `json:"retry_after"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	// The UI's "Turn off" action clears the schedule too (per spec: disabling maintenance
	// clears any window) by sending enabled=false with zeroed window. Scheduling a window
	// without enabling now is enabled=false WITH a window — stored as-is. The scheduler
	// clears a window once it has elapsed.
	if body.WindowStart > 0 && body.WindowEnd > 0 && body.WindowEnd <= body.WindowStart {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "window end must be after start"})
		return
	}
	uname := ""
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		uname = claims.Username
	}
	rec := maintenance.Record{
		Workspace: ws, Project: name, Env: env,
		Enabled: body.Enabled, WindowStart: body.WindowStart, WindowEnd: body.WindowEnd,
		Title: strings.TrimSpace(body.Title), Message: strings.TrimSpace(body.Message),
		RetryAfter: body.RetryAfter, UpdatedAt: time.Now().Unix(), UpdatedBy: uname,
	}

	dir := maintenance.DynDir()
	active := rec.ActiveAt(time.Now())
	// Apply/remove the Traefik fragment immediately (the scheduler only handles window
	// transitions later). If it's active now but the env has no public route, refuse.
	if active {
		hosts, ssl := h.maintenanceHosts(ws, name, env)
		if len(hosts) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this environment has no public route to put into maintenance"})
			return
		}
		if err := maintenance.Apply(dir, ws, name, env, hosts, ssl); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	} else {
		_ = maintenance.Clear(dir, ws, name, env)
	}
	if err := maintenance.NewStore(h.db).Upsert(rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Audit (project column uses the resource-prefix like other action rows).
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		cmd := "maintenance-off"
		if rec.Enabled {
			cmd = "maintenance-on"
		} else if rec.Scheduled() {
			cmd = "maintenance-schedule"
		}
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env, host) VALUES (?,?,?,?,?,?)",
			claims.UserID, claims.Username, h.resourcePrefix(ws, name), cmd, env, h.envHostName(h.resourcePrefix(ws, name), env),
		)
	}
	writeJSON(w, http.StatusOK, maintenanceView(rec))
}

// maintenanceView is the JSON shape returned to the UI (adds a derived `active`).
func maintenanceView(r maintenance.Record) map[string]any {
	return map[string]any{
		"enabled": r.Enabled, "window_start": r.WindowStart, "window_end": r.WindowEnd,
		"title": r.Title, "message": r.Message, "retry_after": r.RetryAfter,
		"active": r.ActiveAt(time.Now()), "scheduled": r.Scheduled(),
		"updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy,
	}
}

// MaintenancePage — PUBLIC GET /maintenance/{workspace}/{project}/{env}. Rendered for
// an env in maintenance: the per-env Traefik router rewrites every request here. Returns
// 503 + Retry-After + a self-contained branded HTML page. Unauthenticated by design.
func (h *Handler) MaintenancePage(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	rec, _, _ := maintenance.NewStore(h.db).Get(ws, name, env)

	title := rec.Title
	if title == "" {
		title = "We'll be right back"
	}
	msg := rec.Message
	if msg == "" {
		msg = "This site is undergoing scheduled maintenance. Please check back shortly."
	}
	retry := rec.RetryAfter
	if retry <= 0 {
		if rec.WindowEnd > 0 {
			if d := rec.WindowEnd - time.Now().Unix(); d > 0 {
				retry = d
			}
		}
		if retry <= 0 {
			retry = 300
		}
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", retry))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprint(w, maintenanceHTML(title, msg))
}

func maintenanceHTML(title, msg string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>` + html.EscapeString(title) + `</title>` +
		`<style>` +
		`html,body{height:100%;margin:0}` +
		`body{display:flex;align-items:center;justify-content:center;` +
		`font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;` +
		`background:#0f172a;color:#e2e8f0}` +
		`.card{max-width:32rem;padding:2.5rem;text-align:center}` +
		`.badge{font-size:2.75rem;line-height:1}` +
		`h1{font-size:1.6rem;margin:1rem 0 .5rem;color:#f8fafc}` +
		`p{font-size:1rem;line-height:1.6;color:#94a3b8;margin:0}` +
		`</style></head><body><div class="card">` +
		`<div class="badge">🛠️</div>` +
		`<h1>` + html.EscapeString(title) + `</h1>` +
		`<p>` + html.EscapeString(msg) + `</p>` +
		`</div></body></html>`
}
