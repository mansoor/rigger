package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/envorder"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Environment deploy-tier order (release-pipeline #4). The explicit order is a
// list of env names stored on project.env_order; the effective order applies the
// envorder resolver (explicit → tier-name guess → alphabetical). A dedicated
// endpoint (not PutConfig) avoids the config editor's env-teardown machinery and
// the read-modify-write race against the big Services/Environments save.

type envOrderResponse struct {
	Order     []string `json:"order"`     // explicit per-project order ([] if unset)
	Effective []string `json:"effective"` // resolved order actually used
	Explicit  bool     `json:"explicit"`  // whether an explicit order is set
}

func (h *Handler) tierNames(ws string) []string {
	vals, _ := settings.GetWorkspaceSettings(h.db, ws)
	return envorder.SplitTierNames(vals["env_tier_names"])
}

// GET /api/workspaces/{workspace}/projects/{name}/env-order
func (h *Handler) GetEnvOrder(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	writeJSON(w, http.StatusOK, envOrderResponse{
		Order:     cfg.Project.EnvOrder,
		Effective: envorder.Resolve(cfg.EnvNames(), cfg.Project.EnvOrder, h.tierNames(ws)),
		Explicit:  len(cfg.Project.EnvOrder) > 0,
	})
}

// PUT /api/workspaces/{workspace}/projects/{name}/env-order  Body: { order:[...] }
// Sets the explicit deploy-tier order. Every name must be a configured env.
func (h *Handler) PutEnvOrder(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var body struct {
		Order []string `json:"order"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	cfgPath := wspath.ConfigPath(h.workspacesDir, ws, name)
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	parsed, err := wsconfig.Parse(raw)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "parse config: " + err.Error()})
		return
	}
	valid := make(map[string]bool, len(parsed.Environments))
	for e := range parsed.Environments {
		valid[e] = true
	}
	clean := make([]string, 0, len(body.Order))
	seen := map[string]bool{}
	for _, e := range body.Order {
		if !valid[e] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown environment %q", e)})
			return
		}
		if !seen[e] {
			clean = append(clean, e)
			seen[e] = true
		}
	}

	// Write back preserving every other field (config.json is a superset of the
	// wsconfig view, so edit the raw document rather than re-marshalling a typed
	// struct, which would drop unknown keys).
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	proj, _ := doc["project"].(map[string]any)
	if proj == nil {
		proj = map[string]any{}
		doc["project"] = proj
	}
	if len(clean) > 0 {
		proj["env_order"] = clean
	} else {
		delete(proj, "env_order")
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := os.WriteFile(cfgPath, out, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "write config: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, envOrderResponse{
		Order:     clean,
		Effective: envorder.Resolve(parsed.EnvNames(), clean, h.tierNames(ws)),
		Explicit:  len(clean) > 0,
	})
}
