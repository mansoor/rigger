package api

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/mansoor/rigger/ui/internal/alerts"
	"github.com/mansoor/rigger/ui/internal/notify"
	"github.com/mansoor/rigger/ui/internal/settings"
)

// targetLabel renders a destination host id for job/notification text.
func (h *Handler) targetLabel(id int64) string {
	if id == 0 {
		return "local control plane"
	}
	if hh, _ := settings.GetHost(h.db, id); hh != nil {
		return hh.Name
	}
	return fmt.Sprintf("host #%d", id)
}

// MigrationJob tracks one async workspace/environment host move. The migration
// runs in a background goroutine so the user can leave the page; completion is
// announced via the in-app alert bell + notification channels.
type MigrationJob struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"` // "workspace" | "env"
	Workspace string     `json:"workspace"`
	Env       string     `json:"env,omitempty"`
	Target    string     `json:"target"` // human label of the destination host
	Status    string     `json:"status"` // running | completed | failed
	Log       string     `json:"log"`
	Error     string     `json:"error,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	DoneAt    *time.Time `json:"done_at,omitempty"`
}

// migStore holds migration jobs in memory.
type migStore struct {
	mu   sync.RWMutex
	jobs map[string]*MigrationJob
}

func newMigStore() *migStore { return &migStore{jobs: make(map[string]*MigrationJob)} }

func (s *migStore) create(kind, ws, env, target string) *MigrationJob {
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	j := &MigrationJob{ID: id, Kind: kind, Workspace: ws, Env: env, Target: target,
		Status: "running", StartedAt: time.Now()}
	s.mu.Lock()
	s.jobs[id] = j
	s.mu.Unlock()
	return j
}

// get returns a copy so the caller can serialize it without holding the lock.
func (s *migStore) get(id string) (MigrationJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return MigrationJob{}, false
	}
	return *j, true
}

func (s *migStore) appendLog(id, text string) {
	s.mu.Lock()
	if j, ok := s.jobs[id]; ok {
		j.Log += text
	}
	s.mu.Unlock()
}

func (s *migStore) finish(id, status, errMsg string) {
	s.mu.Lock()
	if j, ok := s.jobs[id]; ok {
		j.Status = status
		j.Error = errMsg
		now := time.Now()
		j.DoneAt = &now
	}
	s.mu.Unlock()
}

// migWriter streams a running migration's output into its job log.
type migWriter struct {
	store *migStore
	id    string
}

func (w migWriter) Write(p []byte) (int, error) {
	w.store.appendLog(w.id, string(p))
	return len(p), nil
}

// runMigration executes fn (which writes progress to the supplied writer) and,
// on completion, records the final status and announces it.
func (h *Handler) runMigration(job *MigrationJob, fn func(io.Writer) error) {
	err := fn(migWriter{store: h.migJobs, id: job.ID})
	status, errMsg := "completed", ""
	if err != nil {
		status, errMsg = "failed", err.Error()
	}
	h.migJobs.finish(job.ID, status, errMsg)
	h.announceMigration(job, err)
}

// announceMigration fires an in-app alert event (bell + SSE) and dispatches to
// every enabled notification channel when a migration finishes.
func (h *Handler) announceMigration(job *MigrationJob, runErr error) {
	what := job.Workspace
	if job.Env != "" {
		what += "/" + job.Env
	}

	var ev *alerts.Event
	var level string
	if runErr != nil {
		ev, _ = alerts.CreateEvent(h.db, alerts.Event{
			RuleName:      "Migration failed",
			ConditionType: "migration",
			Workspace:     job.Workspace,
			Env:           job.Env,
			Message:       fmt.Sprintf("Move of %s to %s failed: %s", what, job.Target, runErr.Error()),
			Severity:      alerts.SeverityCritical,
		})
		level = notify.LevelFailure
	} else {
		ev, _ = alerts.CreateEvent(h.db, alerts.Event{
			RuleName:      "Migration completed",
			ConditionType: "migration",
			Workspace:     job.Workspace,
			Env:           job.Env,
			Message:       fmt.Sprintf("Move of %s to %s completed.", what, job.Target),
			Severity:      alerts.SeverityInfo,
		})
		level = notify.LevelSuccess
	}
	if ev == nil {
		return
	}

	if h.alertBroker != nil {
		unread, _ := alerts.UnreadCount(h.db)
		h.alertBroker.Publish(alerts.BuildAlertSSE("fired", ev, unread))
	}
	if h.notifier != nil {
		chans, _ := notify.ListChannels(h.db)
		var ids []int64
		for _, c := range chans {
			if c.Enabled {
				ids = append(ids, c.ID)
			}
		}
		if len(ids) > 0 {
			h.notifier.DispatchToChannels(ids, notify.Notification{
				Title: ev.RuleName, Body: ev.Message, Level: level,
			})
		}
	}
}

// GET /api/migration-jobs/{id} — poll a migration job's status + log.
func (h *Handler) GetMigrationJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := h.migJobs.get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}
