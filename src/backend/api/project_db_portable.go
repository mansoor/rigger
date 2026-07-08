package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mansoor/rigger/ui/internal/db"
)

// Portable project DB state for the self-contained .rpb / .rps archives.
//
// A project's on-disk files (config.json, envs, notes) travel in the archive, but
// its operational config lives in SQLite. This exports the project-owned config
// rows into rigger-project-db.json (embedded at the archive root) and re-imports
// them on restore, so a restored project comes back with its pipelines, alerts,
// custom domains, maintenance windows and host bindings.
//
// Included: pipelines (+ webhooks, FK re-linked by name), alert rules, custom
// domains, env maintenance, host bindings (remapped by host NAME so they resolve
// across servers). Excluded by design: deploy/rollback history, access-control
// grants, raw DB-user secrets, and operational logs. Tables key inconsistently —
// pipelines/custom_domains/env_maintenance by (workspace, project), alert_rules by
// (ws_key, project), host bindings by the resource-prefix — handled per table.

const projectDBFile = "rigger-project-db.json"
const projectDBVersion = 1

type projectDBExport struct {
	Version        int               `json:"version"`
	Pipelines      []pipelineDump    `json:"pipelines"`
	AlertRules     []map[string]any  `json:"alert_rules"`
	CustomDomains  []map[string]any  `json:"custom_domains"`
	EnvMaintenance []map[string]any  `json:"env_maintenance"`
	HostEnvs       []hostBindingDump `json:"host_envs"`
	BuildHost      *hostBindingDump  `json:"build_host,omitempty"`
}

type pipelineDump struct {
	Name     string        `json:"name"`
	Stages   string        `json:"stages"`
	Enabled  int64         `json:"enabled"`
	Webhooks []webhookDump `json:"webhooks,omitempty"`
}

type webhookDump struct {
	TokenHash string `json:"token_hash"`
	Secret    string `json:"secret"`
	Enabled   int64  `json:"enabled"`
}

// hostBindingDump records a host binding by NAME so it can be re-resolved to a
// (server-local) host id on the target — dropped if no host of that name exists.
type hostBindingDump struct {
	Env      string `json:"env,omitempty"` // "" for the project build host
	HostName string `json:"host_name"`
}

// exportProjectDB collects the project's portable DB config as JSON.
func (h *Handler) exportProjectDB(ws, project string) ([]byte, error) {
	d := h.db
	exp := projectDBExport{Version: projectDBVersion}

	// Pipelines + their webhooks (webhooks FK pipelines.id — re-linked by name on import).
	type pipeRow struct {
		id      int64
		name    string
		stages  string
		enabled int64
	}
	var pipes []pipeRow
	if rows, err := d.Query(`SELECT id, name, stages, enabled FROM pipelines WHERE workspace=? AND project=?`, ws, project); err == nil {
		for rows.Next() {
			var p pipeRow
			if rows.Scan(&p.id, &p.name, &p.stages, &p.enabled) == nil {
				pipes = append(pipes, p)
			}
		}
		rows.Close()
	}
	for _, p := range pipes {
		pd := pipelineDump{Name: p.name, Stages: p.stages, Enabled: p.enabled}
		if wr, err := d.Query(`SELECT token_hash, secret, enabled FROM pipeline_webhooks WHERE pipeline_id=?`, p.id); err == nil {
			for wr.Next() {
				var wd webhookDump
				if wr.Scan(&wd.TokenHash, &wd.Secret, &wd.Enabled) == nil {
					pd.Webhooks = append(pd.Webhooks, wd)
				}
			}
			wr.Close()
		}
		exp.Pipelines = append(exp.Pipelines, pd)
	}

	// Flat config tables — generic column-preserving copy.
	exp.AlertRules = exportRows(d, `SELECT * FROM alert_rules WHERE ws_key=? AND project=?`, ws, project)
	exp.CustomDomains = exportRows(d, `SELECT * FROM custom_domains WHERE workspace=? AND project=?`, ws, project)
	exp.EnvMaintenance = exportRows(d, `SELECT * FROM env_maintenance WHERE workspace=? AND project=?`, ws, project)

	// Host bindings — resource-prefix keyed, host_id remapped to host name.
	// Materialise the cursor FIRST: the DB uses a single connection, so resolving
	// host names mid-iteration (a nested query) would deadlock.
	prefix := h.resourcePrefix(ws, project)
	type envHost struct {
		env string
		hid int64
	}
	var envHosts []envHost
	if rows, err := d.Query(`SELECT env, host_id FROM workspace_host_envs WHERE project=?`, prefix); err == nil {
		for rows.Next() {
			var eh envHost
			if rows.Scan(&eh.env, &eh.hid) == nil {
				envHosts = append(envHosts, eh)
			}
		}
		rows.Close()
	}
	for _, eh := range envHosts {
		if name := hostNameByID(d, eh.hid); name != "" {
			exp.HostEnvs = append(exp.HostEnvs, hostBindingDump{Env: eh.env, HostName: name})
		}
	}
	var buildHostID int64
	if d.QueryRow(`SELECT host_id FROM project_build_hosts WHERE project=?`, prefix).Scan(&buildHostID) == nil {
		if name := hostNameByID(d, buildHostID); name != "" {
			exp.BuildHost = &hostBindingDump{HostName: name}
		}
	}

	return json.MarshalIndent(exp, "", "  ")
}

// importProjectDB restores the project's DB config, replacing the project's
// existing rows. Best-effort: a failed row never aborts the surrounding restore.
func (h *Handler) importProjectDB(ws, project string, data []byte) {
	var exp projectDBExport
	if json.Unmarshal(data, &exp) != nil {
		return
	}
	d := h.db
	prefix := h.resourcePrefix(ws, project)

	// Pipelines (+ webhooks re-linked to the new pipeline id).
	d.Exec(`DELETE FROM pipeline_webhooks WHERE workspace=? AND project=?`, ws, project)  //nolint:errcheck
	d.Exec(`DELETE FROM pipelines WHERE workspace=? AND project=?`, ws, project)          //nolint:errcheck
	for _, p := range exp.Pipelines {
		res, err := d.Exec(`INSERT INTO pipelines (workspace, project, name, stages, enabled) VALUES (?,?,?,?,?)`,
			ws, project, p.Name, p.Stages, p.Enabled)
		if err != nil {
			continue
		}
		pid, _ := res.LastInsertId()
		for _, wk := range p.Webhooks {
			d.Exec(`INSERT INTO pipeline_webhooks (pipeline_id, workspace, project, token_hash, secret, enabled) VALUES (?,?,?,?,?,?)`, //nolint:errcheck
				pid, ws, project, wk.TokenHash, wk.Secret, wk.Enabled)
		}
	}

	// Flat config tables — replace the project's rows.
	d.Exec(`DELETE FROM alert_rules WHERE ws_key=? AND project=?`, ws, project) //nolint:errcheck
	importRows(d, "alert_rules", exp.AlertRules, "id")
	d.Exec(`DELETE FROM custom_domains WHERE workspace=? AND project=?`, ws, project) //nolint:errcheck
	importRows(d, "custom_domains", exp.CustomDomains, "id")
	d.Exec(`DELETE FROM env_maintenance WHERE workspace=? AND project=?`, ws, project) //nolint:errcheck
	importRows(d, "env_maintenance", exp.EnvMaintenance)

	// Host bindings — resolve host NAME → server-local id; drop if not present.
	d.Exec(`DELETE FROM workspace_host_envs WHERE project=?`, prefix) //nolint:errcheck
	for _, hb := range exp.HostEnvs {
		if hid := hostIDByName(d, hb.HostName); hid > 0 {
			d.Exec(`INSERT OR REPLACE INTO workspace_host_envs (project, env, host_id) VALUES (?,?,?)`, prefix, hb.Env, hid) //nolint:errcheck
		}
	}
	d.Exec(`DELETE FROM project_build_hosts WHERE project=?`, prefix) //nolint:errcheck
	if exp.BuildHost != nil {
		if hid := hostIDByName(d, exp.BuildHost.HostName); hid > 0 {
			d.Exec(`INSERT OR REPLACE INTO project_build_hosts (project, host_id) VALUES (?,?)`, prefix, hid) //nolint:errcheck
		}
	}
}

// exportRows returns every row of a query as column→value maps (byte slices are
// decoded to strings so they marshal to JSON cleanly).
func exportRows(d *db.DB, query string, args ...any) []map[string]any {
	out := []map[string]any{}
	rows, err := d.Query(query, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return out
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if rows.Scan(ptrs...) != nil {
			continue
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				m[c] = string(b)
			} else {
				m[c] = vals[i]
			}
		}
		out = append(out, m)
	}
	return out
}

// importRows inserts each map as a row, skipping the named columns (e.g. an
// autoincrement id). INSERT OR IGNORE tolerates a unique-key clash with another
// project's row rather than aborting.
func importRows(d *db.DB, table string, rows []map[string]any, drop ...string) {
	dropped := map[string]bool{}
	for _, c := range drop {
		dropped[c] = true
	}
	for _, row := range rows {
		var cols, ph []string
		var vals []any
		for c, v := range row {
			if dropped[c] {
				continue
			}
			cols = append(cols, c)
			ph = append(ph, "?")
			vals = append(vals, v)
		}
		if len(cols) == 0 {
			continue
		}
		q := fmt.Sprintf(`INSERT OR IGNORE INTO %s (%s) VALUES (%s)`, table, strings.Join(cols, ","), strings.Join(ph, ","))
		d.Exec(q, vals...) //nolint:errcheck
	}
}

func hostNameByID(d *db.DB, id int64) string {
	var name string
	d.QueryRow(`SELECT name FROM hosts WHERE id=?`, id).Scan(&name) //nolint:errcheck
	return name
}

func hostIDByName(d *db.DB, name string) int64 {
	var id int64
	d.QueryRow(`SELECT id FROM hosts WHERE name=?`, name).Scan(&id) //nolint:errcheck
	return id
}
