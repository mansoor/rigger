package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/backupsync"
	"github.com/mansoor/rigger/ui/internal/settings"
)

// POST /api/settings/backup-targets/{id}/test
// Verifies connectivity + credentials for an S3/SFTP target.
func (h *Handler) TestBackupTarget(w http.ResponseWriter, r *http.Request) {
	clean := strings.TrimSuffix(r.URL.Path, "/test")
	id, err := parseSettingsID(clean, "/api/settings/backup-targets/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	target, err := settings.GetBackupTarget(h.db, id)
	if err != nil || target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "backup target not found"})
		return
	}
	syncer, err := backupsync.New(*target)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := syncer.Test(ctx); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Connection succeeded"})
}

// POST /api/workspaces/{name}/envs/{env}/backup-sync
// Uploads the latest local snapshot for this env to the workspace's configured
// remote backup target. Body may override {"target_id": N}; otherwise the
// target from config.json's backup block is used.
func (h *Handler) SyncEnvBackup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env := r.PathValue("env")
	if name == "" || env == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace and env are required"})
		return
	}

	// Remote-host envs keep their snapshots on the remote box — not yet synced.
	if host, _ := settings.HostForEnv(h.db, name, env); host != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "backup sync is not yet supported for environments running on a remote host",
		})
		return
	}

	// Resolve target: explicit body override, else config.json backup.target_id.
	var body struct {
		TargetID *int64 `json:"target_id"`
		Date     string `json:"date"` // optional specific snapshot; default = latest
	}
	_ = readJSON(r, &body) // body is optional

	targetID := body.TargetID
	if targetID == nil {
		cfgTarget, err := h.configBackupTarget(name)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		targetID = cfgTarget
	}
	if targetID == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "no remote backup target configured for this workspace",
		})
		return
	}

	target, err := settings.GetBackupTarget(h.db, *targetID)
	if err != nil || target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "backup target not found"})
		return
	}

	// Locate the snapshot directory.
	envBackups := filepath.Join(h.workspacesDir, name, "backups", env)
	date := body.Date
	if date == "" {
		date, err = latestSnapshotDate(envBackups)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no local snapshots to sync"})
			return
		}
	}
	snapDir := filepath.Join(envBackups, date)
	if fi, err := os.Stat(snapDir); err != nil || !fi.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "snapshot not found: " + date})
		return
	}

	syncer, err := backupsync.New(*target)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	keyPrefix := path.Join(name, env, date)
	res, syncErr := syncer.UploadDir(ctx, snapDir, keyPrefix)

	// Record outcome (ok or fail) so the UI can show a badge either way.
	status, msg := "ok", ""
	if syncErr != nil {
		status, msg = "fail", syncErr.Error()
	}
	h.recordBackupSync(name, env, date, target, status, msg, res)

	if syncErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": syncErr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "target": target.Name, "files": res.Files, "bytes": res.Bytes, "date": date,
	})
}

// configBackupTarget reads backup.target_id from a workspace's config.json.
func (h *Handler) configBackupTarget(name string) (*int64, error) {
	raw, err := os.ReadFile(filepath.Join(h.workspacesDir, name, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("workspace not found")
	}
	var cfg struct {
		Backup struct {
			TargetID *int64 `json:"target_id"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config.json")
	}
	return cfg.Backup.TargetID, nil
}

// latestSnapshotDate returns the newest snapshot dir name (they sort
// chronologically as YYYY-MM-DD_HH-MM-SS).
func latestSnapshotDate(envBackupsDir string) (string, error) {
	entries, err := os.ReadDir(envBackupsDir)
	if err != nil {
		return "", err
	}
	latest := ""
	for _, e := range entries {
		if e.IsDir() && e.Name() > latest {
			latest = e.Name()
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no snapshots")
	}
	return latest, nil
}

func (h *Handler) recordBackupSync(ws, env, date string, t *settings.BackupTarget, status, msg string, res backupsync.Result) {
	h.db.Exec(`
		INSERT INTO backup_syncs (workspace, env, date, target_id, target_name, status, message, files, bytes, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(workspace, env, date) DO UPDATE SET
			target_id=excluded.target_id, target_name=excluded.target_name,
			status=excluded.status, message=excluded.message,
			files=excluded.files, bytes=excluded.bytes, synced_at=CURRENT_TIMESTAMP`,
		ws, env, date, t.ID, t.Name, status, msg, res.Files, res.Bytes) //nolint:errcheck
}

// syncState is the per-snapshot sync info joined into ListBackups.
type syncState struct {
	Target   string `json:"target"`
	Status   string `json:"status"`
	SyncedAt string `json:"synced_at"`
}

// backupSyncStates returns a map keyed by "workspace\x00env\x00date".
func (h *Handler) backupSyncStates() map[string]syncState {
	out := map[string]syncState{}
	rows, err := h.db.Query(`SELECT workspace, env, date, target_name, status, synced_at FROM backup_syncs`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var ws, env, date, target, status, at string
		if err := rows.Scan(&ws, &env, &date, &target, &status, &at); err != nil {
			continue
		}
		out[ws+"\x00"+env+"\x00"+date] = syncState{Target: target, Status: status, SyncedAt: at}
	}
	return out
}
