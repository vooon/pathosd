package bgp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	api "github.com/osrg/gobgp/v3/api"
	gobgpmetrics "github.com/osrg/gobgp/v3/pkg/metrics"
	"github.com/osrg/gobgp/v3/pkg/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/vooon/pathosd/internal/config"
	pathosmetrics "github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/model"
	"google.golang.org/protobuf/types/known/anypb"
)

type Manager struct {
	server              *server.BgpServer
	cfg                 *config.Config
	localASN            uint32
	fsmTimingsCollector gobgpmetrics.FSMTimingsCollector
	mu                  sync.Mutex
	installedRouteUUID  map[string][]byte
	metrics             *pathosmetrics.Metrics
	routeStateByPrefix  map[string]map[string]routeStateLabels
}

type routeUUID []byte

func NewManager(cfg *config.Config, metrics *pathosmetrics.Metrics) *Manager {
	gobgpLog := slog.Default().With("component", "gobgp")
	fsmTimingsCollector := gobgpmetrics.NewFSMTimingsCollector()
	s := server.NewBgpServer(
		server.LoggerOption(newGoBGPLogger(gobgpLog, cfg.Logging.Level)),
		server.TimingHookOption(fsmTimingsCollector),
	)
	return &Manager{
		server:              s,
		cfg:                 cfg,
		localASN:            cfg.Router.ASN,
		fsmTimingsCollector: fsmTimingsCollector,
		metrics:             metrics,
		installedRouteUUID:  make(map[string][]byte),
		routeStateByPrefix:  make(map[string]map[string]routeStateLabels),
	}
}

func (m *Manager) RegisterMetrics(reg prometheus.Registerer) error {
	collectors := []prometheus.Collector{
		gobgpmetrics.NewBgpCollector(m.server),
		m.fsmTimingsCollector,
	}
	for _, collector := range collectors {
		if err := reg.Register(collector); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); ok {
				continue
			}
			return fmt.Errorf("registering GoBGP collector: %w", err)
		}
	}
	return nil
}

func (m *Manager) Start(ctx context.Context) error {
	go m.server.Serve()

	req := &api.StartBgpRequest{
		Global: m.buildGlobalConfig(),
	}
	if err := m.server.StartBgp(ctx, req); err != nil {
		return fmt.Errorf("starting BGP: %w", err)
	}
	slog.Info("BGP server started", "asn", m.cfg.Router.ASN, "router_id", m.cfg.Router.RouterID)
	return nil
}

func (m *Manager) AddPeers(ctx context.Context) error {
	for _, n := range m.cfg.BGP.Neighbors {
		peer, err := m.buildPeer(n)
		if err != nil {
			return fmt.Errorf("building peer %s (%s): %w", n.Name, n.Address, err)
		}

		if err := m.server.AddPeer(ctx, &api.AddPeerRequest{Peer: peer}); err != nil {
			return fmt.Errorf("adding peer %s (%s): %w", n.Name, n.Address, err)
		}
		slog.Info("BGP peer added", "name", n.Name, "address", n.Address, "peer_asn", n.PeerASN)
	}
	return nil
}

func (m *Manager) buildGlobalConfig() *api.Global {
	listenPort := m.cfg.BGP.ListenPort
	if listenPort == 0 {
		listenPort = 179
	}

	return &api.Global{
		Asn:             m.cfg.Router.ASN,
		RouterId:        m.cfg.Router.RouterID,
		ListenPort:      int32(listenPort),
		ListenAddresses: []string{m.effectiveListenAddress()},
	}
}

func (m *Manager) effectiveListenAddress() string {
	if m.cfg.BGP.ListenAddress != "" {
		return m.cfg.BGP.ListenAddress
	}
	if m.cfg.Router.LocalAddress != "" {
		return m.cfg.Router.LocalAddress
	}
	return "0.0.0.0"
}

func (m *Manager) peerLocalAddress(n config.NeighborConfig) string {
	if n.LocalAddress != "" {
		return n.LocalAddress
	}
	return m.cfg.Router.LocalAddress
}

func (m *Manager) buildPeer(n config.NeighborConfig) (*api.Peer, error) {
	localAddress := m.peerLocalAddress(n)
	if !n.Passive && localAddress != "" && localAddress == n.Address {
		return nil, fmt.Errorf(
			"active peer local_address %q must differ from neighbor address %q to avoid self-dial",
			localAddress,
			n.Address,
		)
	}

	peer := &api.Peer{
		Conf: &api.PeerConf{
			NeighborAddress: n.Address,
			PeerAsn:         n.PeerASN,
			Description:     n.Name,
		},
		Transport: &api.Transport{
			LocalAddress: localAddress,
			RemotePort:   uint32(n.Port),
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

	return peer, nil
}

func (m *Manager) AnnounceVIP(prefix string) error {
	return m.upsertVIP(prefix, 0, nil, "announce")
}

func (m *Manager) WithdrawVIP(prefix string) error {
	nlri, attrs, err := m.buildPath(prefix, 0, nil)
	if err != nil {
		return err
	}
	path := &api.Path{
		Family: &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST},
		Nlri:   nlri,
		Pattrs: attrs,
	}

	uuid := m.installedUUID(prefix)

	if len(uuid) > 0 {
		err = m.server.DeletePath(context.Background(), &api.DeletePathRequest{
			Family: &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST},
			Uuid:   uuid,
		})
	} else {
		err = m.server.DeletePath(context.Background(), &api.DeletePathRequest{
			Path: path,
		})
	}
	if err != nil {
		return m.pathOpError("withdraw", prefix, err)
	}

	m.clearInstalledUUID(prefix)

	m.syncRouteStateMetric(context.Background(), prefix)
	slog.Info("BGP withdraw", "prefix", prefix)
	return nil
}

func (m *Manager) PessimizeVIP(prefix string, prepend int, communities []string) error {
	return m.upsertVIP(prefix, prepend, communities, "pessimize")
}

func (m *Manager) upsertVIP(prefix string, prepend int, communities []string, operation string) error {
	if prepend > 0 && m.hasIBGPPeer() {
		slog.Warn(
			"ignoring local ASN prepend for iBGP-originated route",
			"operation", operation,
			"prefix", prefix,
			"prepend", prepend,
		)
	}

	nlri, attrs, err := m.buildPath(prefix, 0, nil)
	if prepend > 0 || len(communities) > 0 {
		nlri, attrs, err = m.buildPath(prefix, prepend, communities)
	}
	if err != nil {
		return err
	}

	path := &api.Path{
		Family: &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST},
		Nlri:   nlri,
		Pattrs: attrs,
	}

	oldUUID := m.installedUUID(prefix)

	if len(oldUUID) > 0 {
		if err := m.server.DeletePath(context.Background(), &api.DeletePathRequest{
			Family: &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST},
			Uuid:   oldUUID,
		}); err != nil {
			return m.pathOpError(operation, prefix, fmt.Errorf("deleting previous path: %w", err))
		}
	}

	resp, err := m.server.AddPath(context.Background(), &api.AddPathRequest{
		Path: &api.Path{
			Family: &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST},
			Nlri:   path.Nlri,
			Pattrs: path.Pattrs,
		},
	})
	if err != nil {
		return m.pathOpError(operation, prefix, err)
	}

	m.storeInstalledUUID(prefix, resp.GetUuid())

	m.syncRouteStateMetric(context.Background(), prefix)
	if operation == "announce" {
		slog.Info("BGP announce", "prefix", prefix, "uuid", fmt.Sprintf("%x", resp.GetUuid()))
		return nil
	}
	slog.Info(
		"BGP pessimize",
		"prefix", prefix,
		"prepend", prepend,
		"communities", communities,
		"uuid", fmt.Sprintf("%x", resp.GetUuid()),
	)
	return nil
}

func (m *Manager) pathOpError(operation, prefix string, err error) error {
	peerCtx := m.peerContext(context.Background())
	slog.Error(
		"BGP route operation failed",
		"operation", operation,
		"prefix", prefix,
		"error", err,
		"peers", peerCtx,
	)
	return fmt.Errorf("%s prefix %q failed (peers: %s): %w", operation, prefix, peerCtx, err)
}

func (m *Manager) peerContext(ctx context.Context) string {
	peerStates := m.GetPeerStates(ctx)
	if len(peerStates) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(peerStates))
	for _, p := range peerStates {
		name := p.Name
		if name == "" {
			name = "unknown"
		}
		addr := p.Address
		if addr == "" {
			addr = "unknown"
		}
		parts = append(parts, fmt.Sprintf("%s(%s,asn=%d,state=%s)", name, addr, p.PeerASN, p.SessionState))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
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

	origin, err := anypb.New(&api.OriginAttribute{Origin: 0})
	if err != nil {
		return nil, nil, fmt.Errorf("building origin attribute: %w", err)
	}

	nh, err := anypb.New(&api.NextHopAttribute{NextHop: nextHop})
	if err != nil {
		return nil, nil, fmt.Errorf("building next-hop attribute: %w", err)
	}

	asPathAttr := &api.AsPathAttribute{}
	if !m.hasIBGPPeer() {
		asPathAttr.Segments = []*api.AsSegment{{
			Type:    api.AsSegment_AS_SEQUENCE,
			Numbers: buildASPath(m.localASN, prepend),
		}}
	}
	asPath, err := anypb.New(asPathAttr)
	if err != nil {
		return nil, nil, fmt.Errorf("building as-path attribute: %w", err)
	}

	attrs := []*anypb.Any{origin, nh, asPath}

	if len(communities) > 0 {
		comms, err := parseCommunities(communities)
		if err != nil {
			return nil, nil, err
		}
		commAttr, err := anypb.New(&api.CommunitiesAttribute{Communities: comms})
		if err != nil {
			return nil, nil, fmt.Errorf("building communities attribute: %w", err)
		}
		attrs = append(attrs, commAttr)
	}

	return nlri, attrs, nil
}

func (m *Manager) hasIBGPPeer() bool {
	for _, n := range m.cfg.BGP.Neighbors {
		if n.PeerASN == m.localASN {
			return true
		}
	}
	return false
}

func cloneUUID(uuid []byte) routeUUID {
	return routeUUID(slices.Clone(uuid))
}

func (m *Manager) installedUUID(prefix string) routeUUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneUUID(m.installedRouteUUID[prefix])
}

func (m *Manager) storeInstalledUUID(prefix string, uuid []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.installedRouteUUID[prefix] = cloneUUID(uuid)
}

func (m *Manager) clearInstalledUUID(prefix string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.installedRouteUUID, prefix)
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
