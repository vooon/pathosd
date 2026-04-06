package bgp

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/osrg/gobgp/v4/pkg/apiutil"
	"github.com/osrg/gobgp/v4/pkg/server"
	"github.com/vooon/pathosd/internal/metrics"
)

func WatchPeerState(ctx context.Context, s *server.BgpServer, m *metrics.Metrics, neighbors map[string]string) {
	err := s.WatchEvent(
		ctx,
		server.WatchEventMessageCallbacks{
			OnPeerUpdate: func(p *apiutil.WatchEventMessage_PeerEvent, _ time.Time) {
				if p == nil {
					return
				}
				name, addr := resolvePeerIdentity(&p.Peer, neighbors)
				if name == "" {
					name = "unknown"
				}
				if addr == "" {
					addr = "unknown"
				}
				state := strings.ToLower(p.Peer.State.SessionState.String())
				if state == "" {
					state = "unknown"
				}
				slog.Info("BGP peer state change", "name", name, "address", addr, "state", state)
			},
		},
		server.WatchPeer(),
	)
	if err != nil {
		slog.Error("failed to watch BGP events", "error", err)
		return
	}

	<-ctx.Done()
}

func resolvePeerIdentity(peer *apiutil.Peer, neighbors map[string]string) (name string, addr string) {
	if peer == nil {
		return "", ""
	}
	addr = strings.TrimSpace(peer.Conf.NeighborAddress.String())
	if addr == "invalid IP" {
		addr = strings.TrimSpace(peer.State.NeighborAddress.String())
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
