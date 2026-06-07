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

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	res, syncErr := h.uploadSnapshot(ctx, name, env, target, date)
	if syncErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": syncErr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "target": target.Name, "files": res.Files, "bytes": res.Bytes, "date": date,
	})
}

// uploadSnapshot mirrors one env snapshot dir to a target and records the
// outcome in backup_syncs. Shared by SyncEnvBackup and the scheduler.
func (h *Handler) uploadSnapshot(ctx context.Context, ws, env string, target *settings.BackupTarget, date string) (backupsync.Result, error) {
	syncer, err := backupsync.New(*target)
	if err != nil {
		return backupsync.Result{}, err
	}
	snapDir := filepath.Join(h.workspacesDir, ws, "backups", env, date)
	res, syncErr := syncer.UploadDir(ctx, snapDir, path.Join(ws, env, date))
	status, msg := "ok", ""
	if syncErr != nil {
		status, msg = "fail", syncErr.Error()
	}
	h.recordBackupSync(ws, env, date, target, status, msg, res)
	return res, syncErr
}

// configBackupTarget reads backup.target_id from a workspace's config.json.
func (h *Handler) configBackupTarget(name string) (*int64, error) {
	cfg, err := h.readBackupCfg(name)
	if err != nil {
		return nil, err
	}
	return cfg.TargetID, nil
}

type wsBackupCfg struct {
	Enabled   bool   `json:"enabled"`
	TargetID  *int64 `json:"target_id"`
	Schedule  string `json:"schedule"`
	Retention int    `json:"retention"`
}

func (h *Handler) readBackupCfg(name string) (*wsBackupCfg, error) {
	raw, err := os.ReadFile(filepath.Join(h.workspacesDir, name, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("workspace not found")
	}
	var cfg struct {
		Backup wsBackupCfg `json:"backup"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config.json")
	}
	return &cfg.Backup, nil
}

// GET /api/workspaces/{name}/envs/{env}/backup-stats
// Per-env snapshot count, total size, oldest/newest dates, and the configured
// retention limit (11e).
func (h *Handler) GetBackupStats(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env := r.PathValue("env")
	dir := filepath.Join(h.workspacesDir, name, "backups", env)

	type stats struct {
		Count      int    `json:"count"`
		TotalBytes int64  `json:"total_bytes"`
		Oldest     string `json:"oldest"`
		Newest     string `json:"newest"`
		Retention  int    `json:"retention"`
		Schedule   string `json:"schedule"`
		Enabled    bool   `json:"enabled"`
	}
	st := stats{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st.Count++
		if st.Oldest == "" || e.Name() < st.Oldest {
			st.Oldest = e.Name()
		}
		if e.Name() > st.Newest {
			st.Newest = e.Name()
		}
		files, _ := os.ReadDir(filepath.Join(dir, e.Name()))
		for _, f := range files {
			if info, err := f.Info(); err == nil {
				st.TotalBytes += info.Size()
			}
		}
	}
	if cfg, err := h.readBackupCfg(name); err == nil {
		st.Retention = cfg.Retention
		st.Schedule = cfg.Schedule
		st.Enabled = cfg.Enabled
	}
	writeJSON(w, http.StatusOK, st)
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

// ── Full workspace archive (.rwb) remote sync (11a) ──────────────────────────

// uploadArchive uploads a single .rwb archive to a target and records the
// outcome in archive_syncs. Shared by the manual endpoint and auto-upload.
func (h *Handler) uploadArchive(ctx context.Context, filename string, target *settings.BackupTarget) (int64, error) {
	syncer, err := backupsync.New(*target)
	if err != nil {
		return 0, err
	}
	localPath := filepath.Join(archivesDir(h.dataDir), filename)
	n, upErr := syncer.UploadFile(ctx, localPath, path.Join("workspace-archives", filename))
	status, msg := "ok", ""
	if upErr != nil {
		status, msg = "fail", upErr.Error()
	}
	h.recordArchiveSync(filename, target, status, msg, n)
	return n, upErr
}

func (h *Handler) recordArchiveSync(filename string, t *settings.BackupTarget, status, msg string, bytes int64) {
	h.db.Exec(`
		INSERT INTO archive_syncs (filename, target_id, target_name, status, message, bytes, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(filename) DO UPDATE SET
			target_id=excluded.target_id, target_name=excluded.target_name,
			status=excluded.status, message=excluded.message, bytes=excluded.bytes,
			synced_at=CURRENT_TIMESTAMP`,
		filename, t.ID, t.Name, status, msg, bytes) //nolint:errcheck
}

func (h *Handler) archiveSyncStates() map[string]syncState {
	out := map[string]syncState{}
	rows, err := h.db.Query(`SELECT filename, target_name, status, synced_at FROM archive_syncs`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var fn, target, status, at string
		if err := rows.Scan(&fn, &target, &status, &at); err != nil {
			continue
		}
		out[fn] = syncState{Target: target, Status: status, SyncedAt: at}
	}
	return out
}

// POST /api/tools/workspace-archives/{filename}/sync
// Pushes a full .rwb archive to a target (body {target_id} override, else the
// source workspace's configured target).
func (h *Handler) SyncWorkspaceArchive(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if strings.Contains(filename, "/") || strings.Contains(filename, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename"})
		return
	}
	if _, err := os.Stat(filepath.Join(archivesDir(h.dataDir), filename)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "archive not found"})
		return
	}
	var body struct {
		TargetID *int64 `json:"target_id"`
	}
	_ = readJSON(r, &body)
	targetID := body.TargetID
	if targetID == nil {
		if t, err := h.configBackupTarget(wsNameFromArchive(filename)); err == nil {
			targetID = t
		}
	}
	if targetID == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no remote backup target configured for this workspace"})
		return
	}
	target, err := settings.GetBackupTarget(h.db, *targetID)
	if err != nil || target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "backup target not found"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	n, upErr := h.uploadArchive(ctx, filename, target)
	if upErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": upErr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "target": target.Name, "bytes": n})
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
