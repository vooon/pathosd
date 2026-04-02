package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestVipHostIP(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   string
	}{
		{"IPv4 /32", "10.0.0.1/32", "10.0.0.1"},
		{"IPv4 /24", "10.0.0.0/24", ""},
		{"IPv4 /16", "10.0.0.0/16", ""},
		{"IPv6 /128", "2001:db8::1/128", "2001:db8::1"},
		{"IPv6 /64", "2001:db8::/64", ""},
		{"invalid", "notacidr", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, vipHostIP(tc.prefix))
		})
	}
}

func TestApplyDefaults_API(t *testing.T) {
	t.Run("empty Listen defaults to :59179", func(t *testing.T) {
		cfg := &Config{}
		ApplyDefaults(cfg)
		assert.Equal(t, ":59179", cfg.API.Listen)
	})

	t.Run("explicit Listen preserved", func(t *testing.T) {
		cfg := &Config{API: APIConfig{Listen: "127.0.0.1:59179"}}
		ApplyDefaults(cfg)
		assert.Equal(t, "127.0.0.1:59179", cfg.API.Listen)
	})
}

func TestApplyDefaults_Logging(t *testing.T) {
	t.Run("empty values get defaults", func(t *testing.T) {
		cfg := &Config{}
		ApplyDefaults(cfg)
		assert.Equal(t, "info", cfg.Logging.Level)
		assert.Equal(t, "text", cfg.Logging.Format)
	})

	t.Run("non-empty values preserved", func(t *testing.T) {
		cfg := &Config{
			Logging: LoggingConfig{Level: "debug", Format: "json"},
		}
		ApplyDefaults(cfg)
		assert.Equal(t, "debug", cfg.Logging.Level)
		assert.Equal(t, "json", cfg.Logging.Format)
	})
}

func TestApplyDefaults_BGP(t *testing.T) {
	t.Run("GracefulRestart nil gets true", func(t *testing.T) {
		cfg := &Config{}
		ApplyDefaults(cfg)
		assert.NotNil(t, cfg.BGP.GracefulRestart)
		assert.True(t, *cfg.BGP.GracefulRestart)
	})

	t.Run("GracefulRestart false preserved", func(t *testing.T) {
		cfg := &Config{BGP: BGPConfig{GracefulRestart: new(false)}}
		ApplyDefaults(cfg)
		assert.NotNil(t, cfg.BGP.GracefulRestart)
		assert.False(t, *cfg.BGP.GracefulRestart)
	})

	t.Run("neighbor Port defaults to 179", func(t *testing.T) {
		cfg := &Config{
			BGP: BGPConfig{
				Neighbors: []NeighborConfig{{Name: "n1"}},
			},
		}
		ApplyDefaults(cfg)
		assert.Equal(t, uint16(179), cfg.BGP.Neighbors[0].Port)
	})

	t.Run("neighbor Port preserved when set", func(t *testing.T) {
		cfg := &Config{
			BGP: BGPConfig{
				Neighbors: []NeighborConfig{{Name: "n1", Port: 1179}},
			},
		}
		ApplyDefaults(cfg)
		assert.Equal(t, uint16(1179), cfg.BGP.Neighbors[0].Port)
	})

	t.Run("neighbor Required nil gets true", func(t *testing.T) {
		cfg := &Config{
			BGP: BGPConfig{
				Neighbors: []NeighborConfig{{Name: "n1"}},
			},
		}
		ApplyDefaults(cfg)
		assert.NotNil(t, cfg.BGP.Neighbors[0].Required)
		assert.True(t, *cfg.BGP.Neighbors[0].Required)
	})

	t.Run("neighbor Required false preserved", func(t *testing.T) {
		cfg := &Config{
			BGP: BGPConfig{
				Neighbors: []NeighborConfig{{Name: "n1", Required: new(false)}},
			},
		}
		ApplyDefaults(cfg)
		assert.NotNil(t, cfg.BGP.Neighbors[0].Required)
		assert.False(t, *cfg.BGP.Neighbors[0].Required)
	})
}

// makeVIPWithHTTP returns a minimal Config with a single VIP using an HTTP check.
func makeVIPWithHTTP(h HTTPCheckConfig, prefix string) *Config {
	return &Config{
		VIPs: []VIPConfig{{
			Name:   "v",
			Prefix: prefix,
			Check:  CheckConfig{Type: CheckTypeHTTP, HTTP: &h},
		}},
	}
}

func TestApplyDefaults_CheckCommon(t *testing.T) {
	t.Run("Interval defaults to 1s", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, 1*time.Second, cfg.VIPs[0].Check.Interval.Duration)
	})

	t.Run("Interval preserved", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		cfg.VIPs[0].Check.Interval = &Duration{Duration: 5 * time.Second}
		ApplyDefaults(cfg)
		assert.Equal(t, 5*time.Second, cfg.VIPs[0].Check.Interval.Duration)
	})

	t.Run("Timeout defaults to 100ms", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, 100*time.Millisecond, cfg.VIPs[0].Check.Timeout.Duration)
	})

	t.Run("Timeout preserved", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		cfg.VIPs[0].Check.Timeout = &Duration{Duration: 500 * time.Millisecond}
		ApplyDefaults(cfg)
		assert.Equal(t, 500*time.Millisecond, cfg.VIPs[0].Check.Timeout.Duration)
	})

	t.Run("Rise defaults to 1", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, 1, *cfg.VIPs[0].Check.Rise)
	})

	t.Run("Rise preserved", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		cfg.VIPs[0].Check.Rise = new(5)
		ApplyDefaults(cfg)
		assert.Equal(t, 5, *cfg.VIPs[0].Check.Rise)
	})

	t.Run("Fall defaults to 3", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, 3, *cfg.VIPs[0].Check.Fall)
	})

	t.Run("Fall preserved", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		cfg.VIPs[0].Check.Fall = new(2)
		ApplyDefaults(cfg)
		assert.Equal(t, 2, *cfg.VIPs[0].Check.Fall)
	})
}

func TestApplyDefaults_HTTPCheck(t *testing.T) {
	t.Run("URL defaults to /", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "/", cfg.VIPs[0].Check.HTTP.URL)
	})

	t.Run("URL preserved", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/api/health"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "/api/health", cfg.VIPs[0].Check.HTTP.URL)
	})

	t.Run("Proto defaults to http", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "http", cfg.VIPs[0].Check.HTTP.Proto)
	})

	t.Run("Port defaults to 80 for http", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, uint16(80), cfg.VIPs[0].Check.HTTP.Port)
	})

	t.Run("Port defaults to 443 for https", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/", Proto: "https"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, uint16(443), cfg.VIPs[0].Check.HTTP.Port)
	})

	t.Run("Port preserved", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/", Port: 8080}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, uint16(8080), cfg.VIPs[0].Check.HTTP.Port)
	})

	t.Run("Method defaults to GET", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "GET", cfg.VIPs[0].Check.HTTP.Method)
	})

	t.Run("Method preserved", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/", Method: "HEAD"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "HEAD", cfg.VIPs[0].Check.HTTP.Method)
	})

	t.Run("ResponseCodes defaults to [200]", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, []int{200}, cfg.VIPs[0].Check.HTTP.ResponseCodes)
	})

	t.Run("ResponseCodes preserved", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/", ResponseCodes: []int{200, 204}}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, []int{200, 204}, cfg.VIPs[0].Check.HTTP.ResponseCodes)
	})

	t.Run("User-Agent header set", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "pathosd-check/1.0", cfg.VIPs[0].Check.HTTP.Headers["User-Agent"])
	})

	t.Run("User-Agent preserved when set", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{
			URL:     "/",
			Headers: map[string]string{"User-Agent": "my-agent/2.0"},
		}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "my-agent/2.0", cfg.VIPs[0].Check.HTTP.Headers["User-Agent"])
	})

	t.Run("Accept header set when ResponseJQ non-empty", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/", ResponseJQ: ".status"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "application/json", cfg.VIPs[0].Check.HTTP.Headers["Accept"])
	})

	t.Run("Accept header not set when ResponseJQ empty", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		_, hasAccept := cfg.VIPs[0].Check.HTTP.Headers["Accept"]
		assert.False(t, hasAccept)
	})

	t.Run("Accept preserved when ResponseJQ set", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{
			URL:        "/",
			ResponseJQ: ".status",
			Headers:    map[string]string{"Accept": "application/vnd.api+json"},
		}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "application/vnd.api+json", cfg.VIPs[0].Check.HTTP.Headers["Accept"])
	})
}

func TestApplyDefaults_HTTPCheck_FullURL(t *testing.T) {
	t.Run("https full URL derives proto and Host header and rewrites path", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "https://example.com/healthz"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		h := cfg.VIPs[0].Check.HTTP
		assert.Equal(t, "https", h.Proto)
		assert.Equal(t, "/healthz", h.URL)
		assert.Equal(t, "example.com", h.Headers["Host"])
		assert.Equal(t, uint16(443), h.Port)
	})

	t.Run("http full URL sets Host header", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "http://myhost.local/check"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		h := cfg.VIPs[0].Check.HTTP
		assert.Equal(t, "http", h.Proto)
		assert.Equal(t, "/check", h.URL)
		assert.Equal(t, "myhost.local", h.Headers["Host"])
		assert.Equal(t, uint16(80), h.Port)
	})

	t.Run("full URL preserves explicit Proto", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "https://example.com/healthz", Proto: "http"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "http", cfg.VIPs[0].Check.HTTP.Proto)
	})

	t.Run("full URL preserves explicit Host header", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{
			URL:     "https://example.com/healthz",
			Headers: map[string]string{"Host": "override.local"},
		}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "override.local", cfg.VIPs[0].Check.HTTP.Headers["Host"])
	})
}

func TestApplyDefaults_HTTPCheck_VIPHost(t *testing.T) {
	t.Run("/32 prefix - Host derived from VIP IP", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.10.1.5/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "10.10.1.5", cfg.VIPs[0].Check.HTTP.Host)
	})

	t.Run("/128 prefix - Host derived from VIP IP", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "2001:db8::1/128")
		ApplyDefaults(cfg)
		assert.Equal(t, "2001:db8::1", cfg.VIPs[0].Check.HTTP.Host)
	})

	t.Run("/24 prefix - Host stays empty", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/"}, "10.10.1.0/24")
		ApplyDefaults(cfg)
		assert.Equal(t, "", cfg.VIPs[0].Check.HTTP.Host)
	})

	t.Run("explicit Host preserved for /32", func(t *testing.T) {
		cfg := makeVIPWithHTTP(HTTPCheckConfig{URL: "/", Host: "explicit.host"}, "10.10.1.5/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "explicit.host", cfg.VIPs[0].Check.HTTP.Host)
	})
}

func TestApplyDefaults_HTTPCheck_NilConfig(t *testing.T) {
	t.Run("nil HTTP gets initialized", func(t *testing.T) {
		cfg := &Config{
			VIPs: []VIPConfig{{
				Name:   "v",
				Prefix: "10.0.0.1/32",
				Check:  CheckConfig{Type: CheckTypeHTTP},
			}},
		}
		ApplyDefaults(cfg)
		assert.NotNil(t, cfg.VIPs[0].Check.HTTP)
		assert.Equal(t, "/", cfg.VIPs[0].Check.HTTP.URL)
	})
}

func TestApplyDefaults_DNSCheck(t *testing.T) {
	makeDNS := func(d DNSCheckConfig, prefix string) *Config {
		return &Config{
			VIPs: []VIPConfig{{
				Name:   "v",
				Prefix: prefix,
				Check:  CheckConfig{Type: CheckTypeDNS, DNS: &d},
			}},
		}
	}

	t.Run("Port defaults to 53", func(t *testing.T) {
		cfg := makeDNS(DNSCheckConfig{Names: []string{"example.com"}}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, uint16(53), cfg.VIPs[0].Check.DNS.Port)
	})

	t.Run("Port preserved", func(t *testing.T) {
		cfg := makeDNS(DNSCheckConfig{Names: []string{"example.com"}, Port: 5353}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, uint16(5353), cfg.VIPs[0].Check.DNS.Port)
	})

	t.Run("QueryType defaults to A", func(t *testing.T) {
		cfg := makeDNS(DNSCheckConfig{Names: []string{"example.com"}}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "A", cfg.VIPs[0].Check.DNS.QueryType)
	})

	t.Run("QueryType preserved", func(t *testing.T) {
		cfg := makeDNS(DNSCheckConfig{Names: []string{"example.com"}, QueryType: "AAAA"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "AAAA", cfg.VIPs[0].Check.DNS.QueryType)
	})

	t.Run("Resolver defaults to VIP IP for /32", func(t *testing.T) {
		cfg := makeDNS(DNSCheckConfig{Names: []string{"example.com"}}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "10.0.0.1", cfg.VIPs[0].Check.DNS.Resolver)
	})

	t.Run("Resolver empty for /24", func(t *testing.T) {
		cfg := makeDNS(DNSCheckConfig{Names: []string{"example.com"}}, "10.0.0.0/24")
		ApplyDefaults(cfg)
		assert.Equal(t, "", cfg.VIPs[0].Check.DNS.Resolver)
	})

	t.Run("Resolver preserved", func(t *testing.T) {
		cfg := makeDNS(DNSCheckConfig{Names: []string{"example.com"}, Resolver: "8.8.8.8"}, "10.0.0.1/32")
		ApplyDefaults(cfg)
		assert.Equal(t, "8.8.8.8", cfg.VIPs[0].Check.DNS.Resolver)
	})

	t.Run("nil DNS gets initialized", func(t *testing.T) {
		cfg := &Config{
			VIPs: []VIPConfig{{
				Name:   "v",
				Prefix: "10.0.0.1/32",
				Check:  CheckConfig{Type: CheckTypeDNS},
			}},
		}
		ApplyDefaults(cfg)
		assert.NotNil(t, cfg.VIPs[0].Check.DNS)
		assert.Equal(t, uint16(53), cfg.VIPs[0].Check.DNS.Port)
	})
}

func TestApplyDefaults_PingCheck(t *testing.T) {
	makePing := func(p PingCheckConfig) *Config {
		return &Config{
			VIPs: []VIPConfig{{
				Name:   "v",
				Prefix: "10.0.0.1/32",
				Check:  CheckConfig{Type: CheckTypePing, Ping: &p},
			}},
		}
	}

	t.Run("Count defaults to 1", func(t *testing.T) {
		cfg := makePing(PingCheckConfig{})
		ApplyDefaults(cfg)
		assert.Equal(t, 1, cfg.VIPs[0].Check.Ping.Count)
	})

	t.Run("Count preserved", func(t *testing.T) {
		cfg := makePing(PingCheckConfig{Count: 5})
		ApplyDefaults(cfg)
		assert.Equal(t, 5, cfg.VIPs[0].Check.Ping.Count)
	})

	t.Run("nil Ping gets initialized", func(t *testing.T) {
		cfg := &Config{
			VIPs: []VIPConfig{{
				Name:   "v",
				Prefix: "10.0.0.1/32",
				Check:  CheckConfig{Type: CheckTypePing},
			}},
		}
		ApplyDefaults(cfg)
		assert.NotNil(t, cfg.VIPs[0].Check.Ping)
		assert.Equal(t, 1, cfg.VIPs[0].Check.Ping.Count)
	})
}

func TestApplyDefaults_Policy(t *testing.T) {
	makePolicy := func(p PolicyConfig) *Config {
		return &Config{
			VIPs: []VIPConfig{{
				Name:   "v",
				Prefix: "10.0.0.1/32",
				Check:  CheckConfig{Type: CheckTypeHTTP, HTTP: &HTTPCheckConfig{URL: "/"}},
				Policy: p,
			}},
		}
	}

	t.Run("FailAction defaults to lower_priority", func(t *testing.T) {
		cfg := makePolicy(PolicyConfig{})
		ApplyDefaults(cfg)
		assert.Equal(t, "lower_priority", cfg.VIPs[0].Policy.FailAction)
	})

	t.Run("FailAction preserved", func(t *testing.T) {
		cfg := makePolicy(PolicyConfig{FailAction: "withdraw"})
		ApplyDefaults(cfg)
		assert.Equal(t, "withdraw", cfg.VIPs[0].Policy.FailAction)
	})

	t.Run("LowerPriority auto-created for lower_priority action", func(t *testing.T) {
		cfg := makePolicy(PolicyConfig{})
		ApplyDefaults(cfg)
		assert.NotNil(t, cfg.VIPs[0].Policy.LowerPriority)
	})

	t.Run("LowerPriority not created for withdraw action", func(t *testing.T) {
		cfg := makePolicy(PolicyConfig{FailAction: "withdraw"})
		ApplyDefaults(cfg)
		assert.Nil(t, cfg.VIPs[0].Policy.LowerPriority)
	})

	t.Run("ASPathPrepend defaults to 6", func(t *testing.T) {
		cfg := makePolicy(PolicyConfig{})
		ApplyDefaults(cfg)
		assert.NotNil(t, cfg.VIPs[0].Policy.LowerPriority.ASPathPrepend)
		assert.Equal(t, 6, *cfg.VIPs[0].Policy.LowerPriority.ASPathPrepend)
	})

	t.Run("ASPathPrepend preserved", func(t *testing.T) {
		cfg := makePolicy(PolicyConfig{
			LowerPriority: &LowerPriorityConfig{ASPathPrepend: new(3)},
		})
		ApplyDefaults(cfg)
		assert.Equal(t, 3, *cfg.VIPs[0].Policy.LowerPriority.ASPathPrepend)
	})
}
