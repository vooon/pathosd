package bgp

import (
	"testing"

	api "github.com/osrg/gobgp/v3/api"
	"github.com/stretchr/testify/assert"
)

func TestResolvePeerIdentity(t *testing.T) {
	t.Run("uses conf values when available", func(t *testing.T) {
		peer := &api.Peer{
			Conf: &api.PeerConf{
				Description:     "frr",
				NeighborAddress: "10.0.0.2",
			},
		}

		name, addr := resolvePeerIdentity(peer, map[string]string{"10.0.0.2": "neighbor-from-config"})
		assert.Equal(t, "frr", name)
		assert.Equal(t, "10.0.0.2", addr)
	})

	t.Run("falls back to configured name by address", func(t *testing.T) {
		peer := &api.Peer{
			Conf: &api.PeerConf{
				NeighborAddress: "10.0.0.2",
			},
		}

		name, addr := resolvePeerIdentity(peer, map[string]string{"10.0.0.2": "frr"})
		assert.Equal(t, "frr", name)
		assert.Equal(t, "10.0.0.2", addr)
	})

	t.Run("falls back to state fields", func(t *testing.T) {
		peer := &api.Peer{
			State: &api.PeerState{
				Description:     "state-desc",
				NeighborAddress: "10.0.0.3",
			},
		}

		name, addr := resolvePeerIdentity(peer, nil)
		assert.Equal(t, "state-desc", name)
		assert.Equal(t, "10.0.0.3", addr)
	})

	t.Run("falls back to address when name is missing", func(t *testing.T) {
		peer := &api.Peer{
			State: &api.PeerState{
				NeighborAddress: "10.0.0.4",
			},
		}

		name, addr := resolvePeerIdentity(peer, nil)
		assert.Equal(t, "10.0.0.4", name)
		assert.Equal(t, "10.0.0.4", addr)
	})
}
