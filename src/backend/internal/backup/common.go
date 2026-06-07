// Package backup implements per-environment backup and restore natively in Go
// (Phase 6.5c), replacing scripts/backup.sh and scripts/restore.sh on the
// runtime path. It keeps the exact on-disk layout and filename conventions of
// the bash scripts so existing snapshots, the backup listing UI, and restore
// remain compatible.
package backup

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansoor/rigger/ui/internal/executor"
)

// command set this package owns (routed from the shell bridge).
var ownedCommands = map[string]bool{"backup": true, "restore": true}

// Handles reports whether this package owns a command.
func Handles(cmd string) bool { return ownedCommands[cmd] }

// Options configures a backup or restore run.
type Options struct {
	WorkspacesDir string
	Workspace     string
	Command       string // "backup" | "restore"
	Env           string
	Extra         []string // backup: [target]; restore: [snapshot]
	EnvVars       []string // child-process environment
	Stdout        io.Writer
	Stderr        io.Writer
	Timestamp     string // YYYY-MM-DD_HH-MM-SS; injected by caller (deterministic)

	// Exec runs the docker commands. nil → local daemon (Phase 7: remote over SSH).
	Exec executor.Executor
	// DotEnv, when non-nil, supplies the environment's variables (DB credentials)
	// instead of reading the local .env. The bridge sets it from the remote
	// host-authoritative .env for cross-host backup/restore (Phase 7).
	DotEnv map[string]string
}

// Run dispatches backup/restore. Returns (handled, err); handled=false lets the
// caller fall back to bash (unreadable config or a command we don't own).
func Run(opts Options) (bool, error) {
	if !ownedCommands[opts.Command] {
		return false, nil
	}
	cfg, err := loadConfig(opts.WorkspacesDir, opts.Workspace)
	if err != nil {
		return false, nil // let bash try
	}
	switch opts.Command {
	case "backup":
		return true, runBackup(opts, cfg)
	case "restore":
		return true, runRestore(opts, cfg)
	}
	return false, nil
}

// ── config ───────────────────────────────────────────────────────────────────────

type wsConfig struct {
	Project struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"project"`
	Images []struct {
		Name  string `json:"name"`
		Image string `json:"image"`
	} `json:"images"`
	Environments map[string]struct {
		Database      string `json:"database"`
		GarageEnabled bool   `json:"garage_enabled"`
	} `json:"environments"`
	Backup struct {
		Retention int `json:"retention"` // number of snapshots to keep per env (0 = unset → no prune)
	} `json:"backup"`
}

func loadConfig(workspacesDir, workspace string) (*wsConfig, error) {
	data, err := os.ReadFile(filepath.Join(workspacesDir, workspace, "config.json"))
	if err != nil {
		return nil, err
	}
	var c wsConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.Project.Type == "" {
		c.Project.Type = "custom"
	}
	return &c, nil
}

// ctx carries the resolved paths/names shared across a backup or restore run.
type ctx struct {
	opts    Options
	cfg     *wsConfig
	runner  executor.Executor
	project string
	env     string
	prefix  string // {project}_{env} — compose project + service prefix
	envDir  string
	envVars map[string]string // parsed .env (DB credentials etc.)
}

func newCtx(opts Options, cfg *wsConfig) *ctx {
	envDir := filepath.Join(opts.WorkspacesDir, opts.Workspace, "envs", opts.Env)
	envVars := opts.DotEnv // remote host-authoritative .env (Phase 7)
	if envVars == nil {
		envVars = readDotEnv(filepath.Join(envDir, ".env"))
	}
	return &ctx{
		opts:    opts,
		cfg:     cfg,
		runner:  executor.Default(opts.Exec),
		project: cfg.Project.Name,
		env:     opts.Env,
		prefix:  cfg.Project.Name + "_" + opts.Env,
		envDir:  envDir,
		envVars: envVars,
	}
}

func (c *ctx) info(format string, a ...any)    { fmt.Fprintf(c.opts.Stdout, "⚑ "+format+"\n", a...) }
func (c *ctx) success(format string, a ...any) { fmt.Fprintf(c.opts.Stdout, "✓ "+format+"\n", a...) }
func (c *ctx) warn(format string, a ...any)    { fmt.Fprintf(c.opts.Stdout, "⚠ "+format+"\n", a...) }

// resolveSvc prefixes a short service name with the stack prefix unless already present.
func (c *ctx) resolveSvc(svc string) string {
	if strings.HasPrefix(svc, c.prefix+"_") {
		return svc
	}
	return c.prefix + "_" + svc
}

// envOr returns the .env value for key, or def.
func (c *ctx) envOr(key, def string) string {
	if v, ok := c.envVars[key]; ok && v != "" {
		return v
	}
	return def
}

// ── docker helpers ───────────────────────────────────────────────────────────────

// dexec runs a bare docker command through the executor, injecting the env.
func (c *ctx) dexec(spec executor.Spec) error {
	spec.Env = c.opts.EnvVars
	return c.runner.Docker(spec)
}

// dout runs a docker command and captures stdout.
func (c *ctx) dout(args ...string) ([]byte, error) {
	return c.runner.DockerOutput(executor.Spec{Args: args, Env: c.opts.EnvVars})
}

// compose runs a `docker compose` subcommand in the env dir (so ./.env and
// ./docker-compose.yml resolve — locally or on the remote host). The spec
// supplies any Stdin/Stdout/Stderr streaming; Args/Dir/Env are filled here.
func (c *ctx) compose(spec executor.Spec, args ...string) error {
	spec.Args = append([]string{"compose", "-p", c.prefix, "-f", "docker-compose.yml"}, args...)
	spec.Dir = c.envDir
	spec.Env = c.opts.EnvVars
	return c.runner.Docker(spec)
}

// mount is one container mount from `docker inspect`.
type mount struct {
	Type        string
	Destination string
}

// inspectMounts returns the bind/volume mounts of a container.
func (c *ctx) inspectMounts(container string) []mount {
	out, err := c.dout("inspect", container,
		"--format", `{{range .Mounts}}{{.Type}}|{{.Destination}}{{"\n"}}{{end}}`)
	if err != nil {
		return nil
	}
	var mounts []mount
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		parts := strings.SplitN(strings.TrimSpace(scanner.Text()), "|", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		if parts[0] != "bind" && parts[0] != "volume" {
			continue
		}
		mounts = append(mounts, mount{Type: parts[0], Destination: parts[1]})
	}
	return mounts
}

// archiveFromContainer tar+gzips a path inside a (running or stopped) container's
// mounts to outFile, via `docker run --volumes-from <c>:ro alpine tar`. The tar
// output (already gzipped) is written straight to the file. Returns true on a
// non-empty archive.
func (c *ctx) archiveFromContainer(container, srcPath, outFile string) bool {
	f, err := os.Create(outFile)
	if err != nil {
		return false
	}
	defer f.Close()
	err = c.dexec(executor.Spec{
		Args:   []string{"run", "--rm", "--volumes-from", container + ":ro", "alpine:3", "tar", "czf", "-", "-C", srcPath, "."},
		Stdout: f,
	})
	if err != nil {
		f.Close()
		os.Remove(outFile)
		return false
	}
	if fi, err := os.Stat(outFile); err != nil || fi.Size() == 0 {
		os.Remove(outFile)
		return false
	}
	return true
}

// archiveNamedVolumes tar+gzips one or more named volumes (mounted read-only) to
// outFile. mounts maps volume name → mount path inside the temp container.
func (c *ctx) archiveNamedVolumes(outFile, tarCDir string, mounts map[string]string, tarPaths ...string) bool {
	f, err := os.Create(outFile)
	if err != nil {
		return false
	}
	defer f.Close()
	args := []string{"run", "--rm"}
	for vol, dst := range mounts {
		args = append(args, "-v", vol+":"+dst+":ro")
	}
	args = append(args, "alpine:3", "tar", "czf", "-", "-C", tarCDir)
	args = append(args, tarPaths...)
	if err := c.dexec(executor.Spec{Args: args, Stdout: f}); err != nil {
		f.Close()
		os.Remove(outFile)
		return false
	}
	if fi, err := os.Stat(outFile); err != nil || fi.Size() == 0 {
		os.Remove(outFile)
		return false
	}
	return true
}

// volumeExists reports whether a named docker volume exists.
func (c *ctx) volumeExists(vol string) bool {
	return c.dexec(executor.Spec{Args: []string{"volume", "inspect", vol}}) == nil
}

// ── .env parsing ─────────────────────────────────────────────────────────────────

// readDotEnv parses a KEY=VALUE .env file (best-effort; ignores comments/blank
// lines, strips surrounding quotes).
func readDotEnv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
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
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"'`)
		if key != "" {
			out[key] = val
		}
	}
	return out
}
