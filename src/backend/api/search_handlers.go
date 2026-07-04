package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// searchEntry is one jump-to target for the command palette (Phase 2, deep search).
// It mirrors the frontend entry shape so results merge with the client-built
// navigation/project entries without transformation.
type searchEntry struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Category string `json:"category"`
	Icon     string `json:"icon"`
	URL      string `json:"url"`
	Keywords string `json:"keywords"`
}

// SearchIndex assembles the "deep" searchable entities for one workspace that the
// client can't derive from its cached projects list: env-var KEYS and route rules
// (only in the full config.json, not the reduced list view) plus custom domains and
// pipelines (DB-only). It's on-demand — a walk of the workspace's local config files
// (authoritative for every project regardless of which host runs it) plus two scoped
// DB reads — so it needs no persistent index and never touches a remote host.
//
// Security: env-var VALUES are never read or returned — KEYS/names only. Results are
// RBAC-filtered to the projects the caller may see, mirroring ListProjects.
//
// GET /api/workspaces/{workspace}/search-index
func (h *Handler) SearchIndex(w http.ResponseWriter, r *http.Request) {
	wsName := r.PathValue("workspace")
	claims := auth.ClaimsFromContext(r.Context())
	isSuper := claims != nil && auth.IsSuperadmin(claims.Role)

	// Minimal projection of config.json — we read only what we index, and never the
	// env-var values (map values are ignored; we range keys).
	type cfgEnv struct {
		EnvVars map[string]json.RawMessage `json:"env_vars"`
	}
	type cfgRoute struct {
		Match   string `json:"match"`
		Service string `json:"service"`
		Type    string `json:"type"`
	}
	type siConfig struct {
		Project      struct {
			Name string `json:"name"`
		} `json:"project"`
		Environments map[string]cfgEnv `json:"environments"`
		Routes       []cfgRoute        `json:"routes"`
	}

	out := []searchEntry{}
	for _, projKey := range workspace.ProjectKeys(h.workspacesDir, wsName) {
		// Only index projects the caller can see (a super-admin sees all).
		if !isSuper {
			if claims == nil || h.auth.EffectiveRole(claims.UserID, claims.Role, wsName, projKey) == "" {
				continue
			}
		}
		raw, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, wsName, projKey))
		if err != nil {
			continue
		}
		var c siConfig
		if json.Unmarshal(raw, &c) != nil {
			continue
		}
		display := c.Project.Name
		if display == "" {
			display = projKey
		}
		editURL := fmt.Sprintf("/workspaces/%s/projects/%s/edit", wsName, projKey)
		projURL := fmt.Sprintf("/workspaces/%s/projects/%s", wsName, projKey)

		// Env-var KEYS (deduped across envs — the same key in dev/prod is one entry).
		seenKey := map[string]bool{}
		for _, ec := range c.Environments {
			for k := range ec.EnvVars {
				if k == "" || seenKey[k] {
					continue
				}
				seenKey[k] = true
				out = append(out, searchEntry{
					ID: "ev-" + projKey + "-" + k, Title: k,
					Subtitle: "Env var · " + display, Category: "Env var", Icon: "≡",
					URL: editURL, Keywords: k + " " + projKey + " env variable",
				})
			}
		}

		// Route rules (project-level ingress table).
		for i, rt := range c.Routes {
			if rt.Match == "" && rt.Service == "" {
				continue
			}
			out = append(out, searchEntry{
				ID: fmt.Sprintf("rt-%s-%d", projKey, i), Title: rt.Match + " → " + rt.Service,
				Subtitle: "Route · " + display, Category: "Route", Icon: "⇥",
				URL: editURL, Keywords: rt.Match + " " + rt.Service + " " + rt.Type + " route " + projKey,
			})
		}

		// Custom domains (DB) — scoped to this visible project.
		if rows, derr := h.db.Query(
			`SELECT domain, env, is_primary FROM custom_domains WHERE workspace=? AND project=?`, wsName, projKey,
		); derr == nil {
			for rows.Next() {
				var domain, env string
				var primary int
				if rows.Scan(&domain, &env, &primary) == nil {
					star := ""
					if primary == 1 {
						star = " ★"
					}
					out = append(out, searchEntry{
						ID: "dom-" + projKey + "-" + domain, Title: domain + star,
						Subtitle: "Domain · " + display + " · " + env, Category: "Domain", Icon: "◈",
						URL: projURL, Keywords: domain + " " + projKey + " " + env + " custom domain",
					})
				}
			}
			rows.Close()
		}

		// Pipelines (DB) — scoped to this visible project.
		if rows, derr := h.db.Query(
			`SELECT name FROM pipelines WHERE workspace=? AND project=?`, wsName, projKey,
		); derr == nil {
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					out = append(out, searchEntry{
						ID: "pl-" + projKey + "-" + name, Title: name,
						Subtitle: "Pipeline · " + display, Category: "Pipeline", Icon: "⟲",
						URL: projURL, Keywords: name + " " + projKey + " pipeline deploy",
					})
				}
			}
			rows.Close()
		}
	}

	writeJSON(w, http.StatusOK, out)
}
