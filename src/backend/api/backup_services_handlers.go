package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// backupServiceInfo is a candidate data-bearing service for the backup pickers.
type backupServiceInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"` // database | volume | service
	Hint  string `json:"hint"`
}

// GET /api/workspaces/{name}/envs/{env}/backup-services
// Lists the env's services the user can choose to back up, with a hint about how
// each would be captured (SQL dump for recognised DB images, else volumes). The
// user decides — we never assume which service is "the database".
func (h *Handler) GetBackupServices(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env := r.PathValue("env")
	raw, err := os.ReadFile(filepath.Join(h.workspacesDir, name, "config.json"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	var cfg struct {
		Project struct {
			Type string `json:"type"`
		} `json:"project"`
		Images []struct {
			Name  string `json:"name"`
			Image string `json:"image"`
		} `json:"images"`
		Environments map[string]struct {
			Database      string `json:"database"`
			GarageEnabled bool   `json:"garage_enabled"`
		} `json:"environments"`
	}
	_ = json.Unmarshal(raw, &cfg)

	var out []backupServiceInfo
	if cfg.Project.Type == "image" {
		for _, img := range cfg.Images {
			info := backupServiceInfo{ID: img.Name, Label: img.Name, Kind: "service", Hint: "Container volumes (if any)"}
			switch low := strings.ToLower(img.Image); {
			case strings.Contains(low, "postgres"):
				info.Kind, info.Hint = "database", "PostgreSQL — SQL dump"
			case strings.Contains(low, "mariadb"):
				info.Kind, info.Hint = "database", "MariaDB — SQL dump"
			case strings.Contains(low, "mysql"):
				info.Kind, info.Hint = "database", "MySQL — SQL dump"
			}
			out = append(out, info)
		}
	} else {
		e := cfg.Environments[env]
		if e.Database != "" && e.Database != "none" {
			out = append(out, backupServiceInfo{ID: "database", Label: "Database", Kind: "database", Hint: e.Database + " — SQL dump"})
		}
		out = append(out, backupServiceInfo{ID: "uploads", Label: "App uploads", Kind: "volume", Hint: "Uploads volume"})
		if e.GarageEnabled {
			out = append(out, backupServiceInfo{ID: "garage", Label: "Garage S3 data", Kind: "volume", Hint: "Garage object-store volumes"})
		}
	}
	if out == nil {
		out = []backupServiceInfo{}
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Snapshot manifest (Phase 11 per-env schedules) ───────────────────────────

const snapshotManifestFile = "backup-manifest.json"

type snapshotManifest struct {
	ScheduleID   string   `json:"schedule_id"`
	ScheduleName string   `json:"schedule_name"`
	Services     []string `json:"services"` // empty = all
	Trigger      string   `json:"trigger"`  // scheduled | manual
	CreatedAt    string   `json:"created_at"`
}

func readSnapshotManifest(snapDir string) *snapshotManifest {
	raw, err := os.ReadFile(filepath.Join(snapDir, snapshotManifestFile))
	if err != nil {
		return nil
	}
	var m snapshotManifest
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return &m
}
