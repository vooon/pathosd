package bgp

import (
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
