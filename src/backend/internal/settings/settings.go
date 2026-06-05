package settings

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
)

// ── Backup Targets ────────────────────────────────────────────────────────────

type BackupTarget struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Type      string          `json:"type"` // "s3" | "sftp"
	Config    json.RawMessage `json:"config"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
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

func ListBackupTargets(d *db.DB) ([]BackupTarget, error) {
	rows, err := d.Query(`SELECT id, name, type, config, created_at, updated_at FROM backup_targets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BackupTarget
	for rows.Next() {
		var t BackupTarget
		var cfg string
		if err := rows.Scan(&t.ID, &t.Name, &t.Type, &cfg, &t.CreatedAt, &t.UpdatedAt); err != nil {
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

func GetBackupTarget(d *db.DB, id int64) (*BackupTarget, error) {
	var t BackupTarget
	var cfg string
	err := d.QueryRow(`SELECT id, name, type, config, created_at, updated_at FROM backup_targets WHERE id = ?`, id).
		Scan(&t.ID, &t.Name, &t.Type, &cfg, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Config = json.RawMessage(cfg)
	return &t, nil
}

func CreateBackupTarget(d *db.DB, name, typ string, cfg json.RawMessage) (*BackupTarget, error) {
	if err := validateBackupType(typ); err != nil {
		return nil, err
	}
	res, err := d.Exec(`INSERT INTO backup_targets (name, type, config) VALUES (?, ?, ?)`, name, typ, string(cfg))
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
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Username  string    `json:"username"`
	Password  string    `json:"password,omitempty"` // omitted in list responses
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func ListRegistries(d *db.DB) ([]DockerRegistry, error) {
	rows, err := d.Query(`SELECT id, name, url, username, created_at, updated_at FROM docker_registries ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DockerRegistry
	for rows.Next() {
		var r DockerRegistry
		if err := rows.Scan(&r.ID, &r.Name, &r.URL, &r.Username, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if out == nil {
		out = []DockerRegistry{}
	}
	return out, rows.Err()
}

func GetRegistry(d *db.DB, id int64) (*DockerRegistry, error) {
	var r DockerRegistry
	err := d.QueryRow(`SELECT id, name, url, username, password, created_at, updated_at FROM docker_registries WHERE id=?`, id).
		Scan(&r.ID, &r.Name, &r.URL, &r.Username, &r.Password, &r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &r, err
}

func CreateRegistry(d *db.DB, name, url, username, password string) (*DockerRegistry, error) {
	res, err := d.Exec(`INSERT INTO docker_registries (name, url, username, password) VALUES (?, ?, ?, ?)`,
		name, url, username, password)
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
	WorkspacesDir string    `json:"workspaces_dir"` // remote WORKSPACES_DIR ('' = global default)
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func ListHosts(d *db.DB) ([]Host, error) {
	rows, err := d.Query(`SELECT id, name, address, ssh_port, ssh_user, ssh_host_key, workspaces_dir, created_at, updated_at FROM hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Host
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.ID, &h.Name, &h.Address, &h.SSHPort, &h.SSHUser, &h.SSHHostKey, &h.WorkspacesDir, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if out == nil {
		out = []Host{}
	}
	return out, rows.Err()
}

// GetHost returns a host including the encrypted SSH key (for dialing).
func GetHost(d *db.DB, id int64) (*Host, error) {
	var h Host
	err := d.QueryRow(`SELECT id, name, address, ssh_port, ssh_user, ssh_key_encrypted, ssh_host_key, workspaces_dir, created_at, updated_at FROM hosts WHERE id=?`, id).
		Scan(&h.ID, &h.Name, &h.Address, &h.SSHPort, &h.SSHUser, &h.SSHKeyEnc, &h.SSHHostKey, &h.WorkspacesDir, &h.CreatedAt, &h.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &h, err
}

func CreateHost(d *db.DB, name, address string, port int, user, keyEnc, workspacesDir string) (*Host, error) {
	if port == 0 {
		port = 22
	}
	res, err := d.Exec(`INSERT INTO hosts (name, address, ssh_port, ssh_user, ssh_key_encrypted, workspaces_dir) VALUES (?, ?, ?, ?, ?, ?)`,
		name, address, port, user, keyEnc, workspacesDir)
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

// SetHostKey persists the TOFU host fingerprint captured on first connect.
func SetHostKey(d *db.DB, id int64, hostKey string) error {
	_, err := d.Exec(`UPDATE hosts SET ssh_host_key=? WHERE id=?`, hostKey, id)
	return err
}

// SetEnvHost pins one environment to a host (Phase 7). hostID 0 clears the row
// (that env reverts to local). env="" sets the workspace-wide default used by
// environments without an explicit binding.
func SetEnvHost(d *db.DB, workspace, env string, hostID int64) error {
	if hostID == 0 {
		_, err := d.Exec(`DELETE FROM workspace_host_envs WHERE workspace=? AND env=?`, workspace, env)
		return err
	}
	_, err := d.Exec(
		`INSERT INTO workspace_host_envs (workspace, env, host_id) VALUES (?, ?, ?)
		 ON CONFLICT(workspace, env) DO UPDATE SET host_id=excluded.host_id`,
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
		`SELECT host_id FROM workspace_host_envs WHERE workspace=? AND env IN (?, '') ORDER BY env DESC LIMIT 1`,
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
		   JOIN hosts hs ON hs.id = we.host_id WHERE we.workspace=? ORDER BY we.env`, workspace)
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
