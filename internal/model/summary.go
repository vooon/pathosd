package model

import (
	"fmt"
	"sort"
	"strings"
)

// SessionEstablished is the session_state of a peer whose BGP session is up.
const SessionEstablished = "established"

// UnreadyPeers returns the required peers whose session is not established.
// The daemon is ready when the result is empty. /readyz and
// `pathosd healthcheck` both use it, so the two verdicts cannot diverge.
func UnreadyPeers(peers []PeerStatus) []PeerStatus {
	var unready []PeerStatus
	for _, p := range peers {
		if p.Required && p.SessionState != SessionEstablished {
			unready = append(unready, p)
		}
	}
	return unready
}

// Summarize renders the systemd STATUS= line, e.g.
// "VIPs: 2 announced, 1 pessimized, 0 withdrawn; peers: 1/2 established (down: bird)".
func Summarize(vips []VIPStatus, peers []PeerStatus) string {
	var announced, pessimized, withdrawn int
	for _, v := range vips {
		switch v.State {
		case StateAnnounced:
			announced++
		case StatePessimized:
			pessimized++
		default:
			withdrawn++
		}
	}
	return fmt.Sprintf("VIPs: %d announced, %d pessimized, %d withdrawn; %s",
		announced, pessimized, withdrawn, SummarizePeers(peers))
}

// SummarizePeers renders the peer half of the status line, e.g.
// "peers: 1/2 established (down: bird)". It is the output of
// `pathosd healthcheck`, whose /readyz reply carries only the peers.
func SummarizePeers(peers []PeerStatus) string {
	established := 0
	var down []string
	for _, p := range peers {
		if p.SessionState == SessionEstablished {
			established++
		} else {
			down = append(down, p.Name)
		}
	}
	sort.Strings(down)
	s := fmt.Sprintf("peers: %d/%d established", established, len(peers))
	if len(down) > 0 {
		s += " (down: " + strings.Join(down, ", ") + ")"
	}
	return s
}
