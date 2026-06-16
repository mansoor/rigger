package settings

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
)

// ── Backup Targets ────────────────────────────────────────────────────────────

type BackupTarget struct {
	ID         int64           `json:"id"`
	Name       string          `json:"name"`
	Type       string          `json:"type"` // "s3" | "sftp"
	Config     json.RawMessage `json:"config"`
	OwnerScope string          `json:"owner_scope"`      // 'global' or 'ws:{key}'
	Grants     []string        `json:"grants,omitempty"` // for global targets: workspaces offered to ('*' = all)
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// WorkspaceScope returns the workspace key a target is private to, or "" if global.
func (t BackupTarget) WorkspaceScope() string {
	if strings.HasPrefix(t.OwnerScope, "ws:") {
		return t.OwnerScope[len("ws:"):]
	}
	return ""
}

// S3Config holds S3/compatible object storage settings.
type S3Config struct {
	Endpoint   string `json:"endpoint"`
	Bucket     string `json:"bucket"`
	Region     string `json:"region"`
	AccessKey  string `json:"access_key"`
	SecretKey  string `json:"secret_key"`
	PathPrefix string `json:"path_prefix"`
	UseSSL     bool   `json:"use_ssl"`
}

// SFTPConfig holds SFTP settings.
type SFTPConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	AuthType   string `json:"auth_type"` // "password" | "key"
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	RemotePath string `json:"remote_path"`
}

func scanBackupTargets(rows *sql.Rows) ([]BackupTarget, error) {
	defer rows.Close()
	var out []BackupTarget
	for rows.Next() {
		var t BackupTarget
		var cfg string
		if err := rows.Scan(&t.ID, &t.Name, &t.Type, &cfg, &t.OwnerScope, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.Config = json.RawMessage(cfg)
		out = append(out, t)
	}
	if out == nil {
		out = []BackupTarget{}
	}
	return out, rows.Err()
}

func ListBackupTargets(d *db.DB) ([]BackupTarget, error) {
	rows, err := d.Query(`SELECT id, name, type, config, owner_scope, created_at, updated_at FROM backup_targets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return scanBackupTargets(rows)
}

// ListBackupTargetsForWorkspace returns the target pool visible to one workspace:
// its own (owner_scope='ws:{key}') plus any global target granted to it (or '*').
func ListBackupTargetsForWorkspace(d *db.DB, wsKey string) ([]BackupTarget, error) {
	rows, err := d.Query(`
		SELECT id, name, type, config, owner_scope, created_at, updated_at
		FROM backup_targets t
		WHERE t.owner_scope = ?
		   OR (t.owner_scope = 'global' AND EXISTS(
		         SELECT 1 FROM global_backup_target_grants g
		         WHERE g.target_id = t.id AND g.workspace IN (?, '*')))
		ORDER BY name`, WorkspaceOwnerScope(wsKey), wsKey)
	if err != nil {
		return nil, err
	}
	return scanBackupTargets(rows)
}

// TargetInWorkspacePool reports whether a backup target is usable by a workspace.
func TargetInWorkspacePool(d *db.DB, wsKey string, id int64) (bool, error) {
	var n int
	err := d.QueryRow(`
		SELECT COUNT(1) FROM backup_targets t
		WHERE t.id = ?
		  AND (t.owner_scope = ?
		    OR (t.owner_scope = 'global' AND EXISTS(
		          SELECT 1 FROM global_backup_target_grants g
		          WHERE g.target_id = t.id AND g.workspace IN (?, '*'))))`,
		id, WorkspaceOwnerScope(wsKey), wsKey).Scan(&n)
	return n > 0, err
}

// TargetGrants returns the workspace allowlist for a global target ('*' = all).
func TargetGrants(d *db.DB, id int64) ([]string, error) {
	rows, err := d.Query(`SELECT workspace FROM global_backup_target_grants WHERE target_id=? ORDER BY workspace`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ws string
		if err := rows.Scan(&ws); err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

// SetTargetGrants replaces a global target's workspace allowlist.
func SetTargetGrants(d *db.DB, id int64, workspaces []string) error {
	if _, err := d.Exec(`DELETE FROM global_backup_target_grants WHERE target_id=?`, id); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, ws := range workspaces {
		if ws == "" || seen[ws] {
			continue
		}
		seen[ws] = true
		if _, err := d.Exec(`INSERT OR IGNORE INTO global_backup_target_grants (target_id, workspace) VALUES (?, ?)`, id, ws); err != nil {
			return err
		}
	}
	return nil
}

// WorkspaceOwnedTargetIDs returns the ids of backup targets private to a workspace.
func WorkspaceOwnedTargetIDs(d *db.DB, wsKey string) ([]int64, error) {
	rows, err := d.Query(`SELECT id FROM backup_targets WHERE owner_scope=?`, WorkspaceOwnerScope(wsKey))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func GetBackupTarget(d *db.DB, id int64) (*BackupTarget, error) {
	var t BackupTarget
	var cfg string
	err := d.QueryRow(`SELECT id, name, type, config, owner_scope, created_at, updated_at FROM backup_targets WHERE id = ?`, id).
		Scan(&t.ID, &t.Name, &t.Type, &cfg, &t.OwnerScope, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Config = json.RawMessage(cfg)
	return &t, nil
}

func CreateBackupTarget(d *db.DB, name, typ string, cfg json.RawMessage, ownerScope string) (*BackupTarget, error) {
	if err := validateBackupType(typ); err != nil {
		return nil, err
	}
	if ownerScope == "" {
		ownerScope = "global"
	}
	res, err := d.Exec(`INSERT INTO backup_targets (name, type, config, owner_scope) VALUES (?, ?, ?, ?)`, name, typ, string(cfg), ownerScope)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetBackupTarget(d, id)
}

func UpdateBackupTarget(d *db.DB, id int64, name, typ string, cfg json.RawMessage) (*BackupTarget, error) {
	if err := validateBackupType(typ); err != nil {
		return nil, err
	}
	_, err := d.Exec(`UPDATE backup_targets SET name=?, type=?, config=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		name, typ, string(cfg), id)
	if err != nil {
		return nil, err
	}
	return GetBackupTarget(d, id)
}

func DeleteBackupTarget(d *db.DB, id int64) error {
	_, err := d.Exec(`DELETE FROM backup_targets WHERE id=?`, id)
	return err
}

func validateBackupType(t string) error {
	if t != "s3" && t != "sftp" {
		return fmt.Errorf("invalid backup target type %q: must be s3 or sftp", t)
	}
	return nil
}

// ── Docker Registries ─────────────────────────────────────────────────────────

type DockerRegistry struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	URL        string    `json:"url"`
	Username   string    `json:"username"`
	Password   string    `json:"password,omitempty"` // omitted in list responses
	OwnerScope string    `json:"owner_scope"`        // 'global' or 'ws:{key}'
	Grants     []string  `json:"grants,omitempty"`   // for global registries: workspaces offered to ('*' = all)
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// WorkspaceScope returns the workspace key a registry is private to, or "" if global.
func (r DockerRegistry) WorkspaceScope() string {
	if strings.HasPrefix(r.OwnerScope, "ws:") {
		return r.OwnerScope[len("ws:"):]
	}
	return ""
}

func ListRegistries(d *db.DB) ([]DockerRegistry, error) {
	rows, err := d.Query(`SELECT id, name, url, username, owner_scope, created_at, updated_at FROM docker_registries ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DockerRegistry
	for rows.Next() {
		var r DockerRegistry
		if err := rows.Scan(&r.ID, &r.Name, &r.URL, &r.Username, &r.OwnerScope, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if out == nil {
		out = []DockerRegistry{}
	}
	return out, rows.Err()
}

// ListRegistriesForWorkspace returns the registry pool visible to one workspace:
// its own (owner_scope='ws:{key}') plus any global registry granted to it (or '*').
func ListRegistriesForWorkspace(d *db.DB, wsKey string) ([]DockerRegistry, error) {
	rows, err := d.Query(`
		SELECT id, name, url, username, owner_scope, created_at, updated_at
		FROM docker_registries r
		WHERE r.owner_scope = ?
		   OR (r.owner_scope = 'global' AND EXISTS(
		         SELECT 1 FROM global_registry_grants g
		         WHERE g.registry_id = r.id AND g.workspace IN (?, '*')))
		ORDER BY name`, WorkspaceOwnerScope(wsKey), wsKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DockerRegistry
	for rows.Next() {
		var r DockerRegistry
		if err := rows.Scan(&r.ID, &r.Name, &r.URL, &r.Username, &r.OwnerScope, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if out == nil {
		out = []DockerRegistry{}
	}
	return out, rows.Err()
}

// RegistryGrants returns the workspace allowlist for a global registry ('*' = all).
func RegistryGrants(d *db.DB, id int64) ([]string, error) {
	rows, err := d.Query(`SELECT workspace FROM global_registry_grants WHERE registry_id=? ORDER BY workspace`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ws string
		if err := rows.Scan(&ws); err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

// SetRegistryGrants replaces a global registry's workspace allowlist.
func SetRegistryGrants(d *db.DB, id int64, workspaces []string) error {
	if _, err := d.Exec(`DELETE FROM global_registry_grants WHERE registry_id=?`, id); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, ws := range workspaces {
		if ws == "" || seen[ws] {
			continue
		}
		seen[ws] = true
		if _, err := d.Exec(`INSERT OR IGNORE INTO global_registry_grants (registry_id, workspace) VALUES (?, ?)`, id, ws); err != nil {
			return err
		}
	}
	return nil
}

// SetRegistryScope changes a registry's ownership ('global' or 'ws:{key}').
// Re-scoping to a workspace clears its global grants.
func SetRegistryScope(d *db.DB, id int64, ownerScope string) error {
	if ownerScope == "" {
		ownerScope = "global"
	}
	if _, err := d.Exec(`UPDATE docker_registries SET owner_scope=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, ownerScope, id); err != nil {
		return err
	}
	if ownerScope != "global" {
		_, err := d.Exec(`DELETE FROM global_registry_grants WHERE registry_id=?`, id)
		return err
	}
	return nil
}

// WorkspaceOwnedRegistryIDs returns the ids of registries private to a workspace.
func WorkspaceOwnedRegistryIDs(d *db.DB, wsKey string) ([]int64, error) {
	rows, err := d.Query(`SELECT id FROM docker_registries WHERE owner_scope=?`, WorkspaceOwnerScope(wsKey))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func GetRegistry(d *db.DB, id int64) (*DockerRegistry, error) {
	var r DockerRegistry
	err := d.QueryRow(`SELECT id, name, url, username, password, owner_scope, created_at, updated_at FROM docker_registries WHERE id=?`, id).
		Scan(&r.ID, &r.Name, &r.URL, &r.Username, &r.Password, &r.OwnerScope, &r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &r, err
}

func CreateRegistry(d *db.DB, name, url, username, password, ownerScope string) (*DockerRegistry, error) {
	if ownerScope == "" {
		ownerScope = "global"
	}
	res, err := d.Exec(`INSERT INTO docker_registries (name, url, username, password, owner_scope) VALUES (?, ?, ?, ?, ?)`,
		name, url, username, password, ownerScope)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetRegistry(d, id)
}

func UpdateRegistry(d *db.DB, id int64, name, url, username, password string) (*DockerRegistry, error) {
	// If password is empty string, keep existing password
	if password == "" {
		_, err := d.Exec(`UPDATE docker_registries SET name=?, url=?, username=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			name, url, username, id)
		if err != nil {
			return nil, err
		}
	} else {
		_, err := d.Exec(`UPDATE docker_registries SET name=?, url=?, username=?, password=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			name, url, username, password, id)
		if err != nil {
			return nil, err
		}
	}
	return GetRegistry(d, id)
}

func DeleteRegistry(d *db.DB, id int64) error {
	_, err := d.Exec(`DELETE FROM docker_registries WHERE id=?`, id)
	return err
}

// ── Workspace general settings (Phase 3) ──────────────────────────────────────
// Scalar key/value scoped to a single workspace (mirrors app_settings).

// GetWorkspaceSettings returns all stored settings for a workspace as a map.
func GetWorkspaceSettings(d *db.DB, wsKey string) (map[string]string, error) {
	rows, err := d.Query(`SELECT key, value FROM workspace_settings WHERE workspace=?`, wsKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// WorkspaceBaseDomain returns the workspace's own base-domain override (the
// `domain` setting), or "" when unset. Most callers want EffectiveBaseDomain,
// which layers the global default underneath this.
func WorkspaceBaseDomain(d *db.DB, wsKey string) string {
	if d == nil {
		return ""
	}
	vals, err := GetWorkspaceSettings(d, wsKey)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(vals["domain"])
}

// AppSetting returns a global (instance-wide) setting value from app_settings, or
// "" when the db is nil or the key is unset. The admin General tab owns these keys.
func AppSetting(d *db.DB, key string) string {
	if d == nil {
		return ""
	}
	var v string
	d.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v) //nolint:errcheck
	return strings.TrimSpace(v)
}

// EffectiveBaseDomain resolves the apps base domain for a workspace: the workspace's
// own `domain` override wins, else the global `apps_base_domain` (admin default), else
// "" (callers then derive a magic-DNS / *.localhost auto-URL — see AutoURLMode).
// This is the value env routes are built on: {prefix}-{env}.{base}.
func EffectiveBaseDomain(d *db.DB, wsKey string) string {
	if ws := WorkspaceBaseDomain(d, wsKey); ws != "" {
		return ws
	}
	return AppSetting(d, "apps_base_domain")
}

// AutoURLMode returns how to build an env URL when no base domain is set:
// "sslip" | "nip" | "traefikme" | "localhost" | "off". Defaults to "localhost"
// (the historical behaviour) when unset.
func AutoURLMode(d *db.DB) string {
	if m := AppSetting(d, "auto_url_mode"); m != "" {
		return m
	}
	return "localhost"
}

// AppsDNSProvider returns the configured DNS-01 provider for wildcard certs
// (e.g. "cloudflare"), or "" when none — the signal composegen uses to emit a
// wildcard cert for base-domain envs (vs per-host HTTP-01). The provider's API
// token lives in the Rigger stack's env (e.g. CF_DNS_API_TOKEN), not the DB.
func AppsDNSProvider(d *db.DB) string { return AppSetting(d, "apps_dns_provider") }

// AutoURLHost returns the IP/host embedded in a magic-DNS auto-URL
// ({prefix}-{env}.<host>.sslip.io) — the admin sets the LAN/public/Tailscale IP
// other machines use to reach this host. "" ⇒ magic-DNS modes fall back to localhost.
func AutoURLHost(d *db.DB) string { return AppSetting(d, "auto_url_host") }

// SetWorkspaceSetting upserts one workspace-scoped setting.
func SetWorkspaceSetting(d *db.DB, wsKey, key, value string) error {
	_, err := d.Exec(
		`INSERT INTO workspace_settings (workspace, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(workspace, key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`,
		wsKey, key, value)
	return err
}

// DeleteWorkspaceSettings removes all settings for a workspace (used on delete).
func DeleteWorkspaceSettings(d *db.DB, wsKey string) error {
	_, err := d.Exec(`DELETE FROM workspace_settings WHERE workspace=?`, wsKey)
	return err
}

// ── Hosts (Phase 7: Multi-Host Support) ───────────────────────────────────────

// Host is a registered remote host. The encrypted SSH key is never serialized;
// the handler encrypts before Create/Update and decrypts GetHost for dialing.
type Host struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Address       string    `json:"address"`
	SSHPort       int       `json:"ssh_port"`
	SSHUser       string    `json:"ssh_user"`
	SSHKeyEnc     string    `json:"-"` // AES-GCM ciphertext; never exposed
	SSHHostKey    string    `json:"ssh_host_key,omitempty"`
	WorkspacesDir string    `json:"workspaces_dir"`  // remote WORKSPACES_DIR ('' = global default)
	OwnerScope    string    `json:"owner_scope"`     // 'global' or 'ws:{key}'
	Grants        []string  `json:"grants,omitempty"` // for global hosts: workspaces offered to ('*' = all)
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// WorkspaceScope returns the workspace key a host is private to, or "" if the
// host is global (shared via grants).
func (h Host) WorkspaceScope() string {
	if strings.HasPrefix(h.OwnerScope, "ws:") {
		return h.OwnerScope[len("ws:"):]
	}
	return ""
}

// WorkspaceOwnerScope formats the owner_scope value for a workspace-owned host.
func WorkspaceOwnerScope(wsKey string) string { return "ws:" + wsKey }

func ListHosts(d *db.DB) ([]Host, error) {
	rows, err := d.Query(`SELECT id, name, address, ssh_port, ssh_user, ssh_host_key, workspaces_dir, owner_scope, created_at, updated_at FROM hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Host
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.ID, &h.Name, &h.Address, &h.SSHPort, &h.SSHUser, &h.SSHHostKey, &h.WorkspacesDir, &h.OwnerScope, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if out == nil {
		out = []Host{}
	}
	return out, rows.Err()
}

// ListHostsForWorkspace returns the host pool visible to one workspace: its own
// hosts (owner_scope='ws:{key}') plus any global host granted to it (or to '*').
func ListHostsForWorkspace(d *db.DB, wsKey string) ([]Host, error) {
	rows, err := d.Query(`
		SELECT id, name, address, ssh_port, ssh_user, ssh_host_key, workspaces_dir, owner_scope, created_at, updated_at
		FROM hosts h
		WHERE h.owner_scope = ?
		   OR (h.owner_scope = 'global' AND EXISTS(
		         SELECT 1 FROM global_host_grants g
		         WHERE g.host_id = h.id AND g.workspace IN (?, '*')))
		ORDER BY name`, WorkspaceOwnerScope(wsKey), wsKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Host
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.ID, &h.Name, &h.Address, &h.SSHPort, &h.SSHUser, &h.SSHHostKey, &h.WorkspacesDir, &h.OwnerScope, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if out == nil {
		out = []Host{}
	}
	return out, rows.Err()
}

// HostInWorkspacePool reports whether a host is usable by a workspace (its own
// host or a global host granted to it). Used to gate env→host binding.
func HostInWorkspacePool(d *db.DB, wsKey string, hostID int64) (bool, error) {
	var n int
	err := d.QueryRow(`
		SELECT COUNT(1) FROM hosts h
		WHERE h.id = ?
		  AND (h.owner_scope = ?
		    OR (h.owner_scope = 'global' AND EXISTS(
		          SELECT 1 FROM global_host_grants g
		          WHERE g.host_id = h.id AND g.workspace IN (?, '*'))))`,
		hostID, WorkspaceOwnerScope(wsKey), wsKey).Scan(&n)
	return n > 0, err
}

// HostGrants returns the workspace allowlist for a global host ('*' = all).
func HostGrants(d *db.DB, hostID int64) ([]string, error) {
	rows, err := d.Query(`SELECT workspace FROM global_host_grants WHERE host_id=? ORDER BY workspace`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ws string
		if err := rows.Scan(&ws); err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

// SetHostGrants replaces a global host's workspace allowlist. Passing ['*']
// offers it to every workspace; an empty slice offers it to none.
func SetHostGrants(d *db.DB, hostID int64, workspaces []string) error {
	if _, err := d.Exec(`DELETE FROM global_host_grants WHERE host_id=?`, hostID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, ws := range workspaces {
		if ws == "" || seen[ws] {
			continue
		}
		seen[ws] = true
		if _, err := d.Exec(`INSERT OR IGNORE INTO global_host_grants (host_id, workspace) VALUES (?, ?)`, hostID, ws); err != nil {
			return err
		}
	}
	return nil
}

// GetHost returns a host including the encrypted SSH key (for dialing).
func GetHost(d *db.DB, id int64) (*Host, error) {
	var h Host
	err := d.QueryRow(`SELECT id, name, address, ssh_port, ssh_user, ssh_key_encrypted, ssh_host_key, workspaces_dir, owner_scope, created_at, updated_at FROM hosts WHERE id=?`, id).
		Scan(&h.ID, &h.Name, &h.Address, &h.SSHPort, &h.SSHUser, &h.SSHKeyEnc, &h.SSHHostKey, &h.WorkspacesDir, &h.OwnerScope, &h.CreatedAt, &h.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &h, err
}

func CreateHost(d *db.DB, name, address string, port int, user, keyEnc, workspacesDir, ownerScope string) (*Host, error) {
	if port == 0 {
		port = 22
	}
	if ownerScope == "" {
		ownerScope = "global"
	}
	res, err := d.Exec(`INSERT INTO hosts (name, address, ssh_port, ssh_user, ssh_key_encrypted, workspaces_dir, owner_scope) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		name, address, port, user, keyEnc, workspacesDir, ownerScope)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetHost(d, id)
}

// UpdateHost updates a host. An empty keyEnc keeps the existing key.
func UpdateHost(d *db.DB, id int64, name, address string, port int, user, keyEnc, workspacesDir string) (*Host, error) {
	if port == 0 {
		port = 22
	}
	if keyEnc == "" {
		_, err := d.Exec(`UPDATE hosts SET name=?, address=?, ssh_port=?, ssh_user=?, workspaces_dir=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			name, address, port, user, workspacesDir, id)
		if err != nil {
			return nil, err
		}
	} else {
		// Key changed → drop the stored host fingerprint so TOFU re-captures.
		_, err := d.Exec(`UPDATE hosts SET name=?, address=?, ssh_port=?, ssh_user=?, ssh_key_encrypted=?, ssh_host_key='', workspaces_dir=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			name, address, port, user, keyEnc, workspacesDir, id)
		if err != nil {
			return nil, err
		}
	}
	return GetHost(d, id)
}

func DeleteHost(d *db.DB, id int64) error {
	_, err := d.Exec(`DELETE FROM hosts WHERE id=?`, id)
	return err
}

// WorkspaceOwnedHostIDs returns the ids of hosts private to a workspace.
func WorkspaceOwnedHostIDs(d *db.DB, wsKey string) ([]int64, error) {
	rows, err := d.Query(`SELECT id FROM hosts WHERE owner_scope=?`, WorkspaceOwnerScope(wsKey))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetHostKey persists the TOFU host fingerprint captured on first connect.
func SetHostKey(d *db.DB, id int64, hostKey string) error {
	_, err := d.Exec(`UPDATE hosts SET ssh_host_key=? WHERE id=?`, hostKey, id)
	return err
}

// SetHostScope changes a host's ownership ('global' or 'ws:{key}'). Re-scoping a
// host to a workspace clears its global grants (a workspace-owned host is private).
func SetHostScope(d *db.DB, id int64, ownerScope string) error {
	if ownerScope == "" {
		ownerScope = "global"
	}
	if _, err := d.Exec(`UPDATE hosts SET owner_scope=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, ownerScope, id); err != nil {
		return err
	}
	if ownerScope != "global" {
		_, err := d.Exec(`DELETE FROM global_host_grants WHERE host_id=?`, id)
		return err
	}
	return nil
}

// SetEnvHost pins one environment to a host (Phase 7). hostID 0 clears the row
// (that env reverts to local). env="" sets the workspace-wide default used by
// environments without an explicit binding.
func SetEnvHost(d *db.DB, workspace, env string, hostID int64) error {
	if hostID == 0 {
		_, err := d.Exec(`DELETE FROM workspace_host_envs WHERE project=? AND env=?`, workspace, env)
		return err
	}
	_, err := d.Exec(
		`INSERT INTO workspace_host_envs (project, env, host_id) VALUES (?, ?, ?)
		 ON CONFLICT(project, env) DO UPDATE SET host_id=excluded.host_id`,
		workspace, env, hostID)
	return err
}

// HostForEnv resolves the host for one environment (including the encrypted key,
// for dialing): an explicit (workspace, env) row wins over the env='' workspace
// default. (nil, nil) means the environment runs on the local control plane.
func HostForEnv(d *db.DB, workspace, env string) (*Host, error) {
	var hostID int64
	// A non-empty env sorts after '' lexicographically, so ORDER BY env DESC
	// surfaces an exact-env row ahead of the env='' default.
	err := d.QueryRow(
		`SELECT host_id FROM workspace_host_envs WHERE project=? AND env IN (?, '') ORDER BY env DESC LIMIT 1`,
		workspace, env).Scan(&hostID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return GetHost(d, hostID)
}

// EnvHostBinding is one (env → host) association for a workspace; env="" is the
// workspace-wide default.
type EnvHostBinding struct {
	Env      string `json:"env"`
	HostID   int64  `json:"host_id"`
	HostName string `json:"host_name"`
	Address  string `json:"address"`
}

// EnvHosts lists every host binding for a workspace (incl. the env='' default),
// joined to host name + address. Environments with no row are local and not listed.
func EnvHosts(d *db.DB, workspace string) ([]EnvHostBinding, error) {
	rows, err := d.Query(
		`SELECT we.env, hs.id, hs.name, hs.address FROM workspace_host_envs we
		   JOIN hosts hs ON hs.id = we.host_id WHERE we.project=? ORDER BY we.env`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnvHostBinding
	for rows.Next() {
		var b EnvHostBinding
		if err := rows.Scan(&b.Env, &b.HostID, &b.HostName, &b.Address); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
