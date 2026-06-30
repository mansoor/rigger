package shell

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mansoor/rigger/ui/internal/backup"
	"github.com/mansoor/rigger/ui/internal/builder"
	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/customdomains"
	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/deployhistory"
	"github.com/mansoor/rigger/ui/internal/dockerops"
	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/proxyroutes"
	"github.com/mansoor/rigger/ui/internal/remotehost"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/stats"
	"github.com/mansoor/rigger/ui/internal/version"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/gitproviders"
	"github.com/mansoor/rigger/ui/internal/gitsync"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
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
	"logtail": true, // bounded, non-following logs for the /api/v1 REST API
	"refresh": true,
	"backup":  true,
	"restore": true,
	"migrate": true, // cross-env data migration (backup source → restore into target)
	"init":    true,
	"version": true,
	"build":   true,
	"promote": true,
	"test":    true, // Phase 9: sandboxed compose-exec inside a service container
	"script":  true, // Phase 9: one-off tool container (Trivy/Cypress/Sonar/custom)
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

	// Per-workspace directory sizes, refreshed off the dashboard request path.
	disk *diskUsageCache

	// hostWS is the HOST-side path the Docker daemon mapped to workspacesDir inside
	// this (Rigger) container — discovered once via self-inspect. Used to translate
	// bind-mount sources to host-resolvable paths (RIGGER_BIND_ROOT). Empty ⇒ not
	// containerised / undiscoverable, and callers fall back to identity.
	hostWSOnce sync.Once
	hostWS     string
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
		disk:                newDiskUsageCache(5 * time.Minute),
	}
}

// remoteTarget carries the resolved SSH executor for a remote workspace.
type remoteTarget struct {
	exec     remotehost.Remote
	client   *remotehost.Client
	hostName string
	hostID   int64 // resolved host id (used to compare build host vs deploy host)
}

// resolveRemote returns the remote target for one environment, or (nil, nil) when
// that environment is local. Local-only bridges (nil db/pool) always return nil.
// resourcePrefix returns the immutable Docker resource prefix ({workspace}_{project})
// for a project, used as the globally-unique key for host bindings and migration
// leftovers. Falls back to the project name if config can't be read.
func (b *Bridge) resourcePrefix(workspaceName, project string) string {
	cfg, err := wsconfig.Load(wspath.ConfigPath(b.workspacesDir, workspaceName, project))
	if err == nil {
		if p := cfg.Project.Prefix(); p != "" {
			return p
		}
	}
	return project
}

// magicDNSHost resolves the address to embed in an env's magic-DNS auto-URL
// ({prefix}-{env}.<host>.sslip.io). The address is per-LOCATION, not global: an
// env bound to a remote host uses THAT host's address (it's where the workload —
// and its published ports — actually run); a local env uses the configured
// app_host. "" ⇒ the magic-DNS modes degrade to *.localhost. This is what makes
// the auto-URL correct in a mixed local+remote fleet (a single global IP can't
// be right once a second host exists).
func (b *Bridge) magicDNSHost(workspaceName, project, env string) string {
	if b.db == nil {
		return ""
	}
	if host, err := settings.HostForEnv(b.db, b.resourcePrefix(workspaceName, project), env); err == nil && host != nil {
		return host.Address
	}
	return settings.EffectiveAutoURLHost(b.db, workspaceName)
}

// usesOverrideCert reports whether an env's TLS should be served by Traefik's
// file-provider cert (issued out-of-band under a per-env/per-workspace ACME email)
// rather than a Traefik ACME resolver. True only for an SSL env with an explicit
// public domain whose EFFECTIVE email (env → workspace → global) differs from the
// global. composegen then emits tls=true with no certresolver. (The cert itself is
// issued by the API layer's maybeIssueOverrideCert / the renewal scheduler.)
// routerMiddlewares resolves the Traefik file-provider middleware refs to attach to an env's
// APP routers — its workspace access list (auth/IP/GeoIP) + WAF/cache plugins, when enabled
// instance-wide. Best-effort; empty when nothing attaches (golden parity). See
// docs/design/workspace-plugins-and-access-lists.md.
func (b *Bridge) routerMiddlewares(workspaceName, project, env string) []string {
	if b.db == nil {
		return nil
	}
	data, err := os.ReadFile(wspath.ConfigPath(b.workspacesDir, workspaceName, project))
	if err != nil {
		return nil
	}
	geoEnabled := settings.AppSetting(b.db, "proxy_geoip_enabled") == "true"
	out := proxyroutes.ResolveRouterMiddlewares(b.db, workspaceName, data, env,
		settings.AppSetting(b.db, "proxy_waf_enabled") == "true",
		false, // cache (Souin) disabled — incompatible with Traefik's Yaegi interpreter; see traefikcfg.CacheSupported
		geoEnabled)
	// Per-env INLINE access (IP allow-list / GeoIP / block-exploits) when no access list is
	// attached — rendered to the file provider and prepended so it gates before WAF/cache.
	if inline, ierr := proxyroutes.RenderEnvMiddlewares(proxyroutes.DynDir(), workspaceName, project, env, data, geoEnabled); ierr == nil && len(inline) > 0 {
		out = append(inline, out...)
	}
	return out
}

func (b *Bridge) usesOverrideCert(workspaceName, project, env string) bool {
	if b.db == nil {
		return false
	}
	cfg, err := wsconfig.Load(wspath.ConfigPath(b.workspacesDir, workspaceName, project))
	if err != nil {
		return false
	}
	ec, ok := cfg.Environments[env]
	if !ok {
		return false
	}
	// Workspace-wildcard: a base-domain auto-routed env (no explicit per-env domain) in a
	// workspace that overrides the base domain AND opts into Cloudflare DNS-01 with its OWN
	// token → its SSL is served by the out-of-band file-provider wildcard (*.{wsBase})
	// issued under the workspace token, NOT the shared global Traefik resolver (whose one
	// token is on the global zone). SSL is derived for base-domain routing, so don't gate
	// on the stored ec.SSLEnabled here.
	if ec.TraefikEnabled && strings.TrimSpace(ec.Domain) == "" && b.usesWorkspaceWildcard(workspaceName) {
		return true
	}
	if !ec.SSLEnabled {
		return false
	}
	domain := strings.ToLower(strings.TrimSpace(ec.Domain))
	if domain == "" || domain == "localhost" || strings.HasSuffix(domain, ".localhost") || net.ParseIP(domain) != nil {
		return false // base-domain/wildcard or local — Traefik's own resolver handles it
	}
	global := strings.TrimSpace(settings.AppSetting(b.db, "acme_email"))
	eff := settings.EffectiveAcmeEmail(b.db, workspaceName, ec.AcmeEmail)
	return eff != "" && !strings.EqualFold(eff, global)
}

// usesWorkspaceWildcard reports whether a workspace serves its base-domain envs via an
// out-of-band Cloudflare wildcard cert under its OWN token: it overrides the base domain,
// its effective DNS provider is cloudflare, and a per-workspace token is set. (The global
// Traefik dns resolver is single-token/shared, so a workspace on its own zone can't use it.)
func (b *Bridge) usesWorkspaceWildcard(ws string) bool {
	if b.db == nil {
		return false
	}
	if settings.WorkspaceBaseDomain(b.db, ws) == "" {
		return false // not the workspace's OWN domain → global resolver handles it
	}
	if !strings.EqualFold(settings.EffectiveDNSProvider(b.db, ws), "cloudflare") {
		return false
	}
	return settings.WorkspaceDNSToken(b.db, b.cryptoKey, ws) != ""
}

func (b *Bridge) resolveRemote(workspaceName, project, env string) (*remoteTarget, error) {
	if b.db == nil || b.pool == nil {
		return nil, nil
	}
	host, err := settings.HostForEnv(b.db, b.resourcePrefix(workspaceName, project), env)
	if err != nil || host == nil {
		return nil, err
	}
	return b.connectHost(host)
}

// resolveBuildRemote returns the project's EXPLICIT build host (project binding →
// workspace default), or (nil, nil) when none is configured — in which case the
// caller builds on the env's own deploy host (today's behavior). Image-distribution
// Phase 4.
func (b *Bridge) resolveBuildRemote(workspaceName, project string) (*remoteTarget, error) {
	if b.db == nil || b.pool == nil {
		return nil, nil
	}
	host, err := settings.BuildHostFor(b.db, workspaceName, b.resourcePrefix(workspaceName, project))
	if err != nil || host == nil {
		return nil, err
	}
	return b.connectHost(host)
}

// connectHost dials a host (decrypting its key) and wraps it as a remoteTarget.
func (b *Bridge) connectHost(host *settings.Host) (*remoteTarget, error) {
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
		hostID:   host.ID,
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

// PushEnvFile pushes an environment's local .env to its remote host. The .env is
// normally host-authoritative (deploys never overwrite it); this is the one
// sanctioned override, run only on an explicit user edit so the change actually
// reaches the host. It returns (pushed, hostName, error): pushed is false for a
// local env, where the local .env is already authoritative and nothing is shipped.
func (b *Bridge) PushEnvFile(workspaceName, project, env string) (bool, string, error) {
	rt, err := b.resolveRemote(workspaceName, project, env)
	if err != nil {
		return false, "", err
	}
	if rt == nil {
		return false, "", nil // local env — .env is already authoritative
	}
	localEnv := wspath.DotEnv(b.workspacesDir, workspaceName, project, env)
	data, err := os.ReadFile(localEnv)
	if err != nil {
		return false, rt.hostName, fmt.Errorf("read local .env: %w", err)
	}
	if err := rt.client.WriteFile(rt.exec.RemoteDir(localEnv), data, 0o600); err != nil {
		return false, rt.hostName, fmt.Errorf("push .env to %s: %w", rt.hostName, err)
	}
	return true, rt.hostName, nil
}

// Migrate moves a whole workspace to targetHostID (0 = local control plane). It
// is only permitted when every environment currently shares one host; mixed
// setups must be moved per environment. It simply migrates each env in turn.
func (b *Bridge) Migrate(workspaceName, project string, targetHostID int64, out io.Writer) error {
	if b.db == nil || b.pool == nil {
		return fmt.Errorf("migration requires multi-host support")
	}
	ws, err := workspace.Get(b.workspacesDir, workspaceName, project, settings.EffectiveBaseDomain(b.db, workspaceName), settings.EffectiveAutoURLMode(b.db, workspaceName), settings.EffectiveAutoURLHost(b.db, workspaceName))
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	if len(ws.Envs) == 0 {
		return fmt.Errorf("project %q has no environments to migrate", project)
	}
	common, mixed, err := b.commonHost(workspaceName, project, ws.Envs)
	if err != nil {
		return err
	}
	if mixed {
		return fmt.Errorf("environments are on different hosts — move them individually instead")
	}
	if common == targetHostID {
		return fmt.Errorf("project is already on %s", hostLabel(targetHostID))
	}
	for _, env := range ws.Envs {
		if err := b.MigrateEnv(workspaceName, project, env, targetHostID, out); err != nil {
			return fmt.Errorf("%s: %w", env, err)
		}
	}
	fmt.Fprintf(out, "✓ Migration complete: %s is now on %s\n", project, hostLabel(targetHostID))
	return nil
}

// MigrateEnv moves one environment to targetHostID (0 = local). If the env is not
// deployed on its current host it is a plain repoint (next deploy provisions it).
// If it is deployed, its data is moved: back up + stop on the source, ship files
// to the target, repoint, then start + restore on the target. The source copy is
// stopped but its data is left intact. Streams progress to out.
func (b *Bridge) MigrateEnv(workspaceName, project, env string, targetHostID int64, out io.Writer) error {
	if b.db == nil || b.pool == nil {
		return fmt.Errorf("multi-host support is not configured")
	}
	if targetHostID != 0 {
		if h, err := settings.GetHost(b.db, targetHostID); err != nil || h == nil {
			return fmt.Errorf("target host %d not found", targetHostID)
		}
	}
	prefix := b.resourcePrefix(workspaceName, project)
	srcHost, err := settings.HostForEnv(b.db, prefix, env)
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

	srcRT, err := b.resolveRemote(workspaceName, project, env) // still points at the source
	if err != nil {
		return fmt.Errorf("connect to source: %w", err)
	}

	cfg, err := wsconfig.Load(wspath.ConfigPath(b.workspacesDir, workspaceName, project))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	stack := cfg.StackName(env)

	deployed, err := b.envDeployed(stack, srcRT)
	if err != nil {
		return fmt.Errorf("check deployment state: %w", err)
	}

	if !deployed {
		if err := settings.SetEnvHost(b.db, prefix, env, targetHostID); err != nil {
			return err
		}
		fmt.Fprintf(out, "✓ %s/%s repointed to %s (not deployed — provisions on next deploy)\n",
			project, env, hostLabel(targetHostID))
		return nil
	}

	fmt.Fprintf(out, "▶ Backing up %s/%s on %s…\n", project, env, hostLabel(srcID))
	if err := b.Run(RunOptions{Workspace: workspaceName, Project: project, Command: "backup", Env: env, Extra: []string{"all"}, Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("backup source: %w", err)
	}
	snap, err := b.latestSnapshot(workspaceName, project, env)
	if err != nil {
		return fmt.Errorf("locate snapshot: %w", err)
	}

	// Stop the source stack before repointing (containers down, volumes kept) so
	// two copies never run at once.
	fmt.Fprintf(out, "▶ Stopping %s/%s on %s (data kept)…\n", project, env, hostLabel(srcID))
	if err := b.Run(RunOptions{Workspace: workspaceName, Project: project, Command: "stop", Env: env, Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("stop source: %w", err)
	}

	// Bring compose + .env to the control plane if the source is remote.
	localDir := b.localEnvDir(workspaceName, project, env)
	if srcRT != nil {
		if err := srcRT.client.PullDir(srcRT.exec.RemoteDir(localDir), localDir); err != nil {
			return fmt.Errorf("pull files from source: %w", err)
		}
	}

	if err := settings.SetEnvHost(b.db, prefix, env, targetHostID); err != nil {
		return fmt.Errorf("repoint: %w", err)
	}
	fmt.Fprintf(out, "▶ Repointed %s/%s to %s\n", project, env, hostLabel(targetHostID))

	tgtRT, err := b.resolveRemote(workspaceName, project, env) // now points at the target
	if err != nil {
		return fmt.Errorf("connect to target: %w", err)
	}
	if tgtRT != nil {
		if err := tgtRT.client.PushDir(localDir, tgtRT.exec.RemoteDir(localDir)); err != nil {
			return fmt.Errorf("push files to target: %w", err)
		}
	}

	fmt.Fprintf(out, "▶ Starting %s/%s on %s…\n", project, env, hostLabel(targetHostID))
	if err := b.Run(RunOptions{Workspace: workspaceName, Project: project, Command: "start", Env: env, Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("start target: %w", err)
	}
	if snap != "" {
		fmt.Fprintf(out, "▶ Restoring snapshot %s on %s…\n", snap, hostLabel(targetHostID))
		if err := b.Run(RunOptions{Workspace: workspaceName, Project: project, Command: "restore", Env: env, Extra: []string{snap}, Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("restore target: %w", err)
		}
	}

	// Record what was left behind on the source so it can be wiped via Housekeeping.
	srcName := "local control plane"
	if srcHost != nil {
		srcName = srcHost.Name
	}
	b.recordLeftover(srcID, srcName, prefix, env, stack)

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
func (b *Bridge) commonHost(workspaceName, project string, envs []string) (hostID int64, mixed bool, err error) {
	prefix := b.resourcePrefix(workspaceName, project)
	first := int64(-1)
	for _, env := range envs {
		h, err := settings.HostForEnv(b.db, prefix, env)
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
	b.db.Exec(`INSERT INTO migration_leftovers (host_id, host_name, project, env, stack)
		VALUES (?,?,?,?,?)
		ON CONFLICT(host_id, project, env) DO UPDATE SET host_name=excluded.host_name, stack=excluded.stack, created_at=CURRENT_TIMESTAMP`,
		hostID, hostName, ws, env, stack) //nolint:errcheck
}

// ListLeftovers returns all recorded source-host leftovers, newest first.
func (b *Bridge) ListLeftovers() ([]Leftover, error) {
	rows, err := b.db.Query(`SELECT id, host_id, host_name, project, env, stack, created_at FROM migration_leftovers ORDER BY created_at DESC`)
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

// HostExecutor returns the docker executor for an arbitrary host id (0 = local
// control plane). Used by read-only host queries like the port-in-use check.
func (b *Bridge) HostExecutor(hostID int64) (executor.Executor, error) {
	ex, _, err := b.hostExec(hostID)
	return ex, err
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
	err := b.db.QueryRow(`SELECT id, host_id, host_name, project, env, stack FROM migration_leftovers WHERE id=?`, id).
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
	// l.Workspace holds the resource prefix ({workspace}_{project}); split it back.
	lws, lproj := splitPrefix(l.Workspace)
	localDir := b.localEnvDir(lws, lproj, l.Env)
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

// Stats builds the full dashboard payload. Per-project running counts are merged
// across all hosts; the Docker/Host sections stay control-plane local. The
// dashboard table shows per-workspace CPU/mem/network from the separate
// /api/live-stats poll, so this skips the expensive per-container `docker stats`
// sampling here. Per-workspace disk sizes come from an async cache so the request
// never blocks on `du` (slow over bind mounts).
func (b *Bridge) Stats() stats.Stats {
	execs := b.hostExecutors()
	running := fanout(execs, stats.RunningByProjectFor)
	disk := b.disk.snapshot(b.workspacesDir)
	return stats.CollectWith(b.workspacesDir, running, nil, disk)
}

// diskUsageCache holds per-workspace directory sizes (MB), refreshed off the
// request path. `du` over a bind mount (Docker Desktop on Windows especially)
// can take tens of seconds, so the dashboard must never block on it: snapshot()
// returns the last-known sizes immediately and triggers a background refresh for
// any entry that is missing or older than ttl.
type diskUsageCache struct {
	ttl   time.Duration
	mu    sync.Mutex
	sizes map[string]float64
	at    map[string]time.Time
	busy  map[string]bool
}

func newDiskUsageCache(ttl time.Duration) *diskUsageCache {
	return &diskUsageCache{
		ttl:   ttl,
		sizes: map[string]float64{},
		at:    map[string]time.Time{},
		busy:  map[string]bool{},
	}
}

// snapshot returns a copy of the cached sizes (MB) for every project under
// workspacesDir, refreshing stale/missing entries asynchronously. Keys are
// "{workspace}/{project}" so each nested project gets its own (non-blocking) du.
func (c *diskUsageCache) snapshot(workspacesDir string) map[string]float64 {
	wsEntries, _ := os.ReadDir(workspacesDir)
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]float64)
	for _, we := range wsEntries {
		if !we.IsDir() {
			continue
		}
		wsName := we.Name()
		projEntries, _ := os.ReadDir(wspath.ProjectsDir(workspacesDir, wsName))
		for _, pe := range projEntries {
			if !pe.IsDir() {
				continue
			}
			key := wsName + "/" + pe.Name()
			out[key] = c.sizes[key] // 0 until first computed
			if !c.busy[key] && time.Since(c.at[key]) > c.ttl {
				c.busy[key] = true
				go c.refresh(key, wspath.ProjectDir(workspacesDir, wsName, pe.Name()))
			}
		}
	}
	return out
}

func (c *diskUsageCache) refresh(name, path string) {
	mb := stats.WorkspaceDiskMB(path)
	c.mu.Lock()
	c.sizes[name] = mb
	c.at[name] = time.Now()
	c.busy[name] = false
	c.mu.Unlock()
}

// latestSnapshot returns the most recent snapshot dir name under a workspace
// env's backups dir (timestamps sort lexicographically).
func (b *Bridge) latestSnapshot(workspaceName, project, env string) (string, error) {
	dir := wspath.EnvBackupsDir(b.workspacesDir, workspaceName, project, env)
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
func (b *Bridge) localEnvDir(workspaceName, project, env string) string {
	return wspath.EnvDir(b.workspacesDir, workspaceName, project, env)
}

// hostBindRoot returns the HOST-visible absolute path of an env directory — the
// value compose substitutes for ${RIGGER_BIND_ROOT} so the Docker daemon can
// resolve relative bind-mount sources. When Rigger runs inside a container the env
// dir's in-container path (/toolkit/workspaces/…) is meaningless to the host
// daemon, so we translate it through the discovered workspaces host mount. When not
// containerised (or undiscoverable) the local path is already host-visible, so it
// is returned unchanged.
func (b *Bridge) hostBindRoot(workspaceName, project, env string) string {
	local := b.localEnvDir(workspaceName, project, env)
	hw := b.hostWorkspacesDir()
	if hw == "" || hw == b.workspacesDir {
		return local
	}
	return hw + strings.TrimPrefix(local, b.workspacesDir)
}

// hostWorkspacesDir returns the host-side path the Docker daemon mapped to
// workspacesDir inside this container, discovered once. Empty ⇒ undiscoverable.
func (b *Bridge) hostWorkspacesDir() string {
	b.hostWSOnce.Do(func() { b.hostWS = discoverHostMount(b.workspacesDir) })
	return b.hostWS
}

// discoverHostMount finds the host-side source path the Docker daemon mapped to
// `dest` inside this container, by inspecting our own container's mounts. The
// container id is the hostname (Docker's default). Returns "" when not in a
// container, docker is unreachable, or no mount covers dest — callers fall back to
// the in-container path, which is already correct for a non-containerised Rigger.
func discoverHostMount(dest string) string {
	id, err := os.Hostname()
	if err != nil || id == "" {
		return ""
	}
	out, err := executor.Local{}.DockerOutput(executor.Spec{
		Args: []string{"inspect", id, "--format", "{{json .Mounts}}"},
	})
	if err != nil {
		return ""
	}
	var mounts []struct{ Source, Destination string }
	if json.Unmarshal(bytes.TrimSpace(out), &mounts) != nil {
		return ""
	}
	// Pick the mount whose Destination is the longest prefix of dest (handles a
	// mount at the workspaces dir itself or at any parent of it).
	bestLen, host := -1, ""
	for _, m := range mounts {
		d := strings.TrimRight(m.Destination, "/")
		if dest == d || strings.HasPrefix(dest, d+"/") {
			if len(d) > bestLen {
				bestLen = len(d)
				host = m.Source + dest[len(d):]
			}
		}
	}
	return host
}

// DetectHostIP returns the Docker host's primary LAN/public IP. The control-plane
// container can't read this from its own network namespace (it only sees the
// 172.x bridge), so detection runs the rigger binary host-networked in a
// throwaway container: `docker run --rm --network host <self-image> detect-host-ip`.
// In the host namespace the binary's outbound-route lookup resolves to the real
// host interface. The rigger image is reused (guaranteed present — no pull, no
// external image). When Rigger isn't containerised the in-process lookup is
// already the host's, so it's returned directly. Best-effort; surfaces errors so
// the UI can fall back to a manual entry.
func (b *Bridge) DetectHostIP() (string, error) {
	img := selfImage()
	if img == "" {
		// Not containerised (or image undiscoverable): our own outbound IP is the
		// host's. discoverHostMount uses the same self-inspect, so an empty image
		// here means the same thing it does there.
		if ip := outboundIP(); ip != "" {
			return ip, nil
		}
		return "", fmt.Errorf("could not determine the host IP")
	}
	out, err := executor.Local{}.DockerOutput(executor.Spec{
		Args: []string{"run", "--rm", "--network", "host", img, "detect-host-ip"},
	})
	if err != nil {
		return "", fmt.Errorf("detect host ip: %w", err)
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return "", fmt.Errorf("detection returned no address")
	}
	return ip, nil
}

// selfImage returns the image reference of this (Rigger) container, via the same
// hostname-is-container-id self-inspect discoverHostMount uses. "" when Rigger is
// not containerised or docker is unreachable.
func selfImage() string {
	id, err := os.Hostname()
	if err != nil || id == "" {
		return ""
	}
	out, err := executor.Local{}.DockerOutput(executor.Spec{
		Args: []string{"inspect", id, "--format", "{{.Config.Image}}"},
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// outboundIP returns the local IP of the route to the internet (UDP connect does
// a route lookup, sends nothing). Mirrors the cmd/server helper of the same name;
// used as the non-containerised fallback for DetectHostIP.
func outboundIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

// splitPrefix splits a resource prefix "{workspace}_{project}" back into its
// parts (names never contain "_", so the first separator is authoritative).
func splitPrefix(prefix string) (workspace, project string) {
	if i := strings.IndexByte(prefix, '_'); i >= 0 {
		return prefix[:i], prefix[i+1:]
	}
	return prefix, prefix
}

// ExecForEnv returns the docker executor for one environment: Local for a local
// env, a remote SSH executor when the env is bound to a host. Read-only
// observability handlers (status, container list) use it so they query the right
// daemon. An error means the env's host could not be reached.
func (b *Bridge) ExecForEnv(workspaceName, project, env string) (executor.Executor, error) {
	rt, err := b.resolveRemote(workspaceName, project, env)
	if err != nil {
		return nil, err
	}
	if rt != nil {
		return rt.exec, nil
	}
	return executor.Local{}, nil
}

// EnsureSwarmSecret creates the Docker Swarm secret for one secret key/version
// on whichever daemon the environment runs (local or remote), returning the
// secret name. The value is never persisted by Rigger — Swarm holds it
// encrypted at rest in its Raft store. Idempotent (a no-op if it already
// exists, since Swarm secrets are immutable).
func (b *Bridge) EnsureSwarmSecret(workspaceName, project, env, key, value string, version int) (string, error) {
	ws, err := workspace.Get(b.workspacesDir, workspaceName, project, settings.EffectiveBaseDomain(b.db, workspaceName), settings.EffectiveAutoURLMode(b.db, workspaceName), settings.EffectiveAutoURLHost(b.db, workspaceName))
	if err != nil {
		return "", err
	}
	ex, err := b.ExecForEnv(workspaceName, project, env)
	if err != nil {
		return "", err
	}
	name := dockerops.SecretName(ws.Config.Project.Prefix(), env, key, version)
	if err := dockerops.EnsureSecret(ex, name, value); err != nil {
		return "", err
	}
	return name, nil
}

// RemoveSwarmSecret deletes a versioned Swarm secret for one key (best-effort;
// fails if the secret is still referenced by a running service).
func (b *Bridge) RemoveSwarmSecret(workspaceName, project, env, key string, version int) error {
	ws, err := workspace.Get(b.workspacesDir, workspaceName, project, settings.EffectiveBaseDomain(b.db, workspaceName), settings.EffectiveAutoURLMode(b.db, workspaceName), settings.EffectiveAutoURLHost(b.db, workspaceName))
	if err != nil {
		return err
	}
	ex, err := b.ExecForEnv(workspaceName, project, env)
	if err != nil {
		return err
	}
	return dockerops.RemoveSecret(ex, dockerops.SecretName(ws.Config.Project.Prefix(), env, key, version))
}

// TermSession is an interactive PTY into a container — local (Docker socket) or
// remote (`docker exec -it` over SSH). Read/Write stream the PTY bytes, Resize
// tracks the browser terminal's window size, and Close ends the session. Both
// the local *DockerExec and the remote *remotehost.ContainerPTY satisfy it, so
// the terminal handler treats local and remote sessions identically.
type TermSession interface {
	io.ReadWriteCloser
	Resize(rows, cols int)
}

// OpenTerminal opens an interactive shell into a container for one environment,
// on whichever daemon it runs on: the local Docker socket for a local env, or
// `docker exec -it` over an SSH-allocated PTY for a remote host (Wave C —
// cross-host terminal). service is the container name (in Rigger the compose
// service name is the prefixed container name); the caller validates it.
func (b *Bridge) OpenTerminal(workspaceName, project, env, service string, cols, rows int) (TermSession, error) {
	rt, err := b.resolveRemote(workspaceName, project, env)
	if err != nil {
		return nil, err
	}
	if rt == nil {
		// Local: the Docker daemon allocates a PTY in the container via the socket.
		return NewDockerExec(service, cols, rows, "")
	}
	// Remote: SSH allocates the PTY, `docker exec -it` runs on the host's daemon.
	return rt.client.NewContainerPTY(service, cols, rows)
}

// remoteDotEnv reads the host-authoritative .env for a remote workspace env and
// parses it into a map (DB credentials for backup/restore). Best-effort: a read
// failure yields an empty map.
func (b *Bridge) remoteDotEnv(rt *remoteTarget, workspaceName, project, env string) map[string]string {
	remoteEnvDir := rt.exec.RemoteDir(b.localEnvDir(workspaceName, project, env))
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
func (b *Bridge) Bootstrap(workspaceName, project, env string, stdout, stderr io.Writer) error {
	return b.bootstrap(workspaceName, project, env, false, stdout)
}

func (b *Bridge) bootstrap(workspaceName, project, env string, regenEnv bool, out io.Writer) error {
	templatesDir := filepath.Join(b.toolkitRoot, "templates")
	baseDomain := settings.EffectiveBaseDomain(b.db, workspaceName)
	registry := b.effectiveRegistry(workspaceName, project)
	// Thread the auto-URL settings (magic-DNS mode + host) so the FIRST compose a
	// new env gets matches what a later Refresh would produce — otherwise a no-base-
	// domain env is created with a `.localhost` route and only flips to the configured
	// sslip/nip URL after a manual Refresh. magicDNSHost mirrors the deploy/refresh path.
	return workspace.Bootstrap(b.workspacesDir, templatesDir, workspaceName, project, env, regenEnv,
		baseDomain, registry, settings.EffectiveAutoURLMode(b.db, workspaceName), b.magicDNSHost(workspaceName, project, env), out)
}

// effectiveRegistry resolves the registry an env's images live in for a project:
// the project's own registry (config.json) wins, else the workspace/global system
// registry (settings.EffectiveRegistry). "" ⇒ local-only — no registry resolvable
// (Phase 0: the system designation doesn't exist yet, so this returns the project's
// own value). Threaded into builder/dockerops/bootstrap so every image ref agrees.
func (b *Bridge) effectiveRegistry(workspaceName, project string) string {
	projReg := ""
	if cfg, err := wsconfig.Load(wspath.ConfigPath(b.workspacesDir, workspaceName, project)); err == nil {
		projReg = cfg.Project.Registry
	}
	return settings.EffectiveRegistry(b.db, workspaceName, projReg)
}

// resolveGitAuth loads the project's configured git provider (git_provider_id) and
// builds a per-clone gitsync.Auth for private-repo access. Returns (nil, nil) when no
// provider is set (public repo) or the DB is unavailable. The caller owns Cleanup.
func (b *Bridge) resolveGitAuth(workspaceName, project string) (*gitsync.Auth, error) {
	if b.db == nil {
		return nil, nil
	}
	cfg, err := wsconfig.Load(wspath.ConfigPath(b.workspacesDir, workspaceName, project))
	if err != nil || cfg.Project.GitProviderID == 0 {
		return nil, nil //nolint:nilerr — no provider configured ⇒ public clone
	}
	p, err := gitproviders.Get(b.db, b.cryptoKey, cfg.Project.GitProviderID)
	if err != nil {
		return nil, fmt.Errorf("load git provider: %w", err)
	}
	if p == nil {
		return nil, nil // provider was deleted — fall back to a public clone attempt
	}
	return p.BuildAuth(cfg.Project.GitRepo)
}

// deployRegistryGate blocks a deploy whose built image would have nowhere to be
// pulled from (image-distribution Phase 1). A project that builds its own images
// needs a registry when the target is a Swarm (every node pulls) or a remote host
// (the image built on the control plane is absent there). A local single-node
// compose deploy builds and runs on one daemon, so a local-only image is fine and
// no registry is required. Returns nil (allow) for image-only projects, when a
// registry resolves, or for the local case. remote ⇒ the env is bound to a host.
func (b *Bridge) deployRegistryGate(workspaceName, project, env string, remote bool) error {
	cfg, err := wsconfig.Load(wspath.ConfigPath(b.workspacesDir, workspaceName, project))
	if err != nil {
		return nil // can't read config — let the normal deploy path surface the error
	}
	if len(cfg.BuildServices()) == 0 {
		return nil // image-only project: pulls its pinned public images, no Rigger registry
	}
	if settings.EffectiveRegistry(b.db, workspaceName, cfg.Project.Registry) != "" {
		return nil // a registry resolves (project / workspace-system / global-system)
	}
	swarm := false
	if e, ok := cfg.Environments[env]; ok && e.Deployment == "swarm" {
		swarm = true
	}
	if !swarm && !remote {
		return nil // local single-node compose: the locally-built image runs in place
	}
	target := "a remote host"
	if swarm {
		target = "a Swarm"
	}
	return fmt.Errorf("this project builds its own images but no registry is configured — deploying to %s needs one so the image can be pulled. Designate a system registry (Settings → Docker Registries, or Manage Workspace → Registries) or set a project registry, then redeploy", target)
}

// deploySwarmGate blocks a Swarm-mode deploy to a remote host that is known NOT to be
// a Swarm manager (image-distribution Phase 5 preflight). `docker stack deploy` on a
// non-manager fails with a cryptic "this node is not a swarm manager" — this surfaces
// it early and clearly. It uses the capability probed on the host's last Test; if the
// host was never probed (SwarmState ""), it can't assert and allows the deploy. Local
// (control-plane) deploys are not gated here — rt is nil and Docker's own error is clear.
func (b *Bridge) deploySwarmGate(workspaceName, project, env string, rt *remoteTarget) error {
	if rt == nil {
		return nil // local control plane — not probed; Docker reports clearly
	}
	cfg, err := wsconfig.Load(wspath.ConfigPath(b.workspacesDir, workspaceName, project))
	if err != nil {
		return nil
	}
	e, ok := cfg.Environments[env]
	if !ok || e.Deployment != "swarm" {
		return nil // only Swarm-mode envs need a manager
	}
	host, err := settings.GetHost(b.db, rt.hostID)
	if err != nil || host == nil || host.SwarmState == "" {
		return nil // never probed → can't assert; don't block on missing data
	}
	if !host.SwarmManager {
		return fmt.Errorf("environment %q is set to Swarm mode but host %q is not a Swarm manager (docker swarm state: %s). Initialise or join a Swarm and promote it to a manager, then click Test on the host to refresh — or switch the environment to compose", env, host.Name, host.SwarmState)
	}
	return nil
}

// runScript runs a one-off tool container for a pipeline `script` stage, injecting
// the env's context as RIGGER_* variables and streaming output. Runs on the env's
// host (remote) or the local daemon. A non-zero exit fails the stage.
func (b *Bridge) runScript(opts RunOptions, rt *remoteTarget) error {
	if opts.ScriptImage == "" || strings.TrimSpace(opts.ScriptCommand) == "" {
		return fmt.Errorf("script stage requires an image and a command")
	}
	stack := b.resourcePrefix(opts.Workspace, opts.Project) + "_" + opts.Env

	ctx := []string{
		"RIGGER_WORKSPACE=" + opts.Workspace,
		"RIGGER_PROJECT=" + opts.Project,
		"RIGGER_ENV=" + opts.Env,
		"RIGGER_STACK=" + stack,
	}
	// App URL from config (domain → https; else the published HTTP port on localhost).
	if cfg, err := wsconfig.Load(wspath.ConfigPath(b.workspacesDir, opts.Workspace, opts.Project)); err == nil {
		ec := cfg.Environments[opts.Env]
		if ec.Domain != "" {
			ctx = append(ctx, "RIGGER_APP_URL=https://"+ec.Domain)
		} else if p := string(ec.HTTPPort); p != "" {
			ctx = append(ctx, "RIGGER_APP_URL=http://localhost:"+p)
		}
	}
	// Resolved image refs (custom: version/override tags; image: configured tags).
	refs := deployhistory.Resolve(b.workspacesDir, opts.Workspace, opts.Project, opts.Env).Images
	if len(refs) > 0 {
		all := make([]string, 0, len(refs))
		for svc, ref := range refs {
			all = append(all, ref)
			ctx = append(ctx, "RIGGER_IMAGE_"+strings.ToUpper(svc)+"="+ref)
		}
		ctx = append(ctx, "RIGGER_IMAGES="+strings.Join(all, " "))
	}

	args := []string{"run", "--rm"}
	if opts.ScriptNetwork {
		args = append(args, "--network", stack+"_default")
	}
	for _, e := range ctx {
		args = append(args, "-e", e)
	}
	args = append(args, opts.ScriptImage, "sh", "-c", opts.ScriptCommand)

	fmt.Fprintf(opts.Stdout, "⚑ Running tool container %s\n", opts.ScriptImage)
	var base executor.Executor = executor.Local{}
	if rt != nil {
		base = rt.exec
	}
	ex := executor.WithContext(base, opts.Context) // cancellable when the pipeline is
	if err := ex.Docker(executor.Spec{Args: args, Env: shellEnv(), Stdout: opts.Stdout, Stderr: opts.Stderr}); err != nil {
		return err
	}
	fmt.Fprintf(opts.Stdout, "✓ %s completed\n", opts.ScriptImage)
	return nil
}

// ensureRegistryLogin authenticates docker to the project's configured registry
// before a push/pull, using the workspace registry pool's stored credentials. It
// matches the project's registry host (the image-tag prefix, cfg.Project.Registry)
// against a registry record's URL and runs `docker login` on the given executor
// (the local daemon, or the env's remote host). It is a no-op when no registry is
// configured or no matching pool entry exists (e.g. a public/no-auth registry),
// preserving prior behavior. Idempotent — docker caches the credentials.
func (b *Bridge) ensureRegistryLogin(workspaceName, project string, ex executor.Executor, out io.Writer) error {
	if b.db == nil {
		return nil
	}
	// Match the EFFECTIVE registry (project → workspace/global system), so a project
	// inheriting a system registry authenticates to it before push/pull.
	host := normalizeRegistryHost(b.effectiveRegistry(workspaceName, project))
	if host == "" {
		return nil // no registry configured (local-only images)
	}
	// workspaceName is the workspace KEY (folder/route identity), which is exactly
	// how registries are scoped (owner_scope='ws:{key}').
	pool, err := settings.ListRegistriesForWorkspace(b.db, workspaceName)
	if err != nil {
		return nil
	}
	matchID := int64(0)
	for i := range pool {
		if normalizeRegistryHost(pool[i].URL) == host {
			matchID = pool[i].ID
			break
		}
	}
	if matchID == 0 {
		return nil // registry host isn't in this workspace's pool — nothing to log in with
	}
	reg, err := settings.GetRegistry(b.db, matchID) // re-read WITH the password
	if err != nil || reg == nil || reg.Password == "" {
		return nil
	}
	fmt.Fprintf(out, "⚑ Authenticating to registry %s…\n", reg.URL)
	if err := ex.Docker(executor.Spec{
		Args:   []string{"login", "--username", reg.Username, "--password-stdin", reg.URL},
		Stdin:  strings.NewReader(reg.Password),
		Env:    shellEnv(),
		Stdout: out, Stderr: out,
	}); err != nil {
		return fmt.Errorf("docker login to %s failed: %w", reg.URL, err)
	}
	return nil
}

// normalizeRegistryHost reduces a registry reference to a bare host[:port] for
// comparison: it drops the scheme and any trailing path/slash and lowercases.
// "https://registry.example.com/" and "registry.example.com" both → the same key.
func normalizeRegistryHost(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// RunOptions configures a command execution.
type RunOptions struct {
	Workspace string // parent tier
	Project   string // project name
	Command   string
	Env       string
	Extra     []string // additional args (e.g. "db" for backup, "minor" for version bump)
	Stdout    io.Writer
	Stderr    io.Writer

	// PurgeVolumes (down-only): also remove the env's named data volumes
	// (`compose down --volumes`). Default false preserves volumes so a normal
	// env delete keeps its data; previews pass true so per-PR volumes don't pile up.
	PurgeVolumes bool

	// Context, when set, makes the command cancellable: cancelling it kills the
	// underlying docker process(es). Pipelines pass a per-run context so a Cancel
	// request aborts a hung build mid-flight. nil ⇒ uncancellable (existing behavior
	// for one-off actions, the scheduler, migrations, etc.).
	Context context.Context

	// Backup-only (Phase 11 per-env schedules): which services to back up
	// (empty = all) and the schedule metadata recorded in the snapshot manifest.
	Services     []string
	ScheduleID   string
	ScheduleName string
	Trigger      string // "scheduled" | "manual"

	// Migrate-only: SourceEnv is the env whose data is copied INTO Env (the target).
	// SkipTargetBackup skips the pre-migration safety backup of the target.
	SourceEnv        string
	SkipTargetBackup bool

	// Script-stage only (Phase 9 tool stage): run a one-off tool container with the
	// env's context injected.
	ScriptImage   string // tool container image (e.g. aquasec/trivy)
	ScriptCommand string // command run via `sh -c`
	ScriptNetwork bool   // attach to the env's compose network ({prefix}_{env}_default)
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
	rt, err := b.resolveRemote(opts.Workspace, opts.Project, opts.Env)
	if err != nil {
		return err
	}

	// Bind every docker call this command makes to opts.Context (if set) so a
	// pipeline Cancel kills the in-flight process. builder/dockerops/backup all
	// funnel through executor.Default(opts.Exec), so wrapping the executor we hand
	// them propagates cancellation without touching individual call sites. A nil
	// context yields the bare executor (unchanged behavior for one-off actions).
	var baseExec executor.Executor = executor.Local{}
	if rt != nil {
		baseExec = rt.exec
	}
	runExec := executor.WithContext(baseExec, opts.Context)

	// Image-distribution Phase 1 gate: block a build-service deploy to a Swarm/remote
	// host when no registry resolves (the image would be unpullable there). Local
	// single-node deploys are unaffected (a local image runs in place).
	if opts.Command == "start" || opts.Command == "update" || opts.Command == "refresh" {
		if gerr := b.deployRegistryGate(opts.Workspace, opts.Project, opts.Env, rt != nil); gerr != nil {
			return gerr
		}
		// Image-distribution Phase 5 preflight: a Swarm-mode env on a remote host that
		// isn't a Swarm manager fails cryptically at `stack deploy` — block it early
		// with a clear message (uses the capability probed on the host's last Test).
		if gerr := b.deploySwarmGate(opts.Workspace, opts.Project, opts.Env, rt); gerr != nil {
			return gerr
		}
	}

	// Phase 9 tool stage: run a one-off tool container (Trivy/Cypress/Sonar/custom)
	// with the env's context injected as RIGGER_* variables.
	if opts.Command == "script" {
		return b.runScript(opts, rt)
	}

	// Phase 6.5 finish: init re-bootstraps an environment natively in Go.
	// run.sh passed EXTRA to bootstrap.sh; --regen-env forces .env regeneration.
	if opts.Command == "init" || opts.Command == "bootstrap" {
		if err := b.bootstrap(opts.Workspace, opts.Project, opts.Env, contains(opts.Extra, "--regen-env"), opts.Stdout); err != nil {
			return err
		}
		if rt != nil {
			// Ship the freshly-scaffolded env dir to the host, preserving its
			// authoritative .env.
			localDir := b.localEnvDir(opts.Workspace, opts.Project, opts.Env)
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
			Project:       opts.Project,
			Subcommand:    opts.Env,
			Arg:           first(opts.Extra),
			Stdout:        opts.Stdout,
		})
		return err
	}

	// Phase 6.5 finish: build/promote (image build/push, retag-and-redeploy) run
	// natively in Go. Env/Extra carry the run.sh argument layout.
	if builder.Handles(opts.Command) {
		bopts := builder.Options{
			WorkspacesDir: b.workspacesDir,
			Workspace:     opts.Workspace,
			Project:       opts.Project,
			Command:       opts.Command,
			Env:           opts.Env,
			Extra:         opts.Extra,
			EnvVars:       shellEnv(),
			Stdout:        opts.Stdout,
			Stderr:        opts.Stderr,
			BaseDomain:    settings.EffectiveBaseDomain(b.db, opts.Workspace),
			AutoURLMode:   settings.EffectiveAutoURLMode(b.db, opts.Workspace),
			AutoURLHost:   b.magicDNSHost(opts.Workspace, opts.Project, opts.Env),
			DNSProvider:   settings.EffectiveDNSProvider(b.db, opts.Workspace),
			OverrideCert:  b.usesOverrideCert(opts.Workspace, opts.Project, opts.Env),
			CustomDomains: customdomains.VerifiedDomains(b.db, opts.Workspace, opts.Project, opts.Env),
			RouterMiddlewares: b.routerMiddlewares(opts.Workspace, opts.Project, opts.Env),
			Registry:      b.effectiveRegistry(opts.Workspace, opts.Project),
			TemplatesDir:  filepath.Join(b.toolkitRoot, "templates"), // scaffold a missing Dockerfile into _src
			Exec:          runExec, // default: env's deploy host (or local) — used by promote
		}
		// Phase 4: `build` runs on the project's BUILD host, which may differ from the
		// env's deploy host. With no explicit build host the default is to build on the
		// deploy host (rt) — today's behavior. promote always runs on the deploy host.
		buildRT := rt
		if opts.Command == "build" {
			if explicit, berr := b.resolveBuildRemote(opts.Workspace, opts.Project); berr != nil {
				return fmt.Errorf("resolve build host: %w", berr)
			} else if explicit != nil {
				buildRT = explicit
			}
			var base executor.Executor = executor.Local{}
			if buildRT != nil {
				base = buildRT.exec
			}
			bopts.Exec = executor.WithContext(base, opts.Context)
			// If the build host differs from the env's deploy host, the built image
			// can't be used in place — it must travel via a registry. Force --push and
			// require one (replaces shipping source/image to the deploy host).
			deployID, buildID := int64(0), int64(0)
			if rt != nil {
				deployID = rt.hostID
			}
			if buildRT != nil {
				buildID = buildRT.hostID
			}
			if deployID != buildID {
				if b.effectiveRegistry(opts.Workspace, opts.Project) == "" {
					return fmt.Errorf("the build host differs from the deploy host, so the built image must be pushed to a registry — but none is configured. Designate a system registry (Settings → Docker Registries) or a project registry, then rebuild")
				}
				if !contains(opts.Extra, "--push") {
					opts.Extra = append(opts.Extra, "--push")
					bopts.Extra = opts.Extra
				}
			}
		}
		// Authenticate to the registry before any push/pull, on the host that runs
		// docker: the BUILD host for build, the deploy host for promote.
		if (opts.Command == "build" && contains(opts.Extra, "--push")) || opts.Command == "promote" {
			var loginExec executor.Executor = executor.Local{}
			if opts.Command == "build" {
				if buildRT != nil {
					loginExec = buildRT.exec
				}
			} else if rt != nil {
				loginExec = rt.exec
			}
			if lerr := b.ensureRegistryLogin(opts.Workspace, opts.Project, loginExec, opts.Stdout); lerr != nil {
				// Surface but continue — the push/pull gives a definitive error if
				// credentials are genuinely missing or wrong.
				fmt.Fprintf(opts.Stdout, "⚠ registry login: %v\n", lerr)
			}
		}
		switch opts.Command {
		case "build":
			// Ship the build context to the BUILD host (its own daemon builds it). The
			// .env stays host-authoritative and is skipped. Local build host ⇒ nothing
			// to push; when the build host differs from deploy, the image was --pushed.
			if buildRT != nil {
				bopts.RemoteWorkspacesDir = b.remoteWorkspacesDir
				localDir := b.localEnvDir(opts.Workspace, opts.Project, opts.Env)
				if err := buildRT.client.PushDir(localDir, buildRT.exec.RemoteDir(localDir), ".env"); err != nil {
					return fmt.Errorf("push build context to %s: %w", buildRT.hostName, err)
				}
			}
		case "promote":
			// Retag-and-redeploy runs on the env's deploy host; route the post-promote
			// deploy back through the bridge so it lands on the destination env's host.
			if rt != nil {
				bopts.RemoteWorkspacesDir = b.remoteWorkspacesDir
				bopts.SetDeploy(func(env string) error {
					return b.Run(RunOptions{Workspace: opts.Workspace, Project: opts.Project, Command: "start", Env: env, Stdout: opts.Stdout, Stderr: opts.Stderr})
				})
			}
		}
		// Private-repo credentials (Phase 12): resolve the project's git provider into a
		// per-clone auth and hand it to the builder; clean up the temp key/config after.
		if ga, gerr := b.resolveGitAuth(opts.Workspace, opts.Project); gerr != nil {
			return gerr
		} else if ga != nil {
			bopts.GitAuth = ga
			defer ga.Cleanup()
		}
		_, err := builder.Run(bopts)
		return err
	}

	// "refresh" means "regenerate this env's config from config.json + the current
	// Rigger version". dockerops only re-runs composegen (docker-compose.yml); the
	// bootstrap-generated sidecar files (garage.toml, nginx.conf) and the .env are
	// untouched — so a fix to those, or a newly-required managed key (e.g.
	// GARAGE_RPC_SECRET), would never apply via Refresh. Re-bootstrap first
	// (regenEnv=true; envgen preserves secrets + app-owned keys), then let dockerops
	// recompose + redeploy (and sync the regenerated files to a remote host).
	if opts.Command == "refresh" {
		if err := b.bootstrap(opts.Workspace, opts.Project, opts.Env, true, opts.Stdout); err != nil {
			return fmt.Errorf("regenerate env config: %w", err)
		}
	}

	if dockerops.Handles(opts.Command) {
		dopts := dockerops.Options{
			WorkspacesDir: b.workspacesDir,
			Workspace:     opts.Workspace,
			Project:       opts.Project,
			Command:       opts.Command,
			Env:           opts.Env,
			Extra:         opts.Extra,
			PurgeVolumes:  opts.PurgeVolumes,
			EnvVars:       shellEnv(),
			Stdout:        opts.Stdout,
			Stderr:        opts.Stderr,
			BaseDomain:    settings.EffectiveBaseDomain(b.db, opts.Workspace),
			AutoURLMode:   settings.EffectiveAutoURLMode(b.db, opts.Workspace),
			AutoURLHost:   b.magicDNSHost(opts.Workspace, opts.Project, opts.Env),
			DNSProvider:   settings.EffectiveDNSProvider(b.db, opts.Workspace),
			OverrideCert:  b.usesOverrideCert(opts.Workspace, opts.Project, opts.Env),
			CustomDomains: customdomains.VerifiedDomains(b.db, opts.Workspace, opts.Project, opts.Env),
			RouterMiddlewares: b.routerMiddlewares(opts.Workspace, opts.Project, opts.Env),
			Registry:      b.effectiveRegistry(opts.Workspace, opts.Project),
			Exec:          runExec, // context-bound (local or remote) — cancellable
		}
		if rt != nil {
			localDir := b.localEnvDir(opts.Workspace, opts.Project, opts.Env)
			dopts.Remote = true
			dopts.RemoteWorkspacesDir = b.remoteWorkspacesDir
			dopts.Sync = func() error {
				return rt.client.PushDir(localDir, rt.exec.RemoteDir(localDir), ".env")
			}
		} else {
			// Local: tell compose where this env's bind-mount sources live on the HOST
			// daemon (Rigger runs in a container, so its own paths aren't resolvable).
			// composegen emits bind sources rooted at ${RIGGER_BIND_ROOT}. On a remote
			// host the compose runs natively, so the default `.` already resolves.
			dopts.EnvVars = append(dopts.EnvVars, "RIGGER_BIND_ROOT="+b.hostBindRoot(opts.Workspace, opts.Project, opts.Env))
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
			Project:       opts.Project,
			Command:       opts.Command,
			Env:           opts.Env,
			Extra:         opts.Extra,
			EnvVars:       shellEnv(),
			Stdout:        opts.Stdout,
			Stderr:        opts.Stderr,
			Timestamp:     time.Now().UTC().Format("2006-01-02_15-04-05"),
			Services:      opts.Services,
			ScheduleID:    opts.ScheduleID,
			ScheduleName:  opts.ScheduleName,
			Trigger:       opts.Trigger,
			SourceEnv:        opts.SourceEnv,
			SkipTargetBackup: opts.SkipTargetBackup,
			Exec:          runExec, // context-bound (local or remote) — cancellable
		}
		if rt != nil {
			bopts.DotEnv = b.remoteDotEnv(rt, opts.Workspace, opts.Project, opts.Env)
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
