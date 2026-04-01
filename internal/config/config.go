package config

// Config is the top-level configuration for pathosd.
// This is the single authoritative config model — YAML and TOML are just serialization formats.
type Config struct {
	Schema  string        `yaml:"schema"  json:"schema"  toml:"schema"  jsonschema:"required,enum=v1"`
	Router  RouterConfig  `yaml:"router"  json:"router"  toml:"router"  jsonschema:"required"`
	API     APIConfig     `yaml:"api"     json:"api"     toml:"api"     jsonschema:"required"`
	Logging LoggingConfig `yaml:"logging" json:"logging" toml:"logging"`
	BGP     BGPConfig     `yaml:"bgp"     json:"bgp"     toml:"bgp"     jsonschema:"required"`
	VIPs    []VIPConfig   `yaml:"vips"    json:"vips"    toml:"vips"    jsonschema:"required,minItems=1"`
}

// RouterConfig identifies this BGP speaker.
type RouterConfig struct {
	ASN          uint32 `yaml:"asn"           json:"asn"           toml:"asn"           jsonschema:"required,minimum=1,maximum=4294967295"`
	RouterID     string `yaml:"router_id"     json:"router_id"     toml:"router_id"     jsonschema:"required,format=ipv4"`
	LocalAddress string `yaml:"local_address" json:"local_address" toml:"local_address" jsonschema:"format=ipv4"`
}

// APIConfig configures the HTTP API server.
type APIConfig struct {
	Listen string `yaml:"listen" json:"listen" toml:"listen" jsonschema:"required"`
}

// LoggingConfig controls structured logging output.
type LoggingConfig struct {
	Level  string `yaml:"level"  json:"level"  toml:"level"  jsonschema:"enum=debug,enum=info,enum=warn,enum=error,default=info"`
	Format string `yaml:"format" json:"format" toml:"format" jsonschema:"enum=text,enum=json,default=text"`
}

// BGPConfig defines global BGP parameters and neighbors.
type BGPConfig struct {
	GracefulRestart *bool            `yaml:"graceful_restart" json:"graceful_restart" toml:"graceful_restart"`
	HoldTime        *Duration        `yaml:"hold_time"        json:"hold_time"        toml:"hold_time"`
	KeepaliveTime   *Duration        `yaml:"keepalive_time"   json:"keepalive_time"   toml:"keepalive_time"`
	Neighbors       []NeighborConfig `yaml:"neighbors"        json:"neighbors"        toml:"neighbors"        jsonschema:"required,minItems=1"`
}

// NeighborConfig defines a BGP peer.
type NeighborConfig struct {
	Name     string `yaml:"name"     json:"name"     toml:"name"     jsonschema:"required"`
	Address  string `yaml:"address"  json:"address"  toml:"address"  jsonschema:"required,format=ipv4"`
	PeerASN  uint32 `yaml:"peer_asn" json:"peer_asn" toml:"peer_asn" jsonschema:"required,minimum=1,maximum=4294967295"`
	Required *bool  `yaml:"required" json:"required" toml:"required"`
	Port     uint16 `yaml:"port"     json:"port"     toml:"port"`
	Passive  bool   `yaml:"passive"  json:"passive"  toml:"passive"`
}

// VIPConfig defines a Virtual IP with its health check and policy.
type VIPConfig struct {
	Name          string      `yaml:"name"           json:"name"           toml:"name"           jsonschema:"required"`
	Prefix        string      `yaml:"prefix"         json:"prefix"         toml:"prefix"         jsonschema:"required"`
	CheckInterval *Duration   `yaml:"check_interval" json:"check_interval" toml:"check_interval"`
	CheckTimeout  *Duration   `yaml:"check_timeout"  json:"check_timeout"  toml:"check_timeout"`
	Rise          *int        `yaml:"rise"           json:"rise"           toml:"rise"           jsonschema:"minimum=1"`
	Fall          *int        `yaml:"fall"           json:"fall"           toml:"fall"           jsonschema:"minimum=1"`
	Check         CheckConfig `yaml:"check"          json:"check"          toml:"check"          jsonschema:"required"`
	Policy        PolicyConfig `yaml:"policy"        json:"policy"         toml:"policy"         jsonschema:"required"`

	// CheckHistogramBuckets allows overriding default histogram buckets for check duration.
	CheckHistogramBuckets []float64 `yaml:"check_histogram_buckets" json:"check_histogram_buckets,omitempty" toml:"check_histogram_buckets,omitempty"`
}

// CheckConfig is a discriminated union — the Type field selects which sub-config applies.
type CheckConfig struct {
	Type string `yaml:"type" json:"type" toml:"type" jsonschema:"required,enum=http,enum=dns,enum=ping"`

	// HTTP check fields (when type == "http")
	HTTP *HTTPCheckConfig `yaml:"http,omitempty" json:"http,omitempty" toml:"http,omitempty"`

	// DNS check fields (when type == "dns")
	DNS *DNSCheckConfig `yaml:"dns,omitempty" json:"dns,omitempty" toml:"dns,omitempty"`

	// Ping check fields (when type == "ping")
	Ping *PingCheckConfig `yaml:"ping,omitempty" json:"ping,omitempty" toml:"ping,omitempty"`
}

// HTTPCheckConfig defines an HTTP health check.
type HTTPCheckConfig struct {
	Proto         string            `yaml:"proto"          json:"proto"          toml:"proto"          jsonschema:"enum=http,enum=https,default=http"`
	Host          string            `yaml:"host"           json:"host"           toml:"host"`
	Port          uint16            `yaml:"port"           json:"port"           toml:"port"`
	URL           string            `yaml:"url"            json:"url"            toml:"url"            jsonschema:"default=/"`
	Method        string            `yaml:"method"         json:"method"         toml:"method"         jsonschema:"enum=GET,enum=HEAD,default=GET"`
	ResponseCodes []int             `yaml:"response_codes" json:"response_codes" toml:"response_codes"`
	ResponseText  string            `yaml:"response_text"  json:"response_text"  toml:"response_text"`
	SSLHostname   *bool             `yaml:"ssl_hostname"   json:"ssl_hostname"   toml:"ssl_hostname"`
	Headers       map[string]string `yaml:"headers"        json:"headers"        toml:"headers"`
}

// DNSCheckConfig defines a DNS health check.
type DNSCheckConfig struct {
	Names     []string `yaml:"names"      json:"names"      toml:"names"      jsonschema:"required,minItems=1"`
	Resolver  string   `yaml:"resolver"   json:"resolver"   toml:"resolver"`
	Port      uint16   `yaml:"port"       json:"port"       toml:"port"`
	QueryType string   `yaml:"query_type" json:"query_type" toml:"query_type" jsonschema:"enum=A,enum=AAAA,enum=CNAME,enum=PTR,enum=NS,enum=MX,enum=SOA,enum=TXT,enum=SRV,default=A"`
}

// PingCheckConfig defines an ICMP ping health check.
type PingCheckConfig struct {
	DstIP        string   `yaml:"dst_ip"         json:"dst_ip"         toml:"dst_ip"`
	SrcIP        string   `yaml:"src_ip"         json:"src_ip"         toml:"src_ip"`
	Count        int      `yaml:"count"          json:"count"          toml:"count"          jsonschema:"minimum=1,maximum=60,default=1"`
	Timeout      *Duration `yaml:"timeout"        json:"timeout"        toml:"timeout"`
	Interval     *Duration `yaml:"interval"       json:"interval"       toml:"interval"`
	MaxLossRatio float64  `yaml:"max_loss_ratio" json:"max_loss_ratio" toml:"max_loss_ratio" jsonschema:"minimum=0,maximum=1,default=0"`
}

// PolicyConfig defines how VIP state changes on check failure.
type PolicyConfig struct {
	FailAction    string             `yaml:"fail_action"    json:"fail_action"    toml:"fail_action"    jsonschema:"enum=withdraw,enum=lower_priority,default=lower_priority"`
	LowerPriority *LowerPriorityConfig `yaml:"lower_priority" json:"lower_priority,omitempty" toml:"lower_priority,omitempty"`
}

// LowerPriorityConfig defines BGP attributes for pessimized announcements.
type LowerPriorityConfig struct {
	ASPathPrepend *int     `yaml:"as_path_prepend" json:"as_path_prepend" toml:"as_path_prepend" jsonschema:"minimum=1,maximum=16,default=6"`
	Communities   []string `yaml:"communities"     json:"communities"     toml:"communities"`
}
