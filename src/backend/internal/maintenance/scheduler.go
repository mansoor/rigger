package maintenance

import (
	"log"
	"time"
)

// HostResolver returns the public Host() value(s) for an env and whether it serves
// HTTPS (so the writer adds a websecure router). Supplied by the server wiring since
// it needs config.json + settings + custom domains (kept out of this package to avoid
// import cycles). Returns no hosts when the env isn't web-routed.
type HostResolver func(ws, project, env string) (hosts []string, ssl bool)

// Scheduler reconciles the on-disk Traefik maintenance fragments to each env's
// effective state every tick: it writes a fragment when a window opens, removes it
// when maintenance ends, and clears an expired window. Ad-hoc enable/disable is
// applied immediately by the API handler; this loop handles window transitions, a
// boot reconcile (so an active window survives a restart), and drift repair.
type Scheduler struct {
	store    *Store
	dir      string
	resolve  HostResolver
	interval time.Duration
}

func NewScheduler(s *Store, dir string, resolve HostResolver) *Scheduler {
	return &Scheduler{store: s, dir: dir, resolve: resolve, interval: time.Minute}
}

// Run starts the reconcile loop in a background goroutine (initial pass + ticker).
func (sc *Scheduler) Run() {
	go func() {
		sc.reconcile(time.Now())
		t := time.NewTicker(sc.interval)
		defer t.Stop()
		for range t.C {
			sc.reconcile(time.Now())
		}
	}()
}

func (sc *Scheduler) reconcile(now time.Time) {
	recs, err := sc.store.List()
	if err != nil {
		log.Printf("maintenance: list failed: %v", err)
		return
	}
	for _, r := range recs {
		// Expired window → clear it (maintenance ends). The manual `enabled` flag is left
		// untouched so a separate ad-hoc toggle isn't cancelled by a window ending.
		if r.WindowEnd > 0 && now.Unix() >= r.WindowEnd {
			r.WindowStart, r.WindowEnd = 0, 0
			r.UpdatedAt = now.Unix()
			if err := sc.store.Upsert(r); err != nil {
				log.Printf("maintenance: clear expired window %s/%s/%s: %v", r.Workspace, r.Project, r.Env, err)
			}
		}
		active := r.ActiveAt(now)
		exists := Exists(sc.dir, r.Workspace, r.Project, r.Env)
		switch {
		case active && !exists:
			hosts, ssl := sc.resolve(r.Workspace, r.Project, r.Env)
			if len(hosts) == 0 {
				log.Printf("maintenance: %s/%s/%s active but no host to route (not web-routed?)", r.Workspace, r.Project, r.Env)
				continue
			}
			if err := Apply(sc.dir, r.Workspace, r.Project, r.Env, hosts, ssl); err != nil {
				log.Printf("maintenance: apply %s/%s/%s: %v", r.Workspace, r.Project, r.Env, err)
			}
		case !active && exists:
			if err := Clear(sc.dir, r.Workspace, r.Project, r.Env); err != nil {
				log.Printf("maintenance: clear %s/%s/%s: %v", r.Workspace, r.Project, r.Env, err)
			}
		}
	}
}
