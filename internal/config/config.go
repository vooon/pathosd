package config

import (
	"fmt"
	"maps"
	"strings"
)

// Check type constants.
const (
	CheckTypeHTTP = "http"
	CheckTypeDNS  = "dns"
	CheckTypePing = "ping"
	CheckTypeUDP  = "udp"
	CheckTypeTCP  = "tcp"
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
	// OpenTelemetry export configuration (traces, metrics, logs). Disabled when endpoint is empty.
	OTel OTelConfig `yaml:"otel" json:"otel" toml:"otel"`
	// BGP session parameters and neighbor definitions.
	BGP BGPConfig `yaml:"bgp" json:"bgp" toml:"bgp" jsonschema:"required"`
	// List of Virtual IPs to announce via BGP, each with a health check.
	VIPs []VIPConfig `yaml:"vips" json:"vips" toml:"vips" jsonschema:"required,minItems=1"`
}

// OTelSignalConfig configures a single OTEL signal (traces, metrics, or logs).
type OTelSignalConfig struct {
	// Per-signal endpoint URL. Overrides the top-level otel.endpoint for this signal.
	// Leave empty to inherit the global endpoint.
	// The URL scheme determines the transport: grpc:// or grpcs:// selects gRPC;
	// http:// or https:// selects OTLP/HTTP.
	Endpoint string `yaml:"endpoint" json:"endpoint" toml:"endpoint"`
	// Enable this signal. Default: true (inherits the global otel.enabled value).
	Enabled *bool `yaml:"enabled" json:"enabled" toml:"enabled" jsonschema:"default=true"`
	// Skip TLS certificate verification for this signal. Overrides the global otel.insecure.
	// Leave unset to inherit the global value.
	Insecure *bool `yaml:"insecure" json:"insecure" toml:"insecure"`
	// Additional headers for this signal, merged on top of the global otel.headers.
	// Signal-level keys take precedence over global keys.
	Headers map[string]string `yaml:"headers" json:"headers" toml:"headers"`
}

// EffectiveEndpoint returns the signal-specific endpoint when set, otherwise the global endpoint.
func (s OTelSignalConfig) EffectiveEndpoint(global string) string {
	if s.Endpoint != "" {
		return s.Endpoint
	}
	return global
}

// IsEnabled reports whether the signal is enabled.
// Returns true when Enabled is nil (unset = enabled by default).
func (s OTelSignalConfig) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// EffectiveInsecure returns the signal-level insecure flag when explicitly set,
// otherwise falls back to the global value.
func (s OTelSignalConfig) EffectiveInsecure(global bool) bool {
	if s.Insecure != nil {
		return *s.Insecure
	}
	return global
}

// EffectiveHeaders returns the global headers merged with the signal-level headers.
// Signal-level keys take precedence over global keys.
func (s OTelSignalConfig) EffectiveHeaders(global map[string]string) map[string]string {
	if len(s.Headers) == 0 {
		return global
	}
	merged := maps.Clone(global)
	maps.Copy(merged, s.Headers)
	return merged
}

// OTelConfig configures OpenTelemetry export (traces, metrics, logs).
type OTelConfig struct {
	// Master switch for all OTEL export. Default: true.
	// Set to false to disable OTEL entirely without removing the endpoint.
	Enabled *bool `yaml:"enabled" json:"enabled" toml:"enabled" jsonschema:"default=true"`
	// OTLP collector endpoint URL. If empty, OTEL export is disabled.
	// The URL scheme selects the transport protocol automatically:
	//   grpc://host:4317  — gRPC (plaintext)
	//   grpcs://host:4317 — gRPC (TLS)
	//   http://host:4318  — OTLP/HTTP (plaintext)
	//   https://host:4318 — OTLP/HTTP (TLS)
	Endpoint string `yaml:"endpoint" json:"endpoint" toml:"endpoint"`
	// Skip TLS certificate verification for all signals. Not recommended in production.
	// Can be overridden per signal.
	Insecure bool `yaml:"insecure" json:"insecure" toml:"insecure" jsonschema:"default=false"`
	// Additional headers sent with every OTLP request (e.g. for API-key authentication).
	// Can be extended or overridden per signal.
	Headers map[string]string `yaml:"headers" json:"headers" toml:"headers"`
	// OTEL resource service.name attribute. Default: "pathosd".
	ServiceName string `yaml:"service_name" json:"service_name" toml:"service_name"`
	// Per-signal configuration. Each signal inherits the global endpoint, insecure, and headers
	// and is enabled by default. Use these to override per signal.
	Traces  OTelSignalConfig `yaml:"traces" json:"traces" toml:"traces"`
	Metrics OTelSignalConfig `yaml:"metrics" json:"metrics" toml:"metrics"`
	Logs    OTelSignalConfig `yaml:"logs" json:"logs" toml:"logs"`
}

// IsEnabled reports whether OTEL export is enabled globally.
// Returns true when Enabled is nil (unset = enabled by default).
func (o OTelConfig) IsEnabled() bool {
	return o.Enabled == nil || *o.Enabled
}

// OTelProtocol represents the resolved OTLP transport.
type OTelProtocol int

const (
	OTelProtocolGRPC OTelProtocol = iota
	OTelProtocolHTTP
)

// ParseOTelEndpoint resolves the OTLP transport from the URL scheme and
// returns the normalised URL that the OTEL SDK exporters expect.
//
// Scheme mapping:
//
//	grpc://  → gRPC, normalised to http://
//	grpcs:// → gRPC, normalised to https://
//	http://  → OTLP/HTTP, unchanged
//	https:// → OTLP/HTTP, unchanged
func ParseOTelEndpoint(rawURL string) (proto OTelProtocol, normURL string, err error) {
	switch {
	case strings.HasPrefix(rawURL, "grpcs://"):
		return OTelProtocolGRPC, "https://" + rawURL[len("grpcs://"):], nil
	case strings.HasPrefix(rawURL, "grpc://"):
		return OTelProtocolGRPC, "http://" + rawURL[len("grpc://"):], nil
	case strings.HasPrefix(rawURL, "https://"), strings.HasPrefix(rawURL, "http://"):
		return OTelProtocolHTTP, rawURL, nil
	default:
		return OTelProtocolGRPC, rawURL, fmt.Errorf("unrecognised OTLP endpoint scheme in %q; use grpc://, grpcs://, http://, or https://", rawURL)
	}
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
	// TCP port to listen on for inbound BGP TCP sessions. Default: 179. Set to -1 to disable BGP listening entirely.
	ListenPort int `yaml:"listen_port" json:"listen_port" toml:"listen_port" jsonschema:"minimum=-1,maximum=65535"`
	// Embedded GoBGP gRPC API configuration (used by gobgp CLI for debugging).
	GoBGPAPI GoBGPAPIConfig `yaml:"gobgp_api,omitempty" json:"gobgp_api,omitempty" toml:"gobgp_api,omitempty"`
	// List of BGP neighbors (peers) to establish sessions with.
	Neighbors []NeighborConfig `yaml:"neighbors" json:"neighbors" toml:"neighbors" jsonschema:"required,minItems=1"`
}

// GoBGPAPIConfig configures the embedded GoBGP gRPC API.
type GoBGPAPIConfig struct {
	// Enable embedded GoBGP gRPC API server. Default: false.
	Enabled bool `yaml:"enabled" json:"enabled" toml:"enabled" jsonschema:"default=false"`
	// gRPC listen address used when enabled. Supports host:port or unix:///path. Default: 127.0.0.1:50051.
	Listen string `yaml:"listen,omitempty" json:"listen,omitempty" toml:"listen,omitempty"`
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
	// Enable eBGP multihop — required when the peer is not directly connected (e.g. loopback-based sessions).
	EnableMultihop bool `yaml:"enable_multihop" json:"enable_multihop" toml:"enable_multihop"`
	// IP TTL for eBGP multihop sessions. 0 means use GoBGP default (255). Must be >= 2 when set explicitly. Only used when enable_multihop is true.
	MultihopTTL uint32 `yaml:"multihop_ttl" json:"multihop_ttl" toml:"multihop_ttl" jsonschema:"minimum=0,maximum=255"`
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
	Type string `yaml:"type" json:"type" toml:"type" jsonschema:"required,enum=http,enum=dns,enum=ping,enum=udp,enum=tcp"`
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
	// UDP check configuration (required when type is "udp").
	UDP *UDPCheckConfig `yaml:"udp,omitempty" json:"udp,omitempty" toml:"udp,omitempty"`
	// TCP check configuration (required when type is "tcp").
	TCP *TCPCheckConfig `yaml:"tcp,omitempty" json:"tcp,omitempty" toml:"tcp,omitempty"`
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
	// TLS SNI server name sent during the handshake and used for certificate verification.
	// Auto-defaults to the URL hostname for HTTPS checks when the connect host (host field)
	// differs from the URL hostname — covers bare-IP VIPs and k8s Service names alike.
	// Set explicitly to override.
	TLSServerName string `yaml:"tls_server_name" json:"tls_server_name" toml:"tls_server_name"`
	// Additional HTTP headers to send with the check request.
	Headers map[string]string `yaml:"headers" json:"headers" toml:"headers"`
}

// TCPCheckConfig defines a TCP port reachability check.
// The checker dials host:port and considers a successful connection as passing.
type TCPCheckConfig struct {
	// Host to connect to. Defaults to VIP prefix IP for /32 or /128.
	Host string `yaml:"host" json:"host" toml:"host"`
	// TCP port to connect to. Required.
	Port uint16 `yaml:"port" json:"port" toml:"port" jsonschema:"required,minimum=1,maximum=65535"`
}

// UDPCheckConfig defines a UDP port reachability check.
// The checker sends a small probe datagram to host:port. An ICMP port-unreachable
// reply (surfaced as ECONNREFUSED on a connected UDP socket) means nothing is
// listening → check fails. A read timeout with no error means the datagram was
// accepted → check passes.
type UDPCheckConfig struct {
	// Host to send the probe to. Defaults to VIP prefix IP for /32 or /128.
	Host string `yaml:"host" json:"host" toml:"host"`
	// UDP port to probe. Required.
	Port uint16 `yaml:"port" json:"port" toml:"port" jsonschema:"required,minimum=1,maximum=65535"`
	// Payload to send with the probe datagram. Default: single null byte.
	Payload []byte `yaml:"payload" json:"payload" toml:"payload"`
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
	// Use privileged raw ICMP socket mode. Requires root/CAP_NET_RAW. Default: false (unprivileged UDP mode).
	Privileged bool `yaml:"privileged,omitempty" json:"privileged,omitempty" toml:"privileged,omitempty" jsonschema:"default=false"`
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
