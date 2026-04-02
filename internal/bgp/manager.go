package bgp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"

	api "github.com/osrg/gobgp/v3/api"
	"github.com/osrg/gobgp/v3/pkg/server"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
	"google.golang.org/protobuf/types/known/anypb"
)

type Manager struct {
	server   *server.BgpServer
	cfg      *config.Config
	localASN uint32
}

func NewManager(cfg *config.Config) *Manager {
	gobgpLog := slog.Default().With("component", "gobgp")
	s := server.NewBgpServer(server.LoggerOption(newGoBGPLogger(gobgpLog, cfg.Logging.Level)))
	return &Manager{server: s, cfg: cfg, localASN: cfg.Router.ASN}
}

func (m *Manager) Start(ctx context.Context) error {
	go m.server.Serve()

	localAddr := m.cfg.Router.LocalAddress
	if localAddr == "" {
		localAddr = m.cfg.Router.RouterID
	}

	req := &api.StartBgpRequest{
		Global: &api.Global{
			Asn:             m.cfg.Router.ASN,
			RouterId:        m.cfg.Router.RouterID,
			ListenPort:      -1,
			ListenAddresses: []string{localAddr},
		},
	}
	if err := m.server.StartBgp(ctx, req); err != nil {
		return fmt.Errorf("starting BGP: %w", err)
	}
	slog.Info("BGP server started", "asn", m.cfg.Router.ASN, "router_id", m.cfg.Router.RouterID)
	return nil
}

func (m *Manager) AddPeers(ctx context.Context) error {
	for _, n := range m.cfg.BGP.Neighbors {
		peer := &api.Peer{
			Conf: &api.PeerConf{
				NeighborAddress: n.Address,
				PeerAsn:         n.PeerASN,
				Description:     n.Name,
			},
			Transport: &api.Transport{
				RemotePort: uint32(n.Port),
			},
			AfiSafis: []*api.AfiSafi{{
				Config: &api.AfiSafiConfig{
					Family:  &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST},
					Enabled: true,
				},
			}},
		}

		if n.Passive {
			peer.Transport.PassiveMode = true
		}

		if m.cfg.BGP.GracefulRestart != nil && *m.cfg.BGP.GracefulRestart {
			peer.GracefulRestart = &api.GracefulRestart{Enabled: true}
		}

		if m.cfg.BGP.HoldTime != nil {
			peer.Timers = &api.Timers{
				Config: &api.TimersConfig{
					HoldTime: uint64(m.cfg.BGP.HoldTime.Seconds()),
				},
			}
			if m.cfg.BGP.KeepaliveTime != nil {
				peer.Timers.Config.KeepaliveInterval = uint64(m.cfg.BGP.KeepaliveTime.Seconds())
			}
		}

		if err := m.server.AddPeer(ctx, &api.AddPeerRequest{Peer: peer}); err != nil {
			return fmt.Errorf("adding peer %s (%s): %w", n.Name, n.Address, err)
		}
		slog.Info("BGP peer added", "name", n.Name, "address", n.Address, "peer_asn", n.PeerASN)
	}
	return nil
}

func (m *Manager) AnnounceVIP(prefix string) error {
	nlri, attrs, err := m.buildPath(prefix, 0, nil)
	if err != nil {
		return err
	}
	_, err = m.server.AddPath(context.Background(), &api.AddPathRequest{
		Path: &api.Path{
			Family: &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST},
			Nlri:   nlri,
			Pattrs: attrs,
		},
	})
	if err != nil {
		return fmt.Errorf("announcing %s: %w", prefix, err)
	}
	slog.Info("BGP announce", "prefix", prefix)
	return nil
}

func (m *Manager) WithdrawVIP(prefix string) error {
	nlri, attrs, err := m.buildPath(prefix, 0, nil)
	if err != nil {
		return err
	}
	err = m.server.DeletePath(context.Background(), &api.DeletePathRequest{
		Path: &api.Path{
			Family: &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST},
			Nlri:   nlri,
			Pattrs: attrs,
		},
	})
	if err != nil {
		return fmt.Errorf("withdrawing %s: %w", prefix, err)
	}
	slog.Info("BGP withdraw", "prefix", prefix)
	return nil
}

func (m *Manager) PessimizeVIP(prefix string, prepend int, communities []string) error {
	nlri, attrs, err := m.buildPath(prefix, prepend, communities)
	if err != nil {
		return err
	}
	_, err = m.server.AddPath(context.Background(), &api.AddPathRequest{
		Path: &api.Path{
			Family: &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST},
			Nlri:   nlri,
			Pattrs: attrs,
		},
	})
	if err != nil {
		return fmt.Errorf("pessimizing %s: %w", prefix, err)
	}
	slog.Info("BGP pessimize", "prefix", prefix, "prepend", prepend, "communities", communities)
	return nil
}

func (m *Manager) GetPeerStates(ctx context.Context) []model.PeerStatus {
	var peers []model.PeerStatus
	if err := m.server.ListPeer(ctx, &api.ListPeerRequest{}, func(p *api.Peer) {
		state := "unknown"
		if p.State != nil {
			state = strings.ToLower(p.State.SessionState.String())
		}
		name := ""
		addr := ""
		peerASN := uint32(0)
		if p.Conf != nil {
			name = p.Conf.Description
			addr = p.Conf.NeighborAddress
			peerASN = p.Conf.PeerAsn
		}
		required := true
		for _, n := range m.cfg.BGP.Neighbors {
			if n.Address == addr {
				if n.Required != nil {
					required = *n.Required
				}
				if name == "" {
					name = n.Name
				}
				break
			}
		}
		peers = append(peers, model.PeerStatus{
			Name: name, Address: addr, PeerASN: peerASN,
			SessionState: state, Required: required,
		})
	}); err != nil {
		slog.Error("failed to list BGP peers", "error", err)
	}
	return peers
}

func (m *Manager) Stop(ctx context.Context) {
	m.server.Stop()
	slog.Info("BGP server stopped")
}

func (m *Manager) Server() *server.BgpServer { return m.server }

func (m *Manager) buildPath(prefix string, prepend int, communities []string) (*anypb.Any, []*anypb.Any, error) {
	ip, ipNet, err := net.ParseCIDR(prefix)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid prefix %q: %w", prefix, err)
	}
	prefixLen, _ := ipNet.Mask.Size()

	nlri, err := anypb.New(&api.IPAddressPrefix{PrefixLen: uint32(prefixLen), Prefix: ip.String()})
	if err != nil {
		return nil, nil, err
	}

	nextHop := m.cfg.Router.LocalAddress
	if nextHop == "" {
		nextHop = m.cfg.Router.RouterID
	}

	origin, _ := anypb.New(&api.OriginAttribute{Origin: 0})
	nh, _ := anypb.New(&api.NextHopAttribute{NextHop: nextHop})
	asPath, _ := anypb.New(&api.AsPathAttribute{
		Segments: []*api.AsSegment{{
			Type:    api.AsSegment_AS_SEQUENCE,
			Numbers: buildASPath(m.localASN, prepend),
		}},
	})

	attrs := []*anypb.Any{origin, nh, asPath}

	if len(communities) > 0 {
		comms, err := parseCommunities(communities)
		if err != nil {
			return nil, nil, err
		}
		commAttr, _ := anypb.New(&api.CommunitiesAttribute{Communities: comms})
		attrs = append(attrs, commAttr)
	}

	return nlri, attrs, nil
}

func buildASPath(localASN uint32, prepend int) []uint32 {
	count := 1
	if prepend > 0 {
		count = prepend
	}
	path := make([]uint32, count)
	for i := range path {
		path[i] = localASN
	}
	return path
}

func parseCommunities(strs []string) ([]uint32, error) {
	comms := make([]uint32, 0, len(strs))
	for _, s := range strs {
		parts := strings.SplitN(s, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid community %q", s)
		}
		high, err := strconv.ParseUint(parts[0], 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid community %q: %w", s, err)
		}
		low, err := strconv.ParseUint(parts[1], 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid community %q: %w", s, err)
		}
		comms = append(comms, uint32(high<<16|low))
	}
	return comms, nil
}
