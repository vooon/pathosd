package checks

import (
	"context"
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"github.com/vooon/pathosd/internal/config"
)

// DNSCheckerSuite spins up a fresh UDP DNS server before each test and shuts it
// down afterwards, providing s.mux for per-test handler registration.
type DNSCheckerSuite struct {
	suite.Suite
	mux      *dns.ServeMux
	srv      *dns.Server
	resolver string
	port     uint16
}

func (s *DNSCheckerSuite) SetupTest() {
	s.mux = dns.NewServeMux()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	s.Require().NoError(err)

	addr := pc.LocalAddr().(*net.UDPAddr)
	s.resolver = "127.0.0.1"
	s.port = uint16(addr.Port)

	s.srv = &dns.Server{
		PacketConn: pc,
		Net:        "udp",
		Handler:    s.mux,
	}
	go func() { _ = s.srv.ActivateAndServe() }()
}

func (s *DNSCheckerSuite) TearDownTest() {
	_ = s.srv.Shutdown()
}

// checker builds a DNSChecker pointed at the suite's local server.
// names is the list of DNS names to resolve; queryType is e.g. "A" or "AAAA".
func (s *DNSCheckerSuite) checker(names []string, queryType string) *DNSChecker {
	return NewDNSChecker(&config.DNSCheckConfig{
		Names:     names,
		Resolver:  s.resolver,
		Port:      s.port,
		QueryType: queryType,
	})
}

func (s *DNSCheckerSuite) TestType() {
	c := NewDNSChecker(&config.DNSCheckConfig{})
	s.Equal("dns", c.Type())
}

func (s *DNSCheckerSuite) TestSuccess() {
	s.mux.HandleFunc("example.com.", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("1.2.3.4").To4(),
		})
		_ = w.WriteMsg(m)
	})

	result := s.checker([]string{"example.com"}, "A").Check(context.TODO())
	s.True(result.Success)
	s.Contains(result.Detail, "DNS OK")
}

func (s *DNSCheckerSuite) TestNXDOMAIN() {
	s.mux.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeNameError)
		_ = w.WriteMsg(m)
	})

	result := s.checker([]string{"nxdomain.example"}, "A").Check(context.TODO())
	s.False(result.Success)
	s.Contains(result.Detail, "NXDOMAIN")
}

func (s *DNSCheckerSuite) TestEmptyAnswers() {
	s.mux.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		// rcode=NOERROR but no answer records
		_ = w.WriteMsg(m)
	})

	result := s.checker([]string{"empty.example"}, "A").Check(context.TODO())
	s.False(result.Success)
	s.Contains(result.Detail, "no answers")
}

func (s *DNSCheckerSuite) TestMultipleNames_FirstFails() {
	s.mux.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
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

	result := s.checker([]string{"first.example", "second.example"}, "A").Check(context.TODO())
	s.False(result.Success)
	s.Contains(result.Detail, "first.example")
}

// TestDNSCheckerSuite is the entry point that runs all suite methods.
func TestDNSCheckerSuite(t *testing.T) {
	suite.Run(t, new(DNSCheckerSuite))
}

// TestParseQueryType is standalone: it tests a package-level function with no
// server dependency and is clearest as a table-driven test.
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
		{"a", dns.TypeA},       // lowercase treated as default (A)
		{"unknown", dns.TypeA}, // unknown treated as default (A)
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, parseQueryType(tt.input))
		})
	}
}
