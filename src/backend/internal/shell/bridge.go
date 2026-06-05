package shell

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mansoor/rigger/ui/internal/backup"
	"github.com/mansoor/rigger/ui/internal/builder"
	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/dockerops"
	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/remotehost"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/stats"
	"github.com/mansoor/rigger/ui/internal/version"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
)

// Allowlisted run.sh commands. Nothing outside this list can be executed.
var allowedCommands = map[string]bool{
	"start":   true,
	"stop":    true,
	"down":    true,
	"restart": true,
	"update":  true,
	"ps":      true,
	"logs":    true,
	"refresh": true,
	"backup":  true,
	"restore": true,
	"init":    true,
	"version": true,
	"build":   true,
	"promote": true,
}

// Bridge executes workspace commands, locally or — when a workspace is
// associated with a remote host — over SSH (Phase 7).
type Bridge struct {
	workspacesDir       string
	remoteWorkspacesDir string
	toolkitRoot         string

	// Phase 7 multi-host. All nil ⇒ local-only (every workspace runs locally,
	// identical to pre-Phase-7 behavior); resolveRemote short-circuits to local.
	db        *db.DB
	pool      *remotehost.Pool
	cryptoKey []byte
}

// NewBridge builds a bridge. Pass a nil db/pool (and the local workspaces dir as
// remoteWorkspacesDir) for a local-only bridge — e.g. the init-workspace CLI.
func NewBridge(workspacesDir, remoteWorkspacesDir, toolkitRoot string, database *db.DB, pool *remotehost.Pool, cryptoKey []byte) *Bridge {
	if remoteWorkspacesDir == "" {
		remoteWorkspacesDir = workspacesDir
	}
	return &Bridge{
		workspacesDir:       workspacesDir,
		remoteWorkspacesDir: remoteWorkspacesDir,
		toolkitRoot:         toolkitRoot,
		db:                  database,
		pool:                pool,
		cryptoKey:           cryptoKey,
	}
}

// remoteTarget carries the resolved SSH executor for a remote workspace.
type remoteTarget struct {
	exec     remotehost.Remote
	client   *remotehost.Client
	hostName string
}

// resolveRemote returns the remote target for one environment, or (nil, nil) when
// that environment is local. Local-only bridges (nil db/pool) always return nil.
func (b *Bridge) resolveRemote(workspaceName, env string) (*remoteTarget, error) {
	if b.db == nil || b.pool == nil {
		return nil, nil
	}
	host, err := settings.HostForEnv(b.db, workspaceName, env)
	if err != nil || host == nil {
		return nil, err
	}
	keyPEM, err := crypto.Decrypt(b.cryptoKey, host.SSHKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt key for host %q: %w", host.Name, err)
	}
	client, err := b.pool.Get(remotehost.Host{
		ID: host.ID, Name: host.Name, Address: host.Address,
		Port: host.SSHPort, User: host.SSHUser,
		PrivateKey: keyPEM, HostKey: host.SSHHostKey,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to host %q: %w", host.Name, err)
	}
	return &remoteTarget{
		exec:     remotehost.NewRemote(client, b.workspacesDir, b.hostBase(host)),
		client:   client,
		hostName: host.Name,
	}, nil
}

// hostBase returns a host's remote WORKSPACES_DIR — its own setting, or the
// global default when unset.
func (b *Bridge) hostBase(host *settings.Host) string {
	if host != nil && host.WorkspacesDir != "" {
		return host.WorkspacesDir
	}
	return b.remoteWorkspacesDir
}

// Migrate moves a whole workspace to targetHostID (0 = local control plane). It
// is only permitted when every environment currently shares one host; mixed
// setups must be moved per environment. It simply migrates each env in turn.
func (b *Bridge) Migrate(workspaceName string, targetHostID int64, out io.Writer) error {
	if b.db == nil || b.pool == nil {
		return fmt.Errorf("migration requires multi-host support")
	}
	ws, err := workspace.Get(b.workspacesDir, workspaceName)
	if err != nil {
		return fmt.Errorf("load workspace: %w", err)
	}
	if len(ws.Envs) == 0 {
		return fmt.Errorf("workspace %q has no environments to migrate", workspaceName)
	}
	common, mixed, err := b.commonHost(workspaceName, ws.Envs)
	if err != nil {
		return err
	}
	if mixed {
		return fmt.Errorf("environments are on different hosts — move them individually instead")
	}
	if common == targetHostID {
		return fmt.Errorf("workspace is already on %s", hostLabel(targetHostID))
	}
	for _, env := range ws.Envs {
		if err := b.MigrateEnv(workspaceName, env, targetHostID, out); err != nil {
			return fmt.Errorf("%s: %w", env, err)
		}
	}
	fmt.Fprintf(out, "✓ Migration complete: %s is now on %s\n", workspaceName, hostLabel(targetHostID))
	return nil
}

// MigrateEnv moves one environment to targetHostID (0 = local). If the env is not
// deployed on its current host it is a plain repoint (next deploy provisions it).
// If it is deployed, its data is moved: back up + stop on the source, ship files
// to the target, repoint, then start + restore on the target. The source copy is
// stopped but its data is left intact. Streams progress to out.
func (b *Bridge) MigrateEnv(workspaceName, env string, targetHostID int64, out io.Writer) error {
	if b.db == nil || b.pool == nil {
		return fmt.Errorf("multi-host support is not configured")
	}
	if targetHostID != 0 {
		if h, err := settings.GetHost(b.db, targetHostID); err != nil || h == nil {
			return fmt.Errorf("target host %d not found", targetHostID)
		}
	}
	srcHost, err := settings.HostForEnv(b.db, workspaceName, env)
	if err != nil {
		return err
	}
	srcID := int64(0)
	if srcHost != nil {
		srcID = srcHost.ID
	}
	if srcID == targetHostID {
		return fmt.Errorf("environment %q is already on %s", env, hostLabel(targetHostID))
	}

	srcRT, err := b.resolveRemote(workspaceName, env) // still points at the source
	if err != nil {
		return fmt.Errorf("connect to source: %w", err)
	}

	cfg, err := wsconfig.Load(filepath.Join(b.workspacesDir, workspaceName, "config.json"))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	stack := cfg.StackName(env)

	deployed, err := b.envDeployed(stack, srcRT)
	if err != nil {
		return fmt.Errorf("check deployment state: %w", err)
	}

	if !deployed {
		if err := settings.SetEnvHost(b.db, workspaceName, env, targetHostID); err != nil {
			return err
		}
		fmt.Fprintf(out, "✓ %s/%s repointed to %s (not deployed — provisions on next deploy)\n",
			workspaceName, env, hostLabel(targetHostID))
		return nil
	}

	fmt.Fprintf(out, "▶ Backing up %s/%s on %s…\n", workspaceName, env, hostLabel(srcID))
	if err := b.Run(RunOptions{Workspace: workspaceName, Command: "backup", Env: env, Extra: []string{"all"}, Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("backup source: %w", err)
	}
	snap, err := b.latestSnapshot(workspaceName, env)
	if err != nil {
		return fmt.Errorf("locate snapshot: %w", err)
	}

	// Stop the source stack before repointing (containers down, volumes kept) so
	// two copies never run at once.
	fmt.Fprintf(out, "▶ Stopping %s/%s on %s (data kept)…\n", workspaceName, env, hostLabel(srcID))
	if err := b.Run(RunOptions{Workspace: workspaceName, Command: "stop", Env: env, Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("stop source: %w", err)
	}

	// Bring compose + .env to the control plane if the source is remote.
	localDir := b.localEnvDir(workspaceName, env)
	if srcRT != nil {
		if err := srcRT.client.PullDir(srcRT.exec.RemoteDir(localDir), localDir); err != nil {
			return fmt.Errorf("pull files from source: %w", err)
		}
	}

	if err := settings.SetEnvHost(b.db, workspaceName, env, targetHostID); err != nil {
		return fmt.Errorf("repoint: %w", err)
	}
	fmt.Fprintf(out, "▶ Repointed %s/%s to %s\n", workspaceName, env, hostLabel(targetHostID))

	tgtRT, err := b.resolveRemote(workspaceName, env) // now points at the target
	if err != nil {
		return fmt.Errorf("connect to target: %w", err)
	}
	if tgtRT != nil {
		if err := tgtRT.client.PushDir(localDir, tgtRT.exec.RemoteDir(localDir)); err != nil {
			return fmt.Errorf("push files to target: %w", err)
		}
	}

	fmt.Fprintf(out, "▶ Starting %s/%s on %s…\n", workspaceName, env, hostLabel(targetHostID))
	if err := b.Run(RunOptions{Workspace: workspaceName, Command: "start", Env: env, Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("start target: %w", err)
	}
	if snap != "" {
		fmt.Fprintf(out, "▶ Restoring snapshot %s on %s…\n", snap, hostLabel(targetHostID))
		if err := b.Run(RunOptions{Workspace: workspaceName, Command: "restore", Env: env, Extra: []string{snap}, Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("restore target: %w", err)
		}
	}

	// Record what was left behind on the source so it can be wiped via Housekeeping.
	srcName := "local control plane"
	if srcHost != nil {
		srcName = srcHost.Name
	}
	b.recordLeftover(srcID, srcName, workspaceName, env, stack)

	fmt.Fprintf(out, "✓ %s/%s now on %s (old copy stopped, data kept on %s — wipe it in Housekeeping if decommissioning)\n",
		workspaceName, env, hostLabel(targetHostID), srcName)
	return nil
}

// envDeployed reports whether a compose project has any containers (running or
// stopped) on its current host, by querying the daemon for the project label —
// no compose file required.
func (b *Bridge) envDeployed(stack string, rt *remoteTarget) (bool, error) {
	var exec executor.Executor = executor.Local{}
	if rt != nil {
		exec = rt.exec
	}
	out, err := exec.DockerOutput(executor.Spec{
		Args: []string{"ps", "-aq", "--filter", "label=com.docker.compose.project=" + stack},
	})
	if err != nil {
		return false, nil // can't tell → treat as not deployed (safe: plain repoint)
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

// commonHost returns the host id shared by every env (0 = local), or mixed=true
// when they are not all on the same host.
func (b *Bridge) commonHost(workspaceName string, envs []string) (hostID int64, mixed bool, err error) {
	first := int64(-1)
	for _, env := range envs {
		h, err := settings.HostForEnv(b.db, workspaceName, env)
		if err != nil {
			return 0, false, err
		}
		id := int64(0)
		if h != nil {
			id = h.ID
		}
		if first == -1 {
			first = id
		} else if id != first {
			return 0, true, nil
		}
	}
	if first == -1 {
		first = 0
	}
	return first, false, nil
}

// ── Migration leftovers (Housekeeping) ────────────────────────────────────────

// Leftover is data/files an environment left on a source host after migrating
// away (host_id 0 = local control plane).
type Leftover struct {
	ID        int64     `json:"id"`
	HostID    int64     `json:"host_id"`
	HostName  string    `json:"host_name"`
	Workspace string    `json:"workspace"`
	Env       string    `json:"env"`
	Stack     string    `json:"stack"`
	CreatedAt time.Time `json:"created_at"`
}

// recordLeftover notes (best-effort) that a migrated env left data on a source host.
func (b *Bridge) recordLeftover(hostID int64, hostName, ws, env, stack string) {
	if b.db == nil {
		return
	}
	b.db.Exec(`INSERT INTO migration_leftovers (host_id, host_name, workspace, env, stack)
		VALUES (?,?,?,?,?)
		ON CONFLICT(host_id, workspace, env) DO UPDATE SET host_name=excluded.host_name, stack=excluded.stack, created_at=CURRENT_TIMESTAMP`,
		hostID, hostName, ws, env, stack) //nolint:errcheck
}

// ListLeftovers returns all recorded source-host leftovers, newest first.
func (b *Bridge) ListLeftovers() ([]Leftover, error) {
	rows, err := b.db.Query(`SELECT id, host_id, host_name, workspace, env, stack, created_at FROM migration_leftovers ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Leftover{}
	for rows.Next() {
		var l Leftover
		if err := rows.Scan(&l.ID, &l.HostID, &l.HostName, &l.Workspace, &l.Env, &l.Stack, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DismissLeftover drops a leftover record without touching the host (the user
// cleaned it up themselves).
func (b *Bridge) DismissLeftover(id int64) error {
	_, err := b.db.Exec(`DELETE FROM migration_leftovers WHERE id=?`, id)
	return err
}

// hostExec returns an executor and (for remote hosts) an SSH client for an
// arbitrary host id (0 = local control plane; client is nil for local).
func (b *Bridge) hostExec(hostID int64) (executor.Executor, *remotehost.Client, error) {
	if hostID == 0 {
		return executor.Local{}, nil, nil
	}
	if b.db == nil || b.pool == nil {
		return nil, nil, fmt.Errorf("multi-host support is not configured")
	}
	host, err := settings.GetHost(b.db, hostID)
	if err != nil || host == nil {
		return nil, nil, fmt.Errorf("host %d not found", hostID)
	}
	keyPEM, err := crypto.Decrypt(b.cryptoKey, host.SSHKeyEnc)
	if err != nil {
		return nil, nil, err
	}
	client, err := b.pool.Get(remotehost.Host{
		ID: host.ID, Name: host.Name, Address: host.Address,
		Port: host.SSHPort, User: host.SSHUser, PrivateKey: keyPEM, HostKey: host.SSHHostKey,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("connect to host %q: %w", host.Name, err)
	}
	return remotehost.NewRemote(client, b.workspacesDir, b.hostBase(host)), client, nil
}

// CleanLeftover permanently removes a source host's leftover containers, named
// volumes and env-dir files (compose + .env secrets + bind-mount data) for a
// migrated environment, then drops the record. Destructive — intended for
// decommissioning a host. Streams progress to out.
func (b *Bridge) CleanLeftover(id int64, out io.Writer) error {
	if b.db == nil {
		return fmt.Errorf("multi-host support is not configured")
	}
	var l Leftover
	err := b.db.QueryRow(`SELECT id, host_id, host_name, workspace, env, stack FROM migration_leftovers WHERE id=?`, id).
		Scan(&l.ID, &l.HostID, &l.HostName, &l.Workspace, &l.Env, &l.Stack)
	if err != nil {
		return fmt.Errorf("leftover not found: %w", err)
	}
	exec, client, err := b.hostExec(l.HostID)
	if err != nil {
		return err
	}
	where := l.HostName
	if where == "" {
		where = hostLabel(l.HostID)
	}
	label := "label=com.docker.compose.project=" + l.Stack

	// 1. Remove the stack's containers.
	if ids, derr := exec.DockerOutput(executor.Spec{Args: []string{"ps", "-aq", "--filter", label}}); derr == nil {
		if fields := strings.Fields(string(ids)); len(fields) > 0 {
			fmt.Fprintf(out, "▶ Removing %d container(s) on %s…\n", len(fields), where)
			_ = exec.Docker(executor.Spec{Args: append([]string{"rm", "-f"}, fields...), Stdout: out, Stderr: out})
		}
	}
	// 2. Remove the stack's named volumes.
	if vols, derr := exec.DockerOutput(executor.Spec{Args: []string{"volume", "ls", "-q", "--filter", label}}); derr == nil {
		if fields := strings.Fields(string(vols)); len(fields) > 0 {
			fmt.Fprintf(out, "▶ Removing %d volume(s) on %s…\n", len(fields), where)
			_ = exec.Docker(executor.Spec{Args: append([]string{"volume", "rm", "-f"}, fields...), Stdout: out, Stderr: out})
		}
	}
	// 3. Remove the env dir (compose, .env secrets, bind-mount data).
	localDir := b.localEnvDir(l.Workspace, l.Env)
	if client == nil {
		fmt.Fprintf(out, "▶ Removing files %s…\n", localDir)
		_ = os.RemoveAll(localDir)
	} else {
		host, _ := settings.GetHost(b.db, l.HostID)
		remoteDir := b.hostBase(host) + strings.TrimPrefix(localDir, b.workspacesDir)
		fmt.Fprintf(out, "▶ Removing files on %s: %s…\n", where, remoteDir)
		if msg, rerr := client.RunCombined("rm -rf '" + strings.ReplaceAll(remoteDir, "'", `'\''`) + "'"); rerr != nil {
			fmt.Fprintf(out, "⚠ file removal: %s %v\n", strings.TrimSpace(msg), rerr)
		}
	}

	b.db.Exec(`DELETE FROM migration_leftovers WHERE id=?`, id) //nolint:errcheck
	fmt.Fprintf(out, "✓ Cleaned %s/%s on %s\n", l.Workspace, l.Env, where)
	return nil
}

// ── Multi-host workload stats (dashboard + metrics) ──────────────────────────

// hostExecutors returns the docker executor for the local control plane plus each
// remote host that has at least one environment bound to it. Unreachable hosts
// are skipped (their workloads simply show no data until the host recovers).
func (b *Bridge) hostExecutors() map[int64]executor.Executor {
	out := map[int64]executor.Executor{0: executor.Local{}}
	if b.db == nil || b.pool == nil {
		return out
	}
	// Read all host ids first and CLOSE the cursor before dialing: hostExec runs
	// its own DB query (GetHost), and querying while this cursor is open deadlocks
	// on SQLite's connection-limited pool.
	var ids []int64
	rows, err := b.db.Query(`SELECT DISTINCT host_id FROM workspace_host_envs WHERE host_id != 0`)
	if err != nil {
		return out
	}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	for _, id := range ids {
		if ex, _, herr := b.hostExec(id); herr == nil {
			out[id] = ex
		}
	}
	return out
}

// fanout runs gather against every host's executor concurrently and merges the
// per-project results (project names are unique to a single host).
func fanout[V any](execs map[int64]executor.Executor, gather func(executor.Executor) map[string]V) map[string]V {
	merged := map[string]V{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ex := range execs {
		wg.Add(1)
		go func(ex executor.Executor) {
			defer wg.Done()
			m := gather(ex)
			mu.Lock()
			for k, v := range m {
				merged[k] = v
			}
			mu.Unlock()
		}(ex)
	}
	wg.Wait()
	return merged
}

// LiveStats returns near-real-time per-project stats merged across every host.
func (b *Bridge) LiveStats() map[string]stats.ProjectLive {
	return fanout(b.hostExecutors(), stats.LiveProjectStatsFor)
}

// ProjectStats returns per-project resource usage merged across every host (for
// the metrics collector).
func (b *Bridge) ProjectStatsAllHosts() map[string]stats.ProjectStats {
	return fanout(b.hostExecutors(), stats.ContainerStatsByProjectFor)
}

// Stats builds the full dashboard payload with per-project running/memory counts
// aggregated across all hosts (Docker/Host sections stay control-plane local).
func (b *Bridge) Stats() stats.Stats {
	execs := b.hostExecutors()
	running := fanout(execs, stats.RunningByProjectFor)
	mem := fanout(execs, stats.MemByProjectFor)
	return stats.CollectWith(b.workspacesDir, running, mem)
}

// latestSnapshot returns the most recent snapshot dir name under a workspace
// env's backups dir (timestamps sort lexicographically).
func (b *Bridge) latestSnapshot(workspaceName, env string) (string, error) {
	dir := filepath.Join(b.workspacesDir, workspaceName, "backups", env)
	entries, err := os.ReadDir(dir)
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
		return "", fmt.Errorf("no snapshot found in %s", dir)
	}
	return latest, nil
}

// hostLabel renders a host id for progress output.
func hostLabel(id int64) string {
	if id == 0 {
		return "local control plane"
	}
	return fmt.Sprintf("host #%d", id)
}

// EvictHost drops any pooled SSH connection for a host, forcing a re-dial on the
// next command. Call it when a host's address/key changes or it is deleted.
func (b *Bridge) EvictHost(id int64) {
	if b.pool != nil {
		b.pool.Evict(id)
	}
}

// localEnvDir is the control-plane path to a workspace env directory.
func (b *Bridge) localEnvDir(workspaceName, env string) string {
	return filepath.Join(b.workspacesDir, workspaceName, "envs", env)
}

// ExecForEnv returns the docker executor for one environment: Local for a local
// env, a remote SSH executor when the env is bound to a host. Read-only
// observability handlers (status, container list) use it so they query the right
// daemon. An error means the env's host could not be reached.
func (b *Bridge) ExecForEnv(workspaceName, env string) (executor.Executor, error) {
	rt, err := b.resolveRemote(workspaceName, env)
	if err != nil {
		return nil, err
	}
	if rt != nil {
		return rt.exec, nil
	}
	return executor.Local{}, nil
}

// remoteDotEnv reads the host-authoritative .env for a remote workspace env and
// parses it into a map (DB credentials for backup/restore). Best-effort: a read
// failure yields an empty map.
func (b *Bridge) remoteDotEnv(rt *remoteTarget, workspaceName, env string) map[string]string {
	remoteEnvDir := rt.exec.RemoteDir(b.localEnvDir(workspaceName, env))
	data, err := rt.client.ReadFile(remoteEnvDir + "/.env")
	out := map[string]string{}
	if err != nil {
		return out
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.Trim(strings.TrimSpace(line[eq+1:]), `"'`)
		if k != "" {
			out[k] = v
		}
	}
	return out
}

// Bootstrap scaffolds a workspace environment natively in Go (Phase 6.5 finish —
// replaces scripts/bootstrap.sh). Used during workspace creation and by the
// `init` command. stdout/stderr are the same stream in practice; progress is
// written to stdout.
func (b *Bridge) Bootstrap(workspaceName, env string, stdout, stderr io.Writer) error {
	return b.bootstrap(workspaceName, env, false, stdout)
}

func (b *Bridge) bootstrap(workspaceName, env string, regenEnv bool, out io.Writer) error {
	templatesDir := filepath.Join(b.toolkitRoot, "templates")
	return workspace.Bootstrap(b.workspacesDir, templatesDir, workspaceName, env, regenEnv, out)
}

// RunOptions configures a command execution.
type RunOptions struct {
	Workspace string
	Command   string
	Env       string
	Extra     []string // additional args (e.g. "db" for backup, "minor" for version bump)
	Stdout    io.Writer
	Stderr    io.Writer
}

// shellEnv builds the environment for child processes.
// We inherit the server's env (which has PATH, HOME, etc.) and overlay
// a few critical variables to ensure docker compose and toolkit scripts work
// correctly regardless of how the server process was started.
func shellEnv() []string {
	env := os.Environ() // inherit everything from the server process

	// Ensure HOME is set — docker needs it to locate ~/.docker/config.json
	if os.Getenv("HOME") == "" {
		env = append(env, "HOME=/root")
	}

	// Ensure the Alpine tool paths are present — apk installs to these locations
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	// Add docker CLI plugin path so 'docker compose' resolves the plugin
	const pluginPath = "/usr/lib/docker/cli-plugins"
	if !containsStr(path, pluginPath) {
		path = pluginPath + ":" + path
	}
	// Replace PATH in env slice
	env = filterEnv(env, "PATH")
	env = append(env, "PATH="+path)

	// Ensure docker socket is reachable
	if os.Getenv("DOCKER_HOST") == "" {
		env = append(env, "DOCKER_HOST=unix:///var/run/docker.sock")
	}

	// Suppress interactive prompts — scripts should never block waiting for input
	env = append(env, "DEBIAN_FRONTEND=noninteractive")
	env = append(env, "TERM=xterm-256color") // enables colour output from lib.sh

	return env
}

// first returns the first element of s, or "" if empty.
func first(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

// contains reports whether s contains v.
func contains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

func filterEnv(env []string, key string) []string {
	prefix := key + "="
	out := env[:0:len(env)]
	for _, e := range env {
		if len(e) < len(prefix) || e[:len(prefix)] != prefix {
			out = append(out, e)
		}
	}
	return out
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		len(s) > len(sub) && (s[:len(sub)] == sub || s[len(s)-len(sub):] == sub ||
			func() bool {
				for i := 1; i < len(s)-len(sub); i++ {
					if s[i:i+len(sub)] == sub {
						return true
					}
				}
				return false
			}()))
}

// Run executes a workspace command, streaming output to the provided writers.
// Returns an error if the command is not allowlisted or exits non-zero.
//
// Phase 6.5 finish: every allowlisted command runs natively in Go — init/version
// (here), build/promote (builder), the compose+swarm lifecycle (dockerops), and
// backup/restore (backup). There is no bash run.sh fallback.
func (b *Bridge) Run(opts RunOptions) error {
	if !allowedCommands[opts.Command] {
		return fmt.Errorf("command %q is not permitted", opts.Command)
	}

	// Phase 7: resolve whether this environment lives on a remote host.
	rt, err := b.resolveRemote(opts.Workspace, opts.Env)
	if err != nil {
		return err
	}

	// Phase 6.5 finish: init re-bootstraps an environment natively in Go.
	// run.sh passed EXTRA to bootstrap.sh; --regen-env forces .env regeneration.
	if opts.Command == "init" || opts.Command == "bootstrap" {
		if err := b.bootstrap(opts.Workspace, opts.Env, contains(opts.Extra, "--regen-env"), opts.Stdout); err != nil {
			return err
		}
		if rt != nil {
			// Ship the freshly-scaffolded env dir to the host, preserving its
			// authoritative .env.
			localDir := b.localEnvDir(opts.Workspace, opts.Env)
			return rt.client.PushDir(localDir, rt.exec.RemoteDir(localDir), ".env")
		}
		return nil
	}

	// Phase 6.5 finish: version is managed natively in Go (config.json edit).
	// run.sh maps `version <sub> <arg>` to the ENV/Extra slots, so do the same.
	if version.Handles(opts.Command) {
		_, err := version.Run(version.Options{
			WorkspacesDir: b.workspacesDir,
			Workspace:     opts.Workspace,
			Subcommand:    opts.Env,
			Arg:           first(opts.Extra),
			Stdout:        opts.Stdout,
		})
		return err
	}

	// Phase 6.5 finish: build/promote (image build/push, retag-and-redeploy) run
	// natively in Go. Env/Extra carry the run.sh argument layout.
	if builder.Handles(opts.Command) {
		if rt != nil {
			// Remote build/promote needs the full build context on the host and a
			// remote registry login; not yet wired (Phase 7 follow-up). Fail loud
			// rather than silently building on the control plane.
			return fmt.Errorf("%q is not yet supported for workspaces on remote host %q", opts.Command, rt.hostName)
		}
		_, err := builder.Run(builder.Options{
			WorkspacesDir: b.workspacesDir,
			Workspace:     opts.Workspace,
			Command:       opts.Command,
			Env:           opts.Env,
			Extra:         opts.Extra,
			EnvVars:       shellEnv(),
			Stdout:        opts.Stdout,
			Stderr:        opts.Stderr,
		})
		return err
	}

	if dockerops.Handles(opts.Command) {
		dopts := dockerops.Options{
			WorkspacesDir: b.workspacesDir,
			Workspace:     opts.Workspace,
			Command:       opts.Command,
			Env:           opts.Env,
			Extra:         opts.Extra,
			EnvVars:       shellEnv(),
			Stdout:        opts.Stdout,
			Stderr:        opts.Stderr,
		}
		if rt != nil {
			localDir := b.localEnvDir(opts.Workspace, opts.Env)
			dopts.Exec = rt.exec
			dopts.Remote = true
			dopts.RemoteWorkspacesDir = b.remoteWorkspacesDir
			dopts.Sync = func() error {
				return rt.client.PushDir(localDir, rt.exec.RemoteDir(localDir), ".env")
			}
		}
		handled, err := dockerops.Run(dopts)
		if handled {
			return err
		}
		// not handled (swarm / unreadable config) — fall through
	}

	// Phase 6.5c: backup/restore run natively in Go (SQL dump + volume archive).
	// Phase 7: for a remote workspace the docker work runs on the host while the
	// archive streams back to the control-plane backups dir; the host's .env
	// supplies DB credentials.
	if backup.Handles(opts.Command) {
		bopts := backup.Options{
			WorkspacesDir: b.workspacesDir,
			Workspace:     opts.Workspace,
			Command:       opts.Command,
			Env:           opts.Env,
			Extra:         opts.Extra,
			EnvVars:       shellEnv(),
			Stdout:        opts.Stdout,
			Stderr:        opts.Stderr,
			Timestamp:     time.Now().UTC().Format("2006-01-02_15-04-05"),
		}
		if rt != nil {
			bopts.Exec = rt.exec
			bopts.DotEnv = b.remoteDotEnv(rt, opts.Workspace, opts.Env)
		}
		handled, err := backup.Run(bopts)
		if handled {
			return err
		}
	}

	// Unreachable for a well-formed allowlisted command — only hit if a
	// deployment's config.json is missing/unreadable (dockerops returned
	// handled=false). With bash gone, surface it as an error.
	return fmt.Errorf("command %q could not be handled for %q/%q (missing or unreadable config?)",
		opts.Command, opts.Workspace, opts.Env)
}
