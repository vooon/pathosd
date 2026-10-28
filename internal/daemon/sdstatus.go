package daemon

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vooon/pathosd/internal/bgp"
	"github.com/vooon/pathosd/internal/model"
	"github.com/vooon/pathosd/internal/policy"
	"github.com/vooon/pathosd/internal/systemd"
)

// statusReporter pushes a one-line VIP/peer summary to systemd (STATUS=)
// whenever a VIP or peer session changes state.
type statusReporter struct {
	pol *policy.Manager
	bgp *bgp.Manager
	mu  sync.Mutex
}

func provideStatusReporter(pol *policy.Manager, bgpMgr *bgp.Manager) *statusReporter {
	return &statusReporter{pol: pol, bgp: bgpMgr}
}

// Update recomputes the summary and sends it. Safe to call from any
// goroutine; it is a no-op without a notify socket.
func (r *statusReporter) Update() {
	if !systemd.NotifyAvailable() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	systemd.Status(summarize(r.pol.GetVIPStatuses(), r.bgp.GetPeerStates(ctx)))
}

// summarize renders e.g.
// "VIPs: 2 announced, 1 pessimized, 0 withdrawn; peers: 1/2 established (down: bird)".
func summarize(vips []model.VIPStatus, peers []model.PeerStatus) string {
	var announced, pessimized, withdrawn int
	for _, v := range vips {
		switch v.State {
		case model.StateAnnounced:
			announced++
		case model.StatePessimized:
			pessimized++
		default:
			withdrawn++
		}
	}
	established := 0
	var down []string
	for _, p := range peers {
		if p.SessionState == "established" {
			established++
		} else {
			down = append(down, p.Name)
		}
	}
	sort.Strings(down)
	s := fmt.Sprintf("VIPs: %d announced, %d pessimized, %d withdrawn; peers: %d/%d established",
		announced, pessimized, withdrawn, established, len(peers))
	if len(down) > 0 {
		s += " (down: " + strings.Join(down, ", ") + ")"
	}
	return s
}
