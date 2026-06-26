package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

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
			username   TEXT    NOT NULL,
			password   TEXT    NOT NULL,
			role       TEXT    NOT NULL DEFAULT 'admin',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS audit_log (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id     INTEGER REFERENCES users(id),
			username    TEXT,
			project     TEXT,
			command     TEXT,
			env         TEXT,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		-- Recorded action runs (Action output history): one row per run with the
		-- captured output and result, kept per workspace (pruned in actionruns.Record).
		CREATE TABLE IF NOT EXISTS action_runs (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			project     TEXT    NOT NULL,
			env         TEXT,
			command     TEXT    NOT NULL,
			extra       TEXT,
			username    TEXT,
			status      TEXT    NOT NULL,  -- ok | fail
			output      TEXT,
			started_at  INTEGER,           -- epoch ms
			finished_at INTEGER            -- epoch ms
		);
		CREATE INDEX IF NOT EXISTS idx_action_runs_ws ON action_runs(project, id);

		CREATE TABLE IF NOT EXISTS backup_targets (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT    NOT NULL UNIQUE,
			type       TEXT    NOT NULL,
			config     TEXT    NOT NULL DEFAULT '{}',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		-- Settings-scopes (Phase 3): a GLOBAL backup target's workspace allowlist,
		-- mirroring global_host_grants / global_registry_grants. workspace='*' = all.
		CREATE TABLE IF NOT EXISTS global_backup_target_grants (
			target_id INTEGER NOT NULL REFERENCES backup_targets(id) ON DELETE CASCADE,
			workspace TEXT    NOT NULL,
			PRIMARY KEY (target_id, workspace)
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

		-- Settings-scopes (Phase 3): a GLOBAL registry's workspace allowlist, mirroring
		-- global_host_grants. workspace='*' = offered to all (default for pre-scope
		-- registries). A workspace-owned registry (owner_scope='ws:{key}') is private.
		CREATE TABLE IF NOT EXISTS global_registry_grants (
			registry_id INTEGER NOT NULL REFERENCES docker_registries(id) ON DELETE CASCADE,
			workspace   TEXT    NOT NULL,
			PRIMARY KEY (registry_id, workspace)
		);

		-- Git provider connections (Phase 12): private-repo credentials. Mirrors
		-- docker_registries scoping (owner_scope + global_git_provider_grants). The
		-- secret (PAT, SSH private key PEM, or GitHub App private key+ids) is stored
		-- ENCRYPTED (internal/crypto, AES-256-GCM) in secret_enc — never plaintext.
		-- public_key (ssh) and meta (github_app json: app id/slug/installation id) are
		-- non-secret. host scopes token/known-hosts matching ('' = the provider default).
		CREATE TABLE IF NOT EXISTS git_providers (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT    NOT NULL,
			kind        TEXT    NOT NULL,            -- token | ssh_key | github_app
			host        TEXT    NOT NULL DEFAULT '',
			username    TEXT    NOT NULL DEFAULT '', -- token auth user (optional)
			secret_enc  TEXT    NOT NULL DEFAULT '', -- crypto.Encrypt(secret material)
			public_key  TEXT    NOT NULL DEFAULT '', -- ssh deploy public key (non-secret)
			meta        TEXT    NOT NULL DEFAULT '', -- json, kind-specific non-secret fields
			owner_scope TEXT    NOT NULL DEFAULT 'global',
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS global_git_provider_grants (
			provider_id INTEGER NOT NULL REFERENCES git_providers(id) ON DELETE CASCADE,
			workspace   TEXT    NOT NULL,
			PRIMARY KEY (provider_id, workspace)
		);

		-- Per-workspace general settings (Phase 3): scalar key/value scoped to one
		-- workspace (e.g. acme_email, domain). Mirrors app_settings but workspace-keyed.
		CREATE TABLE IF NOT EXISTS workspace_settings (
			workspace  TEXT    NOT NULL,
			key        TEXT    NOT NULL,
			value      TEXT    NOT NULL DEFAULT '',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (workspace, key)
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
			project          TEXT    NOT NULL DEFAULT '',
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
			project        TEXT    NOT NULL DEFAULT '',
			env            TEXT    NOT NULL DEFAULT '',
			message        TEXT    NOT NULL,
			severity       TEXT    NOT NULL DEFAULT 'warning',
			value          REAL    NOT NULL DEFAULT 0,
			fired_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
			resolved_at    DATETIME,
			dismissed      INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_alert_events_open
			ON alert_events(rule_id, project, env, resolved_at);
		CREATE INDEX IF NOT EXISTS idx_alert_events_inbox
			ON alert_events(dismissed, fired_at);

		-- backup_log: per-env backup outcomes. The shell-bridge backup action
		-- records success/failure here so the backup_failed alert condition has a
		-- source (audit_log only records that a backup ran, not whether it worked).
		-- Also seeds Phase 11 (Backup Verification & Scheduling).
		CREATE TABLE IF NOT EXISTS backup_log (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			project    TEXT    NOT NULL,
			env        TEXT    NOT NULL,
			status     TEXT    NOT NULL DEFAULT 'ok',
			message    TEXT    NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_backup_log_target
			ON backup_log(project, env, created_at);

		-- 11d: remote-sync state per snapshot. One row per (workspace, env, date)
		-- snapshot pushed to an S3/SFTP backup target; ListBackups joins this to
		-- show a "synced" badge.
		CREATE TABLE IF NOT EXISTS backup_syncs (
			project     TEXT    NOT NULL,
			env         TEXT    NOT NULL,
			date        TEXT    NOT NULL,
			target_id   INTEGER NOT NULL,
			target_name TEXT    NOT NULL DEFAULT '',
			status      TEXT    NOT NULL DEFAULT 'ok',  -- ok | fail
			message     TEXT    NOT NULL DEFAULT '',
			files       INTEGER NOT NULL DEFAULT 0,
			bytes       INTEGER NOT NULL DEFAULT 0,
			synced_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (project, env, date)
		);

		-- Per-env backup schedule run-tracking (Phase 11 per-env redesign): last
		-- time each schedule executed, so the interval-based scheduler knows when
		-- the next run is due. Keyed by (workspace, env, schedule_id).
		CREATE TABLE IF NOT EXISTS backup_schedule_runs (
			project     TEXT NOT NULL,
			env         TEXT NOT NULL,
			schedule_id TEXT NOT NULL,
			last_run_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (project, env, schedule_id)
		);

		-- 11a: remote-sync state for full workspace archives (.rwb), keyed by the
		-- archive filename. ListWorkspaceArchives joins this for a synced badge.
		CREATE TABLE IF NOT EXISTS archive_syncs (
			filename    TEXT    NOT NULL PRIMARY KEY,
			target_id   INTEGER NOT NULL,
			target_name TEXT    NOT NULL DEFAULT '',
			status      TEXT    NOT NULL DEFAULT 'ok',
			message     TEXT    NOT NULL DEFAULT '',
			bytes       INTEGER NOT NULL DEFAULT 0,
			synced_at   DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		-- 8d: Secret audit trail. Every read (reveal), write, rotate or delete of a
		-- secret-flagged env var is recorded here — key name only, never the value.
		CREATE TABLE IF NOT EXISTS secret_events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			project    TEXT    NOT NULL,
			env        TEXT    NOT NULL,
			key        TEXT    NOT NULL,
			action     TEXT    NOT NULL,            -- read | write | rotate | delete
			username   TEXT    NOT NULL DEFAULT '',
			ip         TEXT    NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_secret_events_target
			ON secret_events(project, env, key, created_at);

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

		-- Settings-scopes (Phase 3): a GLOBAL notification channel's workspace
		-- allowlist, mirroring the host/registry/backup-target grant tables.
		CREATE TABLE IF NOT EXISTS global_notification_channel_grants (
			channel_id INTEGER NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
			workspace  TEXT    NOT NULL,
			PRIMARY KEY (channel_id, workspace)
		);

		-- 6d: Metrics history. A background collector writes one row per
		-- workspace+env every few minutes; env cards render sparklines from it.
		-- Old rows are pruned (90-day retention) by the collector.
		CREATE TABLE IF NOT EXISTS metrics_snapshots (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			project      TEXT    NOT NULL,
			env          TEXT    NOT NULL,
			cpu_pct      REAL    NOT NULL DEFAULT 0,
			memory_bytes INTEGER NOT NULL DEFAULT 0,
			disk_bytes   INTEGER NOT NULL DEFAULT 0,
			net_rx_bytes INTEGER NOT NULL DEFAULT 0,
			net_tx_bytes INTEGER NOT NULL DEFAULT 0,
			recorded_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_metrics_target
			ON metrics_snapshots(project, env, recorded_at);

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
			project   TEXT    PRIMARY KEY,
			host_id   INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE
		);

		-- Per-environment host binding. A row (project, env) pins one environment
		-- to a host; env='' is the project-wide default used when an env has no
		-- explicit row. No matching row ⇒ that env runs on the local control plane.
		CREATE TABLE IF NOT EXISTS workspace_host_envs (
			project   TEXT    NOT NULL,
			env       TEXT    NOT NULL,
			host_id   INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
			PRIMARY KEY (project, env)
		);

		-- Per-project BUILD host (image-distribution Phase 4). A row pins a project's
		-- image builds to a host (project = resource prefix {ws}_{proj}); no row ⇒ the
		-- project inherits its workspace default build host, else builds on the env's
		-- own deploy host (today's behavior). Independent of the deploy-host binding
		-- above so a dedicated builder can push to a registry the deploy targets pull.
		CREATE TABLE IF NOT EXISTS project_build_hosts (
			project   TEXT    PRIMARY KEY,
			host_id   INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE
		);

		-- Settings-scopes (Phase 3): a GLOBAL host's allowlist of workspaces it is
		-- offered to. workspace='*' = offered to every workspace (the default given
		-- to pre-scope hosts on migration). A workspace-owned host
		-- (hosts.owner_scope='ws:{key}') is private to that workspace and never
		-- appears here. Read pool for a workspace = its own hosts ∪ granted globals.
		CREATE TABLE IF NOT EXISTS global_host_grants (
			host_id   INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
			workspace TEXT    NOT NULL,
			PRIMARY KEY (host_id, workspace)
		);

		-- Mirror any legacy per-project binding forward as the env='' default.
		-- Idempotent (PK + OR IGNORE); harmless once workspace_hosts is empty.
		INSERT OR IGNORE INTO workspace_host_envs (project, env, host_id)
			SELECT project, '', host_id FROM workspace_hosts;

		-- Data/files left on a SOURCE host after an environment was migrated away
		-- (host_id 0 = local control plane). Recorded so the user can wipe them via
		-- Housekeeping before decommissioning a host. host_id is intentionally not a
		-- foreign key so the reminder survives even if the host row is removed.
		CREATE TABLE IF NOT EXISTS migration_leftovers (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			host_id    INTEGER NOT NULL,
			host_name  TEXT    NOT NULL DEFAULT '',
			project    TEXT    NOT NULL,
			env        TEXT    NOT NULL,
			stack      TEXT    NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(host_id, project, env)
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

		-- Phase 9: deployment pipelines. A pipeline is an ordered list of stages
		-- (JSON), project-scoped by (workspace, project) keys. Each stage maps to an
		-- existing bridge command (deploy/build/push/restart/backup) or a sandboxed
		-- 'test' (compose exec inside a service container).
		CREATE TABLE IF NOT EXISTS pipelines (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace   TEXT    NOT NULL,
			project     TEXT    NOT NULL,
			name        TEXT    NOT NULL,
			stages      TEXT    NOT NULL DEFAULT '[]',
			enabled     INTEGER NOT NULL DEFAULT 1,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_pipelines_name ON pipelines(workspace, project, name);

		-- One execution of a pipeline. stages holds the per-stage result JSON
		-- (type, env, status, capped output, duration); pruned to the most recent
		-- runs per pipeline.
		CREATE TABLE IF NOT EXISTS pipeline_runs (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			pipeline_id  INTEGER NOT NULL,
			workspace    TEXT    NOT NULL,
			project      TEXT    NOT NULL,
			trigger      TEXT    NOT NULL DEFAULT 'manual',
			username     TEXT    NOT NULL DEFAULT '',
			status       TEXT    NOT NULL,
			stages       TEXT    NOT NULL DEFAULT '[]',
			started_at   INTEGER NOT NULL,
			finished_at  INTEGER
		);
		CREATE INDEX IF NOT EXISTS idx_pipeline_runs_pipe ON pipeline_runs(pipeline_id, started_at);

		-- Phase 9a: inbound webhooks that trigger a pipeline. token_hash = sha256 of
		-- the URL token (raw shown once on create); secret is the optional HMAC key
		-- for verifying GitHub/Gitea-style signatures.
		CREATE TABLE IF NOT EXISTS pipeline_webhooks (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			pipeline_id       INTEGER NOT NULL,
			workspace         TEXT    NOT NULL,
			project           TEXT    NOT NULL,
			token_hash        TEXT    NOT NULL,
			secret            TEXT    NOT NULL DEFAULT '',
			enabled           INTEGER NOT NULL DEFAULT 1,
			created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_triggered_at DATETIME
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_webhooks_token ON pipeline_webhooks(token_hash);

		-- Phase 9e: per-env deploy history — the resolved image refs at each deploy,
		-- so a custom stack can be rolled back to a prior image set. ptype records
		-- the project type ('custom' | 'image'); image stacks are informational only
		-- (their tags aren't per-env overridable, so they roll back via Backup).
		CREATE TABLE IF NOT EXISTS deploy_history (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace   TEXT NOT NULL,
			project     TEXT NOT NULL,
			env         TEXT NOT NULL,
			ptype       TEXT NOT NULL,
			images      TEXT NOT NULL DEFAULT '{}',
			version     TEXT NOT NULL DEFAULT '',
			username    TEXT NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_deploy_history_env ON deploy_history(workspace, project, env, id);

		-- Out-of-band override certs (ACME email hierarchy, Phase 2): one row per domain
		-- whose env uses a per-env/per-workspace ACME email that differs from the global,
		-- so the renewal scheduler knows the (domain → email) pairs to renew (the email is
		-- the ACME account contact and isn't stored in the cert). not_after drives renewal.
		CREATE TABLE IF NOT EXISTS acme_certs (
			domain     TEXT PRIMARY KEY,
			email      TEXT NOT NULL,
			workspace  TEXT NOT NULL DEFAULT '',
			project    TEXT NOT NULL DEFAULT '',
			env        TEXT NOT NULL DEFAULT '',
			not_after  INTEGER NOT NULL DEFAULT 0,
			issued_at  INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT ''
		);

		-- Per-environment maintenance mode: when ON, Rigger writes a Traefik file-provider
		-- fragment that routes the env's host(s) to a "under maintenance" page served by the
		-- always-on rigger container (so it works even while the env's stack is stopped).
		-- Effective ON = enabled OR (now within [window_start, window_end]); window expiry (or an
		-- explicit disable) clears the window. Keyed by (workspace, project=KEY, env) so the
		-- scheduler can read config.json + custom domains directly.
		CREATE TABLE IF NOT EXISTS env_maintenance (
			workspace    TEXT NOT NULL,
			project      TEXT NOT NULL,
			env          TEXT NOT NULL,
			enabled      INTEGER NOT NULL DEFAULT 0,
			window_start INTEGER NOT NULL DEFAULT 0,
			window_end   INTEGER NOT NULL DEFAULT 0,
			title        TEXT NOT NULL DEFAULT '',
			message      TEXT NOT NULL DEFAULT '',
			retry_after  INTEGER NOT NULL DEFAULT 0,
			updated_at   INTEGER NOT NULL DEFAULT 0,
			updated_by   TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (workspace, project, env)
		);

		-- DB Hosting: users Rigger created on a managed database (one per schema it
		-- provisions), so the Manage Database UI can show the user/password and build
		-- a per-user Adminer auto-login link on reload. The password is AES-256-GCM
		-- encrypted at rest (same key as host SSH keys). Scoped by (workspace, project,
		-- env); username is unique within that scope.
		CREATE TABLE IF NOT EXISTS managed_db_users (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace     TEXT NOT NULL,
			project       TEXT NOT NULL,
			env           TEXT NOT NULL,
			engine        TEXT NOT NULL,
			schema_name   TEXT NOT NULL DEFAULT '',
			username      TEXT NOT NULL,
			password_enc  TEXT NOT NULL,
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(workspace, project, env, username)
		);
		CREATE INDEX IF NOT EXISTS idx_managed_db_users_env ON managed_db_users(workspace, project, env);

		-- API access keys (external REST API at /api/v1, separate from the JWT/cookie UI
		-- auth). key_hash = sha256 of the raw key (raw "rgk_…" shown once on create);
		-- key_prefix is a non-secret display snippet. scopes = JSON array of GRANULAR
		-- operation ids (e.g. ["projects.list","env.start"]) — the UI groups them but the
		-- key stores the resolved op set. project_access = 'all' | 'specific'; when
		-- 'specific', api_key_projects holds the (workspace,project) pairs the key may
		-- touch. rate_limit = max requests per minute PER PROJECT (0 = unlimited).
		-- created_at/last_used_at/expires_at are epoch seconds (expires_at 0 = never).
		CREATE TABLE IF NOT EXISTS api_keys (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			name           TEXT    NOT NULL,
			key_hash       TEXT    NOT NULL UNIQUE,
			key_prefix     TEXT    NOT NULL DEFAULT '',
			scopes         TEXT    NOT NULL DEFAULT '[]',
			project_access TEXT    NOT NULL DEFAULT 'all',
			rate_limit     INTEGER NOT NULL DEFAULT 0,
			enabled        INTEGER NOT NULL DEFAULT 1,
			created_by     TEXT    NOT NULL DEFAULT '',
			created_at     INTEGER NOT NULL DEFAULT 0,
			last_used_at   INTEGER NOT NULL DEFAULT 0,
			expires_at     INTEGER NOT NULL DEFAULT 0
		);
		-- Specific-project grants for a key (only consulted when project_access='specific').
		CREATE TABLE IF NOT EXISTS api_key_projects (
			key_id    INTEGER NOT NULL,
			workspace TEXT    NOT NULL,
			project   TEXT    NOT NULL,
			UNIQUE(key_id, workspace, project)
		);
		CREATE INDEX IF NOT EXISTS idx_api_key_projects ON api_key_projects(key_id);

		-- Preview/PR environments (design 6ece941). A preview webhook is per-PROJECT
		-- (not per-pipeline like pipeline_webhooks): a signed inbound URL that drives
		-- the preview lifecycle. token_hash = sha256 of the URL token (raw shown once);
		-- secret verifies the provider HMAC; provider = github|gitlab|gitea.
		CREATE TABLE IF NOT EXISTS preview_webhooks (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace         TEXT    NOT NULL,
			project           TEXT    NOT NULL,
			token_hash        TEXT    NOT NULL,
			secret            TEXT    NOT NULL DEFAULT '',
			provider          TEXT    NOT NULL DEFAULT 'github',
			enabled           INTEGER NOT NULL DEFAULT 1,
			created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_triggered_at DATETIME
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_preview_webhooks_token ON preview_webhooks(token_hash);
		CREATE INDEX IF NOT EXISTS idx_preview_webhooks_proj ON preview_webhooks(workspace, project);

		-- One live (or torn-down) preview environment: the pr{n} env cloned for a PR.
		-- status = creating|running|updating|failed|torn_down. created_at /
		-- last_deployed_at / expires_at are epoch seconds (expires_at 0 = no TTL, lives
		-- until PR close); the reaper sweeps rows where expires_at>0 AND expires_at<=now.
		-- last_run_id links the pipeline run that last deployed it (0 = none).
		CREATE TABLE IF NOT EXISTS preview_environments (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace        TEXT    NOT NULL,
			project          TEXT    NOT NULL,
			pr_number        INTEGER NOT NULL,
			provider         TEXT    NOT NULL DEFAULT 'github',
			branch           TEXT    NOT NULL DEFAULT '',
			head_sha         TEXT    NOT NULL DEFAULT '',
			env_key          TEXT    NOT NULL,
			url              TEXT    NOT NULL DEFAULT '',
			status           TEXT    NOT NULL DEFAULT 'creating',
			last_run_id      INTEGER NOT NULL DEFAULT 0,
			created_at       INTEGER NOT NULL DEFAULT 0,
			last_deployed_at INTEGER NOT NULL DEFAULT 0,
			expires_at       INTEGER NOT NULL DEFAULT 0
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_preview_envs_pr ON preview_environments(workspace, project, pr_number);
		CREATE INDEX IF NOT EXISTS idx_preview_envs_proj ON preview_environments(workspace, project);
		CREATE INDEX IF NOT EXISTS idx_preview_envs_expiry ON preview_environments(expires_at);

		-- Per-project write-back token (Phase 4): a provider PAT used to post the
		-- preview URL/status back to the PR (commit status + comment). AES-256-GCM
		-- encrypted at rest (same key as host SSH keys); never returned to the client.
		CREATE TABLE IF NOT EXISTS preview_writeback_tokens (
			workspace  TEXT    NOT NULL,
			project    TEXT    NOT NULL,
			token_enc  TEXT    NOT NULL,
			created_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (workspace, project)
		);
	`)
	if err != nil {
		return err
	}

	// Incremental column additions for tables that may predate a field.
	// SQLite has no "ADD COLUMN IF NOT EXISTS", so we run the ALTER and ignore
	// the duplicate-column error on databases that already have it.
	d.addColumn("users", "last_login_at DATETIME")                                 // Phase 5: track last login
	d.addColumn("users", "email TEXT NOT NULL DEFAULT ''")                         // Phase 5.1b: required going forward; login identity
	d.addColumn("users", "phone TEXT NOT NULL DEFAULT ''")                         // Phase 5.1b: optional (SMS later)
	d.addColumn("users", "email_verified INTEGER NOT NULL DEFAULT 0")             // Phase 5.1b
	d.addColumn("users", "status TEXT NOT NULL DEFAULT 'active'")                 // Phase 5.1b: 'invited' | 'active'
	d.addColumn("users", "appearance_prefs TEXT NOT NULL DEFAULT ''")             // per-user theme/typography override (W7)
	d.addColumn("users", "confirm_destructive TEXT NOT NULL DEFAULT ''")          // per-user destructive-confirm override ('' = inherit)
	d.addColumn("api_keys", "workspace TEXT NOT NULL DEFAULT ''")                 // ''=global (admin); else confined to that workspace
	// Unique email among accounts that have one (empty allowed for legacy/pre-email rows).
	d.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users(email) WHERE email <> ''`) //nolint:errcheck
	// Drop the legacy UNIQUE constraint on users.username. Email is now the login
	// identity (idx_users_email); username is just the display name shown in the
	// Users list, so two people may share one (e.g. "Mansoor"). The old constraint
	// also surfaced username clashes as a misleading "email already exists" error.
	if err := d.dropUsernameUnique(); err != nil {
		return err
	}
	// Global-role rework: the JWT/global role is now superadmin|user (workspace
	// access comes from membership). Migrate legacy global roles in place —
	// idempotent, so safe to run every boot.
	d.Exec(`UPDATE users SET role='superadmin' WHERE role='admin'`)                   //nolint:errcheck
	d.Exec(`UPDATE users SET role='user' WHERE role NOT IN ('superadmin','user')`)    //nolint:errcheck
	// Phase 5.2: workspace membership (tier) + per-project role overrides. Roles are
	// workspace-tier roles (viewer/developer/operator/admin); project_acl may also be
	// 'none' to revoke a single project. Membership-gated: a non-super-admin sees only
	// the workspaces listed here.
	d.Exec(`CREATE TABLE IF NOT EXISTS workspace_members (
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		ws_key  TEXT    NOT NULL,
		role    TEXT    NOT NULL,
		PRIMARY KEY (user_id, ws_key)
	)`) //nolint:errcheck
	d.Exec(`CREATE TABLE IF NOT EXISTS project_acl (
		user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		ws_key   TEXT    NOT NULL,
		proj_key TEXT    NOT NULL,
		role     TEXT    NOT NULL,
		PRIMARY KEY (user_id, ws_key, proj_key)
	)`) //nolint:errcheck
	// Access requests: a user asks for access to a workspace (and optionally one
	// project) at a role; a workspace admin or super-admin approves/rejects.
	d.Exec(`CREATE TABLE IF NOT EXISTS access_requests (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		ws_key     TEXT    NOT NULL,
		proj_key   TEXT    NOT NULL DEFAULT '',
		role       TEXT    NOT NULL,
		message    TEXT    NOT NULL DEFAULT '',
		status     TEXT    NOT NULL DEFAULT 'pending', -- pending | approved | rejected
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		decided_at DATETIME,
		decided_by INTEGER
	)`) //nolint:errcheck
	// One open (pending) request per (user, workspace, project).
	d.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_access_req_pending
		ON access_requests(user_id, ws_key, proj_key) WHERE status='pending'`) //nolint:errcheck

	// Invite / email-verify / password-reset / 2FA tokens (token_hash = sha256 of the
	// random value handed out in links; the raw value is never stored).
	d.Exec(`CREATE TABLE IF NOT EXISTS user_tokens (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		kind       TEXT    NOT NULL,
		token_hash TEXT    NOT NULL UNIQUE,
		expires_at DATETIME NOT NULL,
		used_at    DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`) //nolint:errcheck
	d.addColumn("alert_rules", "notify_channel_ids TEXT NOT NULL DEFAULT '[]'")
	d.addColumn("alert_rules", "ws_key TEXT NOT NULL DEFAULT ''") // Phase 3: workspace-tier target ('' = all workspaces)
	// Pipeline alerting: channels to notify on run events + which events fire.
	d.addColumn("pipelines", "notify_channel_ids TEXT NOT NULL DEFAULT '[]'")
	d.addColumn("pipelines", "notify_events TEXT NOT NULL DEFAULT '{}'")
	d.addColumn("metrics_snapshots", "net_rx_bytes INTEGER NOT NULL DEFAULT 0")
	d.addColumn("metrics_snapshots", "net_tx_bytes INTEGER NOT NULL DEFAULT 0")
	d.addColumn("audit_log", "host TEXT NOT NULL DEFAULT ''")       // Phase 7: host name
	d.addColumn("hosts", "workspaces_dir TEXT NOT NULL DEFAULT ''") // Phase 7: per-host WORKSPACES_DIR ('' = global default)

	// Password rotation (auth Group A slice 3): when each user's password was last
	// set. Backfill existing rows to "now" on first add so the rotation clock starts
	// at upgrade rather than retroactively expiring everyone the moment an admin
	// configures a max age. Stamped on every password set thereafter.
	if d.addColumn("users", "password_changed_at DATETIME") {
		d.Exec(`UPDATE users SET password_changed_at=CURRENT_TIMESTAMP WHERE password_changed_at IS NULL`) //nolint:errcheck
	}

	// Optional 2FA / TOTP (auth Group A slice 4). totp_secret holds the base32 shared
	// secret (set at enrollment); totp_enabled flips on only after a code is verified.
	d.addColumn("users", "totp_secret TEXT NOT NULL DEFAULT ''")
	d.addColumn("users", "totp_enabled INTEGER NOT NULL DEFAULT 0")

	// 2FA recovery (backup) codes — single-use, stored as SHA-256 hashes (only the
	// plaintext set is shown once at generation). Used at login when the authenticator
	// is unavailable; each row is consumed on use.
	d.Exec(`CREATE TABLE IF NOT EXISTS user_recovery_codes (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		code_hash  TEXT    NOT NULL,
		used_at    DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`) //nolint:errcheck
	d.Exec(`CREATE INDEX IF NOT EXISTS idx_recovery_user ON user_recovery_codes(user_id)`) //nolint:errcheck

	// Custom domains: a verified external domain (e.g. app.example.com) attached to an
	// env IN ADDITION to its auto subdomain (Render-style). Ownership is proven via a
	// per-domain token (TXT / file / CNAME) before the domain is routed + cert-issued.
	// One domain maps to exactly one env (UNIQUE). Token persists so re-verification
	// after a DNS change reuses the same challenge value.
	d.Exec(`CREATE TABLE IF NOT EXISTS custom_domains (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		workspace   TEXT NOT NULL,
		project     TEXT NOT NULL,
		env         TEXT NOT NULL,
		domain      TEXT NOT NULL UNIQUE,
		token       TEXT NOT NULL,
		verified    INTEGER NOT NULL DEFAULT 0,
		verified_at DATETIME,
		is_primary  INTEGER NOT NULL DEFAULT 0,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	)`) //nolint:errcheck
	d.Exec(`CREATE INDEX IF NOT EXISTS idx_custom_domains_env ON custom_domains(workspace, project, env)`) //nolint:errcheck
	// is_primary (the ★ canonical domain) was added after the table shipped; add it to
	// pre-existing tables. Errors (column already present) are ignored.
	d.Exec(`ALTER TABLE custom_domains ADD COLUMN is_primary INTEGER NOT NULL DEFAULT 0`) //nolint:errcheck

	// Phase 3 (settings scopes): host ownership. 'global' = shared via grants;
	// 'ws:{key}' = private to that workspace. When the column is freshly added,
	// every existing host predates scoping — grant each to all workspaces ('*')
	// so multi-host behavior is unchanged.
	if d.addColumn("hosts", "owner_scope TEXT NOT NULL DEFAULT 'global'") {
		d.Exec(`INSERT OR IGNORE INTO global_host_grants (host_id, workspace) SELECT id, '*' FROM hosts`) //nolint:errcheck
	}
	// Image-distribution Phase 5: host capabilities probed from `docker info` on Test
	// (swarm node state + manager flag) + a build-only marker (dedicated builder,
	// excluded from deploy pickers). All default to "unknown"/false for existing hosts.
	d.addColumn("hosts", "swarm_state TEXT NOT NULL DEFAULT ''")
	d.addColumn("hosts", "swarm_manager INTEGER NOT NULL DEFAULT 0")
	d.addColumn("hosts", "build_only INTEGER NOT NULL DEFAULT 0")
	// Same scoping for docker registries (Phase 3); pre-scope registries → '*'.
	if d.addColumn("docker_registries", "owner_scope TEXT NOT NULL DEFAULT 'global'") {
		d.Exec(`INSERT OR IGNORE INTO global_registry_grants (registry_id, workspace) SELECT id, '*' FROM docker_registries`) //nolint:errcheck
	}
	// Image-distribution Phase 1: a registry can be designated the "system" registry
	// used wherever a project sets none (settings.EffectiveRegistry). At most one
	// global system registry + at most one per workspace; enforced in the store, not
	// the schema. Pre-existing registries default to non-system (0).
	d.addColumn("docker_registries", "system INTEGER NOT NULL DEFAULT 0")
	// Same scoping for backup targets (Phase 3); pre-scope targets → '*'.
	if d.addColumn("backup_targets", "owner_scope TEXT NOT NULL DEFAULT 'global'") {
		d.Exec(`INSERT OR IGNORE INTO global_backup_target_grants (target_id, workspace) SELECT id, '*' FROM backup_targets`) //nolint:errcheck
	}
	// Same scoping for notification channels (Phase 3); pre-scope channels → '*'.
	if d.addColumn("notification_channels", "owner_scope TEXT NOT NULL DEFAULT 'global'") {
		d.Exec(`INSERT OR IGNORE INTO global_notification_channel_grants (channel_id, workspace) SELECT id, '*' FROM notification_channels`) //nolint:errcheck
	}

	// SQLite only enforces ON DELETE CASCADE when foreign_keys is ON (off by
	// default), so deleting a host/registry can leave dangling bindings/grants.
	// Sweep any that reference a row that no longer exists.
	d.Exec(`DELETE FROM workspace_host_envs WHERE host_id NOT IN (SELECT id FROM hosts)`)                          //nolint:errcheck
	d.Exec(`DELETE FROM project_build_hosts WHERE host_id NOT IN (SELECT id FROM hosts)`)                          //nolint:errcheck
	d.Exec(`DELETE FROM global_host_grants WHERE host_id NOT IN (SELECT id FROM hosts)`)                           //nolint:errcheck
	d.Exec(`DELETE FROM global_registry_grants WHERE registry_id NOT IN (SELECT id FROM docker_registries)`)       //nolint:errcheck
	d.Exec(`DELETE FROM global_backup_target_grants WHERE target_id NOT IN (SELECT id FROM backup_targets)`)      //nolint:errcheck
	d.Exec(`DELETE FROM global_notification_channel_grants WHERE channel_id NOT IN (SELECT id FROM notification_channels)`) //nolint:errcheck
	return nil
}

// dropUsernameUnique rebuilds the users table without the legacy UNIQUE
// constraint on `username`. It is idempotent: it only acts while the stored DDL
// still carries a UNIQUE keyword (fresh databases are already created without
// it), and runs after addColumn() so every column exists for the copy. Foreign
// keys are toggled off around the rebuild because DROP TABLE with FKs enabled
// performs an implicit row delete that would cascade into child tables; the
// statement runs on the single pooled connection (SetMaxOpenConns(1)), so the
// PRAGMA reliably scopes the rebuild.
func (d *DB) dropUsernameUnique() error {
	var ddl string
	if err := d.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='users'`).Scan(&ddl); err != nil {
		return nil // no users table yet — nothing to rebuild
	}
	if !strings.Contains(strings.ToUpper(ddl), "UNIQUE") {
		return nil // already rebuilt without the constraint
	}

	if _, err := d.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer d.Exec(`PRAGMA foreign_keys=ON`) //nolint:errcheck

	tx, err := d.Begin()
	if err != nil {
		return err
	}
	stmts := []string{
		`CREATE TABLE users_new (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			username         TEXT    NOT NULL,
			password         TEXT    NOT NULL,
			role             TEXT    NOT NULL DEFAULT 'admin',
			created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_login_at    DATETIME,
			email            TEXT    NOT NULL DEFAULT '',
			phone            TEXT    NOT NULL DEFAULT '',
			email_verified   INTEGER NOT NULL DEFAULT 0,
			status           TEXT    NOT NULL DEFAULT 'active',
			appearance_prefs TEXT    NOT NULL DEFAULT '',
			confirm_destructive TEXT  NOT NULL DEFAULT ''
		)`,
		`INSERT INTO users_new (id, username, password, role, created_at, last_login_at, email, phone, email_verified, status, appearance_prefs, confirm_destructive)
		 SELECT id, username, password, role, created_at, last_login_at, email, phone, email_verified, status, appearance_prefs, confirm_destructive FROM users`,
		`DROP TABLE users`,
		`ALTER TABLE users_new RENAME TO users`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users(email) WHERE email <> ''`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("drop username unique: %w", err)
		}
	}
	return tx.Commit()
}

// addColumn adds a column to an existing table, ignoring the error raised when
// the column already exists. colDef is the full column definition, e.g.
// "notify_channel_ids TEXT NOT NULL DEFAULT '[]'". Returns true when the column
// was newly added (the ALTER succeeded), false when it already existed.
func (d *DB) addColumn(table, colDef string) bool {
	_, err := d.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, colDef))
	return err == nil
}

// IsSetupRequired returns true when no users exist yet (first run).
func (d *DB) IsSetupRequired() (bool, error) {
	var count int
	err := d.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	return count == 0, err
}
