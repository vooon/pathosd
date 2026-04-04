package config

// Check type constants.
const (
	CheckTypeHTTP = "http"
	CheckTypeDNS  = "dns"
	CheckTypePing = "ping"
)

// Config is the top-level configuration for pathosd.
type Config struct {
	// Configuration schema version identifier.
	Schema string `yaml:"schema" json:"schema" toml:"schema" jsonschema:"required,enum=v1"`
	// BGP router identity and addressing.
	Router RouterConfig `yaml:"router" json:"router" toml:"router" jsonschema:"required"`
	// HTTP API server configuration (healthz, metrics, status).
	API APIConfig `yaml:"api" json:"api" toml:"api" jsonschema:"required"`
	// Structured logging configuration.
	Logging LoggingConfig `yaml:"logging" json:"logging" toml:"logging"`
	// BGP session parameters and neighbor definitions.
	BGP BGPConfig `yaml:"bgp" json:"bgp" toml:"bgp" jsonschema:"required"`
	// List of Virtual IPs to announce via BGP, each with a health check.
	VIPs []VIPConfig `yaml:"vips" json:"vips" toml:"vips" jsonschema:"required,minItems=1"`
}

// RouterConfig identifies this BGP speaker.
type RouterConfig struct {
	// Autonomous System Number for this router (1–4294967295).
	ASN uint32 `yaml:"asn" json:"asn" toml:"asn" jsonschema:"required,minimum=1,maximum=4294967295"`
	// BGP Router ID in dotted-quad notation (e.g. 10.0.0.1).
	RouterID string `yaml:"router_id" json:"router_id" toml:"router_id" jsonschema:"required,format=ipv4"`
	// Local address to bind BGP sessions. If empty, the OS selects the source address.
	LocalAddress string `yaml:"local_address" json:"local_address" toml:"local_address" jsonschema:"format=ipv4"`
}

// APIConfig configures the HTTP API server.
type APIConfig struct {
	// Listen address for the HTTP API (e.g. ":59179" or "127.0.0.1:59179"). Default: :59179.
	Listen string `yaml:"listen" json:"listen" toml:"listen" jsonschema:"required"`
}

// LoggingConfig controls structured logging output.
type LoggingConfig struct {
	// Minimum log level.
	Level string `yaml:"level" json:"level" toml:"level" jsonschema:"enum=debug,enum=info,enum=warn,enum=error,default=info"`
	// Log output format.
	Format string `yaml:"format" json:"format" toml:"format" jsonschema:"enum=text,enum=json,default=text"`
}

// BGPConfig defines global BGP parameters and neighbors.
type BGPConfig struct {
	// Enable BGP graceful restart.
	GracefulRestart *bool `yaml:"graceful_restart" json:"graceful_restart" toml:"graceful_restart"`
	// BGP hold time. Default: 90s.
	HoldTime *Duration `yaml:"hold_time" json:"hold_time" toml:"hold_time"`
	// BGP keepalive interval. Default: 30s.
	KeepaliveTime *Duration `yaml:"keepalive_time" json:"keepalive_time" toml:"keepalive_time"`
	// Local address to listen for inbound BGP TCP sessions. If empty, falls back to router.local_address, then 0.0.0.0.
	ListenAddress string `yaml:"listen_address" json:"listen_address" toml:"listen_address" jsonschema:"format=ipv4"`
	// TCP port to listen on for inbound BGP TCP sessions. Default: 179.
	ListenPort int `yaml:"listen_port" json:"listen_port" toml:"listen_port" jsonschema:"minimum=1,maximum=65535"`
	// List of BGP neighbors (peers) to establish sessions with.
	Neighbors []NeighborConfig `yaml:"neighbors" json:"neighbors" toml:"neighbors" jsonschema:"required,minItems=1"`
}

// NeighborConfig defines a BGP peer.
type NeighborConfig struct {
	// Human-readable name for this peer (used in logs and metrics).
	Name string `yaml:"name" json:"name" toml:"name" jsonschema:"required"`
	// IPv4 address of the BGP peer.
	Address string `yaml:"address" json:"address" toml:"address" jsonschema:"required,format=ipv4"`
	// Peer's Autonomous System Number.
	PeerASN uint32 `yaml:"peer_asn" json:"peer_asn" toml:"peer_asn" jsonschema:"required,minimum=1,maximum=4294967295"`
	// If true, all VIPs require this peer to be established. Default: false.
	Required *bool `yaml:"required" json:"required" toml:"required"`
	// TCP port for the BGP session. Default: 179.
	Port uint16 `yaml:"port" json:"port" toml:"port"`
	// Local source address for active peering with this neighbor. If empty, falls back to router.local_address.
	LocalAddress string `yaml:"local_address" json:"local_address" toml:"local_address" jsonschema:"format=ipv4"`
	// Wait for the peer to initiate the connection instead of connecting actively.
	Passive bool `yaml:"passive" json:"passive" toml:"passive"`
}

// VIPConfig defines a Virtual IP with its health check and policy.
type VIPConfig struct {
	// Human-readable name for this VIP (used in logs and metrics).
	Name string `yaml:"name" json:"name" toml:"name" jsonschema:"required"`
	// IP prefix to announce (e.g. 10.0.0.1/32 or 2001:db8::1/128).
	Prefix string `yaml:"prefix" json:"prefix" toml:"prefix" jsonschema:"required"`
	// Health check configuration for this VIP.
	Check CheckConfig `yaml:"check" json:"check" toml:"check" jsonschema:"required"`
	// Policy controlling how BGP announcements react to check failures.
	Policy PolicyConfig `yaml:"policy" json:"policy" toml:"policy" jsonschema:"required"`
}

// CheckConfig defines health check type, scheduling, and type-specific parameters.
type CheckConfig struct {
	// Health check backend type.
	Type string `yaml:"type" json:"type" toml:"type" jsonschema:"required,enum=http,enum=dns,enum=ping"`
	// Interval between consecutive health checks. Default: 5s.
	Interval *Duration `yaml:"interval" json:"interval" toml:"interval"`
	// Maximum time to wait for a check to complete; must be less than interval. Default: 2s.
	Timeout *Duration `yaml:"timeout" json:"timeout" toml:"timeout"`
	// Number of consecutive successes required to mark healthy (HAProxy-style). Default: 3.
	Rise *int `yaml:"rise" json:"rise" toml:"rise" jsonschema:"minimum=1"`
	// Number of consecutive failures required to mark unhealthy (HAProxy-style). Default: 3.
	Fall *int `yaml:"fall" json:"fall" toml:"fall" jsonschema:"minimum=1"`
	// HTTP check configuration (required when type is "http").
	HTTP *HTTPCheckConfig `yaml:"http,omitempty" json:"http,omitempty" toml:"http,omitempty"`
	// DNS check configuration (required when type is "dns").
	DNS *DNSCheckConfig `yaml:"dns,omitempty" json:"dns,omitempty" toml:"dns,omitempty"`
	// Ping (ICMP) check configuration (required when type is "ping").
	Ping *PingCheckConfig `yaml:"ping,omitempty" json:"ping,omitempty" toml:"ping,omitempty"`
}

// HTTPCheckConfig defines an HTTP health check.
type HTTPCheckConfig struct {
	// URL to check. Can be a full URL (https://example.com/healthz) or a path (/healthz). When a full URL is given, proto and Host header are derived from it.
	URL string `yaml:"url" json:"url" toml:"url" jsonschema:"required"`
	// Protocol override. Derived from full URL if not set. Default: http.
	Proto string `yaml:"proto" json:"proto" toml:"proto" jsonschema:"enum=http,enum=https"`
	// Host to connect to and send in the Host header. Defaults to VIP prefix IP for /32 or /128 prefixes.
	Host string `yaml:"host" json:"host" toml:"host"`
	// Port override. Defaults to 80 for http, 443 for https.
	Port uint16 `yaml:"port" json:"port" toml:"port"`
	// HTTP method. Default: GET.
	Method string `yaml:"method" json:"method" toml:"method" jsonschema:"enum=GET,enum=HEAD,default=GET"`
	// List of acceptable HTTP response status codes. Default: [200].
	ResponseCodes []int `yaml:"response_codes" json:"response_codes" toml:"response_codes"`
	// Exact string that must be present in the response body.
	ResponseText string `yaml:"response_text" json:"response_text" toml:"response_text"`
	// Regular expression that must match the response body.
	ResponseRegex string `yaml:"response_regex" json:"response_regex" toml:"response_regex"`
	// JQ expression evaluated against the JSON response body; check passes when the result is "true".
	ResponseJQ string `yaml:"response_jq" json:"response_jq" toml:"response_jq"`
	// Skip TLS certificate verification. Mutually exclusive with tls_ca_cert.
	TLSInsecure bool `yaml:"tls_insecure" json:"tls_insecure" toml:"tls_insecure"`
	// Path to a PEM-encoded CA certificate file for TLS verification. Mutually exclusive with tls_insecure.
	TLSCACert string `yaml:"tls_ca_cert" json:"tls_ca_cert" toml:"tls_ca_cert"`
	// Additional HTTP headers to send with the check request.
	Headers map[string]string `yaml:"headers" json:"headers" toml:"headers"`
}

// DNSCheckConfig defines a DNS health check.
type DNSCheckConfig struct {
	// DNS names to query. All must resolve successfully for the check to pass.
	Names []string `yaml:"names" json:"names" toml:"names" jsonschema:"required,minItems=1"`
	// DNS resolver address (ip or ip:port). Defaults to VIP prefix IP.
	Resolver string `yaml:"resolver" json:"resolver" toml:"resolver"`
	// DNS resolver port (used when resolver is an IP without port). Default: 53.
	Port uint16 `yaml:"port" json:"port" toml:"port"`
	// DNS query type. Default: A.
	QueryType string `yaml:"query_type" json:"query_type" toml:"query_type" jsonschema:"enum=A,enum=AAAA,enum=CNAME,enum=PTR,enum=NS,enum=MX,enum=SOA,enum=TXT,enum=SRV,default=A"`
}

// PingCheckConfig defines an ICMP ping health check.
type PingCheckConfig struct {
	// Destination IP address to ping. Defaults to VIP prefix IP.
	DstIP string `yaml:"dst_ip" json:"dst_ip" toml:"dst_ip"`
	// Source IP address for outgoing ICMP packets.
	SrcIP string `yaml:"src_ip" json:"src_ip" toml:"src_ip"`
	// Number of ICMP echo requests to send per check cycle. Default: 1.
	Count int `yaml:"count" json:"count" toml:"count" jsonschema:"minimum=1,maximum=60,default=1"`
	// Per-packet timeout for ICMP replies.
	Timeout *Duration `yaml:"timeout" json:"timeout" toml:"timeout"`
	// Interval between individual ICMP packets within a check cycle.
	Interval *Duration `yaml:"interval" json:"interval" toml:"interval"`
	// Maximum acceptable packet loss ratio (0.0–1.0). Default: 0 (no loss allowed).
	MaxLossRatio float64 `yaml:"max_loss_ratio" json:"max_loss_ratio" toml:"max_loss_ratio" jsonschema:"minimum=0,maximum=1,default=0"`
}

// PolicyConfig defines how VIP state changes on check failure.
type PolicyConfig struct {
	// Action to take when the health check fails: withdraw the route entirely, or lower its BGP priority.
	FailAction string `yaml:"fail_action" json:"fail_action" toml:"fail_action" jsonschema:"enum=withdraw,enum=lower_priority,default=lower_priority"`
	// Path to a drain lock file. When present and check is healthy, force pessimized announcement instead of full priority. Does not prevent withdrawal on failure. Useful for maintenance drains.
	LowerPriorityFile string `yaml:"lower_priority_file" json:"lower_priority_file,omitempty" toml:"lower_priority_file,omitempty"`
	// Configuration for the lower_priority fail action.
	LowerPriority *LowerPriorityConfig `yaml:"lower_priority" json:"lower_priority,omitempty" toml:"lower_priority,omitempty"`
}

// LowerPriorityConfig defines BGP attributes for pessimized announcements.
type LowerPriorityConfig struct {
	// Number of times to prepend the local ASN to the AS_PATH. Default: 6.
	ASPathPrepend *int `yaml:"as_path_prepend" json:"as_path_prepend" toml:"as_path_prepend" jsonschema:"minimum=1,maximum=16,default=6"`
	// BGP communities to attach to pessimized route announcements (e.g. "65535:666").
	Communities []string `yaml:"communities" json:"communities" toml:"communities"`
}
