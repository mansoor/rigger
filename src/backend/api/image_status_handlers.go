package api

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/deployhistory"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// imageServiceStatus is one build service's image pointer state.
type imageServiceStatus struct {
	Name      string `json:"name"`
	Effective string `json:"effective"` // what the next deploy will run (.env override, else latest)
	Latest    string `json:"latest"`    // the current version-derived tag
	Pinned    bool   `json:"pinned"`    // effective != latest (held to an older/other tag)
}

type imageStatusResponse struct {
	Version         string               `json:"version"`          // current config version
	Pinned          bool                 `json:"pinned"`           // any service pinned
	DeployedVersion string               `json:"deployed_version"` // last recorded deploy ("" if none)
	Services        []imageServiceStatus `json:"services"`
}

// GET /api/workspaces/{workspace}/projects/{name}/envs/{env}/image-status
// Per build service: what the next deploy will run (effective) vs the latest
// built version, and whether it's pinned. Drives the env-card "pinned / new
// build ready / up-to-date" indicator.
func (h *Handler) GetImageStatus(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	// Use the effective registry (project → workspace/global system) so the "latest"
	// tag shown matches what build/deploy produce (Phase 0: project's own value).
	cfg.Project.Registry = settings.EffectiveRegistry(h.db, ws, cfg.Project.Registry)
	// Resolve gives the effective image per build service (.env override or tag).
	eff := deployhistory.Resolve(h.workspacesDir, ws, name, env)

	out := imageStatusResponse{Version: cfg.VersionString(), Services: []imageServiceStatus{}}
	for _, svc := range cfg.BuildServices() {
		latest := cfg.ImageTag(svc.Name, env)
		effImg := eff.Images[svc.Name]
		if effImg == "" {
			effImg = latest
		}
		pinned := effImg != latest
		if pinned {
			out.Pinned = true
		}
		out.Services = append(out.Services, imageServiceStatus{
			Name: svc.Name, Effective: effImg, Latest: latest, Pinned: pinned,
		})
	}
	if list, _ := deployhistory.List(h.db, ws, name, env, 1); len(list) > 0 {
		out.DeployedVersion = list[0].Version
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/workspaces/{workspace}/projects/{name}/envs/{env}/track-latest
// Sets every build service's .env image pointer to the current version tag —
// the inverse of rollback (un-pins / catches an env up to the latest build) —
// then regenerates compose. Does NOT deploy; the env card's "new build ready —
// Deploy" guides rollout.
func (h *Handler) TrackLatest(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	// Track against the effective registry so the pinned .env tag matches build/deploy.
	cfg.Project.Registry = settings.EffectiveRegistry(h.db, ws, cfg.Project.Registry)
	updates := map[string]string{}
	for _, svc := range cfg.BuildServices() {
		updates[deployhistory.OverrideKey(svc.Name)] = cfg.ImageTag(svc.Name, env)
	}
	if len(updates) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no build services to track"})
		return
	}
	if err := workspace.UpdateEnvVars(h.workspacesDir, ws, name, env, updates, nil, nil); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update .env: " + err.Error()})
		return
	}
	// Regenerate compose so the baked default agrees with the advanced pointers.
	envDir := wspath.EnvDir(h.workspacesDir, ws, name, env)
	if cfgBytes, rerr := os.ReadFile(wspath.ConfigPath(h.workspacesDir, ws, name)); rerr == nil {
		envContent, _ := os.ReadFile(filepath.Join(envDir, ".env"))
		ro := composegen.RouteOpts{BaseDomain: settings.WorkspaceBaseDomain(h.db, ws), Registry: cfg.Project.Registry, EnvFile: string(envContent)}
		if content, gerr := composegen.GenerateRouted(cfgBytes, env, ro); gerr == nil {
			os.WriteFile(filepath.Join(envDir, "docker-compose.yml"), content, 0o644) //nolint:errcheck
		}
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, h.resourcePrefix(ws, name), "track-latest", env,
		)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "tracking latest", "version": cfg.VersionString()})
}
