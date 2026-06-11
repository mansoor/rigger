package api

import (
	"net/http"
	"sort"

	"github.com/mansoor/rigger/ui/internal/blueprints"
)

// blueprintInfo is the wizard-facing view of a stack blueprint (the no-repo
// "start from a template" picker), including the starter service graph the
// picker pre-fills — built by the same rules as the repo detector so a scanned
// and a picked project of the same stack come out identical.
type blueprintInfo struct {
	ID         string           `json:"id"`
	Label      string           `json:"label"`
	Language   string           `json:"language"`
	Port       string           `json:"port"`
	WebRouted  bool             `json:"web_routed"`
	NeedsNginx bool             `json:"needs_nginx"`
	Services   []map[string]any `json:"services"`
}

// Blueprints — GET /api/blueprints. Lists the stack blueprints the no-repo
// picker can scaffold, sorted by label for a stable UI.
func (h *Handler) Blueprints(w http.ResponseWriter, r *http.Request) {
	all := blueprints.All()
	out := make([]blueprintInfo, 0, len(all))
	for _, b := range all {
		out = append(out, blueprintInfo{
			ID: b.ID, Label: b.Label, Language: b.Language,
			Port: b.Port, WebRouted: b.WebRouted, NeedsNginx: b.NeedsNginx,
			Services: seedBlueprintServices(b),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	writeJSON(w, http.StatusOK, out)
}

// seedBlueprintServices builds the starter service graph for a no-repo project
// from a blueprint: one build service (scaffolded from the blueprint's Dockerfile
// template) plus an nginx front when the framework needs one. Mirrors
// internal/detect.addFrameworkService so picked and scanned projects match.
func seedBlueprintServices(b blueprints.Blueprint) []map[string]any {
	app := map[string]any{
		"name":     "app",
		"build":    map[string]any{"template": b.ID, "context": "."},
		"env_file": true,
		"restart":  "unless-stopped",
	}
	if b.Port != "" {
		app["port"] = b.Port
	}
	if b.Healthcheck != "" {
		app["healthcheck"] = b.Healthcheck
	}
	app["web_routed"] = b.WebRouted && !b.NeedsNginx
	if len(b.ServiceEnv) > 0 {
		ev := map[string]string{}
		for k, v := range b.ServiceEnv {
			ev[k] = v
		}
		app["env_vars"] = ev
	}
	services := []map[string]any{app}
	if b.NeedsNginx {
		services = append(services, map[string]any{
			"name":            "app-nginx",
			"image":           "nginx",
			"tag":             "1.25-alpine",
			"web_routed":      true,
			"port":            "80",
			"depends_on":      []string{"app"},
			"config_template": b.NginxConf,
			"volumes":         []string{"./nginx.conf:/etc/nginx/conf.d/default.conf:ro"},
			"restart":         "unless-stopped",
		})
	}
	return services
}
