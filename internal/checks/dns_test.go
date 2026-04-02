package checks

import (
	"context"
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/config"
)

// startDNSServer binds a UDP socket on a random port, starts a DNS server using
// the provided handler, and registers cleanup via t.Cleanup. Returns the resolver
// address and port.
func startDNSServer(t *testing.T, handler dns.Handler) (string, uint16) {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := pc.LocalAddr().(*net.UDPAddr)
	srv := &dns.Server{
		PacketConn: pc,
		Net:        "udp",
		Handler:    handler,
	}

	t.Cleanup(func() { _ = srv.Shutdown() })
	go func() { _ = srv.ActivateAndServe() }()

	return "127.0.0.1", uint16(addr.Port)
}

func TestDNSChecker_Type(t *testing.T) {
	c := NewDNSChecker(&config.DNSCheckConfig{})
	assert.Equal(t, "dns", c.Type())
}

func TestDNSChecker_Success(t *testing.T) {
	mux := dns.NewServeMux()
	mux.HandleFunc("example.com.", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("1.2.3.4").To4(),
		})
		_ = w.WriteMsg(m)
	})

	resolver, port := startDNSServer(t, mux)
	cfg := &config.DNSCheckConfig{
		Names:     []string{"example.com"},
		Resolver:  resolver,
		Port:      port,
		QueryType: "A",
	}
	c := NewDNSChecker(cfg)
	result := c.Check(context.Background())
	assert.True(t, result.Success)
	assert.Contains(t, result.Detail, "DNS OK")
}

func TestDNSChecker_NXDOMAIN(t *testing.T) {
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeNameError)
		_ = w.WriteMsg(m)
	})

	resolver, port := startDNSServer(t, mux)
	cfg := &config.DNSCheckConfig{
		Names:     []string{"nxdomain.example"},
		Resolver:  resolver,
		Port:      port,
		QueryType: "A",
	}
	c := NewDNSChecker(cfg)
	result := c.Check(context.Background())
	assert.False(t, result.Success)
	assert.Contains(t, result.Detail, "NXDOMAIN")
}

func TestDNSChecker_EmptyAnswers(t *testing.T) {
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		// rcode=NOERROR but no answer records
		_ = w.WriteMsg(m)
	})

	resolver, port := startDNSServer(t, mux)
	cfg := &config.DNSCheckConfig{
		Names:     []string{"empty.example"},
		Resolver:  resolver,
		Port:      port,
		QueryType: "A",
	}
	c := NewDNSChecker(cfg)
	result := c.Check(context.Background())
	assert.False(t, result.Success)
	assert.Contains(t, result.Detail, "no answers")
}

func TestDNSChecker_MultipleNames_FirstFails(t *testing.T) {
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		name := r.Question[0].Name
		if name == "second.example." {
			m.SetReply(r)
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.ParseIP("1.2.3.4").To4(),
			})
		} else {
			m.SetRcode(r, dns.RcodeNameError)
		}
		_ = w.WriteMsg(m)
	})

	resolver, port := startDNSServer(t, mux)
	cfg := &config.DNSCheckConfig{
		Names:     []string{"first.example", "second.example"},
		Resolver:  resolver,
		Port:      port,
		QueryType: "A",
	}
	c := NewDNSChecker(cfg)
	result := c.Check(context.Background())
	assert.False(t, result.Success)
	assert.Contains(t, result.Detail, "first.example")
}

func TestParseQueryType(t *testing.T) {
	tests := []struct {
		input string
		want  uint16
	}{
		{"A", dns.TypeA},
		{"AAAA", dns.TypeAAAA},
		{"CNAME", dns.TypeCNAME},
		{"PTR", dns.TypePTR},
		{"NS", dns.TypeNS},
		{"MX", dns.TypeMX},
		{"SOA", dns.TypeSOA},
		{"TXT", dns.TypeTXT},
		{"SRV", dns.TypeSRV},
		{"a", dns.TypeA},       // lowercase treated as default
		{"unknown", dns.TypeA}, // unknown treated as default (A)
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, parseQueryType(tt.input))
		})
	}
}
