package api

import (
	"net/http"
	"strings"

	"github.com/mansoor/rigger/ui/internal/apikey"
)

// The /api/v1 API documentation: an OpenAPI 3 spec generated from the same scope
// catalog the auth layer enforces (so docs can't drift from behaviour), plus a
// rendered Redoc page. Both are public (no key required) — they're documentation.

// OpenAPISpec: GET /api/v1/openapi.json — the machine-readable contract.
func (h *Handler) OpenAPISpec(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, buildOpenAPI())
}

// APIDocsPage: GET /api/v1/docs — a human-browsable rendering of the spec (Redoc via
// CDN; needs outbound internet to load the renderer, but the spec itself is local).
func (h *Handler) APIDocsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>Rigger API</title>` + //nolint:errcheck
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<style>body{margin:0}</style></head><body>` +
		`<redoc spec-url="/api/v1/openapi.json"></redoc>` +
		`<script src="https://cdn.redoc.ly/redoc/latest/bundles/redoc.standalone.js"></script>` +
		`</body></html>`))
}

// buildOpenAPI assembles the spec from apikey.Groups: each operation becomes a path
// item, with its required scope noted in the description and path params derived from
// the URL template. Standard auth/rate-limit error responses are attached to every op.
func buildOpenAPI() map[string]any {
	paths := map[string]any{}
	for _, g := range apikey.Groups {
		for _, op := range g.Ops {
			method := strings.ToLower(op.Method)
			item, _ := paths[op.Path].(map[string]any)
			if item == nil {
				item = map[string]any{}
			}
			item[method] = map[string]any{
				"summary":     op.Label,
				"description":  "Requires the `" + op.ID + "` scope (group: " + g.Label + ").",
				"operationId": op.ID,
				"tags":        []string{g.Label},
				"parameters":  pathParams(op.Path),
				"security":    []map[string]any{{"bearerAuth": []string{}}},
				"responses": map[string]any{
					"200": map[string]any{"description": "Success"},
					"202": map[string]any{"description": "Accepted (async; e.g. a pipeline run id)"},
					"401": map[string]any{"description": "Missing, invalid, disabled, or expired API key"},
					"403": map[string]any{"description": "Key lacks the scope or project access"},
					"429": map[string]any{"description": "Rate limit exceeded (per key, per project)"},
				},
			}
			paths[op.Path] = item
		}
	}
	return map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "Rigger API",
			"version":     "v1",
			"description": "External REST API for Rigger. Authenticate with an API key (Admin → API Keys) as `Authorization: Bearer rgk_…`. Keys are scoped to operations and projects, and rate-limited per project.",
		},
		"servers":    []map[string]any{{"url": "/", "description": "This Rigger instance"}},
		"paths":      paths,
		"components": map[string]any{"securitySchemes": map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer", "description": "An API key: rgk_…"}}},
		"security":   []map[string]any{{"bearerAuth": []string{}}},
	}
}

// pathParams turns a URL template's {name} segments into OpenAPI path parameters.
func pathParams(tmpl string) []map[string]any {
	var out []map[string]any
	for _, seg := range strings.Split(tmpl, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			name := strings.Trim(seg, "{}")
			typ := "string"
			if name == "id" {
				typ = "integer"
			}
			out = append(out, map[string]any{
				"name": name, "in": "path", "required": true,
				"schema": map[string]any{"type": typ},
			})
		}
	}
	return out
}
