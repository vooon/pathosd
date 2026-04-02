package config

import (
	"net"
	"net/url"
	"time"
)

// ApplyDefaults fills in zero-value fields with sensible defaults.
func ApplyDefaults(cfg *Config) {
	if cfg.API.Listen == "" {
		cfg.API.Listen = ":59179"
	}

	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "text"
	}

	if cfg.BGP.GracefulRestart == nil {
		cfg.BGP.GracefulRestart = new(true)
	}

	for i := range cfg.BGP.Neighbors {
		n := &cfg.BGP.Neighbors[i]
		if n.Port == 0 {
			n.Port = 179
		}
		if n.Required == nil {
			n.Required = new(true)
		}
	}

	for i := range cfg.VIPs {
		v := &cfg.VIPs[i]

		applyCheckDefaults(&v.Check, v.Prefix)
		applyPolicyDefaults(&v.Policy)
	}
}

// vipHostIP extracts the host IP from a /32 or /128 prefix, or "" otherwise.
func vipHostIP(prefix string) string {
	ip, ipNet, err := net.ParseCIDR(prefix)
	if err != nil {
		return ""
	}
	ones, bits := ipNet.Mask.Size()
	if (bits == 32 && ones == 32) || (bits == 128 && ones == 128) {
		return ip.String()
	}
	return ""
}

func applyCheckDefaults(c *CheckConfig, vipPrefix string) {
	if c.Interval == nil {
		c.Interval = &Duration{Duration: 1 * time.Second}
	}
	if c.Timeout == nil {
		c.Timeout = &Duration{Duration: 100 * time.Millisecond}
	}
	if c.Rise == nil {
		c.Rise = new(1)
	}
	if c.Fall == nil {
		c.Fall = new(3)
	}

	switch c.Type {
	case CheckTypeHTTP:
		if c.HTTP == nil {
			c.HTTP = &HTTPCheckConfig{}
		}
		applyHTTPDefaults(c.HTTP, vipPrefix)

	case CheckTypeDNS:
		if c.DNS == nil {
			c.DNS = &DNSCheckConfig{}
		}
		applyDNSDefaults(c.DNS, vipPrefix)

	case CheckTypePing:
		if c.Ping == nil {
			c.Ping = &PingCheckConfig{}
		}
		p := c.Ping
		if p.Count == 0 {
			p.Count = 1
		}
	}
}

func applyHTTPDefaults(h *HTTPCheckConfig, vipPrefix string) {
	if h.URL == "" {
		h.URL = "/"
	}

	if h.Headers == nil {
		h.Headers = make(map[string]string)
	}

	// Parse full URL to derive proto and Host header.
	if u, err := url.Parse(h.URL); err == nil && u.Scheme != "" {
		// Full URL like https://example.com/readyz
		if h.Proto == "" {
			h.Proto = u.Scheme
		}
		// Set Host header from URL hostname if not already set.
		if _, ok := h.Headers["Host"]; !ok && u.Hostname() != "" {
			h.Headers["Host"] = u.Host // includes port if non-default
		}
		// Rewrite URL to just the path (+ query).
		path := u.RequestURI()
		if path == "" {
			path = "/"
		}
		h.URL = path
	}

	if h.Proto == "" {
		h.Proto = "http"
	}

	// Host defaults to VIP IP for /32 or /128.
	if h.Host == "" {
		h.Host = vipHostIP(vipPrefix)
	}

	// Port defaults from proto.
	if h.Port == 0 {
		if h.Proto == "https" {
			h.Port = 443
		} else {
			h.Port = 80
		}
	}

	if h.Method == "" {
		h.Method = "GET"
	}
	if len(h.ResponseCodes) == 0 {
		h.ResponseCodes = []int{200}
	}

	// Default User-Agent.
	if _, ok := h.Headers["User-Agent"]; !ok {
		h.Headers["User-Agent"] = "pathosd-check/1.0"
	}

	// When response_jq is set, default Accept to application/json.
	if h.ResponseJQ != "" {
		if _, ok := h.Headers["Accept"]; !ok {
			h.Headers["Accept"] = "application/json"
		}
	}
}

func applyDNSDefaults(d *DNSCheckConfig, vipPrefix string) {
	if d.Port == 0 {
		d.Port = 53
	}
	if d.QueryType == "" {
		d.QueryType = "A"
	}
	// Resolver defaults to VIP IP (we're checking our own DNS server).
	if d.Resolver == "" {
		d.Resolver = vipHostIP(vipPrefix)
	}
}

func applyPolicyDefaults(p *PolicyConfig) {
	if p.FailAction == "" {
		p.FailAction = "lower_priority"
	}

	// Keep lower_priority block always initialized so policy consumers can rely
	// on a stable shape regardless of fail_action.
	if p.LowerPriority == nil {
		p.LowerPriority = &LowerPriorityConfig{}
	}

	if p.LowerPriority.ASPathPrepend == nil {
		p.LowerPriority.ASPathPrepend = new(6)
	}
}
