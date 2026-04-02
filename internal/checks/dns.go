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

	server := net.JoinHostPort(c.cfg.Resolver, fmt.Sprintf("%d", c.cfg.Port))
	qtype := dns.StringToType[c.cfg.QueryType]

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
