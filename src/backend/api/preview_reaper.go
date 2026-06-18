package api

import (
	"fmt"
	"os"
	"time"

	"github.com/mansoor/rigger/ui/internal/previews"
)

const previewReaperTick = 30 * time.Minute

// StartPreviewReaper tears down preview environments whose TTL has elapsed. It is
// the safety net for PRs whose "closed" webhook was missed or never delivered
// (design §4.6) and the enforcer of the per-project TTLHours inactivity window.
// It ticks every 30 min and reaps rows with expires_at in (0, now]; a preview with
// no TTL (expires_at 0) lives until its PR closes and is never reaped here. The
// sliding window is refreshed on each deploy (create/update set expires_at = now +
// TTLHours), so an actively-pushed PR never expires out from under a reviewer.
func (h *Handler) StartPreviewReaper() {
	go func() {
		time.Sleep(90 * time.Second) // settle after startup (offset from the backup scheduler's 60s)
		h.reapExpiredPreviews(time.Now().Unix())
		ticker := time.NewTicker(previewReaperTick)
		defer ticker.Stop()
		for range ticker.C {
			h.reapExpiredPreviews(time.Now().Unix())
		}
	}()
}

// reapExpiredPreviews tears down every preview past its TTL as of now (epoch secs).
func (h *Handler) reapExpiredPreviews(now int64) {
	expired, err := previews.ListExpired(h.db, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "preview reaper: list expired: %v\n", err)
		return
	}
	for i := range expired {
		p := expired[i]
		fmt.Fprintf(os.Stderr, "preview reaper: tearing down %s/%s/%s (PR#%d, TTL elapsed)\n",
			p.Workspace, p.Project, p.EnvKey, p.PRNumber)
		if terr := h.teardownPreview(p.Workspace, p.Project, &p); terr != nil {
			fmt.Fprintf(os.Stderr, "preview reaper: teardown %s/%s/%s: %v\n",
				p.Workspace, p.Project, p.EnvKey, terr)
		}
	}
}
