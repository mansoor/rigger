// Package dockerops implements stack deployment operations natively in Go,
// wrapping `docker compose` directly (Phase 6.5b). It replaces the runtime path
// through scripts/deploy.sh and run.sh for the compose lifecycle commands. The
// compose project name and service-prefix rule ({project}_{env}) live here, in
// one place, instead of being duplicated across bash scripts.
package dockerops

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// deployCommands are the lifecycle commands dockerops owns (compose and swarm).
// Other commands are handled by their own Go packages in the shell bridge.
var deployCommands = map[string]bool{
	"start":   true,
	"stop":    true,
	"down":    true,
	"restart": true,
	"update":  true,
	"ps":      true,
	"logs":    true,
	"refresh": true,
	"test":    true, // Phase 9: `compose exec` inside a service container
}

// Handles reports whether dockerops owns a command (for compose deployments).
func Handles(cmd string) bool { return deployCommands[cmd] }

// Options configures a deployment operation.
type Options struct {
	WorkspacesDir string
	Workspace     string // parent tier
	Project       string // project name
	Command       string
	Env           string
	Extra         []string
	EnvVars       []string // child-process environment (built by the shell bridge)
	Stdout        io.Writer
	Stderr        io.Writer

	// Exec runs the docker commands. nil → local daemon (executor.Local). Set to
	// a remotehost executor for cross-host operations (Phase 7).
	Exec executor.Executor
	// RemoteWorkspacesDir is the env dir base on the remote host (Phase 7); the
	// local Exec ignores it.
	RemoteWorkspacesDir string
	// Remote marks a cross-host run (Phase 7). When set, the compose file is
	// regenerated locally from config.json (deterministic, no secrets) and the
	// local env-dir checks are skipped — the authoritative .env lives on the host.
	Remote bool
	// Sync pushes the local env dir to the remote host before the first compose
	// call (set by the bridge for Remote runs; nil otherwise).
	Sync func() error
}

type config struct {
	Project struct {
		Name           string `json:"name"`
		Type           string `json:"type"`
		ResourcePrefix string `json:"resource_prefix"`
	} `json:"project"`
	Environments map[string]struct {
		Deployment string `json:"deployment"`
	} `json:"environments"`
}

// stackPrefix is the immutable Docker resource prefix, falling back to the
// display name for configs created before resource_prefix existed.
func (c config) stackPrefix() string {
	if c.Project.ResourcePrefix != "" {
		return c.Project.ResourcePrefix
	}
	return c.Project.Name
}

// Run executes a compose-lifecycle command. The bool return reports whether
// dockerops handled it; false means the caller should fall back to bash
// (non-deploy commands, swarm deployments, or a missing/unreadable config).
func Run(opts Options) (bool, error) {
	if !deployCommands[opts.Command] {
		return false, nil
	}

	cfgBytes, err := os.ReadFile(wspath.ConfigPath(opts.WorkspacesDir, opts.Workspace, opts.Project))
	if err != nil {
		return false, nil // can't read config — let bash try
	}
	var cfg config
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		return false, nil
	}
	envCfg, ok := cfg.Environments[opts.Env]
	if !ok {
		return true, fmt.Errorf("unknown environment %q", opts.Env)
	}

	envDir := wspath.EnvDir(opts.WorkspacesDir, opts.Workspace, opts.Project, opts.Env)
	composePath := filepath.Join(envDir, "docker-compose.yml")

	if opts.Remote {
		// Cross-host: regenerate the compose file locally (deterministic, no
		// secrets) so it exists to push. The remote .env is authoritative and is
		// never generated/pushed here — so the local .env check is skipped too.
		content, err := composegen.Generate(cfgBytes, opts.Env)
		if err != nil {
			return true, fmt.Errorf("generate compose: %w", err)
		}
		if err := writeFile(composePath, content); err != nil {
			return true, err
		}
	} else {
		// refresh regenerates the compose file first, so the file may not exist yet.
		if opts.Command != "refresh" {
			if _, err := os.Stat(composePath); err != nil {
				return true, fmt.Errorf("docker-compose.yml not found for %q — run init first", opts.Env)
			}
		}
		if err := ensureEnvFile(envDir); err != nil {
			return true, err
		}
	}

	// Swarm deployments are now handled natively in Go too (Phase 6.5 finish).
	if envCfg.Deployment == "swarm" {
		s := &swarmRunner{
			opts:        opts,
			cfgBytes:    cfgBytes,
			projectType: cfg.Project.Type,
			stack:       cfg.stackPrefix() + "_" + opts.Env,
			envDir:      envDir,
			composePath: composePath,
		}
		return s.run()
	}

	r := &runner{
		opts:        opts,
		cfgBytes:    cfgBytes,
		projectType: cfg.Project.Type,
		// Compose project name MUST use the immutable resource prefix
		// ({workspace}_{project}) so projects with the same display name in
		// different workspaces don't share a compose project (which would make
		// `compose up` for one tear down the other's containers).
		stack:       cfg.stackPrefix() + "_" + opts.Env,
		envDir:      envDir,
		composePath: composePath,
	}

	switch opts.Command {
	case "start":
		return true, r.up()
	case "stop":
		return true, r.stop()
	case "down":
		return true, r.down()
	case "restart":
		return true, r.restart()
	case "update":
		return true, r.update()
	case "ps":
		return true, r.ps()
	case "logs":
		return true, r.logs()
	case "refresh":
		return true, r.refresh()
	case "test":
		return true, r.test()
	}
	return false, nil
}

type runner struct {
	opts        Options
	cfgBytes    []byte
	projectType string
	stack       string
	envDir      string
	composePath string
	synced      bool // Remote: env dir pushed to host (once per run)
}

// ensureSynced pushes the local env dir to the remote host before the first
// compose call. No-op for local runs (Sync nil) and after the first push.
func (r *runner) ensureSynced() error {
	if r.opts.Sync == nil || r.synced {
		return nil
	}
	r.synced = true
	return r.opts.Sync()
}

// compose runs `docker compose -p <stack> -f docker-compose.yml <args>`,
// streaming to the configured writers. CWD is the env dir so the .env file is
// picked up.
func (r *runner) compose(args ...string) error {
	if err := r.ensureSynced(); err != nil {
		return err
	}
	full := append([]string{"compose", "-p", r.stack, "-f", "docker-compose.yml"}, args...)
	return executor.Default(r.opts.Exec).Docker(executor.Spec{
		Args: full, Dir: r.envDir, Env: r.opts.EnvVars,
		Stdout: r.opts.Stdout, Stderr: r.opts.Stderr,
	})
}

// composeOutput runs a compose command and captures stdout (no streaming).
func (r *runner) composeOutput(args ...string) ([]byte, error) {
	if err := r.ensureSynced(); err != nil {
		return nil, err
	}
	full := append([]string{"compose", "-p", r.stack, "-f", "docker-compose.yml"}, args...)
	return executor.Default(r.opts.Exec).DockerOutput(executor.Spec{
		Args: full, Dir: r.envDir, Env: r.opts.EnvVars,
	})
}

func (r *runner) info(format string, a ...any) {
	fmt.Fprintf(r.opts.Stdout, "⚑ "+format+"\n", a...)
}
func (r *runner) success(format string, a ...any) {
	fmt.Fprintf(r.opts.Stdout, "✓ "+format+"\n", a...)
}

func (r *runner) up() error {
	// A service name in Extra targets a single container (per-container Start
	// button); empty deploys/starts the whole stack.
	if svc := r.firstExtra(); svc != "" {
		r.info("Starting %s in '%s'", svc, r.stack)
		if err := r.compose("up", "-d", r.resolveSvc(svc)); err != nil {
			return err
		}
		r.success("Started %s", svc)
		return nil
	}
	r.info("Deploying '%s' (compose)", r.stack)
	if err := r.compose("up", "-d", "--remove-orphans"); err != nil {
		return err
	}
	r.success("Stack '%s' is up", r.stack)
	return nil
}

func (r *runner) stop() error {
	if svc := r.firstExtra(); svc != "" {
		r.info("Stopping %s in '%s'", svc, r.stack)
		if err := r.compose("stop", r.resolveSvc(svc)); err != nil {
			return err
		}
		r.success("Stopped %s", svc)
		return nil
	}
	r.info("Stopping stack '%s' (containers kept)", r.stack)
	if err := r.compose("stop"); err != nil {
		return err
	}
	r.success("Stack '%s' stopped", r.stack)
	return nil
}

func (r *runner) down() error {
	r.info("Bringing down stack '%s' (containers removed)", r.stack)
	if err := r.compose("down"); err != nil {
		return err
	}
	r.success("Stack '%s' is down", r.stack)
	return nil
}

func (r *runner) restart() error {
	svc := r.firstExtra()
	var target []string
	if svc != "" {
		target = []string{r.resolveSvc(svc)}
		r.info("Restarting %s in '%s'", svc, r.stack)
	} else {
		r.info("Restarting all services in '%s'", r.stack)
	}
	// Prefer a real `compose restart`, which bounces the running containers in
	// place (what "Restart" promises). But `restart` is a no-op when nothing is
	// running — e.g. the target was previously `down`ed or stopped — so in that
	// case fall back to `up -d` to (re)create it, matching the old behaviour.
	running := false
	if out, err := r.composeOutput(append([]string{"ps", "--status", "running", "--quiet"}, target...)...); err == nil {
		running = len(bytes.TrimSpace(out)) > 0
	}
	if running {
		if err := r.compose(append([]string{"restart"}, target...)...); err != nil {
			return err
		}
		r.success("Restart complete")
		return nil
	}
	r.info("Nothing running — bringing it up instead")
	up := []string{"up", "-d"}
	if svc == "" {
		up = append(up, "--remove-orphans")
	}
	up = append(up, target...)
	if err := r.compose(up...); err != nil {
		return err
	}
	r.success("Started (was not running)")
	return nil
}

func (r *runner) update() error {
	// A service name in Extra updates a single container; empty updates the stack.
	svc := r.firstExtra()
	var target []string
	label := "'" + r.stack + "'"
	if svc != "" {
		target = []string{r.resolveSvc(svc)}
		label = svc + " in '" + r.stack + "'"
	}
	r.info("Updating images for %s", label)
	// Preserve running state: a stopped target stays stopped after update.
	runningBefore := false
	if out, err := r.composeOutput(append([]string{"ps", "--status", "running", "--quiet"}, target...)...); err == nil {
		runningBefore = len(bytes.TrimSpace(out)) > 0
	}
	r.info("Pulling latest images...")
	if err := r.compose(append([]string{"pull"}, target...)...); err != nil {
		return err
	}
	if runningBefore {
		r.info("Recreating containers with new images...")
		up := []string{"up", "-d"}
		if svc == "" {
			up = append(up, "--remove-orphans")
		}
		up = append(up, target...)
		if err := r.compose(up...); err != nil {
			return err
		}
		r.success("Updated %s and restarted", label)
	} else {
		r.success("Updated %s (images pulled, stays stopped)", label)
	}
	return nil
}

func (r *runner) ps() error {
	if err := r.compose("ps"); err != nil {
		return err
	}
	if r.projectType == "image" {
		printImageUpdates(r.opts.Stdout, r.opts.WorkspacesDir, r.opts.Workspace, r.opts.Project, r.opts.Env)
	}
	return nil
}

func (r *runner) logs() error {
	svc := r.firstExtra()
	if svc == "" {
		return r.compose("logs", "-f")
	}
	return r.compose("logs", "-f", r.resolveSvc(svc))
}

func (r *runner) refresh() error {
	r.info("Regenerating docker-compose.yml for '%s'...", r.opts.Env)
	content, err := composegen.Generate(r.cfgBytes, r.opts.Env)
	if err != nil {
		return fmt.Errorf("generate compose: %w", err)
	}
	if err := writeFile(r.composePath, content); err != nil {
		return err
	}
	return r.up()
}

// test runs a command inside a running service container via `compose exec`
// (Phase 9 pipeline `test` stage). Extra[0] is the service, Extra[1] the command.
// The command is run through `sh -c` so a single string can be a full shell
// expression; -T disables the TTY so output streams cleanly to the writers.
func (r *runner) test() error {
	svc := r.firstExtra()
	cmd := ""
	if len(r.opts.Extra) > 1 {
		cmd = r.opts.Extra[1]
	}
	if svc == "" || strings.TrimSpace(cmd) == "" {
		return fmt.Errorf("test stage requires a service and a command")
	}
	r.info("Testing '%s' — exec in %s: %s", r.stack, svc, cmd)
	if err := r.compose("exec", "-T", r.resolveSvc(svc), "sh", "-c", cmd); err != nil {
		return err
	}
	r.success("Test passed")
	return nil
}

// writeFile writes compose content, creating the parent dir if needed.
func writeFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

// resolveSvc accepts either a short service name ("app") or the full prefixed
// name ("myapp_prod_app") and returns the compose service name.
func (r *runner) resolveSvc(svc string) string {
	if strings.HasPrefix(svc, r.stack+"_") {
		return svc
	}
	return r.stack + "_" + svc
}

func (r *runner) firstExtra() string {
	if len(r.opts.Extra) > 0 {
		return r.opts.Extra[0]
	}
	return ""
}

// ensureEnvFile makes sure the env's .env exists, copying from .env.example when
// present (mirrors lib.sh ensure_env_file). docker compose needs it in CWD.
func ensureEnvFile(envDir string) error {
	envPath := filepath.Join(envDir, ".env")
	if _, err := os.Stat(envPath); err == nil {
		return nil
	}
	example := filepath.Join(envDir, ".env.example")
	if data, err := os.ReadFile(example); err == nil {
		return os.WriteFile(envPath, data, 0o644)
	}
	return fmt.Errorf(".env missing for environment (and no .env.example to seed it)")
}
