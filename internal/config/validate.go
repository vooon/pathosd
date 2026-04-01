package config

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Validate checks the config for structural and semantic correctness.
// Returns a slice of errors — empty means valid.
func Validate(cfg *Config) []error {
	var errs []error
	add := func(path, msg string) {
		errs = append(errs, fmt.Errorf("%s: %s", path, msg))
	}

	// Schema version
	if cfg.Schema == "" {
		add("schema", "required")
	} else if cfg.Schema != "v1" {
		add("schema", fmt.Sprintf("unsupported version %q (expected \"v1\")", cfg.Schema))
	}

	// Router
	if cfg.Router.ASN == 0 {
		add("router.asn", "required and must be > 0")
	}
	if cfg.Router.RouterID == "" {
		add("router.router_id", "required")
	} else if ip := net.ParseIP(cfg.Router.RouterID); ip == nil || ip.To4() == nil {
		add("router.router_id", fmt.Sprintf("must be a valid IPv4 address, got %q", cfg.Router.RouterID))
	}
	if cfg.Router.LocalAddress != "" {
		if ip := net.ParseIP(cfg.Router.LocalAddress); ip == nil {
			add("router.local_address", fmt.Sprintf("must be a valid IP address, got %q", cfg.Router.LocalAddress))
		}
	}

	// API
	if cfg.API.Listen == "" {
		add("api.listen", "required")
	}

	// Logging
	switch cfg.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		add("logging.level", fmt.Sprintf("must be one of debug, info, warn, error; got %q", cfg.Logging.Level))
	}
	switch cfg.Logging.Format {
	case "text", "json":
	default:
		add("logging.format", fmt.Sprintf("must be one of text, json; got %q", cfg.Logging.Format))
	}

	// BGP neighbors
	if len(cfg.BGP.Neighbors) == 0 {
		add("bgp.neighbors", "at least one neighbor is required")
	}

	neighborNames := make(map[string]bool)
	for i, n := range cfg.BGP.Neighbors {
		prefix := fmt.Sprintf("bgp.neighbors[%d]", i)

		if n.Name == "" {
			add(prefix+".name", "required")
		} else if neighborNames[n.Name] {
			add(prefix+".name", fmt.Sprintf("duplicate neighbor name %q", n.Name))
		} else {
			neighborNames[n.Name] = true
		}

		if n.Address == "" {
			add(prefix+".address", "required")
		} else if ip := net.ParseIP(n.Address); ip == nil {
			add(prefix+".address", fmt.Sprintf("must be a valid IP address, got %q", n.Address))
		}

		if n.PeerASN == 0 {
			add(prefix+".peer_asn", "required and must be > 0")
		}
	}

	// VIPs
	if len(cfg.VIPs) == 0 {
		add("vips", "at least one VIP is required")
	}

	vipNames := make(map[string]bool)
	vipPrefixes := make(map[string]bool)
	for i, v := range cfg.VIPs {
		prefix := fmt.Sprintf("vips[%d]", i)
		errs = append(errs, validateVIP(prefix, &v, vipNames, vipPrefixes)...)
	}

	return errs
}

func validateVIP(prefix string, v *VIPConfig, names, prefixes map[string]bool) []error {
	var errs []error
	add := func(path, msg string) {
		errs = append(errs, fmt.Errorf("%s: %s", path, msg))
	}

	if v.Name == "" {
		add(prefix+".name", "required")
	} else if names[v.Name] {
		add(prefix+".name", fmt.Sprintf("duplicate VIP name %q", v.Name))
	} else {
		names[v.Name] = true
	}

	if v.Prefix == "" {
		add(prefix+".prefix", "required")
	} else {
		_, ipNet, err := net.ParseCIDR(v.Prefix)
		if err != nil {
			add(prefix+".prefix", fmt.Sprintf("must be a valid CIDR, got %q", v.Prefix))
		} else {
			canonical := ipNet.String()
			if prefixes[canonical] {
				add(prefix+".prefix", fmt.Sprintf("duplicate VIP prefix %q", v.Prefix))
			} else {
				prefixes[canonical] = true
			}
		}
	}

	// Check interval / timeout relationship
	if v.Check.Interval != nil && v.Check.Timeout != nil {
		if v.Check.Timeout.Duration >= v.Check.Interval.Duration {
			add(prefix+".check.timeout", fmt.Sprintf(
				"must be less than interval (%s), got %s",
				v.Check.Interval.Duration, v.Check.Timeout.Duration))
		}
	}

	// Rise / fall
	if v.Check.Rise != nil && *v.Check.Rise < 1 {
		add(prefix+".check.rise", "must be >= 1")
	}
	if v.Check.Fall != nil && *v.Check.Fall < 1 {
		add(prefix+".check.fall", "must be >= 1")
	}

	// Check config
	errs = append(errs, validateCheck(prefix+".check", &v.Check, v.Prefix)...)

	// Policy config
	errs = append(errs, validatePolicy(prefix+".policy", &v.Policy)...)

	return errs
}

func validateCheck(prefix string, c *CheckConfig, vipPrefix string) []error {
	var errs []error
	add := func(path, msg string) {
		errs = append(errs, fmt.Errorf("%s: %s", path, msg))
	}

	switch c.Type {
	case "http":
		if c.HTTP == nil {
			add(prefix+".http", "required when type is \"http\"")
		} else {
			errs = append(errs, validateHTTPCheck(prefix+".http", c.HTTP, vipPrefix)...)
		}

	case "dns":
		if c.DNS == nil {
			add(prefix+".dns", "required when type is \"dns\"")
		} else {
			d := c.DNS
			if len(d.Names) == 0 {
				add(prefix+".dns.names", "at least one DNS name is required")
			}
			validQTypes := map[string]bool{
				"A": true, "AAAA": true, "CNAME": true, "PTR": true,
				"NS": true, "MX": true, "SOA": true, "TXT": true, "SRV": true,
			}
			if !validQTypes[strings.ToUpper(d.QueryType)] {
				add(prefix+".dns.query_type", fmt.Sprintf("unsupported query type %q", d.QueryType))
			}
		}

	case "ping":
		if c.Ping == nil {
			add(prefix+".ping", "required when type is \"ping\"")
		} else {
			p := c.Ping
			if p.Count < 1 || p.Count > 60 {
				add(prefix+".ping.count", fmt.Sprintf("must be 1..60, got %d", p.Count))
			}
			if p.MaxLossRatio < 0 || p.MaxLossRatio >= 1.0 {
				add(prefix+".ping.max_loss_ratio", fmt.Sprintf("must be 0 <= x < 1.0, got %f", p.MaxLossRatio))
			}
		}

	case "":
		add(prefix+".type", "required")
	default:
		add(prefix+".type", fmt.Sprintf("unsupported check type %q", c.Type))
	}

	return errs
}

func validateHTTPCheck(prefix string, h *HTTPCheckConfig, vipPrefix string) []error {
	var errs []error
	add := func(path, msg string) {
		errs = append(errs, fmt.Errorf("%s: %s", path, msg))
	}

	if h.Proto != "" {
		switch h.Proto {
		case "http", "https":
		default:
			add(prefix+".proto", fmt.Sprintf("must be http or https, got %q", h.Proto))
		}
	}

	switch h.Method {
	case "GET", "HEAD":
	default:
		add(prefix+".method", fmt.Sprintf("must be GET or HEAD, got %q", h.Method))
	}

	// Host is required when VIP prefix is not a single address.
	if h.Host == "" && !isSingleHost(vipPrefix) {
		add(prefix+".host", "required when VIP prefix is not /32 or /128")
	}

	if h.ResponseRegex != "" {
		if _, err := regexp.Compile(h.ResponseRegex); err != nil {
			add(prefix+".response_regex", fmt.Sprintf("invalid regex: %v", err))
		}
	}

	if h.TLSCACert != "" && h.TLSInsecure {
		add(prefix+".tls_ca_cert", "cannot be set together with tls_insecure")
	}

	return errs
}

// isSingleHost returns true if the prefix is a /32 (IPv4) or /128 (IPv6).
func isSingleHost(prefix string) bool {
	_, ipNet, err := net.ParseCIDR(prefix)
	if err != nil {
		return false
	}
	ones, bits := ipNet.Mask.Size()
	return (bits == 32 && ones == 32) || (bits == 128 && ones == 128)
}

func validatePolicy(prefix string, p *PolicyConfig) []error {
	var errs []error
	add := func(path, msg string) {
		errs = append(errs, fmt.Errorf("%s: %s", path, msg))
	}

	switch p.FailAction {
	case "withdraw":
		if p.LowerPriority != nil {
			add(prefix+".lower_priority", "must not be set when fail_action is \"withdraw\"")
		}

	case "lower_priority":
		if p.LowerPriority != nil {
			lp := p.LowerPriority
			if lp.ASPathPrepend != nil {
				v := *lp.ASPathPrepend
				if v < 1 || v > 16 {
					add(prefix+".lower_priority.as_path_prepend", fmt.Sprintf("must be 1..16, got %d", v))
				}
			}
		}

	case "":
		add(prefix+".fail_action", "required")
	default:
		add(prefix+".fail_action", fmt.Sprintf("must be withdraw or lower_priority, got %q", p.FailAction))
	}

	return errs
}
