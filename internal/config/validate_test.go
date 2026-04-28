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
}
