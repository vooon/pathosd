package config

import "time"

// ApplyDefaults fills in zero-value fields with sensible defaults.
func ApplyDefaults(cfg *Config) {
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "text"
	}

	if cfg.BGP.GracefulRestart == nil {
		t := true
		cfg.BGP.GracefulRestart = &t
	}

	for i := range cfg.BGP.Neighbors {
		n := &cfg.BGP.Neighbors[i]
		if n.Port == 0 {
			n.Port = 179
		}
		if n.Required == nil {
			t := true
			n.Required = &t
		}
	}

	for i := range cfg.VIPs {
		v := &cfg.VIPs[i]

		if v.CheckInterval == nil {
			v.CheckInterval = &Duration{Duration: 1 * time.Second}
		}
		if v.CheckTimeout == nil {
			v.CheckTimeout = &Duration{Duration: 100 * time.Millisecond}
		}
		if v.Rise == nil {
			r := 1
			v.Rise = &r
		}
		if v.Fall == nil {
			f := 3
			v.Fall = &f
		}

		applyCheckDefaults(&v.Check)
		applyPolicyDefaults(&v.Policy)
	}
}

func applyCheckDefaults(c *CheckConfig) {
	switch c.Type {
	case "http":
		if c.HTTP == nil {
			c.HTTP = &HTTPCheckConfig{}
		}
		h := c.HTTP
		if h.Proto == "" {
			h.Proto = "http"
		}
		if h.URL == "" {
			h.URL = "/"
		}
		if h.Method == "" {
			h.Method = "GET"
		}
		if len(h.ResponseCodes) == 0 {
			h.ResponseCodes = []int{200, 301}
		}
		if h.Port == 0 {
			if h.Proto == "https" {
				h.Port = 443
			} else {
				h.Port = 80
			}
		}
		if h.SSLHostname == nil {
			t := true
			h.SSLHostname = &t
		}

	case "dns":
		if c.DNS == nil {
			c.DNS = &DNSCheckConfig{}
		}
		d := c.DNS
		if d.Port == 0 {
			d.Port = 53
		}
		if d.QueryType == "" {
			d.QueryType = "A"
		}

	case "ping":
		if c.Ping == nil {
			c.Ping = &PingCheckConfig{}
		}
		p := c.Ping
		if p.Count == 0 {
			p.Count = 1
		}
	}
}

func applyPolicyDefaults(p *PolicyConfig) {
	if p.FailAction == "" {
		p.FailAction = "lower_priority"
	}

	if p.FailAction == "lower_priority" && p.LowerPriority == nil {
		p.LowerPriority = &LowerPriorityConfig{}
	}

	if p.LowerPriority != nil && p.LowerPriority.ASPathPrepend == nil {
		v := 6
		p.LowerPriority.ASPathPrepend = &v
	}
}
