// Package deployhistory records, per environment, the resolved image references at
// each deploy so a custom stack can be rolled back to a prior image set (Phase 9e).
// Image-type stacks are recorded for audit only — their tags aren't per-env
// overridable, so they roll back via the backup/restore path instead.
package deployhistory

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

const keepPerEnv = 30

// Entry is one recorded deploy of an environment.
type Entry struct {
	ID        int64             `json:"id"`
	Workspace string            `json:"workspace"`
	Project   string            `json:"project"`
	Env       string            `json:"env"`
	Ptype     string            `json:"ptype"`   // custom | image
	Images    map[string]string `json:"images"`  // service → image:tag (custom only)
	Version   string            `json:"version"` // custom: VersionString() at deploy
	Username  string            `json:"username"`
	CreatedAt int64             `json:"created_at"` // epoch ms
}

// Record inserts an entry and prunes the env's history to keepPerEnv.
func Record(d *db.DB, e Entry) error {
	imgs, _ := json.Marshal(e.Images)
	if _, err := d.Exec(
		`INSERT INTO deploy_history (workspace, project, env, ptype, images, version, username, created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		e.Workspace, e.Project, e.Env, e.Ptype, string(imgs), e.Version, e.Username, e.CreatedAt,
	); err != nil {
		return err
	}
	d.Exec( //nolint:errcheck
		`DELETE FROM deploy_history WHERE workspace=? AND project=? AND env=? AND id NOT IN
		   (SELECT id FROM deploy_history WHERE workspace=? AND project=? AND env=? ORDER BY id DESC LIMIT ?)`,
		e.Workspace, e.Project, e.Env, e.Workspace, e.Project, e.Env, keepPerEnv,
	)
	return nil
}

// List returns up to limit recent entries for an env, newest first.
func List(d *db.DB, workspace, project, env string, limit int) ([]Entry, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := d.Query(
		`SELECT id, workspace, project, env, ptype, images, version, username, created_at
		   FROM deploy_history WHERE workspace=? AND project=? AND env=? ORDER BY id DESC LIMIT ?`,
		workspace, project, env, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// Get returns one entry by id.
func Get(d *db.DB, id int64) (*Entry, error) {
	return scan(d.QueryRow(
		`SELECT id, workspace, project, env, ptype, images, version, username, created_at
		   FROM deploy_history WHERE id=?`, id))
}

type scanner interface{ Scan(dest ...any) error }

func scan(s scanner) (*Entry, error) {
	var e Entry
	var imgs string
	if err := s.Scan(&e.ID, &e.Workspace, &e.Project, &e.Env, &e.Ptype, &imgs, &e.Version, &e.Username, &e.CreatedAt); err != nil {
		return nil, err
	}
	e.Images = map[string]string{}
	if imgs != "" {
		json.Unmarshal([]byte(imgs), &e.Images) //nolint:errcheck
	}
	return &e, nil
}

// Resolve computes the deploy entry for an env at deploy time from config.json +
// the env's .env. For custom stacks it captures each built service's effective
// image ref (the .env override if set, else the version-derived ImageTag) plus the
// version string; image stacks return ptype=image with no image map.
func Resolve(workspacesDir, workspace, project, env string) Entry {
	e := Entry{Workspace: workspace, Project: project, Env: env, Ptype: "custom", Images: map[string]string{}}
	raw, err := os.ReadFile(wspath.ConfigPath(workspacesDir, workspace, project))
	if err != nil {
		return e
	}
	cfg, err := wsconfig.Parse(raw)
	if err != nil {
		return e
	}
	e.Ptype = cfg.ProjectType()
	if e.Ptype == "image" {
		// Image stacks: capture the configured images[].image:tag (project-level).
		var ic struct {
			Images []struct {
				Name  string `json:"name"`
				Image string `json:"image"`
				Tag   string `json:"tag"`
			} `json:"images"`
		}
		json.Unmarshal(raw, &ic) //nolint:errcheck
		for _, im := range ic.Images {
			ref := im.Image
			if im.Tag != "" {
				ref += ":" + im.Tag
			}
			key := im.Name
			if key == "" {
				key = im.Image
			}
			if ref != "" {
				e.Images[key] = ref
			}
		}
		return e
	}
	e.Version = cfg.VersionString()
	envCfg := cfg.Environments[env]
	dotEnv := readDotEnv(wspath.DotEnv(workspacesDir, workspace, project, env))
	resolveSvc := func(svc, overrideKey string) {
		if v := dotEnv[overrideKey]; v != "" {
			e.Images[svc] = v
		} else {
			e.Images[svc] = cfg.ImageTag(svc, env)
		}
	}
	resolveSvc("backend", "BACKEND_IMAGE")
	if envCfg.FrontendEnabled {
		resolveSvc("frontend", "FRONTEND_IMAGE")
	}
	return e
}

// OverrideKey maps a built service name to its compose image-override env var.
func OverrideKey(service string) string {
	switch service {
	case "frontend":
		return "FRONTEND_IMAGE"
	default:
		return "BACKEND_IMAGE"
	}
}

// readDotEnv is a minimal KEY=VALUE parser (comments/blank lines ignored).
func readDotEnv(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}
