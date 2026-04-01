package checks

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/vooon/pathosd/internal/config"
)

type DNSChecker struct {
	cfg    config.DNSCheckConfig
	client *dns.Client
}

func NewDNSChecker(cfg *config.DNSCheckConfig) *DNSChecker {
	return &DNSChecker{cfg: *cfg, client: &dns.Client{}}
}

func (c *DNSChecker) Type() string { return "dns" }

func (c *DNSChecker) Check(ctx context.Context) Result {
	start := time.Now()

	resolver := c.cfg.Resolver
	if resolver == "" {
		conf, err := dns.ClientConfigFromFile("/etc/resolv.conf")
		if err != nil || len(conf.Servers) == 0 {
			return Result{Duration: time.Since(start), Err: err, Detail: "cannot determine resolver"}
		}
		resolver = conf.Servers[0]
	}

	server := net.JoinHostPort(resolver, fmt.Sprintf("%d", c.cfg.Port))
	qtype := parseQueryType(c.cfg.QueryType)

	for _, name := range c.cfg.Names {
		msg := new(dns.Msg)
		msg.SetQuestion(dns.Fqdn(name), qtype)
		msg.RecursionDesired = true

		resp, _, err := c.client.ExchangeContext(ctx, msg, server)
		dur := time.Since(start)
		if err != nil {
			timedOut := ctx.Err() == context.DeadlineExceeded
			return Result{Duration: dur, Err: err, Detail: fmt.Sprintf("query %s: %v", name, err), TimedOut: timedOut}
		}
		if resp.Rcode != dns.RcodeSuccess {
			return Result{Duration: dur, Detail: fmt.Sprintf("query %s: %s", name, dns.RcodeToString[resp.Rcode])}
		}
		if len(resp.Answer) == 0 {
			return Result{Duration: dur, Detail: fmt.Sprintf("query %s: no answers", name)}
		}
	}

	return Result{Success: true, Duration: time.Since(start), Detail: fmt.Sprintf("DNS OK: %s", strings.Join(c.cfg.Names, ", "))}
}

func parseQueryType(s string) uint16 {
	switch strings.ToUpper(s) {
	case "AAAA":
		return dns.TypeAAAA
	case "CNAME":
		return dns.TypeCNAME
	case "PTR":
		return dns.TypePTR
	case "NS":
		return dns.TypeNS
	case "MX":
		return dns.TypeMX
	case "SOA":
		return dns.TypeSOA
	case "TXT":
		return dns.TypeTXT
	case "SRV":
		return dns.TypeSRV
	default:
		return dns.TypeA
	}
}
