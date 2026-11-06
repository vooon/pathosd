package bgp

import (
	"bytes"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/osrg/gobgp/v4/pkg/apiutil"
	"github.com/stretchr/testify/assert"
)

func TestResolvePeerIdentity(t *testing.T) {
	t.Run("uses configured name by address", func(t *testing.T) {
		peer := &apiutil.Peer{
			Conf: apiutil.PeerConf{
				NeighborAddress: netip.MustParseAddr("10.0.0.2"),
			},
		}

		name, addr := resolvePeerIdentity(peer, map[string]string{"10.0.0.2": "neighbor-from-config"})
		assert.Equal(t, "neighbor-from-config", name)
		assert.Equal(t, "10.0.0.2", addr)
	})

	t.Run("falls back to state address when conf address is empty", func(t *testing.T) {
		peer := &apiutil.Peer{
			State: apiutil.PeerState{
				NeighborAddress: netip.MustParseAddr("10.0.0.3"),
			},
		}

		name, addr := resolvePeerIdentity(peer, nil)
		assert.Equal(t, "10.0.0.3", addr)
		assert.Equal(t, "10.0.0.3", name)
	})

	t.Run("falls back to address when name is missing", func(t *testing.T) {
		peer := &apiutil.Peer{
			State: apiutil.PeerState{
				NeighborAddress: netip.MustParseAddr("10.0.0.4"),
			},
		}

		name, addr := resolvePeerIdentity(peer, nil)
		assert.Equal(t, "10.0.0.4", name)
		assert.Equal(t, "10.0.0.4", addr)
	})
}

func TestLogPeerEvent(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	t.Run("skips the end-of-init marker", func(t *testing.T) {
		buf.Reset()
		assert.False(t, logPeerEvent(&apiutil.WatchEventMessage_PeerEvent{Type: apiutil.PEER_EVENT_END_OF_INIT}, nil))
		assert.Empty(t, buf.String())
	})

	t.Run("skips nil", func(t *testing.T) {
		buf.Reset()
		assert.False(t, logPeerEvent(nil, nil))
		assert.Empty(t, buf.String())
	})

	t.Run("logs init and state events", func(t *testing.T) {
		for _, typ := range []apiutil.PeerEventType{apiutil.PEER_EVENT_INIT, apiutil.PEER_EVENT_STATE} {
			buf.Reset()
			ev := &apiutil.WatchEventMessage_PeerEvent{
				Type: typ,
				Peer: apiutil.Peer{Conf: apiutil.PeerConf{NeighborAddress: netip.MustParseAddr("10.0.0.2")}},
			}
			assert.True(t, logPeerEvent(ev, map[string]string{"10.0.0.2": "r1"}))
			assert.Contains(t, buf.String(), "name=r1 address=10.0.0.2 state=")
			assert.NotContains(t, buf.String(), "invalid IP")
		}
	})
}
