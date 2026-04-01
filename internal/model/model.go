package model

import "time"

// VIPState represents the effective routing state of a VIP.
type VIPState int

const (
	StateWithdrawn  VIPState = 0
	StateAnnounced  VIPState = 1
	StatePessimized VIPState = 2
)

func (s VIPState) String() string {
	switch s {
	case StateWithdrawn:
		return "withdrawn"
	case StateAnnounced:
		return "announced"
	case StatePessimized:
		return "pessimized"
	default:
		return "unknown"
	}
}

// HealthStatus tracks whether a VIP's service is considered healthy.
type HealthStatus int

const (
	HealthUnknown   HealthStatus = 0
	HealthHealthy   HealthStatus = 1
	HealthUnhealthy HealthStatus = 2
)

func (h HealthStatus) String() string {
	switch h {
	case HealthUnknown:
		return "unknown"
	case HealthHealthy:
		return "healthy"
	case HealthUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// VIPStatus is the full observable state for one VIP.
type VIPStatus struct {
	Name                 string       `json:"name"`
	Prefix               string       `json:"prefix"`
	State                VIPState     `json:"state"`
	StateName            string       `json:"state_name"`
	Health               HealthStatus `json:"health"`
	HealthName           string       `json:"health_name"`
	ConsecutiveOK        int          `json:"consecutive_ok"`
	ConsecutiveFail      int          `json:"consecutive_fail"`
	LastCheckSuccess     bool         `json:"last_check_success"`
	LastCheckDetail      string       `json:"last_check_detail"`
	LastCheckTime        time.Time    `json:"last_check_time"`
	LastTransitionTime   time.Time    `json:"last_transition_time"`
	LastTransitionReason string       `json:"last_transition_reason"`
	CheckType            string       `json:"check_type"`
}

// PeerStatus is the observable state for one BGP neighbor.
type PeerStatus struct {
	Name         string `json:"name"`
	Address      string `json:"address"`
	PeerASN      uint32 `json:"peer_asn"`
	SessionState string `json:"session_state"`
	Required     bool   `json:"required"`
	Uptime       string `json:"uptime,omitempty"`
}

// DaemonStatus is the full daemon state returned on the diagnostic endpoint.
type DaemonStatus struct {
	RouterID  string       `json:"router_id"`
	ASN       uint32       `json:"asn"`
	Version   string       `json:"version"`
	Commit    string       `json:"commit"`
	StartTime time.Time    `json:"start_time"`
	Peers     []PeerStatus `json:"peers"`
	VIPs      []VIPStatus  `json:"vips"`
}
