package alerts

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/imagecheck"
	"github.com/mansoor/rigger/ui/internal/notify"
	"github.com/mansoor/rigger/ui/internal/stats"
	"github.com/mansoor/rigger/ui/internal/workspace"
)

// evalInterval is how often the evaluator re-checks every enabled rule.
const evalInterval = 60 * time.Second

// Evaluator runs the rule loop: read enabled rules → compute each rule's metric
// for its target(s) → open or auto-resolve alert events, broadcasting changes
// over the SSE broker. It owns no state beyond its dependencies; all alert
// state lives in the DB so it survives restarts.
type Evaluator struct {
	db            *db.DB
	workspacesDir string
	imgCache      *imagecheck.Cache
	broker        *Broker
	notifier      *notify.Dispatcher
}

func NewEvaluator(d *db.DB, workspacesDir string, imgCache *imagecheck.Cache, broker *Broker, notifier *notify.Dispatcher) *Evaluator {
	return &Evaluator{db: d, workspacesDir: workspacesDir, imgCache: imgCache, broker: broker, notifier: notifier}
}

// Run starts the evaluator loop in a background goroutine.
func (e *Evaluator) Run() {
	go func() {
		// Small initial delay so the first tick doesn't race container startup.
		time.Sleep(10 * time.Second)
		e.evaluate()
		ticker := time.NewTicker(evalInterval)
		defer ticker.Stop()
		for range ticker.C {
			e.evaluate()
		}
	}()
}

// target is one concrete thing a rule is evaluated against.
type target struct {
	ws          string // workspace (display) name — "" for host-global checks
	env         string
	project     string // compose project name: {project}_{env}
	composePath string
}

func (e *Evaluator) evaluate() {
	rules, err := ListEnabledRules(e.db)
	if err != nil {
		log.Printf("alerts: list rules: %v", err)
		return
	}
	if len(rules) == 0 {
		return
	}

	wss, err := workspace.List(e.workspacesDir)
	if err != nil {
		log.Printf("alerts: list workspaces: %v", err)
	}

	// Lazily-collected shared metrics (only sampled if a rule needs them).
	var projStats map[string]stats.ProjectStats
	statsOf := func(project string) stats.ProjectStats {
		if projStats == nil {
			projStats = stats.ContainerStatsByProject()
		}
		return projStats[project]
	}

	for _, rule := range rules {
		for _, t := range e.expandTargets(rule, wss) {
			met, value, msg := e.check(rule, t, statsOf)
			e.apply(rule, t, met, value, msg)
		}
	}
}

// expandTargets turns a rule's targeting into the concrete checks to run.
func (e *Evaluator) expandTargets(rule Rule, wss []workspace.Workspace) []target {
	// Disk is a host-global metric — evaluate once, labelled by the rule.
	if rule.ConditionType == CondDiskAbovePct {
		return []target{{ws: rule.Workspace, env: rule.Env}}
	}

	var out []target
	for _, w := range wss {
		if rule.Workspace != "" && w.Name != rule.Workspace {
			continue
		}
		proj := w.Config.Project.Name
		if proj == "" {
			proj = w.Name
		}
		for _, env := range w.Envs {
			if rule.Env != "" && env != rule.Env {
				continue
			}
			out = append(out, target{
				ws:          w.Name,
				env:         env,
				project:     proj + "_" + env,
				composePath: filepath.Join(e.workspacesDir, w.Name, "envs", env, "docker-compose.yml"),
			})
		}
	}
	return out
}

// check evaluates one rule against one target, returning whether the condition
// is currently met, the numeric value that drove it, and a human message.
func (e *Evaluator) check(rule Rule, t target, statsOf func(string) stats.ProjectStats) (bool, float64, string) {
	switch rule.ConditionType {

	case CondContainerDown:
		ctrs := projectContainers(t.project, t.composePath)
		if len(ctrs) == 0 {
			return false, 0, "" // never deployed — not "down"
		}
		var down []string
		for _, c := range ctrs {
			if isDownState(c.State) {
				down = append(down, c.Service)
			}
		}
		if len(down) == 0 {
			return false, 0, ""
		}
		return true, float64(len(down)), fmt.Sprintf("%s/%s: %d container(s) down (%s)",
			t.ws, t.env, len(down), joinUpTo(down, 4))

	case CondRestartCount:
		ctrs := projectContainers(t.project, t.composePath)
		if len(ctrs) == 0 {
			return false, 0, ""
		}
		ids := make([]string, 0, len(ctrs))
		for _, c := range ctrs {
			if c.ID != "" {
				ids = append(ids, c.ID)
			}
		}
		n, name := maxRestartCount(ids)
		if float64(n) < rule.Threshold {
			return false, float64(n), ""
		}
		return true, float64(n), fmt.Sprintf("%s/%s: %s restarted %d times (threshold %g)",
			t.ws, t.env, name, n, rule.Threshold)

	case CondDiskAbovePct:
		pct := stats.Host().DiskUsedPct
		if pct < rule.Threshold {
			return false, pct, ""
		}
		return true, pct, fmt.Sprintf("Host disk usage %.1f%% (threshold %g%%)", pct, rule.Threshold)

	case CondCPUAbovePct:
		ps := statsOf(t.project)
		if ps.Containers == 0 || ps.CPUPct < rule.Threshold {
			return false, ps.CPUPct, ""
		}
		return true, ps.CPUPct, fmt.Sprintf("%s/%s: CPU %.1f%% (threshold %g%%)",
			t.ws, t.env, ps.CPUPct, rule.Threshold)

	case CondMemoryAbovePct:
		ps := statsOf(t.project)
		if ps.Containers == 0 || ps.MemPct < rule.Threshold {
			return false, ps.MemPct, ""
		}
		return true, ps.MemPct, fmt.Sprintf("%s/%s: memory %.1f%% of host (threshold %g%%)",
			t.ws, t.env, ps.MemPct, rule.Threshold)

	case CondBackupFailed:
		status, _, ok, err := LatestBackupStatus(e.db, t.ws, t.env)
		if err != nil || !ok || status != "error" {
			return false, 0, ""
		}
		return true, 1, fmt.Sprintf("%s/%s: last backup failed", t.ws, t.env)

	case CondImageUpdate:
		entry, ok := e.imgCache.Get(t.ws, t.env)
		if !ok {
			return false, 0, ""
		}
		var names []string
		for _, u := range entry.Results {
			if u.HasUpdate {
				names = append(names, u.Service)
			}
		}
		if len(names) == 0 {
			return false, 0, ""
		}
		return true, float64(len(names)), fmt.Sprintf("%s/%s: %d image update(s) available (%s)",
			t.ws, t.env, len(names), joinUpTo(names, 4))
	}

	return false, 0, ""
}

// apply is the open/resolve state machine for one rule+target.
func (e *Evaluator) apply(rule Rule, t target, met bool, value float64, msg string) {
	open, err := OpenEventFor(e.db, rule.ID, t.ws, t.env)
	if err != nil {
		log.Printf("alerts: open lookup: %v", err)
		return
	}

	if met {
		if open != nil {
			return // already firing — don't duplicate
		}
		// Respect the cooldown window since the last fire for this rule+target.
		if last, _ := LastEventFor(e.db, rule.ID, t.ws, t.env); last != nil {
			if time.Since(last.FiredAt) < time.Duration(rule.CooldownMinutes)*time.Minute {
				return
			}
		}
		ev, err := CreateEvent(e.db, Event{
			RuleID:        &rule.ID,
			RuleName:      rule.Name,
			ConditionType: rule.ConditionType,
			Workspace:     t.ws,
			Env:           t.env,
			Message:       msg,
			Severity:      rule.Severity,
			Value:         value,
		})
		if err != nil {
			log.Printf("alerts: create event: %v", err)
			return
		}
		e.publish("fired", ev)
		e.notifyChannels(rule, ev, false)
		return
	}

	// Condition not met — auto-resolve any open event.
	if open != nil {
		if err := ResolveEvent(e.db, open.ID); err != nil {
			log.Printf("alerts: resolve event: %v", err)
			return
		}
		if fresh, _ := GetEvent(e.db, open.ID); fresh != nil {
			e.publish("resolved", fresh)
			e.notifyChannels(rule, fresh, true)
		}
	}
}

// notifyChannels delivers an alert (fired or resolved) to the rule's assigned
// notification channels. No-op when there are no channels or no dispatcher.
func (e *Evaluator) notifyChannels(rule Rule, ev *Event, resolved bool) {
	if e.notifier == nil || len(rule.NotifyChannelIDs) == 0 || ev == nil {
		return
	}
	var n notify.Notification
	if resolved {
		n = notify.Notification{
			Title: "Rigger resolved: " + rule.Name,
			Body:  "✓ Resolved — " + ev.Message,
			Level: notify.LevelSuccess,
		}
	} else {
		n = notify.Notification{
			Title: "Rigger alert: " + rule.Name,
			Body:  ev.Message,
			Level: severityLevel(rule.Severity),
		}
	}
	e.notifier.DispatchToChannels(rule.NotifyChannelIDs, n)
}

// severityLevel maps an alert severity to a notification delivery level.
func severityLevel(severity string) string {
	switch severity {
	case SeverityCritical:
		return notify.LevelFailure
	case SeverityInfo:
		return notify.LevelInfo
	default:
		return notify.LevelWarning
	}
}

// publish pushes an alert change to all SSE subscribers via the broker.
func (e *Evaluator) publish(action string, ev *Event) {
	if e.broker == nil || ev == nil {
		return
	}
	unread, _ := UnreadCount(e.db)
	e.broker.Publish(BuildAlertSSE(action, ev, unread))
}

// BuildAlertSSE marshals an alert change into the SSE `alert` event payload.
// Shared so api handlers (dismiss) can broadcast badge updates too.
func BuildAlertSSE(action string, ev *Event, unread int) []byte {
	payload, _ := json.Marshal(map[string]any{
		"action":       action, // "fired" | "resolved" | "dismissed"
		"event":        ev,
		"unread_count": unread,
	})
	return payload
}

func joinUpTo(items []string, n int) string {
	if len(items) <= n {
		return join(items)
	}
	return join(items[:n]) + fmt.Sprintf(" +%d more", len(items)-n)
}

func join(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
