package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSummarize(t *testing.T) {
	vips := []VIPStatus{
		{Name: "a", State: StateAnnounced},
		{Name: "b", State: StateAnnounced},
		{Name: "c", State: StatePessimized},
		{Name: "d", State: StateWithdrawn},
	}
	peers := []PeerStatus{
		{Name: "frr", SessionState: SessionEstablished},
		{Name: "bird", SessionState: "active"},
	}
	assert.Equal(t,
		"VIPs: 2 announced, 1 pessimized, 1 withdrawn; peers: 1/2 established (down: bird)",
		Summarize(vips, peers))

	assert.Equal(t,
		"VIPs: 0 announced, 0 pessimized, 0 withdrawn; peers: 0/0 established",
		Summarize(nil, nil))

	peers[1].SessionState = SessionEstablished
	assert.Equal(t,
		"VIPs: 2 announced, 1 pessimized, 1 withdrawn; peers: 2/2 established",
		Summarize(vips, peers))
}

func TestSummarizePeers(t *testing.T) {
	assert.Equal(t, "peers: 0/0 established", SummarizePeers(nil))
	assert.Equal(t, "peers: 0/2 established (down: a, b)", SummarizePeers([]PeerStatus{{Name: "b"}, {Name: "a"}}))
}

func TestUnreadyPeers(t *testing.T) {
	peers := []PeerStatus{
		{Name: "frr", Required: true, SessionState: SessionEstablished},
		{Name: "bird", Required: true, SessionState: "active"},
		{Name: "optional", Required: false, SessionState: "idle"},
	}
	unready := UnreadyPeers(peers)
	if assert.Len(t, unready, 1) {
		assert.Equal(t, "bird", unready[0].Name)
	}
	assert.Empty(t, UnreadyPeers(nil))
	assert.Empty(t, UnreadyPeers(peers[2:]), "optional peers never block readiness")
}
