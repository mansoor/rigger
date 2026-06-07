package db

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

func Open(dataDir string) (*DB, error) {
	path := filepath.Join(dataDir, "rigger.db")
	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conn.SetMaxOpenConns(1) // SQLite is single-writer
	d := &DB{conn}
	if err := d.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

func (d *DB) migrate() error {
	_, err := d.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			username   TEXT    NOT NULL UNIQUE,
			password   TEXT    NOT NULL,
			role       TEXT    NOT NULL DEFAULT 'admin',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS audit_log (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id     INTEGER REFERENCES users(id),
			username    TEXT,
			workspace   TEXT,
			command     TEXT,
			env         TEXT,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		-- Recorded action runs (Action output history): one row per run with the
		-- captured output and result, kept per workspace (pruned in actionruns.Record).
		CREATE TABLE IF NOT EXISTS action_runs (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace   TEXT    NOT NULL,
			env         TEXT,
			command     TEXT    NOT NULL,
			extra       TEXT,
			username    TEXT,
			status      TEXT    NOT NULL,  -- ok | fail
			output      TEXT,
			started_at  INTEGER,           -- epoch ms
			finished_at INTEGER            -- epoch ms
		);
		CREATE INDEX IF NOT EXISTS idx_action_runs_ws ON action_runs(workspace, id);

		CREATE TABLE IF NOT EXISTS backup_targets (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT    NOT NULL UNIQUE,
			type       TEXT    NOT NULL,
			config     TEXT    NOT NULL DEFAULT '{}',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS docker_registries (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT    NOT NULL UNIQUE,
			url        TEXT    NOT NULL,
			username   TEXT    NOT NULL,
			password   TEXT    NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS housekeeping_log (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			task         TEXT    NOT NULL,
			trigger      TEXT    NOT NULL DEFAULT 'manual',
			status       TEXT    NOT NULL DEFAULT 'ok',
			output       TEXT,
			freed_bytes  INTEGER DEFAULT 0,
			items_removed INTEGER DEFAULT 0,
			created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS app_settings (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL DEFAULT '',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS template_usage (
			name         TEXT PRIMARY KEY,
			use_count    INTEGER  NOT NULL DEFAULT 1,
			last_used_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		-- ── Phase 6: Observability & Alerting ──────────────────────────────────
		-- 6a: Alert rules. A rule targets a workspace+env (specific), a workspace
		-- (all its envs), or nothing (all workspaces). condition_type drives which
		-- metric the evaluator reads; threshold is used by the numeric conditions
		-- (restart_count, *_above_pct) and ignored by the boolean ones.
		CREATE TABLE IF NOT EXISTS alert_rules (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			name             TEXT    NOT NULL,
			condition_type   TEXT    NOT NULL,
			threshold        REAL    NOT NULL DEFAULT 0,
			workspace        TEXT    NOT NULL DEFAULT '',
			env              TEXT    NOT NULL DEFAULT '',
			severity         TEXT    NOT NULL DEFAULT 'warning',
			cooldown_minutes INTEGER NOT NULL DEFAULT 15,
			enabled          INTEGER NOT NULL DEFAULT 1,
			created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at       DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		-- 6c: Alert events (history + inbox). Rule fields are denormalised so an
		-- event survives deletion of its rule. resolved_at IS NULL ⇒ still active;
		-- dismissed=1 ⇒ acknowledged (drops out of the unread badge count).
		CREATE TABLE IF NOT EXISTS alert_events (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			rule_id        INTEGER REFERENCES alert_rules(id) ON DELETE SET NULL,
			rule_name      TEXT    NOT NULL DEFAULT '',
			condition_type TEXT    NOT NULL DEFAULT '',
			workspace      TEXT    NOT NULL DEFAULT '',
			env            TEXT    NOT NULL DEFAULT '',
			message        TEXT    NOT NULL,
			severity       TEXT    NOT NULL DEFAULT 'warning',
			value          REAL    NOT NULL DEFAULT 0,
			fired_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
			resolved_at    DATETIME,
			dismissed      INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_alert_events_open
			ON alert_events(rule_id, workspace, env, resolved_at);
		CREATE INDEX IF NOT EXISTS idx_alert_events_inbox
			ON alert_events(dismissed, fired_at);

		-- backup_log: per-env backup outcomes. The shell-bridge backup action
		-- records success/failure here so the backup_failed alert condition has a
		-- source (audit_log only records that a backup ran, not whether it worked).
		-- Also seeds Phase 11 (Backup Verification & Scheduling).
		CREATE TABLE IF NOT EXISTS backup_log (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace  TEXT    NOT NULL,
			env        TEXT    NOT NULL,
			status     TEXT    NOT NULL DEFAULT 'ok',
			message    TEXT    NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_backup_log_target
			ON backup_log(workspace, env, created_at);

		-- 11d: remote-sync state per snapshot. One row per (workspace, env, date)
		-- snapshot pushed to an S3/SFTP backup target; ListBackups joins this to
		-- show a "synced" badge.
		CREATE TABLE IF NOT EXISTS backup_syncs (
			workspace   TEXT    NOT NULL,
			env         TEXT    NOT NULL,
			date        TEXT    NOT NULL,
			target_id   INTEGER NOT NULL,
			target_name TEXT    NOT NULL DEFAULT '',
			status      TEXT    NOT NULL DEFAULT 'ok',  -- ok | fail
			message     TEXT    NOT NULL DEFAULT '',
			files       INTEGER NOT NULL DEFAULT 0,
			bytes       INTEGER NOT NULL DEFAULT 0,
			synced_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (workspace, env, date)
		);

		-- 8d: Secret audit trail. Every read (reveal), write, rotate or delete of a
		-- secret-flagged env var is recorded here — key name only, never the value.
		CREATE TABLE IF NOT EXISTS secret_events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace  TEXT    NOT NULL,
			env        TEXT    NOT NULL,
			key        TEXT    NOT NULL,
			action     TEXT    NOT NULL,            -- read | write | rotate | delete
			username   TEXT    NOT NULL DEFAULT '',
			ip         TEXT    NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_secret_events_target
			ON secret_events(workspace, env, key, created_at);

		-- 6b: Notification channels. type 'email' is delivered directly via SMTP;
		-- all other types ('apprise') are delivered through the Apprise API
		-- sidecar, so Slack/Discord/Telegram/webhook/etc. need no bespoke code.
		-- config holds the type-specific settings as JSON (SMTP creds, or the
		-- Apprise URL(s)).
		CREATE TABLE IF NOT EXISTS notification_channels (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT    NOT NULL UNIQUE,
			type       TEXT    NOT NULL,
			config     TEXT    NOT NULL DEFAULT '{}',
			enabled    INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		-- 6d: Metrics history. A background collector writes one row per
		-- workspace+env every few minutes; env cards render sparklines from it.
		-- Old rows are pruned (90-day retention) by the collector.
		CREATE TABLE IF NOT EXISTS metrics_snapshots (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace    TEXT    NOT NULL,
			env          TEXT    NOT NULL,
			cpu_pct      REAL    NOT NULL DEFAULT 0,
			memory_bytes INTEGER NOT NULL DEFAULT 0,
			disk_bytes   INTEGER NOT NULL DEFAULT 0,
			net_rx_bytes INTEGER NOT NULL DEFAULT 0,
			net_tx_bytes INTEGER NOT NULL DEFAULT 0,
			recorded_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_metrics_target
			ON metrics_snapshots(workspace, env, recorded_at);

		-- ── Phase 7: Multi-Host Support ────────────────────────────────────────
		-- 7a: registered remote hosts. ssh_key_encrypted is AES-256-GCM over the
		-- PEM private key (key derived from JWT_SECRET); ssh_host_key is the
		-- base64 TOFU fingerprint captured on first successful connect.
		CREATE TABLE IF NOT EXISTS hosts (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			name              TEXT    NOT NULL UNIQUE,
			address           TEXT    NOT NULL,
			ssh_port          INTEGER NOT NULL DEFAULT 22,
			ssh_user          TEXT    NOT NULL,
			ssh_key_encrypted TEXT    NOT NULL,
			ssh_host_key      TEXT    NOT NULL DEFAULT '',
			created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		-- 7b: legacy per-workspace host binding (superseded by workspace_host_envs
		-- below). Kept so existing rows can be mirrored forward; new code never
		-- writes here.
		CREATE TABLE IF NOT EXISTS workspace_hosts (
			workspace TEXT    PRIMARY KEY,
			host_id   INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE
		);

		-- Per-environment host binding. A row (workspace, env) pins one environment
		-- to a host; env='' is the workspace-wide default used when an env has no
		-- explicit row. No matching row ⇒ that env runs on the local control plane.
		CREATE TABLE IF NOT EXISTS workspace_host_envs (
			workspace TEXT    NOT NULL,
			env       TEXT    NOT NULL,
			host_id   INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
			PRIMARY KEY (workspace, env)
		);

		-- Mirror any legacy per-workspace binding forward as the env='' default.
		-- Idempotent (PK + OR IGNORE); harmless once workspace_hosts is empty.
		INSERT OR IGNORE INTO workspace_host_envs (workspace, env, host_id)
			SELECT workspace, '', host_id FROM workspace_hosts;

		-- Data/files left on a SOURCE host after an environment was migrated away
		-- (host_id 0 = local control plane). Recorded so the user can wipe them via
		-- Housekeeping before decommissioning a host. host_id is intentionally not a
		-- foreign key so the reminder survives even if the host row is removed.
		CREATE TABLE IF NOT EXISTS migration_leftovers (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			host_id    INTEGER NOT NULL,
			host_name  TEXT    NOT NULL DEFAULT '',
			workspace  TEXT    NOT NULL,
			env        TEXT    NOT NULL,
			stack      TEXT    NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(host_id, workspace, env)
		);

		-- The single Rigger-managed SSH identity. Generated on first request; the
		-- private key is AES-256-GCM encrypted (same key as host keys) and the
		-- public key is handed to the user to install on hosts' authorized_keys.
		CREATE TABLE IF NOT EXISTS managed_ssh_key (
			id                    INTEGER PRIMARY KEY CHECK (id = 1),
			public_key            TEXT    NOT NULL,
			private_key_encrypted TEXT    NOT NULL,
			created_at            DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		return err
	}

	// Incremental column additions for tables that may predate a field.
	// SQLite has no "ADD COLUMN IF NOT EXISTS", so we run the ALTER and ignore
	// the duplicate-column error on databases that already have it.
	d.addColumn("alert_rules", "notify_channel_ids TEXT NOT NULL DEFAULT '[]'")
	d.addColumn("metrics_snapshots", "net_rx_bytes INTEGER NOT NULL DEFAULT 0")
	d.addColumn("metrics_snapshots", "net_tx_bytes INTEGER NOT NULL DEFAULT 0")
	d.addColumn("audit_log", "host TEXT NOT NULL DEFAULT ''")       // Phase 7: host name
	d.addColumn("hosts", "workspaces_dir TEXT NOT NULL DEFAULT ''") // Phase 7: per-host WORKSPACES_DIR ('' = global default)

	// SQLite only enforces ON DELETE CASCADE when foreign_keys is ON (off by
	// default), so deleting a host can leave dangling env bindings. Sweep any that
	// reference a host that no longer exists.
	d.Exec(`DELETE FROM workspace_host_envs WHERE host_id NOT IN (SELECT id FROM hosts)`) //nolint:errcheck
	return nil
}

// addColumn adds a column to an existing table, ignoring the error raised when
// the column already exists. colDef is the full column definition, e.g.
// "notify_channel_ids TEXT NOT NULL DEFAULT '[]'".
func (d *DB) addColumn(table, colDef string) {
	_, _ = d.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, colDef)) //nolint:errcheck
}

// IsSetupRequired returns true when no users exist yet (first run).
func (d *DB) IsSetupRequired() (bool, error) {
	var count int
	err := d.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	return count == 0, err
}
