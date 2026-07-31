package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/mansoor/rigger/ui/internal/alerts"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/shell"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

const schedulerTick = 30 * time.Minute

// StartBackupScheduler runs per-env backup schedules on an interval (Phase 11
// per-env redesign). It ticks every 30 min; each schedule runs when at least
// its interval_hours have elapsed since its last run (tracked in
// backup_schedule_runs, so it survives restarts).
func (h *Handler) StartBackupScheduler() {
	go func() {
		time.Sleep(60 * time.Second) // let the app settle after startup
		h.runDueBackups(time.Now().UTC())
		ticker := time.NewTicker(schedulerTick)
		defer ticker.Stop()
		for range ticker.C {
			h.runDueBackups(time.Now().UTC())
		}
	}()
}

func (h *Handler) runDueBackups(now time.Time) {
	wsEntries, err := os.ReadDir(h.workspacesDir)
	if err != nil {
		return
	}
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
			raw, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, ws, name))
			if err != nil {
				continue
			}
			for env := range configEnvNames(raw) {
				for _, s := range h.readEnvSchedules(ws, name, env) {
					if !s.Enabled || s.IntervalHours <= 0 {
						continue
					}
					last := h.scheduleLastRun(ws+"_"+name, env, s.ID)
					interval := time.Duration(s.IntervalHours) * time.Hour
					// Small slack so a slightly-early tick still fires on schedule.
					if !last.IsZero() && now.Sub(last) < interval-time.Minute {
						continue
					}
					h.runScheduledBackup(ws, name, env, s)
				}
			}
		}
	}
}

// runScheduledBackup executes one schedule: records the run up front (so an
// overlapping tick can't double-fire), backs up the schedule's services, logs,
// syncs to the target, and prunes to the schedule's retention.
func (h *Handler) runScheduledBackup(ws, name, env string, s workspace.BackupSchedule) {
	pkey := ws + "_" + name
	h.recordScheduleRun(pkey, env, s.ID)

	var out bytes.Buffer
	runErr := h.bridge.Run(shell.RunOptions{
		Workspace: ws, Project: name, Command: "backup", Env: env, Extra: []string{"all"},
		Services: s.Services, ScheduleID: s.ID, ScheduleName: s.Name, Trigger: "scheduled",
		Stdout: &out, Stderr: &out,
	})

	logStatus, msg, hkStatus := "ok", "", "ok"
	if runErr != nil {
		logStatus, msg, hkStatus = "error", runErr.Error(), "fail"
	}
	alerts.LogBackup(h.db, pkey, env, logStatus, msg, 0) //nolint:errcheck
	label := s.Name
	if label == "" {
		label = s.ID
	}
	// A scheduled backup runs on the environment's own host, which may be remote —
	// record that rather than defaulting to the control plane.
	hkHost := h.envHostName(pkey, env)
	if hkHost == "" {
		hkHost = controlPlaneLabel
	}
	h.logHousekeeping(hkHost, "backup:"+pkey+":"+env+":"+label, "scheduled", hkStatus, out.String(), 0, 0)
	if runErr != nil {
		return
	}

	// Sync the new snapshot to this schedule's target (local-control-plane only).
	if s.TargetID != nil {
		if host, _ := settings.HostForEnv(h.db, pkey, env); host == nil {
			if target, err := settings.GetBackupTarget(h.db, *s.TargetID); err == nil && target != nil {
				if date, err := latestSnapshotDate(wspath.EnvBackupsDir(h.workspacesDir, ws, name, env)); err == nil {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
					h.uploadSnapshot(ctx, ws, name, env, target, date) //nolint:errcheck
					cancel()
				}
			}
		}
	}

	h.pruneScheduleSnapshots(ws, name, env, s.ID, s.Retention)
}

// pruneScheduleSnapshots keeps the newest `retention` snapshots that belong to a
// schedule (matched via the snapshot manifest). retention<=0 = keep all.
func (h *Handler) pruneScheduleSnapshots(ws, name, env, scheduleID string, retention int) {
	if retention <= 0 {
		return
	}
	envDir := wspath.EnvBackupsDir(h.workspacesDir, ws, name, env)
	ents, err := os.ReadDir(envDir)
	if err != nil {
		return
	}
	var dates []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if m := readSnapshotManifest(filepath.Join(envDir, e.Name())); m != nil && m.ScheduleID == scheduleID {
			dates = append(dates, e.Name())
		}
	}
	if len(dates) <= retention {
		return
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	for _, d := range dates[retention:] {
		os.RemoveAll(filepath.Join(envDir, d)) //nolint:errcheck
	}
}

func (h *Handler) scheduleLastRun(pkey, env, id string) time.Time {
	var ts string
	h.db.QueryRow(`SELECT last_run_at FROM backup_schedule_runs WHERE project=? AND env=? AND schedule_id=?`,
		pkey, env, id).Scan(&ts) //nolint:errcheck
	if ts == "" {
		return time.Time{}
	}
	if t, err := time.Parse("2006-01-02 15:04:05", ts); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func (h *Handler) recordScheduleRun(pkey, env, id string) {
	h.db.Exec(`
		INSERT INTO backup_schedule_runs (project, env, schedule_id, last_run_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project, env, schedule_id) DO UPDATE SET last_run_at=CURRENT_TIMESTAMP`,
		pkey, env, id) //nolint:errcheck
}

// MigrateBackupConfig is a one-time migration (Phase 11 per-env redesign) that
// converts a legacy workspace-level config.backup into an equivalent per-env
// backup schedule, then removes the old key. Guarded by an app_settings flag so
// it runs once.
func (h *Handler) MigrateBackupConfig() {
	var done string
	h.db.QueryRow(`SELECT value FROM app_settings WHERE key='backup_migrated_v2'`).Scan(&done) //nolint:errcheck
	if done == "1" {
		return
	}
	wsEntries, err := os.ReadDir(h.workspacesDir)
	if err == nil {
		for _, we := range wsEntries {
			if !we.IsDir() {
				continue
			}
			projEntries, perr := os.ReadDir(wspath.ProjectsDir(h.workspacesDir, we.Name()))
			if perr != nil {
				continue
			}
			for _, pe := range projEntries {
				if pe.IsDir() {
					migrateWorkspaceBackup(wspath.ConfigPath(h.workspacesDir, we.Name(), pe.Name()))
				}
			}
		}
	}
	h.db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES ('backup_migrated_v2','1',CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value='1', updated_at=CURRENT_TIMESTAMP`) //nolint:errcheck
}

func migrateWorkspaceBackup(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var cfg map[string]any
	if json.Unmarshal(raw, &cfg) != nil {
		return
	}
	bk, ok := cfg["backup"].(map[string]any)
	if !ok {
		return // nothing legacy to migrate
	}
	enabled, _ := bk["enabled"].(bool)
	schedule, _ := bk["schedule"].(string)
	interval := 24
	if schedule == "weekly" {
		interval = 168
	}
	envs, _ := cfg["environments"].(map[string]any)

	// Only synthesize schedules when the old config was an active daily/weekly one.
	if enabled && schedule != "manual" && envs != nil {
		i := 0
		for envName, ev := range envs {
			em, _ := ev.(map[string]any)
			if em == nil {
				continue
			}
			if _, has := em["backup_schedules"]; has {
				continue
			}
			sched := map[string]any{
				"id":             fmt.Sprintf("bk_mig_%d", i),
				"name":           "Migrated backup",
				"services":       []any{}, // all
				"interval_hours": interval,
				"target_id":      bk["target_id"],
				"retention":      bk["retention"],
				"enabled":        true,
			}
			em["backup_schedules"] = []any{sched}
			envs[envName] = em
			i++
		}
	}

	delete(cfg, "backup")
	if out, err := json.MarshalIndent(cfg, "", "  "); err == nil {
		os.WriteFile(path, out, 0o644) //nolint:errcheck
	}
}
