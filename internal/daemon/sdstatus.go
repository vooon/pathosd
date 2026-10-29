package daemon

import (
	"context"
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
	// configStatus, when set, appends the config notice (failed reload,
	// edited file, pending verdict) to the line.
	configStatus func() model.ConfigStatus
}

// SetConfigStatus wires the reloader in once it exists.
func (r *statusReporter) SetConfigStatus(f func() model.ConfigStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configStatus = f
}

func provideStatusReporter(pol *policy.Manager, bgpMgr *bgp.Manager) *statusReporter {
	return &statusReporter{pol: pol, bgp: bgpMgr}
}

// Update recomputes the summary (model.Summarize) and sends it. Safe to
// call from any goroutine; it is a no-op without a notify socket.
func (r *statusReporter) Update() {
	if !systemd.NotifyAvailable() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	line := model.Summarize(r.pol.GetVIPStatuses(), r.bgp.GetPeerStates(ctx))
	if r.configStatus != nil {
		st := r.configStatus()
		if notice := model.ConfigNotice(&st); notice != "" {
			line += "; " + notice
		}
	}
	systemd.Status(line)
}
