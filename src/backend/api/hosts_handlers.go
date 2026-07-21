package api

import (
	"bytes"
	"database/sql"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/edge"
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
	BuildOnly     bool     `json:"build_only"`      // dedicated builder — excluded from deploy targets
	Reachability  string   `json:"reachability"`    // 'public' (direct) | 'private' (behind control-plane gateway)
	PublicAddress string   `json:"public_address"`  // public web IP/hostname override ('' = use address)
	// AutoRefreshRoutes: on an address change that alters bound envs' auto-URLs,
	// redeploy those envs so the new address is baked into their Traefik labels.
	// Opt-in from the host editor's pre-save warning; false ⇒ save address only.
	AutoRefreshRoutes bool `json:"auto_refresh_routes"`
}

// buildOnlyConflict reports whether marking the host build-only would strand a
// project: it returns a user-facing message listing the environments still bound
// to this host as a deploy target, or "" when none (safe to designate build-only).
// A dedicated builder carries no workload and can be torn down at any time, so a
// host that is still a deploy target must be repointed first.
func buildOnlyConflict(d *db.DB, hostID int64) (string, error) {
	uses, err := settings.HostEnvBindings(d, hostID)
	if err != nil {
		return "", err
	}
	if len(uses) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(uses))
	for _, u := range uses {
		env := u.Env
		if env == "" {
			env = "(all environments)"
		}
		parts = append(parts, u.Project+" / "+env)
	}
	return "Can't mark this host build-only — it's still the deploy target for: " +
		strings.Join(parts, ", ") +
		". Move those environments to another host (Edit Project → Environments → Host), then try again.", nil
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

// hostSaveResp is the create response: the host, plus whether Rigger could reach
// it and the state of its remote workspaces directory. Embedding *settings.Host
// keeps the host's own fields at the top level for existing consumers.
type hostSaveResp struct {
	*settings.Host
	Connected            bool   `json:"connected"`
	ConnectError         string `json:"connect_error,omitempty"`
	WorkspacesDir        string `json:"workspaces_dir,omitempty"`
	WorkspacesDirExists  bool   `json:"workspaces_dir_exists"`
	WorkspacesDirCreated bool   `json:"workspaces_dir_created"`
	// EdgeInstalling ⇒ Rigger kicked off provisioning the Traefik edge (traefik_net +
	// traefik/socket-proxy/fallback) on this host in the background so web-routed
	// deploys there are reachable. Progress isn't awaited here (image pulls are slow);
	// the first routed deploy also ensures the edge, and the host Test reports its state.
	EdgeInstalling bool `json:"edge_installing"`
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
	if b.BuildOnly {
		// A freshly-created host has no env bindings yet, so no conflict check needed.
		_ = settings.SetHostBuildOnly(h.db, host.ID, true) //nolint:errcheck
		host.BuildOnly = true
	}
	if b.Reachability == "private" {
		_ = settings.SetHostReachability(h.db, host.ID, "private") //nolint:errcheck
		host.Reachability = "private"
	}
	if b.PublicAddress != "" {
		_ = settings.SetHostPublicAddress(h.db, host.ID, b.PublicAddress) //nolint:errcheck
		host.PublicAddress = strings.TrimSpace(b.PublicAddress)
	}
	host.Grants, _ = settings.HostGrants(h.db, host.ID)

	writeJSON(w, http.StatusCreated, h.provisionHostWorkspacesDir(host))
}

// provisionHostWorkspacesDir verifies reachability and creates the host's remote
// workspaces directory (mkdir -p) when missing, returning the create-response the UI
// uses to show connection + directory status. On a fresh host the operator has
// (hopefully) just installed the key — if we can connect, create the workspaces dir
// now so scan/deploy work immediately. If we can't connect, the host is still saved
// (they can fix the key and Test later); the response tells the UI to warn. Shared by
// the admin (CreateHost) and workspace (CreateWorkspaceHost) create paths so BOTH
// provision the dir on registration.
func (h *Handler) provisionHostWorkspacesDir(host *settings.Host) hostSaveResp {
	resp := hostSaveResp{Host: host, WorkspacesDir: h.hostWorkspacesDir(host)}
	if rh, derr := h.dialHost(host.ID); derr != nil {
		resp.ConnectError = derr.Error()
	} else {
		resp.Connected = true
		if exists, _ := rh.DirExists(resp.WorkspacesDir); exists {
			resp.WorkspacesDirExists = true
		} else if merr := rh.MkdirAll(resp.WorkspacesDir); merr != nil {
			resp.ConnectError = "connected, but couldn't create the workspaces directory " + resp.WorkspacesDir + ": " + merr.Error()
		} else {
			resp.WorkspacesDirExists = true
			resp.WorkspacesDirCreated = true
		}
		rh.Close()
		// With the host reachable and its workspaces dir ready, provision the Traefik
		// edge so routed workloads deployed here are reachable. Done in the background:
		// it pulls traefik/nginx images (slow) and the first routed deploy re-ensures it
		// anyway. Skip dedicated builders — they carry no workload. (remote-host edge/G1.)
		if resp.Connected && resp.WorkspacesDirExists && !host.BuildOnly {
			resp.EdgeInstalling = true
			go h.provisionHostEdgeAsync(host.ID)
		}
	}
	return resp
}

// provisionHostEdge ensures the Traefik edge is present + running on a host so
// web-routed workloads deployed there are reachable. It first probes (and persists)
// the host's swarm capability to pick the edge mode (standalone bridge vs Swarm
// overlay). out (may be nil) receives the deploy command's output.
func (h *Handler) provisionHostEdge(rh *remotehost.Client, host *settings.Host, out io.Writer) (edge.Result, error) {
	swarm := host.SwarmManager
	if info, perr := rh.RunCombined(`docker info --format '{{.Swarm.LocalNodeState}}|{{.Swarm.ControlAvailable}}'`); perr == nil {
		state, mgr := parseSwarmInfo(info)
		_ = settings.SetHostCapability(h.db, host.ID, state, mgr) //nolint:errcheck
		swarm = mgr
	}
	base := h.hostWorkspacesDir(host)
	ex := remotehost.NewRemote(rh, h.workspacesDir, base)
	return edge.Ensure(ex, rh, base, edge.Options{Swarm: swarm}, out)
}

// provisionHostEdgeAsync provisions the edge on its own SSH connection, for the
// background call at host registration. Failures are logged, not surfaced (the
// deploy-time guard re-ensures the edge before any routed deploy).
func (h *Handler) provisionHostEdgeAsync(id int64) {
	host, err := settings.GetHost(h.db, id)
	if err != nil || host == nil {
		return
	}
	rh, err := h.dialHost(id)
	if err != nil {
		log.Printf("edge: cannot dial host %q to provision edge: %v", host.Name, err)
		return
	}
	defer rh.Close()
	if res, err := h.provisionHostEdge(rh, host, nil); err != nil {
		log.Printf("edge: provisioning on host %q failed: %v", host.Name, err)
	} else if res.AlreadyRunning {
		log.Printf("edge: already running on host %q", host.Name)
	} else {
		log.Printf("edge: Traefik edge ready on host %q", host.Name)
	}
}

// installEdgeByID (re)installs the Traefik edge on a host synchronously and returns
// the deploy output. Shared by the admin + workspace install-edge endpoints.
func (h *Handler) installEdgeByID(w http.ResponseWriter, id int64) {
	host, err := settings.GetHost(h.db, id)
	if err != nil || host == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "host not found"})
		return
	}
	rh, err := h.dialHost(id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	defer rh.Close()
	var buf bytes.Buffer
	res, eerr := h.provisionHostEdge(rh, host, &buf)
	if eerr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": eerr.Error(), "log": buf.String()})
		return
	}
	msg := "Traefik edge is ready on " + host.Name
	if res.AlreadyRunning {
		msg = "Traefik edge already running on " + host.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "message": msg, "log": buf.String(),
		"already_running": res.AlreadyRunning, "network_created": res.NetworkCreated, "deployed": res.Deployed,
	})
}

// POST /api/hosts/{id}/install-edge — admin: (re)install the Traefik edge on a host.
func (h *Handler) InstallHostEdge(w http.ResponseWriter, r *http.Request) {
	id, err := parseSettingsID(strings.TrimSuffix(r.URL.Path, "/install-edge"), "/api/hosts/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	h.installEdgeByID(w, id)
}

// POST /api/hosts/{id}/workspaces-dir — dial the host and create its effective
// remote workspaces directory (mkdir -p). Used by the Test flow's "create it now"
// prompt when the directory is missing.
func (h *Handler) CreateHostWorkspacesDir(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/workspaces-dir")
	id, err := parseSettingsID(path, "/api/hosts/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	h.createWorkspacesDirByID(w, id)
}

// createWorkspacesDirByID dials a host and creates its effective remote workspaces
// directory (mkdir -p). Shared by the admin + workspace-scoped endpoints.
func (h *Handler) createWorkspacesDirByID(w http.ResponseWriter, id int64) {
	host, err := settings.GetHost(h.db, id)
	if err != nil || host == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "host not found"})
		return
	}
	rh, err := h.dialHost(id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "can't reach host over SSH: " + err.Error()})
		return
	}
	defer rh.Close()
	dir := h.hostWorkspacesDir(host)
	if err := rh.MkdirAll(dir); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "couldn't create " + dir + ": " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "workspaces_dir": dir, "created": true})
}

// hostComponents is a one-probe snapshot of a host's installable/provisionable
// components, backing the unified per-host "Components" panel (Docker / Nixpacks /
// Traefik edge / workspaces dir). ConnectError is set when the host is unreachable.
type hostComponents struct {
	Reachability        string `json:"reachability"`
	ConnectError        string `json:"connect_error,omitempty"`
	DockerVersion       string `json:"docker_version,omitempty"`
	DockerReachable     bool   `json:"docker_reachable"`
	NixpacksVersion     string `json:"nixpacks_version,omitempty"`
	EdgeRunning         bool   `json:"edge_running"`
	SwarmState          string `json:"swarm_state,omitempty"`
	SwarmManager        bool   `json:"swarm_manager"`
	WorkspacesDir       string `json:"workspaces_dir,omitempty"`
	WorkspacesDirExists bool   `json:"workspaces_dir_exists"`
}

// probeHostComponents gathers a host's component status in a single SSH connection.
func (h *Handler) probeHostComponents(id int64) (hostComponents, bool) {
	host, err := settings.GetHost(h.db, id)
	if err != nil || host == nil {
		return hostComponents{}, false
	}
	c := hostComponents{Reachability: host.Reachability, WorkspacesDir: h.hostWorkspacesDir(host)}
	rh, err := h.dialHost(id)
	if err != nil {
		c.ConnectError = err.Error()
		return c, true
	}
	defer rh.Close()
	if out, e := rh.RunCombined(`docker version --format '{{.Server.Version}}'`); e == nil {
		c.DockerVersion = strings.TrimSpace(out)
		c.DockerReachable = c.DockerVersion != ""
	}
	if out, e := rh.RunCombined(`nixpacks --version 2>/dev/null`); e == nil {
		c.NixpacksVersion = normalizeNixpacksVersion(strings.TrimSpace(out))
	}
	if info, e := rh.RunCombined(`docker info --format '{{.Swarm.LocalNodeState}}|{{.Swarm.ControlAvailable}}'`); e == nil {
		c.SwarmState, c.SwarmManager = parseSwarmInfo(info)
		_ = settings.SetHostCapability(h.db, id, c.SwarmState, c.SwarmManager) //nolint:errcheck
	}
	ex := remotehost.NewRemote(rh, h.workspacesDir, c.WorkspacesDir)
	c.EdgeRunning, _ = edge.Status(ex, c.SwarmManager)
	c.WorkspacesDirExists, _ = rh.DirExists(c.WorkspacesDir)
	return c, true
}

// GET /api/hosts/{id}/components — admin: per-host component status.
func (h *Handler) HostComponents(w http.ResponseWriter, r *http.Request) {
	id, err := parseSettingsID(strings.TrimSuffix(r.URL.Path, "/components"), "/api/hosts/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	c, ok := h.probeHostComponents(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "host not found"})
		return
	}
	writeJSON(w, http.StatusOK, c)
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
	// Guard the Deploy → Build-only transition: a build-only host carries no
	// workload and may be decommissioned, so it must not still be a deploy target.
	cur, _ := settings.GetHost(h.db, id)
	if b.BuildOnly && (cur == nil || !cur.BuildOnly) {
		if msg, gerr := buildOnlyConflict(h.db, id); gerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": gerr.Error()})
			return
		} else if msg != "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": msg})
			return
		}
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
	_ = settings.SetHostBuildOnly(h.db, id, b.BuildOnly) //nolint:errcheck
	host.BuildOnly = b.BuildOnly
	_ = settings.SetHostReachability(h.db, id, b.Reachability) //nolint:errcheck
	host.Reachability = b.Reachability
	_ = settings.SetHostPublicAddress(h.db, id, b.PublicAddress) //nolint:errcheck
	host.PublicAddress = strings.TrimSpace(b.PublicAddress)
	if host.OwnerScope == "global" && b.Grants != nil {
		_ = settings.SetHostGrants(h.db, id, b.Grants) //nolint:errcheck
	}
	host.Grants, _ = settings.HostGrants(h.db, id)
	h.bridge.EvictHost(id) // drop any pooled connection — address/key may have changed
	// If the address change breaks bound envs' auto-URLs and the user opted in,
	// redeploy those envs so the new address lands in their Traefik labels. Computed
	// from the OLD host (cur) → new web address; async + best-effort.
	if b.AutoRefreshRoutes {
		h.refreshImpactedRoutes(h.hostRouteImpact(cur, host.WebAddress()))
	}
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
	// Image-distribution Phase 5: probe Swarm capability so the deploy preflight and
	// the host-list badges know whether this host can run a Swarm stack. Best-effort —
	// a probe failure doesn't fail the Test (it just leaves the capability as-is).
	swarmState, swarmManager := "", false
	if info, perr := rh.RunCombined(`docker info --format '{{.Swarm.LocalNodeState}}|{{.Swarm.ControlAvailable}}'`); perr == nil {
		swarmState, swarmManager = parseSwarmInfo(info)
		_ = settings.SetHostCapability(h.db, id, swarmState, swarmManager) //nolint:errcheck
	}
	// Also report whether the remote workspaces directory exists — a successful
	// connection with a missing dir is the case the UI prompts to fix — and whether the
	// Traefik edge is running (so the UI can offer "Install edge" when it isn't).
	wsDir, wsExists, edgeRunning := "", false, false
	if host, _ := settings.GetHost(h.db, id); host != nil {
		wsDir = h.hostWorkspacesDir(host)
		wsExists, _ = rh.DirExists(wsDir)
		ex := remotehost.NewRemote(rh, h.workspacesDir, wsDir)
		edgeRunning, _ = edge.Status(ex, swarmManager)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok",
		"message":               "Connected — Docker " + strings.TrimSpace(out),
		"swarm_state":           swarmState,
		"swarm_manager":         swarmManager,
		"workspaces_dir":        wsDir,
		"workspaces_dir_exists": wsExists,
		"edge_running":          edgeRunning})
}

// POST /api/hosts/{id}/build-only — admin: mark/unmark a host as a dedicated
// builder (excluded from deploy-host pickers + env binding). Body: {"build_only": bool}.
func (h *Handler) SetHostBuildOnly(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/build-only")
	id, err := parseSettingsID(path, "/api/hosts/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	host, err := settings.GetHost(h.db, id)
	if err != nil || host == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "host not found"})
		return
	}
	var body struct {
		BuildOnly bool `json:"build_only"`
	}
	_ = readJSON(r, &body) //nolint:errcheck — absent/invalid ⇒ unset (false)
	if body.BuildOnly && !host.BuildOnly {
		if msg, gerr := buildOnlyConflict(h.db, id); gerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": gerr.Error()})
			return
		} else if msg != "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": msg})
			return
		}
	}
	if err := settings.SetHostBuildOnly(h.db, id, body.BuildOnly); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "build_only": body.BuildOnly})
}

// parseSwarmInfo parses `docker info --format '{{.Swarm.LocalNodeState}}|{{.Swarm.ControlAvailable}}'`
// output (e.g. "active|true") into the node's swarm state and whether it is a manager.
func parseSwarmInfo(out string) (state string, manager bool) {
	parts := strings.SplitN(strings.TrimSpace(out), "|", 2)
	state = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		manager = strings.EqualFold(strings.TrimSpace(parts[1]), "true")
	}
	return state, manager
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
