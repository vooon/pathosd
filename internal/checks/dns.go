package checks

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

type DNSChecker struct {
	cfg    config.DNSCheckConfig
	client *dns.Client
}

func NewDNSChecker(cfg *config.DNSCheckConfig) *DNSChecker {
	c := &DNSChecker{cfg: *cfg, client: &dns.Client{}}
	// dns.StringToType is case-sensitive; accept "aaaa" as well as "AAAA".
	c.cfg.QueryType = strings.ToUpper(c.cfg.QueryType)
	return c
}

func (c *DNSChecker) Type() string { return "dns" }

func (c *DNSChecker) Check(ctx context.Context) Result {
	start := time.Now()

	server := net.JoinHostPort(c.cfg.Resolver, fmt.Sprintf("%d", c.cfg.Port))
	qtype := dns.StringToType[c.cfg.QueryType]

	details := &model.DNSDetails{Names: make([]model.DNSNameDetails, 0, len(c.cfg.Names))}
	for _, name := range c.cfg.Names {
		msg := new(dns.Msg)
		msg.SetQuestion(dns.Fqdn(name), qtype)
		msg.RecursionDesired = true

		resp, _, err := c.client.ExchangeContext(ctx, msg, server)
		dur := time.Since(start)
		if err != nil {
			timedOut := ctx.Err() == context.DeadlineExceeded
			return Result{Duration: dur, Err: err, Detail: fmt.Sprintf("query %s: %v", name, err), TimedOut: timedOut, Details: details}
		}
		details.Names = append(details.Names, model.DNSNameDetails{Name: name, Rcode: resp.Rcode, Answers: len(resp.Answer)})
		if resp.Rcode != dns.RcodeSuccess {
			return Result{Duration: dur, Detail: fmt.Sprintf("query %s: %s", name, dns.RcodeToString[resp.Rcode]), Details: details}
		}
		if len(resp.Answer) == 0 {
			return Result{Duration: dur, Detail: fmt.Sprintf("query %s: no answers", name), Details: details}
		}
	}

	return Result{Success: true, Duration: time.Since(start), Detail: fmt.Sprintf("DNS OK: %s", strings.Join(c.cfg.Names, ", ")), Details: details}
}
