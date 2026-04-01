package bgp

import (
	"context"
	"log/slog"

	api "github.com/osrg/gobgp/v3/api"
	"github.com/osrg/gobgp/v3/pkg/server"
	"github.com/vooon/pathosd/internal/metrics"
)

func WatchPeerState(ctx context.Context, s *server.BgpServer, m *metrics.Metrics, neighbors []string) {
	err := s.WatchEvent(ctx, &api.WatchEventRequest{
		Peer: &api.WatchEventRequest_Peer{},
	}, func(r *api.WatchEventResponse) {
		if p := r.GetPeer(); p != nil {
			peer := p.GetPeer()
			if peer == nil || peer.Conf == nil {
				return
			}
			addr := peer.Conf.NeighborAddress
			name := peer.Conf.Description
			state := "unknown"
			if peer.State != nil {
				state = peer.State.SessionState.String()
			}
			slog.Info("BGP peer state change", "name", name, "address", addr, "state", state)
		}
	})
	if err != nil {
		slog.Error("failed to watch BGP events", "error", err)
		return
	}

	<-ctx.Done()
}
