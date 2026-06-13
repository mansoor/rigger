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
	// Build/deploy/update/restart/refresh act on the whole env by default; a
	// Service scopes them to one service (microservices). The underlying commands
	// take a single-service target via Extra[0] (start/stop/restart/update/refresh
	// resolve it with firstExtra; build treats a non-flag arg as the target).
	switch s.Type {
	case "deploy":
		o.Command = "start" // compose up -d on current/just-built images (no pull)
		if s.Service != "" {
			o.Extra = []string{s.Service}
		}
	case "refresh":
		o.Command = "refresh" // regenerate docker-compose.yml from config/.env, then up -d
		if s.Service != "" {
			o.Extra = []string{s.Service}
		}
	case "update":
		o.Command = "update" // pull latest images from the registry, then recreate
		if s.Service != "" {
			o.Extra = []string{s.Service}
		}
	case "build":
		o.Command = "build" // build service images for the env (all, or one via Service)
		if s.Service != "" {
			o.Extra = append(o.Extra, s.Service) // build only this service (target arg)
		}
		if s.Part != "" {
			// Bump the version as part of the build, BEFORE the image is tagged — so
			// the build owns the version change and its image-pointer advance reads the
			// correct pre-bump version (a separate `version` stage desyncs that). This
			// mirrors the project Build button (`--bump <part>`).
			o.Extra = append(o.Extra, "--bump", s.Part)
		}
		if s.Push {
			o.Extra = append(o.Extra, "--push") // also push to the registry (required before a later promote)
		}
	case "restart":
		o.Command = "restart"
		if s.Service != "" {
			o.Extra = []string{s.Service}
		}
	case "backup":
		o.Command = "backup"
		if s.Service != "" {
			o.Extra = []string{s.Service}
		}
		o.Trigger = "manual"
		o.ScheduleID = "manual"
		o.ScheduleName = "Pipeline backup"
	case "push":
		o.Command = "promote" // pull src image → retag/push → deploy dst (via registry)
		o.Extra = []string{s.ToEnv}
	case "test":
		o.Command = "test" // sandboxed compose exec inside the service container
		o.Extra = []string{s.Service, s.Command}
	case "version":
		o.Command = "version" // bridge maps Env→subcommand, Extra[0]→arg
		o.Env = "bump"
		o.Extra = []string{s.Part}
	case "script":
		o.Command = "script" // one-off tool container with env context injected
		o.ScriptImage = s.Image
		o.ScriptCommand = s.Command
		o.ScriptNetwork = s.Network
	}
	return o
}

func stageLabel(s Stage) string {
	switch s.Type {
	case "gate":
		if s.Command != "" {
			return "gate: " + s.Command
		}
		return "manual gate"
	case "push":
		return "promote " + s.Env + " → " + s.ToEnv
	case "version":
		return "version bump " + s.Part
	case "script":
		return "script: " + s.Image
	case "test":
		return fmt.Sprintf("test %s: %s", s.Service, s.Command)
	case "backup":
		if s.Service != "" {
			return fmt.Sprintf("backup %s (%s)", s.Env, s.Service)
		}
		return "backup " + s.Env
	case "build":
		label := "build " + s.Env
		if s.Service != "" {
			label += "/" + s.Service
		}
		if s.Part != "" {
			label += " ↑" + s.Part
		}
		if s.Push {
			label += " (+push)"
		}
		return label
	default:
		label := s.Type + " " + s.Env
		if s.Service != "" {
			label += "/" + s.Service
		}
		return label
	}
}

// Outcome values returned by Execute.
const (
	OutcomeOK       = "ok"
	OutcomeFail     = "fail"
	OutcomeAwaiting = "awaiting" // paused at a manual gate; resume with Execute(startIdx)
)

// Execute runs a pipeline's stages from startIdx, streaming output to out, and
// returns the per-stage results for the segment plus an outcome:
//   - "ok"       — all stages in the segment succeeded
//   - "fail"     — a stage failed with on_failure=stop (remaining marked skipped)
//   - "awaiting" — hit a manual `gate` stage; the gate is recorded as the last
//     result with status "awaiting". Resume by calling Execute again with
//     startIdx = (previous accumulated stage count).
//
// startIdx is 0 for a fresh run; for a resume it is the number of stages already
// recorded (so the gate slot is consumed and execution continues after it).
//
// progress, if non-nil, is invoked as the run advances — when a stage starts
// (status "running"), periodically as its output streams, and when it settles
// (ok/fail) — with the current segment results. Callers persist this so a polling
// client (or a reopened log window) can track where the pipeline is right now,
// independent of any live socket. The slice is reused between calls; copy it if
// you retain it past the callback.
func Execute(bridge BridgeRunner, p Pipeline, out io.Writer, startIdx int, progress func(results []StageResult)) (results []StageResult, outcome string) {
	results = []StageResult{}
	outcome = OutcomeOK
	emit := func() {
		if progress != nil {
			progress(results)
		}
	}
	for i := startIdx; i < len(p.Stages); i++ {
		s := p.Stages[i]
		label := stageLabel(s)

		// Manual gate: pause here. Record it as awaiting and stop the segment.
		if s.Type == "gate" {
			fmt.Fprintf(out, "\n\033[1;33m⏸ Stage %d/%d: %s — awaiting approval\033[0m\n", i+1, len(p.Stages), label)
			results = append(results, StageResult{Type: "gate", Label: label, Status: OutcomeAwaiting})
			emit()
			return results, OutcomeAwaiting
		}

		fmt.Fprintf(out, "\n\033[1;36m━━ Stage %d/%d: %s ━━\033[0m\n", i+1, len(p.Stages), label)

		// Record the stage as running and publish progress before it executes, so
		// the UI shows the current step pulsing immediately.
		results = append(results, StageResult{Type: s.Type, Env: s.Env, Label: label, Status: "running"})
		cur := &results[len(results)-1]
		emit()

		cw := &capWriter{cap: maxOutputBytes}
		// progressWriter flushes the in-flight stage's captured tail to the run
		// record at most ~once a second, so a long stage streams into the polled
		// log view instead of only appearing once it finishes.
		pw := &progressWriter{cap: cw, onFlush: func() { cur.Output = cw.String(); emit() }}
		mw := io.MultiWriter(out, pw)
		opts := StageRunOptions(p.Workspace, p.Project, s)
		opts.Stdout = mw
		opts.Stderr = mw

		start := time.Now()
		err := bridge.Run(opts)
		cur.MS = time.Since(start).Milliseconds()

		if err != nil {
			fmt.Fprintf(mw, "\n\033[31m✗ %s failed: %s\033[0m\n", label, err.Error())
			cur.Status = "fail"
			cur.Output = cw.String()
			outcome = OutcomeFail
			emit()
			if s.OnFailure != "continue" {
				for j := i + 1; j < len(p.Stages); j++ {
					sk := p.Stages[j]
					results = append(results, StageResult{
						Type: sk.Type, Env: sk.Env, Label: stageLabel(sk), Status: "skipped",
					})
				}
				fmt.Fprintf(out, "\n\033[33m■ Pipeline halted after stage %d (on_failure=stop).\033[0m\n", i+1)
				emit()
				return results, OutcomeFail
			}
			continue
		}

		fmt.Fprintf(mw, "\033[32m✓ %s ok\033[0m\n", label)
		cur.Status = "ok"
		cur.Output = cw.String()
		emit()
	}
	return results, outcome
}

// progressWriter wraps a capWriter and throttles a flush callback to ~once a
// second as bytes stream in, so the in-flight stage's captured output is
// published to the run record while it runs (not only at completion). It never
// returns a short write, so it is safe inside an io.MultiWriter.
type progressWriter struct {
	cap     *capWriter
	onFlush func()
	last    time.Time
}

func (w *progressWriter) Write(b []byte) (int, error) {
	n, _ := w.cap.Write(b)
	if time.Since(w.last) >= time.Second {
		w.last = time.Now()
		w.onFlush()
	}
	return n, nil
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
