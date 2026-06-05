package dockerops

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"

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
	if err := s.docker("stack", "deploy", "--compose-file", "docker-compose.yml", "--with-registry-auth", s.stack); err != nil {
		return err
	}
	s.success("Stack '%s' is up", s.stack)
	return nil
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
		printImageUpdates(s.opts.Stdout, s.opts.WorkspacesDir, s.opts.Workspace, s.opts.Env)
	}
	return nil
}

func (s *swarmRunner) logs() error {
	svc := s.firstExtra()
	if svc == "" {
		return fmt.Errorf("swarm logs require a service: logs %s <service>", s.opts.Env)
	}
	return s.docker("service", "logs", "-f", s.resolveSvc(svc))
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
	out, err := s.dockerOutput("stack", "services", s.stack, "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		name := strings.TrimSpace(sc.Text())
		if name == "" {
			continue
		}
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
	content, err := composegen.Generate(s.cfgBytes, s.opts.Env)
	if err != nil {
		return fmt.Errorf("generate compose: %w", err)
	}
	if err := writeFile(s.composePath, content); err != nil {
		return err
	}
	return s.deploy()
}

func (s *swarmRunner) resolveSvc(svc string) string {
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
func printImageUpdates(w io.Writer, workspacesDir, ws, env string) {
	results := imagecheck.Check(workspacesDir, ws, env)
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
