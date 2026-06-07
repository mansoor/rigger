package api

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/mansoor/rigger/ui/internal/alerts"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/shell"
)

// StartBackupScheduler runs enabled workspace backups once a day at hourUTC
// (Phase 11a). Mirrors StartHousekeepingScheduler: sleep until the next
// hour:00, run, repeat. Per-workspace schedule: daily = every day, weekly =
// Sundays, manual/unset = never.
func (h *Handler) StartBackupScheduler(hourUTC int) {
	go func() {
		for {
			now := time.Now().UTC()
			next := time.Date(now.Year(), now.Month(), now.Day(), hourUTC, 0, 0, 0, time.UTC)
			if now.After(next) {
				next = next.Add(24 * time.Hour)
			}
			time.Sleep(time.Until(next))
			h.runScheduledBackups(time.Now().UTC())
		}
	}()
}

func backupScheduleDue(schedule string, now time.Time) bool {
	switch schedule {
	case "daily":
		return true
	case "weekly":
		return now.Weekday() == time.Sunday
	default: // "manual" or unset
		return false
	}
}

func (h *Handler) runScheduledBackups(now time.Time) {
	entries, err := os.ReadDir(h.workspacesDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ws := e.Name()
		raw, err := os.ReadFile(filepath.Join(h.workspacesDir, ws, "config.json"))
		if err != nil {
			continue
		}
		cfg, err := h.readBackupCfg(ws)
		if err != nil || !cfg.Enabled || !backupScheduleDue(cfg.Schedule, now) {
			continue
		}
		for env := range configEnvNames(raw) {
			h.runScheduledBackupEnv(ws, env, cfg.TargetID)
		}
	}
}

// runScheduledBackupEnv runs one env backup, records the outcome, and pushes the
// resulting snapshot to the configured remote target (local-control-plane envs
// only — remote-host snapshots live on the host and aren't synced from here).
func (h *Handler) runScheduledBackupEnv(ws, env string, targetID *int64) {
	var out bytes.Buffer
	runErr := h.bridge.Run(shell.RunOptions{
		Workspace: ws, Command: "backup", Env: env, Extra: []string{"all"},
		Stdout: &out, Stderr: &out,
	})

	logStatus, msg := "ok", ""
	hkStatus := "ok"
	if runErr != nil {
		logStatus, msg, hkStatus = "error", runErr.Error(), "fail"
	}
	alerts.LogBackup(h.db, ws, env, logStatus, msg, 0)                              //nolint:errcheck
	h.logHousekeeping("backup:"+ws+":"+env, "scheduled", hkStatus, out.String(), 0, 0)

	if runErr != nil || targetID == nil {
		return
	}
	if host, _ := settings.HostForEnv(h.db, ws, env); host != nil {
		return // remote-host snapshot — not synced from the control plane
	}
	target, err := settings.GetBackupTarget(h.db, *targetID)
	if err != nil || target == nil {
		return
	}
	date, err := latestSnapshotDate(filepath.Join(h.workspacesDir, ws, "backups", env))
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	h.uploadSnapshot(ctx, ws, env, target, date) //nolint:errcheck
}
