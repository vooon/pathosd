package bgp

import (
	"context"
	"log/slog"
	"strings"

	api "github.com/osrg/gobgp/v3/api"
	"github.com/osrg/gobgp/v3/pkg/server"
	"github.com/vooon/pathosd/internal/metrics"
)

func WatchPeerState(ctx context.Context, s *server.BgpServer, m *metrics.Metrics, neighbors map[string]string) {
	err := s.WatchEvent(ctx, &api.WatchEventRequest{
		Peer: &api.WatchEventRequest_Peer{},
	}, func(r *api.WatchEventResponse) {
		if p := r.GetPeer(); p != nil {
			peer := p.GetPeer()
			if peer == nil {
				return
			}
			name, addr := resolvePeerIdentity(peer, neighbors)
			if name == "" {
				name = "unknown"
			}
			if addr == "" {
				addr = "unknown"
			}
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

func resolvePeerIdentity(peer *api.Peer, neighbors map[string]string) (name string, addr string) {
	if peer.GetConf() != nil {
		name = strings.TrimSpace(peer.GetConf().GetDescription())
		addr = strings.TrimSpace(peer.GetConf().GetNeighborAddress())
	}
	if peer.GetState() != nil {
		if addr == "" {
			addr = strings.TrimSpace(peer.GetState().GetNeighborAddress())
		}
		if name == "" {
			name = strings.TrimSpace(peer.GetState().GetDescription())
		}
	}
	if name == "" && addr != "" {
		if configuredName, ok := neighbors[addr]; ok {
			name = configuredName
		}
	}
	if name == "" {
		name = addr
	}
	return name, addr
}
