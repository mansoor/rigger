package pipelines

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/mansoor/rigger/ui/internal/shell"
)

// BridgeRunner is the subset of *shell.Bridge the executor needs — kept as an
// interface so the stage mapping and run loop are unit-testable without a real
// bridge (and so the executor doesn't depend on bridge construction).
type BridgeRunner interface {
	Run(opts shell.RunOptions) error
}

// StageRunOptions maps a stage to the shell-bridge command + args for one project.
// Exported for unit testing.
func StageRunOptions(workspace, project string, s Stage) shell.RunOptions {
	o := shell.RunOptions{Workspace: workspace, Project: project, Env: s.Env}
	switch s.Type {
	case "deploy":
		o.Command = "start" // compose up -d on current/just-built images (no pull)
	case "update":
		o.Command = "update" // pull latest images from the registry, then recreate
	case "build":
		o.Command = "build" // build all service images for the env (no push)
	case "restart":
		o.Command = "restart"
	case "backup":
		o.Command = "backup"
		if s.Service != "" {
			o.Extra = []string{s.Service}
		}
		o.Trigger = "manual"
		o.ScheduleID = "manual"
		o.ScheduleName = "Pipeline backup"
	case "test":
		o.Command = "test" // sandboxed compose exec inside the service container
		o.Extra = []string{s.Service, s.Command}
	}
	return o
}

func stageLabel(s Stage) string {
	switch s.Type {
	case "test":
		return fmt.Sprintf("test %s: %s", s.Service, s.Command)
	case "backup":
		if s.Service != "" {
			return fmt.Sprintf("backup %s (%s)", s.Env, s.Service)
		}
		return "backup " + s.Env
	default:
		return s.Type + " " + s.Env
	}
}

// Execute runs a pipeline stage-by-stage, streaming all output to out, and returns
// the per-stage results plus whether every (non-skipped) stage succeeded. A failed
// stage with on_failure=stop halts the run and marks the remaining stages skipped.
func Execute(bridge BridgeRunner, p Pipeline, out io.Writer) (results []StageResult, ok bool) {
	results = []StageResult{}
	ok = true
	for i, s := range p.Stages {
		label := stageLabel(s)
		fmt.Fprintf(out, "\n\033[1;36m━━ Stage %d/%d: %s ━━\033[0m\n", i+1, len(p.Stages), label)

		cap := &capWriter{cap: maxOutputBytes}
		mw := io.MultiWriter(out, cap)
		opts := StageRunOptions(p.Workspace, p.Project, s)
		opts.Stdout = mw
		opts.Stderr = mw

		start := time.Now()
		err := bridge.Run(opts)
		res := StageResult{Type: s.Type, Env: s.Env, Label: label, MS: time.Since(start).Milliseconds()}

		if err != nil {
			fmt.Fprintf(mw, "\n\033[31m✗ %s failed: %s\033[0m\n", label, err.Error())
			res.Status = "fail"
			res.Output = cap.String()
			results = append(results, res)
			ok = false
			if s.OnFailure != "continue" {
				for j := i + 1; j < len(p.Stages); j++ {
					sk := p.Stages[j]
					results = append(results, StageResult{
						Type: sk.Type, Env: sk.Env, Label: stageLabel(sk), Status: "skipped",
					})
				}
				fmt.Fprintf(out, "\n\033[33m■ Pipeline halted after stage %d (on_failure=stop).\033[0m\n", i+1)
				return results, false
			}
			continue
		}

		fmt.Fprintf(mw, "\033[32m✓ %s ok\033[0m\n", label)
		res.Status = "ok"
		res.Output = cap.String()
		results = append(results, res)
	}
	return results, ok
}

// capWriter captures written bytes for the recorded history, bounded to ~cap bytes
// (keeping the tail, where failures land). It never returns a short write so it is
// safe inside an io.MultiWriter.
type capWriter struct {
	buf bytes.Buffer
	cap int
}

func (c *capWriter) Write(p []byte) (int, error) {
	c.buf.Write(p)
	if c.buf.Len() > c.cap*2 {
		b := c.buf.Bytes()
		tail := append([]byte(nil), b[len(b)-c.cap:]...)
		c.buf.Reset()
		c.buf.Write(tail)
	}
	return len(p), nil
}

func (c *capWriter) String() string {
	b := c.buf.Bytes()
	if len(b) > c.cap {
		return "…(truncated)…\n" + string(b[len(b)-c.cap:])
	}
	return string(b)
}
