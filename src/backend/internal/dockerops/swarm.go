package dockerops

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/imagecheck"
)

// swarmRunner executes the deploy lifecycle for swarm-mode environments using
// `docker stack` / `docker service` (Phase 6.5 finish — replaces the swarm
// branch of scripts/deploy.sh). The compose path lives in deploy.go's runner.
type swarmRunner struct {
	opts        Options
	cfgBytes    []byte
	projectType string
	stack       string
	envDir      string
	composePath string
	synced      bool // Remote: env dir pushed to host (once per run)
}

// ensureSynced pushes the local env dir to the remote host before the first
// docker call. No-op for local runs (Sync nil) and after the first push.
func (s *swarmRunner) ensureSynced() error {
	if s.opts.Sync == nil || s.synced {
		return nil
	}
	s.synced = true
	return s.opts.Sync()
}

func (s *swarmRunner) run() (bool, error) {
	switch s.opts.Command {
	case "start":
		return true, s.deploy()
	case "stop", "down":
		return true, s.remove()
	case "ps":
		return true, s.ps()
	case "logs":
		return true, s.logs()
	case "restart":
		return true, s.restart()
	case "update":
		return true, s.update()
	case "refresh":
		return true, s.refresh()
	case "test":
		return true, fmt.Errorf("pipeline 'test' stages are not supported on Swarm environments yet")
	}
	return false, nil
}

// docker runs a docker command, streaming to the configured writers. CWD is the
// env dir so docker-compose.yml / .env resolve.
func (s *swarmRunner) docker(args ...string) error {
	if err := s.ensureSynced(); err != nil {
		return err
	}
	return executor.Default(s.opts.Exec).Docker(executor.Spec{
		Args: args, Dir: s.envDir, Env: s.opts.EnvVars,
		Stdout: s.opts.Stdout, Stderr: s.opts.Stderr,
	})
}

func (s *swarmRunner) dockerOutput(args ...string) ([]byte, error) {
	if err := s.ensureSynced(); err != nil {
		return nil, err
	}
	return executor.Default(s.opts.Exec).DockerOutput(executor.Spec{
		Args: args, Dir: s.envDir, Env: s.opts.EnvVars,
	})
}

func (s *swarmRunner) info(format string, a ...any) {
	fmt.Fprintf(s.opts.Stdout, "⚑ "+format+"\n", a...)
}
func (s *swarmRunner) success(format string, a ...any) {
	fmt.Fprintf(s.opts.Stdout, "✓ "+format+"\n", a...)
}

func (s *swarmRunner) deploy() error {
	s.info("Deploying '%s' (swarm)", s.stack)
	if err := s.ensureSynced(); err != nil {
		return err
	}
	// `docker stack deploy` does NOT read the env's .env for ${VAR} interpolation
	// the way `docker compose` does — so the environment:/ports: ${VAR}
	// placeholders would resolve to empty (breaking DB creds, ports, etc.).
	// Inject the .env into the deploy process so interpolation works.
	err := executor.Default(s.opts.Exec).Docker(executor.Spec{
		Args:   []string{"stack", "deploy", "--compose-file", "docker-compose.yml", "--with-registry-auth", s.stack},
		Dir:    s.envDir,
		Env:    s.deployEnv(),
		Stdout: s.opts.Stdout,
		Stderr: s.opts.Stderr,
	})
	if err != nil {
		return err
	}
	s.success("Stack '%s' is up", s.stack)
	return nil
}

// deployEnv merges the shell environment with the env's .env file so
// `docker stack deploy` can interpolate ${VAR} placeholders (it doesn't read
// .env itself, unlike `docker compose`).
func (s *swarmRunner) deployEnv() []string {
	env := append([]string{}, s.opts.EnvVars...)
	for k, v := range parseDotEnv(filepath.Join(s.envDir, ".env")) {
		env = append(env, k+"="+v)
	}
	return env
}

// parseDotEnv reads KEY=VALUE pairs from a .env file (best-effort; ignores
// comments/blanks, strips surrounding quotes and an `export ` prefix).
func parseDotEnv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
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

// remove backs both stop and down — swarm has no "stop without remove".
func (s *swarmRunner) remove() error {
	s.info("Removing swarm stack '%s'", s.stack)
	if err := s.docker("stack", "rm", s.stack); err != nil {
		return err
	}
	s.success("Stack '%s' removed", s.stack)
	return nil
}

func (s *swarmRunner) ps() error {
	if err := s.docker("stack", "services", s.stack); err != nil {
		return err
	}
	if s.projectType == "image" {
		printImageUpdates(s.opts.Stdout, s.opts.WorkspacesDir, s.opts.Workspace, s.opts.Project, s.opts.Env)
	}
	return nil
}

func (s *swarmRunner) logs() error {
	if svc := s.firstExtra(); svc != "" {
		return s.docker("service", "logs", "-f", "--tail", "200", s.resolveSvc(svc))
	}
	// No service → swarm has no `docker stack logs`, so fan out `service logs -f`
	// for every service in the stack, merged into one stream (lines are prefixed
	// with the task name so the source is clear).
	names := s.serviceNames()
	if len(names) == 0 {
		return fmt.Errorf("no services in stack %s — deploy it first", s.stack)
	}
	if err := s.ensureSynced(); err != nil {
		return err
	}
	// Serialize writes so concurrent service streams don't interleave mid-line.
	mw := &mutexWriter{w: s.opts.Stdout}
	var wg sync.WaitGroup
	for _, n := range names {
		n := n
		wg.Add(1)
		go func() {
			defer wg.Done()
			executor.Default(s.opts.Exec).Docker(executor.Spec{ //nolint:errcheck
				Args:   []string{"service", "logs", "-f", "--tail", "200", n},
				Dir:    s.envDir,
				Env:    s.opts.EnvVars,
				Stdout: mw,
				Stderr: mw,
			})
		}()
	}
	wg.Wait()
	return nil
}

// mutexWriter serializes concurrent writes to an underlying writer.
type mutexWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (m *mutexWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.w.Write(p)
}

func (s *swarmRunner) restart() error {
	if svc := s.firstExtra(); svc != "" {
		s.info("Restarting %s in '%s'", svc, s.stack)
		if err := s.docker("service", "update", "--force", s.resolveSvc(svc)); err != nil {
			return err
		}
		s.success("Restart complete")
		return nil
	}
	s.info("Restarting all services in '%s'", s.stack)
	for _, name := range s.serviceNames() {
		if err := s.docker("service", "update", "--force", name); err != nil {
			return err
		}
	}
	s.success("Restart complete")
	return nil
}

// update for swarm re-deploys the stack, which pulls newer images and rolls
// services. (Compose's pull-then-recreate has no direct swarm analogue.)
func (s *swarmRunner) update() error {
	s.info("Updating '%s' (swarm redeploy)", s.stack)
	return s.deploy()
}

func (s *swarmRunner) refresh() error {
	s.info("Regenerating docker-compose.yml for '%s'...", s.opts.Env)
	content, err := composegen.GenerateRouted(s.cfgBytes, s.opts.Env, composegen.RouteOpts{BaseDomain: s.opts.BaseDomain, AutoURLMode: s.opts.AutoURLMode, AutoURLHost: s.opts.AutoURLHost, EnvFile: readDotenv(s.envDir)})
	if err != nil {
		return fmt.Errorf("generate compose: %w", err)
	}
	if err := writeFile(s.composePath, content); err != nil {
		return err
	}
	return s.deploy()
}

// serviceNames lists the actual deployed swarm service names for this stack.
func (s *swarmRunner) serviceNames() []string {
	out, err := s.dockerOutput("stack", "services", s.stack, "--format", "{{.Name}}")
	if err != nil {
		return nil
	}
	var names []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if n := strings.TrimSpace(sc.Text()); n != "" {
			names = append(names, n)
		}
	}
	return names
}

// resolveSvc maps a service identifier from the UI (short name like "app", the
// compose key "test_prod_app", or the full swarm name) to the actual swarm
// service name. Swarm names are <stack>_<composeKey> and the compose key is
// itself <project>_<env>_<short>, so the stack prefix appears twice — string
// prefixing alone is unreliable. Match against the real service list by exact
// name or "_<svc>" suffix, falling back to a best-effort prefix.
func (s *swarmRunner) resolveSvc(svc string) string {
	for _, n := range s.serviceNames() {
		if n == svc || strings.HasSuffix(n, "_"+svc) {
			return n
		}
	}
	if strings.HasPrefix(svc, s.stack+"_") {
		return svc
	}
	return s.stack + "_" + svc
}

func (s *swarmRunner) firstExtra() string {
	if len(s.opts.Extra) > 0 {
		return s.opts.Extra[0]
	}
	return ""
}

// printImageUpdates checks image-stack updates and prints a summary (report
// only — the `update` command performs the actual pull). Shared by compose and
// swarm `ps`; replaces scripts/image-check.sh in the ps path.
func printImageUpdates(w io.Writer, workspacesDir, ws, proj, env string) {
	results := imagecheck.Check(workspacesDir, ws, proj, env)
	if len(results) == 0 {
		return
	}
	fmt.Fprintln(w, "\nImage updates:")
	for _, u := range results {
		ref := u.Image + ":" + u.Tag
		switch {
		case u.Error != "":
			fmt.Fprintf(w, "  ! %s (%s) — %s\n", u.Service, ref, u.Error)
		case u.Indeterminate:
			fmt.Fprintf(w, "  ? %s (%s) — could not determine (built locally?)\n", u.Service, ref)
		case u.HasUpdate:
			fmt.Fprintf(w, "  ⬆ %s (%s) — update available: %s\n", u.Service, ref, u.NewerTag)
		default:
			fmt.Fprintf(w, "  ✓ %s (%s) — up to date\n", u.Service, ref)
		}
	}
}
