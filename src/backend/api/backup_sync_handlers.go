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
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wspath"
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
	h.testBackupTargetByID(w, r, id)
}

func (h *Handler) testBackupTargetByID(w http.ResponseWriter, r *http.Request, id int64) {
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
	ws := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")
	if ws == "" || name == "" || env == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace, project and env are required"})
		return
	}
	pkey := ws + "_" + name

	// Remote-host envs keep their snapshots on the remote box — not yet synced.
	if host, _ := settings.HostForEnv(h.db, pkey, env); host != nil {
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
		targetID = h.firstScheduleTarget(ws, name, env)
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
	// Pool guard (Phase 3): the target must be one this workspace can use.
	if inPool, _ := settings.TargetInWorkspacePool(h.db, ws, *targetID); !inPool {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "that backup target is not available to this workspace"})
		return
	}

	// Locate the snapshot directory.
	envBackups := wspath.EnvBackupsDir(h.workspacesDir, ws, name, env)
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
	res, syncErr := h.uploadSnapshot(ctx, ws, name, env, target, date)
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
func (h *Handler) uploadSnapshot(ctx context.Context, ws, name, env string, target *settings.BackupTarget, date string) (backupsync.Result, error) {
	syncer, err := backupsync.New(*target)
	if err != nil {
		return backupsync.Result{}, err
	}
	pkey := ws + "_" + name
	snapDir := filepath.Join(wspath.EnvBackupsDir(h.workspacesDir, ws, name, env), date)
	res, syncErr := syncer.UploadDir(ctx, snapDir, path.Join(pkey, env, date))
	status, msg := "ok", ""
	if syncErr != nil {
		status, msg = "fail", syncErr.Error()
	}
	h.recordBackupSync(pkey, env, date, target, status, msg, res)
	return res, syncErr
}

// readEnvSchedules returns the per-env backup schedules for one environment
// (Phase 11 per-env redesign). nil if none / unreadable.
func (h *Handler) readEnvSchedules(ws, name, env string) []workspace.BackupSchedule {
	raw, err := os.ReadFile(h.projectConfigPath(ws, name))
	if err != nil {
		return nil
	}
	var cfg struct {
		Environments map[string]struct {
			BackupSchedules []workspace.BackupSchedule `json:"backup_schedules"`
		} `json:"environments"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return nil
	}
	return cfg.Environments[env].BackupSchedules
}

// projectConfigPath resolves a project's config.json. With a workspace tier it's
// the nested path; ws=="" falls back to the legacy flat layout (used only by the
// peripheral .rwb archive sync, which knows only the project name).
func (h *Handler) projectConfigPath(ws, name string) string {
	if ws == "" {
		return filepath.Join(h.workspacesDir, name, "config.json")
	}
	return wspath.ConfigPath(h.workspacesDir, ws, name)
}

// firstScheduleTarget returns the target of the first enabled schedule that has
// one — within env, or across all envs when env=="". Used as the default target
// for manual snapshot/archive syncs. nil = no remote target configured.
func (h *Handler) firstScheduleTarget(ws, name, env string) *int64 {
	raw, err := os.ReadFile(h.projectConfigPath(ws, name))
	if err != nil {
		return nil
	}
	var cfg struct {
		Environments map[string]struct {
			BackupSchedules []workspace.BackupSchedule `json:"backup_schedules"`
		} `json:"environments"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return nil
	}
	for envName, e := range cfg.Environments {
		if env != "" && envName != env {
			continue
		}
		for _, s := range e.BackupSchedules {
			if s.Enabled && s.TargetID != nil {
				return s.TargetID
			}
		}
	}
	return nil
}

// GET /api/workspaces/{name}/envs/{env}/backup-stats
// Per-env snapshot count, total size, oldest/newest dates, and a summary of the
// env's backup schedules (Phase 11 per-env redesign).
func (h *Handler) GetBackupStats(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	name := r.PathValue("name")
	env := r.PathValue("env")
	dir := wspath.EnvBackupsDir(h.workspacesDir, ws, name, env)

	type stats struct {
		Count          int    `json:"count"`
		TotalBytes     int64  `json:"total_bytes"`
		Oldest         string `json:"oldest"`
		Newest         string `json:"newest"`
		Schedules      int    `json:"schedules"`       // total schedules defined
		ActiveSchedule int    `json:"active_schedules"` // enabled schedules
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
	for _, s := range h.readEnvSchedules(ws, name, env) {
		st.Schedules++
		if s.Enabled {
			st.ActiveSchedule++
		}
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
		INSERT INTO backup_syncs (project, env, date, target_id, target_name, status, message, files, bytes, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project, env, date) DO UPDATE SET
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
		// Resolve the source project's configured target via the archive manifest.
		if m, ok := readArchiveManifest(filepath.Join(archivesDir(h.dataDir), filename)); ok {
			targetID = h.firstScheduleTarget(m.Workspace, m.Project, "")
		} else {
			targetID = h.firstScheduleTarget("", wsNameFromArchive(filename), "")
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

// ── Backup health coverage (11b) ─────────────────────────────────────────────

// GET /api/backups/coverage
// Per workspace/env: schedule, last-backup age, snapshot count, remote-sync
// state, and a health verdict (current | stale | never | disabled).
func (h *Handler) GetBackupCoverage(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Workspace  string     `json:"workspace"`
		Project    string     `json:"project"`
		Env        string     `json:"env"`
		Enabled    bool       `json:"enabled"`   // any schedule enabled
		Schedules  int        `json:"schedules"` // total schedules defined
		Summary    string     `json:"summary"`   // enabled frequencies, e.g. "every 4h · daily"
		Count      int        `json:"count"`
		LastBackup string     `json:"last_backup"`
		AgeHours   float64    `json:"age_hours"` // -1 = never
		Health     string     `json:"health"`
		Sync       *syncState `json:"sync,omitempty"`
	}

	syncStates := h.backupSyncStates()
	var rows []row
	wsEntries, _ := os.ReadDir(h.workspacesDir)
	for _, we := range wsEntries {
		if !we.IsDir() {
			continue
		}
		ws := we.Name()
		projEntries, perr := os.ReadDir(wspath.ProjectsDir(h.workspacesDir, ws))
		if perr != nil {
			continue
		}
		for _, pe := range projEntries {
			if !pe.IsDir() {
				continue
			}
			name := pe.Name()
			pkey := ws + "_" + name
			raw, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, ws, name))
			if err != nil {
				continue
			}
			for env := range configEnvNames(raw) {
				rw := row{Workspace: ws, Project: name, Env: env, AgeHours: -1}

				minInterval := 0
				seen := map[string]bool{}
				var freqs []string
				for _, s := range h.readEnvSchedules(ws, name, env) {
					rw.Schedules++
					if !s.Enabled {
						continue
					}
					rw.Enabled = true
					if minInterval == 0 || s.IntervalHours < minInterval {
						minInterval = s.IntervalHours
					}
					if lbl := intervalLabel(s.IntervalHours); !seen[lbl] {
						seen[lbl] = true
						freqs = append(freqs, lbl)
					}
				}
				rw.Summary = strings.Join(freqs, " · ")

				envDir := wspath.EnvBackupsDir(h.workspacesDir, ws, name, env)
				newest := ""
				if ents, err := os.ReadDir(envDir); err == nil {
					for _, s := range ents {
						if s.IsDir() {
							rw.Count++
							if s.Name() > newest {
								newest = s.Name()
							}
						}
					}
				}
				if newest != "" {
					rw.LastBackup = newest
					if fi, err := os.Stat(filepath.Join(envDir, newest)); err == nil {
						rw.AgeHours = time.Since(fi.ModTime()).Hours()
					}
					if st, ok := syncStates[pkey+"\x00"+env+"\x00"+newest]; ok {
						s := st
						rw.Sync = &s
					}
				}
				rw.Health = backupHealth(rw.Enabled, minInterval, newest != "", rw.AgeHours)
				rows = append(rows, rw)
			}
		}
	}
	if rows == nil {
		rows = []row{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// intervalLabel renders an interval in hours as a short label.
func intervalLabel(h int) string {
	switch h {
	case 24:
		return "daily"
	case 168:
		return "weekly"
	default:
		return fmt.Sprintf("every %dh", h)
	}
}

// backupHealth classifies coverage from the env's shortest enabled interval.
// "disabled" = no enabled schedule; else current / stale (overdue past 1.5× the
// interval) / never.
func backupHealth(enabled bool, minIntervalH int, hasBackup bool, ageH float64) string {
	if !enabled || minIntervalH <= 0 {
		return "disabled"
	}
	if !hasBackup {
		return "never"
	}
	if ageH > float64(minIntervalH)*1.5 {
		return "stale"
	}
	return "current"
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
	rows, err := h.db.Query(`SELECT project, env, date, target_name, status, synced_at FROM backup_syncs`)
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
