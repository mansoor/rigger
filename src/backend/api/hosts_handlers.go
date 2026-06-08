package api

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/remotehost"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/stats"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
)

// hostBody is the create/update payload. ssh_key is the plaintext PEM private
// key; it is encrypted at rest and never returned. An empty ssh_key on update
// keeps the existing key.
type hostBody struct {
	Name          string   `json:"name"`
	Address       string   `json:"address"`
	SSHPort       int      `json:"ssh_port"`
	SSHUser       string   `json:"ssh_user"`
	SSHKey        string   `json:"ssh_key"`
	UseManagedKey bool     `json:"use_managed_key"` // use the Rigger-managed key instead of a pasted one
	WorkspacesDir string   `json:"workspaces_dir"`  // remote WORKSPACES_DIR ('' = global default)
	Grants        []string `json:"grants"`          // admin only: global host's workspace allowlist ('*' = all)
}

// hostKeyEnc resolves the encrypted SSH key for a create/update from the request
// body: the Rigger-managed key, a freshly-pasted key, or "" to keep the existing
// one. ok is false when an error response has already been written.
func (h *Handler) hostKeyEnc(w http.ResponseWriter, b hostBody) (keyEnc string, ok bool) {
	switch {
	case b.UseManagedKey:
		_, enc, err := h.managedKey()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "managed key: " + err.Error()})
			return "", false
		}
		return enc, true
	case b.SSHKey != "":
		enc, err := crypto.Encrypt(h.cryptoKey, []byte(b.SSHKey))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "encrypt key: " + err.Error()})
			return "", false
		}
		return enc, true
	default:
		return "", true // keep existing key (update only)
	}
}

// hostWorkspacesDir returns a host's effective remote workspaces dir: its own
// setting, or the global REMOTE_WORKSPACES_DIR default when unset.
func (h *Handler) hostWorkspacesDir(host *settings.Host) string {
	if host != nil && host.WorkspacesDir != "" {
		return host.WorkspacesDir
	}
	return h.remoteWorkspacesDir
}

// managedKey returns the Rigger-managed SSH identity, generating + persisting it on
// first use. It returns the public authorized_keys line and the AES-GCM encrypted
// private key (ready to store in a host row).
func (h *Handler) managedKey() (pub, privEnc string, err error) {
	err = h.db.QueryRow(`SELECT public_key, private_key_encrypted FROM managed_ssh_key WHERE id=1`).Scan(&pub, &privEnc)
	if err == nil {
		return pub, privEnc, nil
	}
	if err != sql.ErrNoRows {
		return "", "", err
	}
	line, pemBytes, gerr := crypto.GenerateSSHKeypair("rigger-managed")
	if gerr != nil {
		return "", "", gerr
	}
	privEnc, err = crypto.Encrypt(h.cryptoKey, pemBytes)
	if err != nil {
		return "", "", err
	}
	if _, err = h.db.Exec(`INSERT INTO managed_ssh_key (id, public_key, private_key_encrypted) VALUES (1, ?, ?)`, line, privEnc); err != nil {
		return "", "", err
	}
	return line, privEnc, nil
}

// GET /api/hosts/managed-key — the Rigger-managed public key to install on a host.
func (h *Handler) ManagedHostKey(w http.ResponseWriter, r *http.Request) {
	pub, _, err := h.managedKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"public_key": pub})
}

// GET /api/hosts — admin view: every host across all scopes, each global host
// annotated with its workspace allowlist (grants).
func (h *Handler) ListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := settings.ListHosts(h.db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for i := range hosts {
		if hosts[i].OwnerScope == "global" {
			hosts[i].Grants, _ = settings.HostGrants(h.db, hosts[i].ID) //nolint:errcheck
		}
	}
	writeJSON(w, http.StatusOK, hosts)
}

// POST /api/hosts — admin create. Creates a global host; grants default to all
// workspaces ('*') when omitted.
func (h *Handler) CreateHost(w http.ResponseWriter, r *http.Request) {
	var b hostBody
	if err := readJSON(r, &b); err != nil || b.Name == "" || b.Address == "" || b.SSHUser == "" || (b.SSHKey == "" && !b.UseManagedKey) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, address, ssh_user and an SSH key (pasted or Rigger-managed) are required"})
		return
	}
	keyEnc, ok := h.hostKeyEnc(w, b)
	if !ok {
		return
	}
	host, err := settings.CreateHost(h.db, b.Name, b.Address, b.SSHPort, b.SSHUser, keyEnc, b.WorkspacesDir, "global")
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a host with that name already exists"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	grants := b.Grants
	if grants == nil {
		grants = []string{"*"} // default: offered to every workspace
	}
	_ = settings.SetHostGrants(h.db, host.ID, grants) //nolint:errcheck
	host.Grants, _ = settings.HostGrants(h.db, host.ID)
	writeJSON(w, http.StatusCreated, host)
}

// PUT /api/hosts/{id} — admin update, including the workspace allowlist (grants)
// for global hosts.
func (h *Handler) UpdateHost(w http.ResponseWriter, r *http.Request) {
	id, err := parseSettingsID(r.URL.Path, "/api/hosts/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var b hostBody
	if err := readJSON(r, &b); err != nil || b.Name == "" || b.Address == "" || b.SSHUser == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, address and ssh_user are required"})
		return
	}
	keyEnc, ok := h.hostKeyEnc(w, b)
	if !ok {
		return
	}
	host, err := settings.UpdateHost(h.db, id, b.Name, b.Address, b.SSHPort, b.SSHUser, keyEnc, b.WorkspacesDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if host == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if host.OwnerScope == "global" && b.Grants != nil {
		_ = settings.SetHostGrants(h.db, id, b.Grants) //nolint:errcheck
	}
	host.Grants, _ = settings.HostGrants(h.db, id)
	h.bridge.EvictHost(id) // drop any pooled connection — address/key may have changed
	writeJSON(w, http.StatusOK, host)
}

// DELETE /api/hosts/{id}
func (h *Handler) DeleteHost(w http.ResponseWriter, r *http.Request) {
	id, err := parseSettingsID(r.URL.Path, "/api/hosts/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := settings.DeleteHost(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.bridge.EvictHost(id)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/hosts/{id}/test — SSH-connect and run `docker version` on the host.
// On a first successful connect the TOFU host fingerprint is persisted.
func (h *Handler) TestHost(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/test")
	id, err := parseSettingsID(path, "/api/hosts/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	h.testHostByID(w, id)
}

func (h *Handler) testHostByID(w http.ResponseWriter, id int64) {
	rh, err := h.dialHost(id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	defer rh.Close()

	out, err := rh.RunCombined(`docker version --format '{{.Server.Version}}' 2>&1 || docker version`)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error",
			"error": "connected, but docker not reachable on host: " + strings.TrimSpace(out)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok",
		"message": "Connected — Docker " + strings.TrimSpace(out)})
}

// dialHost loads a host, decrypts its key, dials it, and persists the TOFU
// fingerprint on first connect. Caller must Close the returned client.
func (h *Handler) dialHost(id int64) (*remotehost.Client, error) {
	host, err := settings.GetHost(h.db, id)
	if err != nil || host == nil {
		return nil, errHostNotFound
	}
	keyPEM, err := crypto.Decrypt(h.cryptoKey, host.SSHKeyEnc)
	if err != nil {
		return nil, err
	}
	client, err := remotehost.Dial(remotehost.Host{
		ID: host.ID, Name: host.Name, Address: host.Address,
		Port: host.SSHPort, User: host.SSHUser,
		PrivateKey: keyPEM, HostKey: host.SSHHostKey,
	})
	if err != nil {
		return nil, err
	}
	if host.SSHHostKey == "" && client.HostKey != "" {
		_ = settings.SetHostKey(h.db, host.ID, client.HostKey) // persist TOFU fingerprint
	}
	return client, nil
}

// scannedWorkspace is one workspace discovered on a remote host.
type scannedWorkspace struct {
	Name     string   `json:"name"`
	Project  string   `json:"project"`
	Type     string   `json:"type"`
	Envs     []string `json:"envs"`
	Imported bool     `json:"imported"` // already associated with this host locally
}

// POST /api/hosts/{id}/scan — SSH to the host, list REMOTE_WORKSPACES_DIR, and
// parse each workspace's config.json. Returns the discovered workspaces.
func (h *Handler) ScanHost(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/scan")
	id, err := parseSettingsID(path, "/api/hosts/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	rh, err := h.dialHost(id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	defer rh.Close()

	host, _ := settings.GetHost(h.db, id)
	root := h.hostWorkspacesDir(host)
	names, err := rh.ListDir(root)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error",
			"error": "list " + root + ": " + err.Error()})
		return
	}

	// Which of this host's workspaces are already imported locally?
	imported := map[string]bool{}
	if rows, qerr := h.db.Query(`SELECT DISTINCT project FROM workspace_host_envs WHERE host_id=?`, id); qerr == nil {
		for rows.Next() {
			var ws string
			if rows.Scan(&ws) == nil { //nolint:errcheck
				imported[ws] = true
			}
		}
		rows.Close()
	}

	found := []scannedWorkspace{}
	for _, name := range names {
		data, rerr := rh.ReadFile(root + "/" + name + "/config.json")
		if rerr != nil {
			continue // not a workspace dir (no config.json) — skip
		}
		cfg, perr := wsconfig.Parse(data)
		if perr != nil {
			continue
		}
		found = append(found, scannedWorkspace{
			Name:     name,
			Project:  cfg.Project.Name,
			Type:     cfg.ProjectType(),
			Envs:     cfg.EnvNames(),
			Imported: imported[name],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "workspaces": found})
}

// POST /api/hosts/{id}/import {"workspaces": ["name", ...]} — for each named
// workspace, cache its remote config.json locally and associate it with the
// host (so workspace.List surfaces it, badged with the host).
func (h *Handler) ImportHost(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/import")
	id, err := parseSettingsID(path, "/api/hosts/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body struct {
		Workspaces []string `json:"workspaces"`
	}
	if err := readJSON(r, &body); err != nil || len(body.Workspaces) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspaces is required"})
		return
	}
	rh, err := h.dialHost(id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	defer rh.Close()

	host, _ := settings.GetHost(h.db, id)
	root := h.hostWorkspacesDir(host)
	imported := []string{}
	errs := map[string]string{}
	for _, name := range body.Workspaces {
		if name == "" || strings.ContainsAny(name, "/\\") {
			errs[name] = "invalid workspace name"
			continue
		}
		data, rerr := rh.ReadFile(root + "/" + name + "/config.json")
		if rerr != nil {
			errs[name] = "read remote config.json: " + rerr.Error()
			continue
		}
		cfg, perr := wsconfig.Parse(data)
		if perr != nil {
			errs[name] = "parse config.json: " + perr.Error()
			continue
		}
		dir := filepath.Join(h.workspacesDir, name)
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			errs[name] = mkErr.Error()
			continue
		}
		if wErr := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); wErr != nil {
			errs[name] = wErr.Error()
			continue
		}
		// Bind every discovered environment to the host with an explicit per-env
		// row, so each env can later be moved independently (an explicit row also
		// lets a future move to Local clear it cleanly).
		bindErr := ""
		for _, en := range cfg.EnvNames() {
			if dbErr := settings.SetEnvHost(h.db, name, en, id); dbErr != nil {
				bindErr = dbErr.Error()
				break
			}
		}
		if bindErr != "" {
			errs[name] = bindErr
			continue
		}
		imported = append(imported, name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "imported": imported, "errors": errs})
}

// GET /api/hosts/{id}/stats — SSH to the host and gather Docker + host health
// (docker info, df, meminfo, nproc, uname, os-release, uptime).
func (h *Handler) HostStats(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/stats")
	id, err := parseSettingsID(path, "/api/hosts/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	h.hostStatsByID(w, id)
}

func (h *Handler) hostStatsByID(w http.ResponseWriter, id int64) {
	rh, err := h.dialHost(id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	defer rh.Close()

	docker, host := stats.CollectRemote(rh)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "docker": docker, "host": host})
}

// envHostName returns the name of the remote host an environment runs on, or ""
// when it is local. Used to stamp the audit log (Phase 7). An empty env resolves
// the workspace-wide default.
func (h *Handler) envHostName(workspace, env string) string {
	host, err := settings.HostForEnv(h.db, workspace, env)
	if err != nil || host == nil {
		return ""
	}
	return host.Name
}

var errHostNotFound = errString("host not found")

type errString string

func (e errString) Error() string { return string(e) }
