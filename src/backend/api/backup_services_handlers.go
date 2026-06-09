package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansoor/rigger/ui/internal/wspath"
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
	ws := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")
	raw, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	var cfg struct {
		Services []struct {
			Name  string          `json:"name"`
			Image string          `json:"image"`
			Build json.RawMessage `json:"build"`
		} `json:"services"`
		Environments map[string]struct {
			Database      string `json:"database"`
			GarageEnabled bool   `json:"garage_enabled"`
		} `json:"environments"`
	}
	_ = json.Unmarshal(raw, &cfg)

	e := cfg.Environments[env]
	var out []backupServiceInfo
	hasBuild := false

	// The managed database (env toggle) backs up as a SQL dump.
	if e.Database != "" && e.Database != "none" {
		out = append(out, backupServiceInfo{ID: "database", Label: "Database", Kind: "database", Hint: e.Database + " — SQL dump"})
	}

	// Pull-image services that look like a database are dump candidates too.
	for _, s := range cfg.Services {
		if len(s.Build) > 0 {
			hasBuild = true
			continue
		}
		if s.Image == "" {
			continue
		}
		info := backupServiceInfo{ID: s.Name, Label: s.Name, Kind: "service", Hint: "Container volumes (if any)"}
		switch low := strings.ToLower(s.Image); {
		case strings.Contains(low, "postgres"):
			info.Kind, info.Hint = "database", "PostgreSQL — SQL dump"
		case strings.Contains(low, "mariadb"):
			info.Kind, info.Hint = "database", "MariaDB — SQL dump"
		case strings.Contains(low, "mysql"):
			info.Kind, info.Hint = "database", "MySQL — SQL dump"
		}
		out = append(out, info)
	}

	// App build services typically have an uploads volume worth capturing.
	if hasBuild {
		out = append(out, backupServiceInfo{ID: "uploads", Label: "App uploads", Kind: "volume", Hint: "Uploads volume"})
	}
	if e.GarageEnabled {
		out = append(out, backupServiceInfo{ID: "garage", Label: "Garage S3 data", Kind: "volume", Hint: "Garage object-store volumes"})
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
