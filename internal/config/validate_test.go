package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validConfig returns a minimal valid *Config with all required fields populated.
// Call validConfig() to get a fresh independent copy for each test case.
func validConfig() *Config {
	trueVal := true
	rise := 3
	fall := 3
	prepend := 6

	return &Config{
		Schema: "v1",
		Router: RouterConfig{
			ASN:      65001,
			RouterID: "10.0.0.1",
		},
		API: APIConfig{Listen: ":8080"},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "text",
		},
		BGP: BGPConfig{
			GracefulRestart: &trueVal,
			Neighbors: []NeighborConfig{
				{
					Name:     "spine-1",
					Address:  "10.0.0.254",
					PeerASN:  65000,
					Required: &trueVal,
					Port:     179,
				},
			},
		},
		VIPs: []VIPConfig{
			{
				Name:   "web-vip",
				Prefix: "10.10.1.1/32",
				Check: CheckConfig{
					Type:     CheckTypeHTTP,
					Interval: &Duration{Duration: 5 * time.Second},
					Timeout:  &Duration{Duration: 2 * time.Second},
					Rise:     &rise,
					Fall:     &fall,
					HTTP: &HTTPCheckConfig{
						URL:           "/healthz",
						Proto:         "http",
						Host:          "10.10.1.1",
						Port:          80,
						Method:        "GET",
						ResponseCodes: []int{200},
						Headers:       map[string]string{"User-Agent": "pathosd-check/1.0"},
					},
				},
				Policy: PolicyConfig{
					FailAction: "lower_priority",
					LowerPriority: &LowerPriorityConfig{
						ASPathPrepend: &prepend,
					},
				},
			},
		},
	}
}

// assertErrorContains checks that at least one error in the slice contains substr.
func assertErrorContains(t *testing.T, errs []error, substr string) {
	t.Helper()
	require.NotEmpty(t, errs, "expected at least one error containing %q, got none", substr)
	for _, e := range errs {
		if strings.Contains(e.Error(), substr) {
			return
		}
	}
	t.Errorf("no error containing %q; got:\n%v", substr, errs)
}

func TestValidate_MinimalValidConfig(t *testing.T) {
	errs := Validate(validConfig())
	assert.Empty(t, errs, "expected no validation errors for valid config")
}

func TestValidate_Schema(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		wantErr bool
	}{
		{"empty schema", "", true},
		{"wrong version v2", "v2", true},
		{"valid v1", "v1", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Schema = tc.schema
			errs := Validate(cfg)
			if tc.wantErr {
				assertErrorContains(t, errs, "schema")
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

func TestValidate_Router(t *testing.T) {
	t.Run("missing ASN", func(t *testing.T) {
		cfg := validConfig()
		cfg.Router.ASN = 0
		assertErrorContains(t, Validate(cfg), "router.asn")
	})

	t.Run("missing router_id", func(t *testing.T) {
		cfg := validConfig()
		cfg.Router.RouterID = ""
		assertErrorContains(t, Validate(cfg), "router.router_id")
	})

	t.Run("invalid router_id not an IP", func(t *testing.T) {
		cfg := validConfig()
		cfg.Router.RouterID = "notanip"
		assertErrorContains(t, Validate(cfg), "router.router_id")
	})

	t.Run("IPv6 router_id rejected", func(t *testing.T) {
		cfg := validConfig()
		cfg.Router.RouterID = "2001:db8::1"
		assertErrorContains(t, Validate(cfg), "router.router_id")
	})

	t.Run("valid local_address accepted", func(t *testing.T) {
		cfg := validConfig()
		cfg.Router.LocalAddress = "192.168.1.1"
		assert.Empty(t, Validate(cfg))
	})

	t.Run("invalid local_address", func(t *testing.T) {
		cfg := validConfig()
		cfg.Router.LocalAddress = "invalid"
		assertErrorContains(t, Validate(cfg), "router.local_address")
	})
}

func TestValidate_IPv6VIP(t *testing.T) {
	t.Run("IPv6 VIP requires IPv6 local_address_ipv6", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Prefix = "2001:db8::1/128"
		cfg.VIPs[0].Check.HTTP.Host = "2001:db8::1"
		cfg.Router.LocalAddressIPv6 = "" // not set
		assertErrorContains(t, Validate(cfg), "router.local_address_ipv6")
	})

	t.Run("IPv6 VIP rejected with IPv4 local_address_ipv6", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Prefix = "2001:db8::1/128"
		cfg.VIPs[0].Check.HTTP.Host = "2001:db8::1"
		cfg.Router.LocalAddressIPv6 = "192.168.1.1"
		assertErrorContains(t, Validate(cfg), "router.local_address_ipv6")
	})

	t.Run("IPv6 VIP with IPv6 local_address_ipv6 is valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Prefix = "2001:db8::1/128"
		cfg.VIPs[0].Check.HTTP.Host = "2001:db8::1"
		cfg.Router.LocalAddressIPv6 = "2001:db8::2"
		assert.Empty(t, Validate(cfg))
	})
}

func TestValidate_API(t *testing.T) {
	t.Run("missing listen", func(t *testing.T) {
		cfg := validConfig()
		cfg.API.Listen = ""
		assertErrorContains(t, Validate(cfg), "api.listen")
	})
}

func TestValidate_Logging(t *testing.T) {
	tests := []struct {
		name    string
		level   string
		format  string
		wantErr bool
		errKey  string
	}{
		{"valid info/text", "info", "text", false, ""},
		{"valid debug/json", "debug", "json", false, ""},
		{"valid warn/text", "warn", "text", false, ""},
		{"valid error/json", "error", "json", false, ""},
		{"invalid level verbose", "verbose", "text", true, "logging.level"},
		{"invalid level empty", "", "text", true, "logging.level"},
		{"invalid format xml", "info", "xml", true, "logging.format"},
		{"invalid format empty", "info", "", true, "logging.format"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Logging.Level = tc.level
			cfg.Logging.Format = tc.format
			errs := Validate(cfg)
			if tc.wantErr {
				assertErrorContains(t, errs, tc.errKey)
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

func TestValidate_Neighbors(t *testing.T) {
	t.Run("valid bgp.listen settings", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.ListenAddress = "127.0.0.1"
		cfg.BGP.ListenPort = 1179
		assert.Empty(t, Validate(cfg))
	})

	t.Run("invalid bgp.listen_address", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.ListenAddress = "invalid"
		assertErrorContains(t, Validate(cfg), "bgp.listen_address")
	})

	t.Run("invalid bgp.listen_port", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.ListenPort = 70000
		assertErrorContains(t, Validate(cfg), "bgp.listen_port")
	})

	t.Run("bgp.listen_port -1 disables listening", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.ListenPort = -1
		assert.Empty(t, Validate(cfg))
	})

	t.Run("bgp.listen_port -2 is invalid", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.ListenPort = -2
		assertErrorContains(t, Validate(cfg), "bgp.listen_port")
	})

	t.Run("empty neighbor list", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors = nil
		assertErrorContains(t, Validate(cfg), "bgp.neighbors")
	})

	t.Run("duplicate neighbor names", func(t *testing.T) {
		cfg := validConfig()
		dup := cfg.BGP.Neighbors[0]
		cfg.BGP.Neighbors = append(cfg.BGP.Neighbors, dup)
		assertErrorContains(t, Validate(cfg), "duplicate neighbor name")
	})

	t.Run("missing neighbor address", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors[0].Address = ""
		assertErrorContains(t, Validate(cfg), ".address")
	})

	t.Run("invalid neighbor IP address", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors[0].Address = "notanip"
		assertErrorContains(t, Validate(cfg), ".address")
	})

	t.Run("valid neighbor local_address", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors[0].LocalAddress = "127.0.0.1"
		assert.Empty(t, Validate(cfg))
	})

	t.Run("invalid neighbor local_address", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors[0].LocalAddress = "invalid"
		assertErrorContains(t, Validate(cfg), ".local_address")
	})

	t.Run("missing peer_asn", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors[0].PeerASN = 0
		assertErrorContains(t, Validate(cfg), ".peer_asn")
	})

	t.Run("missing neighbor name (empty string, reports as required)", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors[0].Name = ""
		assertErrorContains(t, Validate(cfg), ".name")
	})

	t.Run("multihop enabled without TTL is valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors[0].EnableMultihop = true
		assert.Empty(t, Validate(cfg))
	})

	t.Run("multihop enabled with TTL >= 2 is valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors[0].EnableMultihop = true
		cfg.BGP.Neighbors[0].MultihopTTL = 5
		assert.Empty(t, Validate(cfg))
	})

	t.Run("multihop enabled with TTL=1 is invalid", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors[0].EnableMultihop = true
		cfg.BGP.Neighbors[0].MultihopTTL = 1
		assertErrorContains(t, Validate(cfg), ".multihop_ttl")
	})

	t.Run("auth_password longer than 80 bytes is invalid", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors[0].AuthPassword = strings.Repeat("x", 81)
		assertErrorContains(t, Validate(cfg), ".auth_password")
	})

	t.Run("auth_password of 80 bytes is valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors[0].AuthPassword = strings.Repeat("x", 80)
		assert.Empty(t, Validate(cfg))
	})

	t.Run("multihop_ttl set without enable_multihop is invalid", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.Neighbors[0].MultihopTTL = 5
		assertErrorContains(t, Validate(cfg), ".multihop_ttl")
	})
}

func TestValidate_GoBGPAPI(t *testing.T) {
	t.Run("enabled with tcp listen address is valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.GoBGPAPI = GoBGPAPIConfig{
			Enabled: true,
			Listen:  "127.0.0.1:50051",
		}
		assert.Empty(t, Validate(cfg))
	})

	t.Run("enabled with unix socket listen is valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.GoBGPAPI = GoBGPAPIConfig{
			Enabled: true,
			Listen:  "unix:///tmp/pathosd-gobgp.sock",
		}
		assert.Empty(t, Validate(cfg))
	})

	t.Run("enabled without listen is invalid", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.GoBGPAPI = GoBGPAPIConfig{Enabled: true}
		assertErrorContains(t, Validate(cfg), "bgp.gobgp_api.listen")
	})

	t.Run("listen without enabled is invalid", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.GoBGPAPI = GoBGPAPIConfig{Listen: "127.0.0.1:50051"}
		assertErrorContains(t, Validate(cfg), "bgp.gobgp_api.listen")
	})

	t.Run("enabled with invalid listen address is invalid", func(t *testing.T) {
		cfg := validConfig()
		cfg.BGP.GoBGPAPI = GoBGPAPIConfig{
			Enabled: true,
			Listen:  "127.0.0.1",
		}
		assertErrorContains(t, Validate(cfg), "bgp.gobgp_api.listen")
	})
}

func TestValidate_VIPs(t *testing.T) {
	t.Run("empty VIP list", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs = nil
		assertErrorContains(t, Validate(cfg), "vips")
	})

	t.Run("duplicate VIP names", func(t *testing.T) {
		cfg := validConfig()
		dup := cfg.VIPs[0]
		cfg.VIPs = append(cfg.VIPs, dup)
		assertErrorContains(t, Validate(cfg), "duplicate VIP name")
	})

	t.Run("duplicate VIP prefixes", func(t *testing.T) {
		cfg := validConfig()
		dup := cfg.VIPs[0]
		dup.Name = "web-vip-2"
		cfg.VIPs = append(cfg.VIPs, dup)
		assertErrorContains(t, Validate(cfg), "duplicate VIP prefix")
	})

	t.Run("invalid CIDR prefix", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Prefix = "notacidr"
		assertErrorContains(t, Validate(cfg), ".prefix")
	})

	t.Run("missing VIP name", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Name = ""
		assertErrorContains(t, Validate(cfg), ".name")
	})
}

func TestValidate_CheckCommon(t *testing.T) {
	t.Run("missing check type", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = ""
		assertErrorContains(t, Validate(cfg), ".type")
	})

	t.Run("unsupported check type", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = "ftp"
		assertErrorContains(t, Validate(cfg), ".type")
	})

	t.Run("timeout equal to interval", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.Timeout = &Duration{Duration: 5 * time.Second}
		cfg.VIPs[0].Check.Interval = &Duration{Duration: 5 * time.Second}
		assertErrorContains(t, Validate(cfg), ".check.timeout")
	})

	t.Run("timeout greater than interval", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.Timeout = &Duration{Duration: 6 * time.Second}
		cfg.VIPs[0].Check.Interval = &Duration{Duration: 5 * time.Second}
		assertErrorContains(t, Validate(cfg), ".check.timeout")
	})

	t.Run("rise less than 1", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.Rise = new(0)
		assertErrorContains(t, Validate(cfg), ".check.rise")
	})

	t.Run("fall less than 1", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.Fall = new(0)
		assertErrorContains(t, Validate(cfg), ".check.fall")
	})
}

func TestValidate_MultiCheck(t *testing.T) {
	multi := func() *Config {
		cfg := validConfig()
		single := cfg.VIPs[0].Check
		cfg.VIPs[0].Check = CheckConfig{}
		cfg.VIPs[0].Checks = []CheckConfig{
			single,
			{Type: CheckTypeDNS, DNS: &DNSCheckConfig{Names: []string{"example.test."}, QueryType: "A"}},
		}
		return cfg
	}

	t.Run("checks list is valid", func(t *testing.T) {
		assert.Empty(t, Validate(multi()))
	})

	t.Run("check and checks together", func(t *testing.T) {
		cfg := multi()
		cfg.VIPs[0].Check = CheckConfig{Type: CheckTypeTCP, TCP: &TCPCheckConfig{Port: 80}}
		assertErrorContains(t, Validate(cfg), "vips[0].checks: cannot be combined with check")
	})

	t.Run("neither check nor checks", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check = CheckConfig{}
		assertErrorContains(t, Validate(cfg), "vips[0].check: required")
	})

	t.Run("same type twice needs names", func(t *testing.T) {
		cfg := multi()
		cfg.VIPs[0].Checks[1] = cfg.VIPs[0].Checks[0]
		assertErrorContains(t, Validate(cfg), "vips[0].checks[1].name: required")
	})

	t.Run("duplicate explicit names", func(t *testing.T) {
		cfg := multi()
		cfg.VIPs[0].Checks[0].Name = "a"
		cfg.VIPs[0].Checks[1].Name = "a"
		assertErrorContains(t, Validate(cfg), `vips[0].checks[1].name: duplicate check name "a"`)
	})

	t.Run("invalid and reserved names", func(t *testing.T) {
		cfg := multi()
		cfg.VIPs[0].Checks[0].Name = "web-primary"
		cfg.VIPs[0].Checks[1].Name = "any"
		errs := Validate(cfg)
		assertErrorContains(t, errs, "vips[0].checks[0].name: must match")
		assertErrorContains(t, errs, `vips[0].checks[1].name: "any" is reserved`)
	})

	t.Run("per-check interval and timeout errors carry the index", func(t *testing.T) {
		cfg := multi()
		cfg.VIPs[0].Checks[1].Interval = &Duration{Duration: time.Second}
		cfg.VIPs[0].Checks[1].Timeout = &Duration{Duration: time.Second}
		assertErrorContains(t, Validate(cfg), "vips[0].checks[1].timeout")
	})

	t.Run("policy all and any", func(t *testing.T) {
		for _, p := range []string{ChecksPolicyAll, ChecksPolicyAny} {
			cfg := multi()
			cfg.VIPs[0].ChecksPolicy = p
			assert.Empty(t, Validate(cfg), p)
		}
	})

	t.Run("policy expression over names", func(t *testing.T) {
		cfg := multi()
		cfg.VIPs[0].ChecksPolicy = "http || dns"
		assert.Empty(t, Validate(cfg))
	})

	t.Run("policy expression with unknown name", func(t *testing.T) {
		cfg := multi()
		cfg.VIPs[0].ChecksPolicy = "http || tcp"
		assertErrorContains(t, Validate(cfg), "vips[0].checks_policy: unknown name tcp")
	})

	t.Run("policy expression with a single check", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].ChecksPolicy = "http"
		assertErrorContains(t, Validate(cfg), "vips[0].checks_policy: an expression needs checks")
	})
}

func TestValidate_HTTPCheck(t *testing.T) {
	t.Run("nil http config when type is http", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP = nil
		assertErrorContains(t, Validate(cfg), ".http")
	})

	t.Run("invalid proto ftp", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.Proto = "ftp"
		assertErrorContains(t, Validate(cfg), ".proto")
	})

	t.Run("HEAD cannot match a body", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.Method = "HEAD"
		cfg.VIPs[0].Check.HTTP.ResponseText = "ok"
		cfg.VIPs[0].Check.HTTP.ResponseRegex = "ok"
		cfg.VIPs[0].Check.HTTP.ResponseJQ = ".ok"
		errs := Validate(cfg)
		assertErrorContains(t, errs, ".response_text: cannot be used with method HEAD")
		assertErrorContains(t, errs, ".response_regex: cannot be used with method HEAD")
		assertErrorContains(t, errs, ".response_jq: cannot be used with method HEAD")
		cfg.VIPs[0].Check.HTTP.ResponseText, cfg.VIPs[0].Check.HTTP.ResponseRegex, cfg.VIPs[0].Check.HTTP.ResponseJQ = "", "", ""
		assert.Empty(t, Validate(cfg), "HEAD with status codes only is fine")
	})

	t.Run("invalid method POST", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.Method = "POST"
		assertErrorContains(t, Validate(cfg), ".method")
	})

	t.Run("empty method is invalid", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.Method = ""
		assertErrorContains(t, Validate(cfg), ".method")
	})

	t.Run("host required for non-/32 prefix", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Prefix = "10.10.1.0/24"
		cfg.VIPs[0].Check.HTTP.Host = ""
		assertErrorContains(t, Validate(cfg), ".host")
	})

	t.Run("host not required for /32 prefix", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.Host = ""
		assert.Empty(t, Validate(cfg))
	})

	t.Run("invalid response_regex", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.ResponseRegex = "[invalid"
		assertErrorContains(t, Validate(cfg), ".response_regex")
	})

	t.Run("valid response_regex accepted", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.ResponseRegex = `^ok$`
		assert.Empty(t, Validate(cfg))
	})

	t.Run("tls_ca_cert and tls_insecure mutually exclusive", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.TLSCACert = "/etc/ssl/ca.crt"
		cfg.VIPs[0].Check.HTTP.TLSInsecure = true
		assertErrorContains(t, Validate(cfg), ".tls_ca_cert")
	})

	t.Run("tls_ca_cert alone is valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.TLSCACert = "/etc/ssl/ca.crt"
		assert.Empty(t, Validate(cfg))
	})

	t.Run("tls_insecure alone is valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.TLSInsecure = true
		assert.Empty(t, Validate(cfg))
	})

	t.Run("valid proxy URL with hostname origin", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.Proxy = "http://proxy.example:3128"
		cfg.VIPs[0].Check.HTTP.Host = "example.com"
		assert.Empty(t, Validate(cfg))
	})

	t.Run("invalid proxy scheme", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.Proxy = "socks5://proxy.example:1080"
		cfg.VIPs[0].Check.HTTP.Host = "example.com"
		assertErrorContains(t, Validate(cfg), ".proxy")
	})

	t.Run("proxy requires hostname origin, not VIP IP", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.Proxy = "http://proxy.example:3128"
		// Host left as the VIP IP default (10.10.1.1) — a forward proxy fetch
		// target must be a resolvable hostname, so this must be rejected.
		assertErrorContains(t, Validate(cfg), ".proxy")
	})

	t.Run("proxy with empty host is invalid", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.HTTP.Proxy = "http://proxy.example:3128"
		cfg.VIPs[0].Check.HTTP.Host = ""
		assertErrorContains(t, Validate(cfg), ".proxy")
	})
}

func TestValidate_DNSCheck(t *testing.T) {
	makeDNSConfig := func() *Config {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = CheckTypeDNS
		cfg.VIPs[0].Check.HTTP = nil
		cfg.VIPs[0].Check.DNS = &DNSCheckConfig{
			Names:     []string{"example.com"},
			QueryType: "A",
		}
		return cfg
	}

	t.Run("valid DNS config", func(t *testing.T) {
		assert.Empty(t, Validate(makeDNSConfig()))
	})

	t.Run("nil dns config when type is dns", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = CheckTypeDNS
		cfg.VIPs[0].Check.HTTP = nil
		assertErrorContains(t, Validate(cfg), ".dns")
	})

	t.Run("empty names list", func(t *testing.T) {
		cfg := makeDNSConfig()
		cfg.VIPs[0].Check.DNS.Names = nil
		assertErrorContains(t, Validate(cfg), ".dns.names")
	})

	t.Run("invalid query_type", func(t *testing.T) {
		cfg := makeDNSConfig()
		cfg.VIPs[0].Check.DNS.QueryType = "INVALID"
		assertErrorContains(t, Validate(cfg), ".dns.query_type")
	})

	t.Run("empty query_type is invalid", func(t *testing.T) {
		cfg := makeDNSConfig()
		cfg.VIPs[0].Check.DNS.QueryType = ""
		assertErrorContains(t, Validate(cfg), ".dns.query_type")
	})
}

func TestValidate_PingCheck(t *testing.T) {
	makePingConfig := func() *Config {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = CheckTypePing
		cfg.VIPs[0].Check.HTTP = nil
		cfg.VIPs[0].Check.Ping = &PingCheckConfig{Count: 1}
		return cfg
	}

	t.Run("valid ping config", func(t *testing.T) {
		assert.Empty(t, Validate(makePingConfig()))
	})

	t.Run("nil ping config when type is ping", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = CheckTypePing
		cfg.VIPs[0].Check.HTTP = nil
		assertErrorContains(t, Validate(cfg), ".ping")
	})

	t.Run("count zero", func(t *testing.T) {
		cfg := makePingConfig()
		cfg.VIPs[0].Check.Ping.Count = 0
		assertErrorContains(t, Validate(cfg), ".ping.count")
	})

	t.Run("count above 60", func(t *testing.T) {
		cfg := makePingConfig()
		cfg.VIPs[0].Check.Ping.Count = 61
		assertErrorContains(t, Validate(cfg), ".ping.count")
	})

	t.Run("count at boundary 60 is valid", func(t *testing.T) {
		cfg := makePingConfig()
		cfg.VIPs[0].Check.Ping.Count = 60
		assert.Empty(t, Validate(cfg))
	})

	t.Run("max_loss_ratio negative", func(t *testing.T) {
		cfg := makePingConfig()
		cfg.VIPs[0].Check.Ping.MaxLossRatio = -0.1
		assertErrorContains(t, Validate(cfg), ".ping.max_loss_ratio")
	})

	t.Run("max_loss_ratio exactly 1.0 is invalid", func(t *testing.T) {
		cfg := makePingConfig()
		cfg.VIPs[0].Check.Ping.MaxLossRatio = 1.0
		assertErrorContains(t, Validate(cfg), ".ping.max_loss_ratio")
	})

	t.Run("max_loss_ratio 0.5 is valid", func(t *testing.T) {
		cfg := makePingConfig()
		cfg.VIPs[0].Check.Ping.MaxLossRatio = 0.5
		assert.Empty(t, Validate(cfg))
	})

	t.Run("dst_ip required when prefix is not a single host", func(t *testing.T) {
		cfg := makePingConfig()
		cfg.VIPs[0].Prefix = "10.0.0.0/24"
		assertErrorContains(t, Validate(cfg), ".ping.dst_ip")
	})

	t.Run("dst_ip optional for /32 prefix", func(t *testing.T) {
		cfg := makePingConfig()
		cfg.VIPs[0].Check.Ping.DstIP = ""
		assert.Empty(t, Validate(cfg))
	})

	t.Run("dst_ip must be an IP address", func(t *testing.T) {
		cfg := makePingConfig()
		cfg.VIPs[0].Check.Ping.DstIP = "gateway.example"
		assertErrorContains(t, Validate(cfg), ".ping.dst_ip")
	})

	t.Run("src_ip must be an IP address", func(t *testing.T) {
		cfg := makePingConfig()
		cfg.VIPs[0].Check.Ping.SrcIP = "not-an-ip"
		assertErrorContains(t, Validate(cfg), ".ping.src_ip")
	})
}

func TestValidate_UDPCheck(t *testing.T) {
	makeUDPConfig := func() *Config {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = CheckTypeUDP
		cfg.VIPs[0].Check.HTTP = nil
		cfg.VIPs[0].Check.UDP = &UDPCheckConfig{Host: "203.0.113.1", Port: 514}
		return cfg
	}

	t.Run("valid udp config", func(t *testing.T) {
		assert.Empty(t, Validate(makeUDPConfig()))
	})

	t.Run("nil udp config when type is udp", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = CheckTypeUDP
		cfg.VIPs[0].Check.HTTP = nil
		assertErrorContains(t, Validate(cfg), ".udp")
	})

	t.Run("port zero is required", func(t *testing.T) {
		cfg := makeUDPConfig()
		cfg.VIPs[0].Check.UDP.Port = 0
		assertErrorContains(t, Validate(cfg), ".udp.port")
	})

	t.Run("host required for non-/32 prefix", func(t *testing.T) {
		cfg := makeUDPConfig()
		cfg.VIPs[0].Prefix = "203.0.113.0/24"
		cfg.VIPs[0].Check.UDP.Host = ""
		assertErrorContains(t, Validate(cfg), ".udp.host")
	})

	t.Run("host not required for /32 prefix (defaults to VIP IP)", func(t *testing.T) {
		cfg := makeUDPConfig()
		cfg.VIPs[0].Check.UDP.Host = ""
		// Defaults should fill Host; validation checks post-defaults state.
		// Since validateCheck is called before ApplyDefaults in tests, we just
		// verify no error when host is set.
		cfg.VIPs[0].Check.UDP.Host = "203.0.113.1"
		assert.Empty(t, Validate(cfg))
	})
}

func TestValidate_ListenCheck(t *testing.T) {
	makeListen := func() *Config {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = CheckTypeListen
		cfg.VIPs[0].Check.HTTP = nil
		cfg.VIPs[0].Check.Listen = &ListenCheckConfig{Proto: "udp", Host: "203.0.113.1", Port: 4739}
		return cfg
	}

	t.Run("valid", func(t *testing.T) {
		assert.Empty(t, Validate(makeListen()))
	})
	t.Run("nil listen config", func(t *testing.T) {
		cfg := makeListen()
		cfg.VIPs[0].Check.Listen = nil
		assertErrorContains(t, Validate(cfg), ".listen: required")
	})
	t.Run("proto", func(t *testing.T) {
		cfg := makeListen()
		cfg.VIPs[0].Check.Listen.Proto = "sctp"
		assertErrorContains(t, Validate(cfg), ".listen.proto")
	})
	t.Run("port", func(t *testing.T) {
		cfg := makeListen()
		cfg.VIPs[0].Check.Listen.Port = 0
		assertErrorContains(t, Validate(cfg), ".listen.port")
	})
	t.Run("host must be an IP", func(t *testing.T) {
		cfg := makeListen()
		cfg.VIPs[0].Check.Listen.Host = "collector.example"
		assertErrorContains(t, Validate(cfg), ".listen.host: must be an IP")
	})
	t.Run("netns_pid negative", func(t *testing.T) {
		cfg := makeListen()
		cfg.VIPs[0].Check.Listen.NetnsPID = -1
		assertErrorContains(t, Validate(cfg), ".listen.netns_pid")
	})
	t.Run("host required for non-/32 prefix", func(t *testing.T) {
		cfg := makeListen()
		cfg.VIPs[0].Prefix = "203.0.113.0/24"
		cfg.VIPs[0].Check.Listen.Host = ""
		assertErrorContains(t, Validate(cfg), ".listen.host")
	})
}

func TestValidate_KafkaCheck(t *testing.T) {
	makeKafka := func() *Config {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = CheckTypeKafka
		cfg.VIPs[0].Check.HTTP = nil
		cfg.VIPs[0].Check.Kafka = &KafkaCheckConfig{Host: "203.0.113.1", Port: 9092, SASL: &KafkaSASLConfig{Mechanism: KafkaSASLScramSHA512, User: "u", Password: "p"}}
		return cfg
	}

	t.Run("valid", func(t *testing.T) {
		assert.Empty(t, Validate(makeKafka()))
		cfg := makeKafka()
		cfg.VIPs[0].Check.Kafka.SASL = nil
		assert.Empty(t, Validate(cfg), "sasl is optional")
	})
	t.Run("nil kafka config", func(t *testing.T) {
		cfg := makeKafka()
		cfg.VIPs[0].Check.Kafka = nil
		assertErrorContains(t, Validate(cfg), ".kafka: required")
	})
	t.Run("host required for non-/32 prefix", func(t *testing.T) {
		cfg := makeKafka()
		cfg.VIPs[0].Prefix = "203.0.113.0/24"
		cfg.VIPs[0].Check.Kafka.Host = ""
		assertErrorContains(t, Validate(cfg), ".kafka.host")
	})
	t.Run("sasl fields", func(t *testing.T) {
		cfg := makeKafka()
		cfg.VIPs[0].Check.Kafka.SASL = &KafkaSASLConfig{}
		errs := Validate(cfg)
		assertErrorContains(t, errs, ".kafka.sasl.mechanism: required")
		assertErrorContains(t, errs, ".kafka.sasl.user: required")
		assertErrorContains(t, errs, ".kafka.sasl.password: required")
		cfg.VIPs[0].Check.Kafka.SASL.Mechanism = "gssapi"
		assertErrorContains(t, Validate(cfg), ".kafka.sasl.mechanism: must be plain, scram-sha-256 or scram-sha-512")
	})
	t.Run("tls options need tls", func(t *testing.T) {
		cfg := makeKafka()
		cfg.VIPs[0].Check.Kafka.TLSInsecure = true
		cfg.VIPs[0].Check.Kafka.TLSCACert = "/ca.pem"
		errs := Validate(cfg)
		assertErrorContains(t, errs, ".kafka.tls_insecure: requires tls")
		assertErrorContains(t, errs, ".kafka.tls_ca_cert: requires tls")
		cfg.VIPs[0].Check.Kafka.TLS = true
		assertErrorContains(t, Validate(cfg), ".kafka.tls_insecure: mutually exclusive with tls_ca_cert")
	})
}

func TestValidate_SQLCheck(t *testing.T) {
	makeSQL := func() *Config {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = CheckTypeSQL
		cfg.VIPs[0].Check.HTTP = nil
		cfg.VIPs[0].Check.SQL = &SQLCheckConfig{Driver: SQLDriverMySQL, Host: "203.0.113.1", User: "pathosd", Queries: []SQLQueryConfig{
			{SQL: "SELECT 1"},
			{Name: "ro", SQL: "SELECT @@read_only AS ro", ResultJQ: ".[0].ro == 0"},
		}}
		return cfg
	}

	t.Run("valid", func(t *testing.T) {
		assert.Empty(t, Validate(makeSQL()))
	})
	t.Run("nil sql config", func(t *testing.T) {
		cfg := makeSQL()
		cfg.VIPs[0].Check.SQL = nil
		assertErrorContains(t, Validate(cfg), ".sql: required")
	})
	t.Run("driver", func(t *testing.T) {
		cfg := makeSQL()
		cfg.VIPs[0].Check.SQL.Driver = ""
		assertErrorContains(t, Validate(cfg), ".sql.driver: required")
		cfg.VIPs[0].Check.SQL.Driver = "oracle"
		assertErrorContains(t, Validate(cfg), ".sql.driver: must be mysql or postgres")
	})
	t.Run("user required", func(t *testing.T) {
		cfg := makeSQL()
		cfg.VIPs[0].Check.SQL.User = ""
		assertErrorContains(t, Validate(cfg), ".sql.user: required")
	})
	t.Run("host required for non-/32 prefix", func(t *testing.T) {
		cfg := makeSQL()
		cfg.VIPs[0].Prefix = "203.0.113.0/24"
		cfg.VIPs[0].Check.SQL.Host = ""
		assertErrorContains(t, Validate(cfg), ".sql.host")
	})
	t.Run("tls options need tls", func(t *testing.T) {
		cfg := makeSQL()
		cfg.VIPs[0].Check.SQL.TLSInsecure = true
		cfg.VIPs[0].Check.SQL.TLSCACert = "/ca.pem"
		cfg.VIPs[0].Check.SQL.TLSServerName = "db"
		errs := Validate(cfg)
		assertErrorContains(t, errs, ".sql.tls_insecure: requires tls")
		assertErrorContains(t, errs, ".sql.tls_ca_cert: requires tls")
		assertErrorContains(t, errs, ".sql.tls_server_name: requires tls")
		cfg.VIPs[0].Check.SQL.TLS = true
		assertErrorContains(t, Validate(cfg), ".sql.tls_insecure: mutually exclusive with tls_ca_cert")
	})
	t.Run("queries", func(t *testing.T) {
		cfg := makeSQL()
		cfg.VIPs[0].Check.SQL.Queries[0].SQL = "  "
		assertErrorContains(t, Validate(cfg), ".sql.queries[0].sql: required")
		cfg = makeSQL()
		cfg.VIPs[0].Check.SQL.Queries[1].ResultJQ = ".[0] =="
		assertErrorContains(t, Validate(cfg), ".sql.queries[1].result_jq: invalid jq")
		cfg = makeSQL()
		cfg.VIPs[0].Check.SQL.Queries[1].Name = "q1"
		assertErrorContains(t, Validate(cfg), `.sql.queries[1].name: duplicate query name "q1"`)
	})
}

func TestValidate_TCPCheck(t *testing.T) {
	makeTCPConfig := func() *Config {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = CheckTypeTCP
		cfg.VIPs[0].Check.HTTP = nil
		cfg.VIPs[0].Check.TCP = &TCPCheckConfig{Host: "203.0.113.1", Port: 8080}
		return cfg
	}

	t.Run("valid tcp config", func(t *testing.T) {
		assert.Empty(t, Validate(makeTCPConfig()))
	})

	t.Run("nil tcp config when type is tcp", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Check.Type = CheckTypeTCP
		cfg.VIPs[0].Check.HTTP = nil
		assertErrorContains(t, Validate(cfg), ".tcp")
	})

	t.Run("port zero is required", func(t *testing.T) {
		cfg := makeTCPConfig()
		cfg.VIPs[0].Check.TCP.Port = 0
		assertErrorContains(t, Validate(cfg), ".tcp.port")
	})

	t.Run("host required for non-/32 prefix", func(t *testing.T) {
		cfg := makeTCPConfig()
		cfg.VIPs[0].Prefix = "203.0.113.0/24"
		cfg.VIPs[0].Check.TCP.Host = ""
		assertErrorContains(t, Validate(cfg), ".tcp.host")
	})

	t.Run("host not required for /32 prefix", func(t *testing.T) {
		cfg := makeTCPConfig()
		cfg.VIPs[0].Check.TCP.Host = "203.0.113.1"
		assert.Empty(t, Validate(cfg))
	})
}

func TestValidate_OTel(t *testing.T) {
	t.Run("disabled when endpoint empty", func(t *testing.T) {
		cfg := validConfig()
		cfg.OTel = OTelConfig{}
		assert.Empty(t, Validate(cfg))
	})

	t.Run("valid grpc config", func(t *testing.T) {
		cfg := validConfig()
		cfg.OTel = OTelConfig{Endpoint: "grpc://localhost:4317", ServiceName: "pathosd"}
		assert.Empty(t, Validate(cfg))
	})

	t.Run("valid http config", func(t *testing.T) {
		cfg := validConfig()
		cfg.OTel = OTelConfig{Endpoint: "http://localhost:4318", ServiceName: "pathosd"}
		assert.Empty(t, Validate(cfg))
	})

	t.Run("invalid endpoint scheme", func(t *testing.T) {
		cfg := validConfig()
		cfg.OTel = OTelConfig{Endpoint: "tcp://localhost:4317", ServiceName: "pathosd"}
		assertErrorContains(t, Validate(cfg), ".otel.endpoint")
	})

	t.Run("empty service_name", func(t *testing.T) {
		cfg := validConfig()
		cfg.OTel = OTelConfig{Endpoint: "grpc://localhost:4317", ServiceName: ""}
		assertErrorContains(t, Validate(cfg), ".otel.service_name")
	})
}

func TestValidate_Policy(t *testing.T) {
	t.Run("empty fail_action", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Policy.FailAction = ""
		assertErrorContains(t, Validate(cfg), ".fail_action")
	})

	t.Run("invalid fail_action ignore", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Policy.FailAction = "ignore"
		assertErrorContains(t, Validate(cfg), ".fail_action")
	})

	t.Run("lower_priority block present with withdraw is allowed", func(t *testing.T) {
		prepend := 6
		cfg := validConfig()
		cfg.VIPs[0].Policy.FailAction = "withdraw"
		cfg.VIPs[0].Policy.LowerPriority = &LowerPriorityConfig{ASPathPrepend: &prepend}
		assert.Empty(t, Validate(cfg))
	})

	t.Run("valid withdraw without lower_priority", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Policy.FailAction = "withdraw"
		cfg.VIPs[0].Policy.LowerPriority = nil
		assert.Empty(t, Validate(cfg))
	})

	t.Run("as_path_prepend zero is out of range", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Policy.LowerPriority.ASPathPrepend = new(0)
		assertErrorContains(t, Validate(cfg), ".as_path_prepend")
	})

	t.Run("as_path_prepend 17 is out of range", func(t *testing.T) {
		val := 17
		cfg := validConfig()
		cfg.VIPs[0].Policy.LowerPriority.ASPathPrepend = &val
		assertErrorContains(t, Validate(cfg), ".as_path_prepend")
	})

	t.Run("as_path_prepend at boundary 16 is valid", func(t *testing.T) {
		val := 16
		cfg := validConfig()
		cfg.VIPs[0].Policy.LowerPriority.ASPathPrepend = &val
		assert.Empty(t, Validate(cfg))
	})

	t.Run("as_path_prepend at boundary 1 is valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Policy.LowerPriority.ASPathPrepend = new(1)
		assert.Empty(t, Validate(cfg))
	})

	t.Run("announce.as_path_prepend 0 is valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Policy.Announce = &AnnounceConfig{ASPathPrepend: new(0)}
		assert.Empty(t, Validate(cfg))
	})

	t.Run("announce.as_path_prepend 16 is valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Policy.Announce = &AnnounceConfig{ASPathPrepend: new(16)}
		assert.Empty(t, Validate(cfg))
	})

	t.Run("announce.as_path_prepend negative is out of range", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Policy.Announce = &AnnounceConfig{ASPathPrepend: new(-1)}
		assertErrorContains(t, Validate(cfg), ".announce.as_path_prepend")
	})

	t.Run("announce.as_path_prepend 17 is out of range", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Policy.Announce = &AnnounceConfig{ASPathPrepend: new(17)}
		assertErrorContains(t, Validate(cfg), ".announce.as_path_prepend")
	})

	t.Run("announce.communities accepts ASN:value", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Policy.Announce = &AnnounceConfig{Communities: []string{"65535:100", "0:0"}}
		assert.Empty(t, Validate(cfg))
	})

	t.Run("announce.communities rejects malformed entries", func(t *testing.T) {
		for _, bad := range []string{"65535", "65536:1", "1:65536", "a:b", "1:2:3"} {
			cfg := validConfig()
			cfg.VIPs[0].Policy.Announce = &AnnounceConfig{Communities: []string{bad}}
			assertErrorContains(t, Validate(cfg), ".announce.communities[0]")
		}
	})

	t.Run("lower_priority.communities rejects malformed entries", func(t *testing.T) {
		cfg := validConfig()
		cfg.VIPs[0].Policy.LowerPriority.Communities = []string{"65535:666", "nope"}
		assertErrorContains(t, Validate(cfg), ".lower_priority.communities[1]")
	})
}
