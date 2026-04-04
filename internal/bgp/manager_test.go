package bgp

import (
	"context"
	"net"
	"testing"
	"time"

	api "github.com/osrg/gobgp/v3/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/metrics"
	"google.golang.org/protobuf/types/known/anypb"
)

type decodedPathAttrs struct {
	origin      *api.OriginAttribute
	nextHop     *api.NextHopAttribute
	asPath      *api.AsPathAttribute
	communities *api.CommunitiesAttribute
}

func decodePathAttrs(t *testing.T, attrs []*anypb.Any) decodedPathAttrs {
	t.Helper()

	out := decodedPathAttrs{}
	for _, attr := range attrs {
		switch {
		case attr.MessageIs(&api.OriginAttribute{}):
			v := &api.OriginAttribute{}
			require.NoError(t, attr.UnmarshalTo(v))
			out.origin = v
		case attr.MessageIs(&api.NextHopAttribute{}):
			v := &api.NextHopAttribute{}
			require.NoError(t, attr.UnmarshalTo(v))
			out.nextHop = v
		case attr.MessageIs(&api.AsPathAttribute{}):
			v := &api.AsPathAttribute{}
			require.NoError(t, attr.UnmarshalTo(v))
			out.asPath = v
		case attr.MessageIs(&api.CommunitiesAttribute{}):
			v := &api.CommunitiesAttribute{}
			require.NoError(t, attr.UnmarshalTo(v))
			out.communities = v
		}
	}

	return out
}

func repeatedASN(asn uint32, n int) []uint32 {
	path := make([]uint32, n)
	for i := range path {
		path[i] = asn
	}
	return path
}

func TestBuildASPath(t *testing.T) {
	const localASN uint32 = 65000

	tests := []struct {
		name    string
		prepend int
		want    []uint32
	}{
		{
			name:    "prepend zero returns single ASN",
			prepend: 0,
			want:    []uint32{localASN},
		},
		{
			name:    "prepend one returns single ASN",
			prepend: 1,
			want:    []uint32{localASN},
		},
		{
			name:    "prepend three returns three ASNs",
			prepend: 3,
			want:    []uint32{localASN, localASN, localASN},
		},
		{
			name:    "prepend six returns six ASNs",
			prepend: 6,
			want:    repeatedASN(localASN, 6),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildASPath(localASN, tc.prepend)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseCommunities(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []uint32
		wantErr bool
	}{
		{
			name: "single valid community",
			in:   []string{"65535:666"},
			want: []uint32{0xFFFF029A},
		},
		{
			name: "multiple valid communities",
			in:   []string{"100:200", "300:400"},
			want: []uint32{(100 << 16) | 200, (300 << 16) | 400},
		},
		{
			name:    "invalid missing colon",
			in:      []string{"bad"},
			wantErr: true,
		},
		{
			name:    "invalid high greater than uint16",
			in:      []string{"99999:1"},
			wantErr: true,
		},
		{
			name:    "invalid low greater than uint16",
			in:      []string{"1:99999"},
			wantErr: true,
		},
		{
			name: "empty slice returns empty result",
			in:   []string{},
			want: []uint32{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCommunities(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestManagerBuildPath(t *testing.T) {
	newManager := func(localAddress string) *Manager {
		return &Manager{
			localASN: 65000,
			cfg: &config.Config{
				Router: config.RouterConfig{
					ASN:          65000,
					RouterID:     "10.0.0.1",
					LocalAddress: localAddress,
				},
			},
		}
	}

	tests := []struct {
		name            string
		manager         *Manager
		prefix          string
		prepend         int
		communities     []string
		wantErr         bool
		wantNextHop     string
		wantASPath      []uint32
		wantCommunities []uint32
	}{
		{
			name:        "valid /32 returns nlri and required attrs",
			manager:     newManager(""),
			prefix:      "10.1.0.1/32",
			wantNextHop: "10.0.0.1",
			wantASPath:  []uint32{65000},
		},
		{
			name:        "prepend adds repeated ASNs",
			manager:     newManager(""),
			prefix:      "10.1.0.2/32",
			prepend:     4,
			wantNextHop: "10.0.0.1",
			wantASPath:  repeatedASN(65000, 4),
		},
		{
			name:            "communities add communities attribute",
			manager:         newManager(""),
			prefix:          "10.1.0.3/32",
			communities:     []string{"65535:666"},
			wantNextHop:     "10.0.0.1",
			wantASPath:      []uint32{65000},
			wantCommunities: []uint32{0xFFFF029A},
		},
		{
			name:    "invalid prefix returns error",
			manager: newManager(""),
			prefix:  "not-a-prefix",
			wantErr: true,
		},
		{
			name:        "next hop uses local address when set",
			manager:     newManager("10.0.0.2"),
			prefix:      "10.1.0.4/32",
			wantNextHop: "10.0.0.2",
			wantASPath:  []uint32{65000},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nlri, attrs, err := tc.manager.buildPath(tc.prefix, tc.prepend, tc.communities)
			if tc.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, nlri)
			require.NotEmpty(t, attrs)

			prefix := &api.IPAddressPrefix{}
			require.NoError(t, nlri.UnmarshalTo(prefix))
			assert.Equal(t, uint32(32), prefix.PrefixLen)

			decoded := decodePathAttrs(t, attrs)
			require.NotNil(t, decoded.origin)
			assert.Equal(t, uint32(0), decoded.origin.Origin)
			require.NotNil(t, decoded.nextHop)
			assert.Equal(t, tc.wantNextHop, decoded.nextHop.NextHop)
			require.NotNil(t, decoded.asPath)
			require.Len(t, decoded.asPath.Segments, 1)
			assert.Equal(t, tc.wantASPath, decoded.asPath.Segments[0].Numbers)

			if len(tc.wantCommunities) > 0 {
				require.NotNil(t, decoded.communities)
				assert.Equal(t, tc.wantCommunities, decoded.communities.Communities)
				return
			}
			assert.Nil(t, decoded.communities)
		})
	}

	t.Run("iBGP peers keep local AS out of AS_PATH for valid same-AS export", func(t *testing.T) {
		manager := &Manager{
			localASN: 65000,
			cfg: &config.Config{
				Router: config.RouterConfig{
					ASN:      65000,
					RouterID: "10.0.0.1",
				},
				BGP: config.BGPConfig{
					Neighbors: []config.NeighborConfig{
						{Name: "ibgp-peer", Address: "10.0.0.2", PeerASN: 65000, Port: 179},
					},
				},
			},
		}

		_, attrs, err := manager.buildPath("10.1.0.99/32", 4, []string{"65535:666"})
		require.NoError(t, err)
		decoded := decodePathAttrs(t, attrs)
		require.NotNil(t, decoded.asPath)
		assert.Empty(t, decoded.asPath.Segments)
		require.NotNil(t, decoded.communities)
		assert.Equal(t, []uint32{0xFFFF029A}, decoded.communities.Communities)
	})
}

func TestManagerBuildGlobalConfig(t *testing.T) {
	t.Run("uses configured listen address and port", func(t *testing.T) {
		m := &Manager{
			cfg: &config.Config{
				Router: config.RouterConfig{
					ASN:      65000,
					RouterID: "10.0.0.1",
				},
				BGP: config.BGPConfig{
					ListenAddress: "127.0.0.1",
					ListenPort:    1179,
				},
			},
		}

		global := m.buildGlobalConfig()
		assert.Equal(t, int32(1179), global.ListenPort)
		assert.Equal(t, []string{"127.0.0.1"}, global.ListenAddresses)
	})

	t.Run("falls back to router.local_address and default listen port", func(t *testing.T) {
		m := &Manager{
			cfg: &config.Config{
				Router: config.RouterConfig{
					ASN:          65000,
					RouterID:     "10.0.0.1",
					LocalAddress: "127.0.0.2",
				},
			},
		}

		global := m.buildGlobalConfig()
		assert.Equal(t, int32(179), global.ListenPort)
		assert.Equal(t, []string{"127.0.0.2"}, global.ListenAddresses)
	})

	t.Run("falls back to wildcard listen address when no local_address", func(t *testing.T) {
		m := &Manager{
			cfg: &config.Config{
				Router: config.RouterConfig{
					ASN:      65000,
					RouterID: "10.0.0.1",
				},
			},
		}

		global := m.buildGlobalConfig()
		assert.Equal(t, []string{"0.0.0.0"}, global.ListenAddresses)
	})
}

func TestManagerBuildPeer(t *testing.T) {
	newManager := func(routerLocalAddress string) *Manager {
		graceful := true
		return &Manager{
			cfg: &config.Config{
				Router: config.RouterConfig{
					ASN:          65000,
					RouterID:     "10.0.0.1",
					LocalAddress: routerLocalAddress,
				},
				BGP: config.BGPConfig{
					GracefulRestart: &graceful,
				},
			},
		}
	}

	t.Run("passive peer keeps passive mode and local bind fallback", func(t *testing.T) {
		m := newManager("127.0.0.1")
		peer, err := m.buildPeer(config.NeighborConfig{
			Name:    "frr",
			Address: "127.0.0.2",
			PeerASN: 65002,
			Port:    179,
			Passive: true,
		})
		require.NoError(t, err)
		require.NotNil(t, peer.Transport)
		assert.True(t, peer.Transport.PassiveMode)
		assert.Equal(t, "127.0.0.1", peer.Transport.LocalAddress)
		assert.Equal(t, uint32(179), peer.Transport.RemotePort)
	})

	t.Run("active peer uses neighbor local_address override", func(t *testing.T) {
		m := newManager("127.0.0.1")
		peer, err := m.buildPeer(config.NeighborConfig{
			Name:         "frr",
			Address:      "127.0.0.2",
			PeerASN:      65002,
			Port:         179,
			LocalAddress: "127.0.0.3",
		})
		require.NoError(t, err)
		require.NotNil(t, peer.Transport)
		assert.False(t, peer.Transport.PassiveMode)
		assert.Equal(t, "127.0.0.3", peer.Transport.LocalAddress)
		assert.Equal(t, uint32(179), peer.Transport.RemotePort)
	})

	t.Run("active localhost self-endpoint is rejected", func(t *testing.T) {
		m := newManager("127.0.0.2")
		_, err := m.buildPeer(config.NeighborConfig{
			Name:    "loop",
			Address: "127.0.0.2",
			PeerASN: 65002,
			Port:    179,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must differ from neighbor address")
	})
}

func startTestBGPServer(t *testing.T) *Manager {
	t.Helper()

	cfg := &config.Config{
		Router: config.RouterConfig{
			ASN:      65000,
			RouterID: "10.0.0.1",
		},
		BGP: config.BGPConfig{
			ListenPort: 1179,
			Neighbors:  []config.NeighborConfig{},
		},
	}

	m := NewManager(cfg, nil)
	require.NoError(t, m.Start(context.Background()))
	t.Cleanup(func() { m.Stop(context.Background()) })
	return m
}

func TestManagerIntegrationWithLocalGoBGP(t *testing.T) {
	m := startTestBGPServer(t)

	t.Run("start succeeds with valid config", func(t *testing.T) {
		require.NotNil(t, m.Server())
	})

	t.Run("announce VIP succeeds", func(t *testing.T) {
		require.NoError(t, m.AnnounceVIP("10.1.0.1/32"))
	})

	t.Run("withdraw VIP succeeds", func(t *testing.T) {
		require.NoError(t, m.WithdrawVIP("10.1.0.1/32"))
	})

	t.Run("pessimize VIP succeeds", func(t *testing.T) {
		require.NoError(t, m.PessimizeVIP("10.1.0.1/32", 3, []string{"65535:666"}))
	})

	t.Run("peer states empty without configured peers", func(t *testing.T) {
		peers := m.GetPeerStates(context.Background())
		assert.Empty(t, peers)
	})

	t.Run("announce invalid prefix returns error", func(t *testing.T) {
		err := m.AnnounceVIP("invalid-prefix")
		require.Error(t, err)
	})
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func waitForPeerEstablished(t *testing.T, m *Manager, addr string) {
	t.Helper()

	require.Eventually(t, func() bool {
		for _, peer := range m.GetPeerStates(context.Background()) {
			if peer.Address == addr && peer.SessionState == "established" {
				return true
			}
		}
		return false
	}, 8*time.Second, 100*time.Millisecond)
}

func listAdjOutPaths(t *testing.T, m *Manager, peerAddr, prefix string) []*api.Path {
	t.Helper()

	var out []*api.Path
	err := m.Server().ListPath(context.Background(), &api.ListPathRequest{
		TableType: api.TableType_ADJ_OUT,
		Name:      peerAddr,
		Family:    &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST},
		Prefixes: []*api.TableLookupPrefix{{
			Prefix: prefix,
			Type:   api.TableLookupPrefix_EXACT,
		}},
	}, func(dst *api.Destination) {
		out = append(out, dst.GetPaths()...)
	})
	require.NoError(t, err)
	return out
}

func hasCommunity(path *api.Path, community uint32) bool {
	for _, attr := range path.GetPattrs() {
		if !attr.MessageIs(&api.CommunitiesAttribute{}) {
			continue
		}
		commAttr := &api.CommunitiesAttribute{}
		if err := attr.UnmarshalTo(commAttr); err != nil {
			return false
		}
		for _, c := range commAttr.GetCommunities() {
			if c == community {
				return true
			}
		}
	}
	return false
}

func routeStateSamples(t *testing.T, m *metrics.Metrics, prefix, peerIP string) []float64 {
	t.Helper()

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)

	var values []float64
	for _, mf := range mfs {
		if mf.GetName() != "pathosd_route_state" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			var nlri, peer string
			for _, label := range metric.GetLabel() {
				switch label.GetName() {
				case "nlri":
					nlri = label.GetValue()
				case "peer_ip":
					peer = label.GetValue()
				}
			}
			if nlri == prefix && peer == peerIP {
				values = append(values, metric.GetGauge().GetValue())
			}
		}
	}
	return values
}

func TestManagerIntegrationIBGPTransitionsProduceAdjOutAndRouteStateMetrics(t *testing.T) {
	senderPort := reserveTCPPort(t)
	receiverPort := reserveTCPPort(t)

	receiverCfg := &config.Config{
		Router: config.RouterConfig{
			ASN:      65000,
			RouterID: "127.0.0.2",
		},
		BGP: config.BGPConfig{
			ListenPort: receiverPort,
			Neighbors: []config.NeighborConfig{
				{
					Name:    "sender",
					Address: "127.0.0.1",
					PeerASN: 65000,
					Port:    uint16(senderPort),
					Passive: true,
				},
			},
		},
	}
	senderCfg := &config.Config{
		Router: config.RouterConfig{
			ASN:      65000,
			RouterID: "127.0.0.1",
		},
		BGP: config.BGPConfig{
			ListenPort: senderPort,
			Neighbors: []config.NeighborConfig{
				{
					Name:    "receiver",
					Address: "127.0.0.1",
					PeerASN: 65000,
					Port:    uint16(receiverPort),
				},
			},
		},
	}

	senderMetrics := metrics.New([]float64{0.1})

	receiver := NewManager(receiverCfg, nil)
	require.NoError(t, receiver.Start(context.Background()))
	t.Cleanup(func() { receiver.Stop(context.Background()) })
	require.NoError(t, receiver.AddPeers(context.Background()))

	sender := NewManager(senderCfg, senderMetrics)
	require.NoError(t, sender.Start(context.Background()))
	t.Cleanup(func() { sender.Stop(context.Background()) })
	require.NoError(t, sender.AddPeers(context.Background()))

	waitForPeerEstablished(t, sender, "127.0.0.1")
	waitForPeerEstablished(t, receiver, "127.0.0.1")

	const prefix = "10.1.0.50/32"
	require.NoError(t, sender.AnnounceVIP(prefix))

	var announced []*api.Path
	require.Eventually(t, func() bool {
		announced = listAdjOutPaths(t, sender, "127.0.0.1", prefix)
		return len(announced) > 0
	}, 8*time.Second, 100*time.Millisecond)

	announceAttrs := decodePathAttrs(t, announced[0].GetPattrs())
	require.NotNil(t, announceAttrs.asPath)
	assert.Empty(t, announceAttrs.asPath.GetSegments())

	require.Eventually(t, func() bool {
		samples := routeStateSamples(t, senderMetrics, prefix, "127.0.0.1")
		for _, v := range samples {
			if v == 1 {
				return true
			}
		}
		return false
	}, 8*time.Second, 100*time.Millisecond)

	require.NoError(t, sender.PessimizeVIP(prefix, 3, []string{"65535:666"}))

	require.Eventually(t, func() bool {
		paths := listAdjOutPaths(t, sender, "127.0.0.1", prefix)
		for _, p := range paths {
			if hasCommunity(p, 0xFFFF029A) {
				decoded := decodePathAttrs(t, p.GetPattrs())
				return decoded.asPath != nil && len(decoded.asPath.GetSegments()) == 0
			}
		}
		return false
	}, 8*time.Second, 100*time.Millisecond)

	require.NoError(t, sender.AnnounceVIP(prefix))

	require.Eventually(t, func() bool {
		paths := listAdjOutPaths(t, sender, "127.0.0.1", prefix)
		if len(paths) == 0 {
			return false
		}
		for _, p := range paths {
			if hasCommunity(p, 0xFFFF029A) {
				return false
			}
		}
		return true
	}, 8*time.Second, 100*time.Millisecond)

	require.NoError(t, sender.WithdrawVIP(prefix))
	require.Eventually(t, func() bool {
		return len(listAdjOutPaths(t, sender, "127.0.0.1", prefix)) == 0
	}, 8*time.Second, 100*time.Millisecond)

	require.Eventually(t, func() bool {
		samples := routeStateSamples(t, senderMetrics, prefix, "127.0.0.1")
		for _, v := range samples {
			if v == 0 {
				return true
			}
		}
		return false
	}, 8*time.Second, 100*time.Millisecond)
}
