package api

// Supplying the alert evaluator with fleet-wide disk usage.
//
// internal/alerts must not reach a remote host itself: doing so means SSH, which
// means settings and remotehost, which import back towards alerts. So the
// evaluator declares what it needs (alerts.HostDiskSampler) and this file — which
// already has the credentials and the dialer — provides it.
//
// The control plane is always sampled and always first; a fleet whose hosts are
// all unreachable still reports the machine Rigger is running on.

import (
	"log"
	"sync"

	"github.com/mansoor/rigger/ui/internal/alerts"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/stats"
)

// maxDiskSampleConcurrency bounds how many hosts are dialled at once. A fleet
// sample happens every few minutes and is never on a request path, so this is
// about not opening thirty SSH connections in the same instant, not speed.
const maxDiskSampleConcurrency = 4

// SampleHostDisk reports disk usage for the control plane and every registered
// host. Unreachable hosts come back with Reachable false rather than 0%, which
// the evaluator treats as "don't know" — never as "healthy".
func (h *Handler) SampleHostDisk() []alerts.HostDisk {
	out := []alerts.HostDisk{{
		// "" is the control plane, matching the host column on events recorded
		// before alerts could see more than one machine.
		Host: "", UsedPct: stats.Host().DiskUsedPct, Reachable: true,
	}}

	hosts, err := settings.ListHosts(h.db)
	if err != nil {
		log.Printf("alerts: list hosts for disk sample: %v", err)
		return out
	}
	if len(hosts) == 0 {
		return out
	}

	results := make([]alerts.HostDisk, len(hosts))
	sem := make(chan struct{}, maxDiskSampleConcurrency)
	var wg sync.WaitGroup
	for i, host := range hosts {
		wg.Add(1)
		go func(i int, host settings.Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = h.sampleOneHostDisk(host)
		}(i, host)
	}
	wg.Wait()

	return append(out, results...)
}

// sampleOneHostDisk dials a host and reads its disk usage. Every failure path —
// no credentials, host down, unparseable df — lands on Reachable false, because
// the only honest thing to say about a host we couldn't read is nothing.
func (h *Handler) sampleOneHostDisk(host settings.Host) alerts.HostDisk {
	s := alerts.HostDisk{Host: host.Name}
	rh, err := h.dialHost(host.ID)
	if err != nil {
		// Expected in normal operation — a laptop that's closed, a box being
		// rebooted. Not logged per-sample: at a five-minute cadence that would
		// fill the log with something the Hosts page already shows.
		return s
	}
	defer rh.Close()

	_, hs := stats.CollectRemote(rh)
	if hs.DiskTotalGB <= 0 {
		// Reached the host but couldn't read a filesystem — df missing, or output
		// in a shape the parser doesn't know. Also "don't know".
		return s
	}
	s.UsedPct, s.Reachable = hs.DiskUsedPct, true
	return s
}
