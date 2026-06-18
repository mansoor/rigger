// Package apikey implements the external REST API's authentication: long-lived API
// keys with granular operation scopes, project access control, expiry, and a
// per-key-per-project request-rate limit. It is intentionally separate from the
// JWT/cookie auth that guards the web UI (internal/auth) — an API key authenticates
// machine clients of the /api/v1 surface, never the UI.
//
// A key's secret is shown ONCE at creation ("rgk_<random>"); only its SHA-256 hash is
// stored, so a leaked DB never yields usable keys. Scopes are stored as a flat set of
// granular operation ids (the admin UI groups them for convenience but persists the
// resolved op set), so enforcement is a simple membership check.
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Operation ids — the granular unit of permission. Each /api/v1 endpoint requires
// exactly one. Keep these stable: they're persisted in api_keys.scopes and documented.
const (
	OpProjectsList  = "projects.list"  // GET /projects
	OpServicesList  = "services.list"  // GET .../services
	OpLogsRead      = "logs.read"      // GET .../logs
	OpEnvStart      = "env.start"      // POST .../actions/start
	OpEnvStop       = "env.stop"       // POST .../actions/stop
	OpEnvRestart    = "env.restart"    // POST .../actions/restart
	OpEnvRefresh    = "env.refresh"    // POST .../actions/refresh
	OpEnvInactivate = "env.inactivate" // POST .../actions/inactivate (compose down)
	OpEnvBackup     = "env.backup"     // POST .../actions/backup
	OpPipelineRun   = "pipeline.run"   // POST .../pipelines/{id}/run
)

// Group is a UI-facing bundle of operations. The admin form lets a user toggle a whole
// group, then expand it to override individual operations — but only the resolved op
// set (Group.Ops members that end up enabled) is persisted on the key.
type Group struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Desc  string   `json:"desc"`
	Ops   []OpInfo `json:"ops"`
}

// OpInfo describes one operation for the scope picker + the generated docs.
type OpInfo struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Groups is the canonical scope catalog, surfaced to the UI (scope picker) and the docs
// generator. Adding an endpoint = add its op here + require it in the handler.
var Groups = []Group{
	{ID: "read", Label: "Read", Desc: "List projects/services and read logs.", Ops: []OpInfo{
		{OpProjectsList, "List projects", "GET", "/api/v1/projects"},
		{OpServicesList, "List services", "GET", "/api/v1/projects/{workspace}/{project}/envs/{env}/services"},
		{OpLogsRead, "Read service logs", "GET", "/api/v1/projects/{workspace}/{project}/envs/{env}/services/{service}/logs"},
	}},
	{ID: "operate", Label: "Operate", Desc: "Lifecycle actions on an environment.", Ops: []OpInfo{
		{OpEnvStart, "Start", "POST", "/api/v1/projects/{workspace}/{project}/envs/{env}/actions/start"},
		{OpEnvStop, "Stop", "POST", "/api/v1/projects/{workspace}/{project}/envs/{env}/actions/stop"},
		{OpEnvRestart, "Restart", "POST", "/api/v1/projects/{workspace}/{project}/envs/{env}/actions/restart"},
		{OpEnvRefresh, "Refresh", "POST", "/api/v1/projects/{workspace}/{project}/envs/{env}/actions/refresh"},
		{OpEnvInactivate, "Inactivate (down)", "POST", "/api/v1/projects/{workspace}/{project}/envs/{env}/actions/inactivate"},
		{OpEnvBackup, "Backup", "POST", "/api/v1/projects/{workspace}/{project}/envs/{env}/actions/backup"},
	}},
	{ID: "pipeline", Label: "Pipeline", Desc: "Trigger deployment pipelines.", Ops: []OpInfo{
		{OpPipelineRun, "Trigger a pipeline run", "POST", "/api/v1/projects/{workspace}/{project}/pipelines/{id}/run"},
	}},
}

// validOps is the set of all known operation ids (for create-time validation).
var validOps = func() map[string]bool {
	m := map[string]bool{}
	for _, g := range Groups {
		for _, o := range g.Ops {
			m[o.ID] = true
		}
	}
	return m
}()

// ValidOp reports whether op is a known operation id.
func ValidOp(op string) bool { return validOps[op] }

// Key is one API key. Token is populated ONLY by Generate/Create (the one-time reveal);
// it's never read back from the store.
type Key struct {
	ID            int64        `json:"id"`
	Name          string       `json:"name"`
	Workspace     string       `json:"workspace,omitempty"`    // ""=global (admin-minted); else confined to this workspace
	KeyPrefix     string       `json:"key_prefix"`             // non-secret display snippet, e.g. "rgk_ab12…"
	Scopes        []string     `json:"scopes"`                 // granular op ids
	ProjectAccess string       `json:"project_access"`         // "all" | "specific"
	Projects      []ProjectRef `json:"projects,omitempty"`     // populated when ProjectAccess=="specific"
	RateLimit     int          `json:"rate_limit"`             // requests/min/project; 0 = unlimited
	Enabled       bool         `json:"enabled"`
	CreatedBy     string       `json:"created_by"`
	CreatedAt     int64        `json:"created_at"`             // epoch seconds
	LastUsedAt    int64        `json:"last_used_at,omitempty"` // epoch seconds; 0 = never
	ExpiresAt     int64        `json:"expires_at,omitempty"`   // epoch seconds; 0 = never
	Token         string       `json:"token,omitempty"`        // raw key — only on create
}

// ProjectRef identifies a project a "specific"-access key may touch.
type ProjectRef struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
}

// keyPrefix is the literal prefix every raw key carries, so leaked secrets are
// recognizable in logs/scanners ("rgk" = riGGer Key).
const keyPrefix = "rgk_"

// Generate returns a fresh raw key, its SHA-256 hash (what we store), and a non-secret
// display prefix. The raw key is 32 bytes of entropy, hex-encoded, behind "rgk_".
func Generate() (raw, hash, prefix string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", err
	}
	raw = keyPrefix + hex.EncodeToString(b)
	hash = Hash(raw)
	prefix = raw[:12] + "…" // "rgk_" + first 8 hex chars
	return raw, hash, prefix, nil
}

// Hash returns the hex SHA-256 of a raw key — the lookup/comparison value. Constant
// across calls, so the middleware hashes the presented key and looks it up directly.
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Common authorization errors, surfaced as distinct HTTP statuses by the middleware.
var (
	ErrDisabled   = errors.New("api key disabled")
	ErrExpired    = errors.New("api key expired")
	ErrScope      = errors.New("api key lacks the required scope")
	ErrProject    = errors.New("api key has no access to this project")
	ErrRateLimit  = errors.New("rate limit exceeded")
	ErrNotFound   = errors.New("api key not found")
)

// HasScope reports whether the key may perform op.
func (k *Key) HasScope(op string) bool {
	for _, s := range k.Scopes {
		if s == op {
			return true
		}
	}
	return false
}

// CanAccess reports whether the key may touch (workspace, project). A workspace-confined
// key (Workspace != "") can only ever touch its own workspace. Within that, "all"-access
// keys reach any project; "specific" keys must list the pair.
func (k *Key) CanAccess(workspace, project string) bool {
	if k.Workspace != "" && k.Workspace != workspace {
		return false
	}
	if k.ProjectAccess != "specific" {
		return true
	}
	for _, p := range k.Projects {
		if p.Workspace == workspace && p.Project == project {
			return true
		}
	}
	return false
}

// Authorize runs the full per-request gate (excluding rate limiting, which the
// middleware applies separately so it can return Retry-After). Order matters: enabled →
// not expired → scope → project access, returning the most specific error.
func (k *Key) Authorize(op, workspace, project string, now time.Time) error {
	if !k.Enabled {
		return ErrDisabled
	}
	if k.ExpiresAt > 0 && now.Unix() >= k.ExpiresAt {
		return ErrExpired
	}
	if !k.HasScope(op) {
		return ErrScope
	}
	if !k.CanAccess(workspace, project) {
		return ErrProject
	}
	return nil
}
