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

// RouteAttrs are the BGP path attributes pathosd controls on an originated
// VIP route. The zero value means a plain announcement: no extra AS_PATH
// prepends and no communities.
type RouteAttrs struct {
	// ASPathPrepend is the number of additional local-ASN hops added to the
	// AS_PATH (on top of the single hop GoBGP adds on eBGP export).
	ASPathPrepend int `json:"as_path_prepend"`
	// Communities are standard BGP communities in "ASN:value" form.
	Communities []string `json:"communities,omitempty"`
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
	// CheckType is the single check's type, or for a VIP with several
	// checks "all(http, dns)", "any(...)" or the policy expression.
	CheckType string `json:"check_type"`
	// ChecksPolicy is how the checks combine: "all", "any" or an expression.
	ChecksPolicy string `json:"checks_policy"`
	// Checks lists every check of the VIP in config order.
	Checks     []CheckStatus `json:"checks"`
	BGPPending bool          `json:"bgp_pending"`
	// ASPathPrependBase is policy.announce.as_path_prepend: the prepend this
	// node applies to every announcement of the VIP (its standby rank).
	ASPathPrependBase int `json:"as_path_prepend_base"`
	// ASPathPrepend is the prepend currently applied: the base while
	// announced (or withdrawn), base + lower_priority while pessimized.
	ASPathPrepend int `json:"as_path_prepend"`
	// Communities currently attached to the route (announce block, merged
	// with lower_priority while pessimized).
	Communities []string `json:"communities,omitempty"`
}

// CheckStatus is the observable state of one check of a VIP.
type CheckStatus struct {
	Name            string       `json:"name"`
	Type            string       `json:"type"`
	Health          HealthStatus `json:"health"`
	HealthName      string       `json:"health_name"`
	ConsecutiveOK   int          `json:"consecutive_ok"`
	ConsecutiveFail int          `json:"consecutive_fail"`
	LastSuccess     bool         `json:"last_success"`
	LastDetail      string       `json:"last_detail"`
	LastTime        time.Time    `json:"last_time"`
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
	// Config describes the running config and the last reload; nil when
	// the daemon runs without a reloader (tests).
	Config *ConfigStatus `json:"config,omitempty"`
}

// Reload outcomes, see ReloadStatus.Outcome.
const (
	ReloadApplied   = "applied"
	ReloadUnchanged = "unchanged"
	ReloadRejected  = "rejected"
	ReloadFailed    = "failed"
)

// ConfigStatus is the config part of DaemonStatus.
type ConfigStatus struct {
	Path string `json:"path"`
	// LoadedAt is when the running config was read: startup or the last
	// applied reload.
	LoadedAt time.Time `json:"loaded_at"`
	// Generation counts applied reloads; 1 is the startup config.
	Generation int `json:"generation"`
	// FileChanged is true while the file on disk differs from what is
	// running: an edit nobody reloaded, or a reload that was rejected.
	FileChanged bool `json:"file_changed"`
	// PendingVIPs lists VIPs whose new check definitions still run in
	// shadow; their old definition governs until the new one has a verdict.
	PendingVIPs []string `json:"pending_vips,omitempty"`
	// LastReload is nil until the first reload attempt.
	LastReload *ReloadStatus `json:"last_reload,omitempty"`
}

// ReloadStatus is the result of one reload attempt. It is also the body of
// POST /api/v1/reload.
type ReloadStatus struct {
	At time.Time `json:"at"`
	// Outcome is applied, unchanged, rejected (config invalid or a
	// restart-only section changed; nothing was touched) or failed (the
	// diff was applied but a step reported an error, see Errors).
	Outcome string `json:"outcome"`
	// Error is the reason for a rejected reload.
	Error string `json:"error,omitempty"`
	// Errors lists what went wrong in a failed reload.
	Errors  []string `json:"errors,omitempty"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	// Changed lists VIPs whose definition changed; those in Pending are
	// still waiting for their new checks' verdicts.
	Changed []string `json:"changed,omitempty"`
	Pending []string `json:"pending,omitempty"`
}

// OK reports whether the attempt left the daemon running the file's config
// (applied or unchanged).
func (r *ReloadStatus) OK() bool {
	return r != nil && (r.Outcome == ReloadApplied || r.Outcome == ReloadUnchanged)
}

// CheckDetails carries check-type-specific observations alongside a check
// Result. They are exported as metrics only when check.detailed_metrics is
// enabled for the VIP; otherwise they are only visible in the ad-hoc check
// response.
type CheckDetails interface {
	checkDetails()
}

// PingDetails is reported by the ping checker.
type PingDetails struct {
	// Round-trip time of every echo reply received in this run.
	RTTs []time.Duration `json:"rtts"`
	// Fraction of probes lost, 0..1.
	PacketLoss  float64 `json:"packet_loss"`
	PacketsSent int     `json:"packets_sent"`
	PacketsRecv int     `json:"packets_recv"`
}

// HTTPDetails is reported by the http checker once a response was received.
type HTTPDetails struct {
	StatusCode int `json:"status_code"`
	// NotAfter of the leaf certificate presented by the server; zero for plain HTTP.
	TLSCertNotAfter time.Time `json:"tls_cert_not_after,omitzero"`
}

// DNSNameDetails is one queried name within DNSDetails.
type DNSNameDetails struct {
	Name    string `json:"name"`
	Rcode   int    `json:"rcode"`
	Answers int    `json:"answers"`
}

// DNSDetails is reported by the dns checker for every name that got a reply.
type DNSDetails struct {
	Names []DNSNameDetails `json:"names"`
}

// GRPCDetails is reported by the grpc checker.
type GRPCDetails struct {
	// gRPC status code of the call (0 = OK).
	StatusCode uint32 `json:"status_code"`
	// Serving status from the standard health protocol; empty for custom methods.
	ServingStatus string `json:"serving_status,omitempty"`
}

// ListenDetails is reported by the listen checker.
type ListenDetails struct {
	// Local addresses of the matching bound sockets, e.g. "0.0.0.0:4739".
	Sockets []string `json:"sockets"`
}

// SQLQueryDetails is one statement of a SQL check run.
type SQLQueryDetails struct {
	Name     string        `json:"name"`
	Rows     int           `json:"rows"`
	Duration time.Duration `json:"duration"`
	Passed   bool          `json:"passed"`
}

// SQLDetails is reported by the sql checker; queries after a failing one are
// not run and therefore absent.
type SQLDetails struct {
	Queries []SQLQueryDetails `json:"queries"`
}

// KafkaDetails is reported by the kafka checker. Partition fields are zero
// unless a topic is configured.
type KafkaDetails struct {
	ClusterID               string `json:"cluster_id"`
	Controller              int32  `json:"controller"`
	Brokers                 int    `json:"brokers"`
	Topic                   string `json:"topic,omitempty"`
	Partitions              int    `json:"partitions,omitempty"`
	PartitionsWithoutLeader int    `json:"partitions_without_leader,omitempty"`
}

func (*KafkaDetails) checkDetails()  {}
func (*SQLDetails) checkDetails()    {}
func (*ListenDetails) checkDetails() {}
func (*PingDetails) checkDetails()   {}
func (*HTTPDetails) checkDetails()   {}
func (*DNSDetails) checkDetails()    {}
func (*GRPCDetails) checkDetails()   {}
