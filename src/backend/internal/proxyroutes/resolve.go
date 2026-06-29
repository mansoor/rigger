package proxyroutes

import (
	"encoding/json"

	"github.com/mansoor/rigger/ui/internal/db"
)

// envAttach is the per-env access/plugin attachment persisted in config.json (environments.<env>).
// AccessListID picks a reusable workspace list; when it's 0 the IP/Geo fields configure the same
// protections inline for just this env (mutually exclusive in the UI). BlockExploits applies in
// every mode. See docs/design/workspace-plugins-and-access-lists.md.
type envAttach struct {
	AccessListID int64 `json:"access_list_id"`
	WAF          bool  `json:"waf"`
	Cache        bool  `json:"cache"`
	// Inline access (used only when AccessListID == 0):
	IPMode        string `json:"ip_mode"`       // "" | allow  (Traefik ipAllowList is allow-only)
	IPCIDRs       string `json:"ip_cidrs"`      // comma/space/newline IPs or CIDRs
	GeoMode       string `json:"geo_mode"`      // "" | allow | block  (GeoIP country policy)
	GeoCountries  string `json:"geo_countries"` // comma-separated ISO 3166-1 alpha-2 codes
	BlockExploits bool   `json:"block_exploits"`
}

// ResolveRouterMiddlewares returns the file-provider middleware refs to attach to an env's APP
// routers: the env's workspace access list (auth / IP / GeoIP) plus WAF/cache when toggled on for
// the env AND enabled instance-wide. `workspace` scopes the access-list lookup (strict isolation —
// a foreign or global list id is ignored). Returns nil when nothing attaches (golden parity).
// Best-effort: a bad config or missing list yields fewer/zero refs, never an error.
// See docs/design/workspace-plugins-and-access-lists.md.
func ResolveRouterMiddlewares(d *db.DB, workspace string, configBytes []byte, env string, wafEnabled, cacheEnabled, geoEnabled bool) []string {
	var cfg struct {
		Environments map[string]envAttach `json:"environments"`
	}
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		return nil
	}
	a, ok := cfg.Environments[env]
	if !ok {
		return nil
	}
	var out []string
	if a.AccessListID > 0 {
		if al, found, err := NewStore(d).GetAccessList(a.AccessListID); err == nil && found && al.Workspace == workspace && al.Workspace != "" {
			out = append(out, SharedACLMiddlewareNames(al, geoEnabled)...)
		}
	}
	if a.WAF && wafEnabled {
		out = append(out, "proxy-waf@file")
	}
	if a.Cache && cacheEnabled {
		out = append(out, "proxy-cache@file")
	}
	return out
}
