package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vooon/pathosd/internal/model"
)

func TestSummarize(t *testing.T) {
	vips := []model.VIPStatus{
		{Name: "a", State: model.StateAnnounced},
		{Name: "b", State: model.StateAnnounced},
		{Name: "c", State: model.StatePessimized},
		{Name: "d", State: model.StateWithdrawn},
	}
	peers := []model.PeerStatus{
		{Name: "frr", SessionState: "established"},
		{Name: "bird", SessionState: "active"},
	}
	assert.Equal(t,
		"VIPs: 2 announced, 1 pessimized, 1 withdrawn; peers: 1/2 established (down: bird)",
		summarize(vips, peers))

	assert.Equal(t,
		"VIPs: 0 announced, 0 pessimized, 0 withdrawn; peers: 0/0 established",
		summarize(nil, nil))

	peers[1].SessionState = "established"
	assert.Equal(t,
		"VIPs: 2 announced, 1 pessimized, 1 withdrawn; peers: 2/2 established",
		summarize(vips, peers))
}
