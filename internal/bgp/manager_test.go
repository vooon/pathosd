package bgp

import (
	"context"
	"testing"

	api "github.com/osrg/gobgp/v3/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/config"
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

	m := NewManager(cfg)
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
