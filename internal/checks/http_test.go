package checks

import (
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/vooon/pathosd/internal/config"
)

// HTTPCheckerSuite provides helper methods for spinning up test HTTP(S) servers
// and building HTTPChecker instances without repeating host/port extraction.
type HTTPCheckerSuite struct {
	suite.Suite
}

// plainServer starts a plain HTTP test server and registers cleanup.
func (s *HTTPCheckerSuite) plainServer(handler http.HandlerFunc) *httptest.Server {
	ts := httptest.NewServer(handler)
	s.T().Cleanup(ts.Close)
	return ts
}

// tlsServer starts a TLS test server and registers cleanup.
func (s *HTTPCheckerSuite) tlsServer(handler http.HandlerFunc) *httptest.Server {
	ts := httptest.NewTLSServer(handler)
	s.T().Cleanup(ts.Close)
	return ts
}

// checker builds an HTTPChecker from a test server. cfg must be fully populated
// except for Host and Port, which are derived from the server address.
func (s *HTTPCheckerSuite) checker(ts *httptest.Server, cfg *config.HTTPCheckConfig) *HTTPChecker {
	host, portStr, err := net.SplitHostPort(ts.Listener.Addr().String())
	s.Require().NoError(err)
	port, err := strconv.Atoi(portStr)
	s.Require().NoError(err)
	cfg.Host = host
	cfg.Port = uint16(port)
	c, err := NewHTTPChecker(cfg)
	s.Require().NoError(err)
	return c
}

func (s *HTTPCheckerSuite) TestType() {
	c, err := NewHTTPChecker(&config.HTTPCheckConfig{})
	s.Require().NoError(err)
	s.Equal("http", c.Type())
}

func (s *HTTPCheckerSuite) TestNewHTTPChecker_InvalidRegex() {
	_, err := NewHTTPChecker(&config.HTTPCheckConfig{ResponseRegex: `[invalid`})
	s.Error(err)
	s.Contains(err.Error(), "invalid response_regex")
}

func (s *HTTPCheckerSuite) TestNewHTTPChecker_InvalidJQ() {
	_, err := NewHTTPChecker(&config.HTTPCheckConfig{ResponseJQ: `!!!bad`})
	s.Error(err)
	s.Contains(err.Error(), "invalid response_jq")
}

func (s *HTTPCheckerSuite) TestSuccess200() {
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "healthy")
	})
	c := s.checker(ts, &config.HTTPCheckConfig{Proto: "http", URL: "/", Method: "GET", ResponseCodes: []int{200}})
	result := c.Check(context.TODO())
	s.True(result.Success)
	s.Contains(result.Detail, "200")
}

func (s *HTTPCheckerSuite) TestWrongStatusCode() {
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := s.checker(ts, &config.HTTPCheckConfig{Proto: "http", URL: "/", Method: "GET", ResponseCodes: []int{200}})
	result := c.Check(context.TODO())
	s.False(result.Success)
	s.Contains(result.Detail, "unexpected status")
}

func (s *HTTPCheckerSuite) TestMultipleResponseCodes() {
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	c := s.checker(ts, &config.HTTPCheckConfig{Proto: "http", URL: "/", Method: "GET", ResponseCodes: []int{200, 204}})
	result := c.Check(context.TODO())
	s.True(result.Success)
}

func (s *HTTPCheckerSuite) TestResponseTextMatch() {
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "status: healthy")
	})
	c := s.checker(ts, &config.HTTPCheckConfig{Proto: "http", URL: "/", Method: "GET", ResponseCodes: []int{200}, ResponseText: "healthy"})
	result := c.Check(context.TODO())
	s.True(result.Success)
}

func (s *HTTPCheckerSuite) TestResponseTextMismatch() {
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "status: degraded")
	})
	c := s.checker(ts, &config.HTTPCheckConfig{Proto: "http", URL: "/", Method: "GET", ResponseCodes: []int{200}, ResponseText: "healthy"})
	result := c.Check(context.TODO())
	s.False(result.Success)
	s.Contains(result.Detail, "body missing")
}

func (s *HTTPCheckerSuite) TestCustomHeaders() {
	var receivedHeader string
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-Custom")
		_, _ = fmt.Fprint(w, "ok")
	})
	c := s.checker(ts, &config.HTTPCheckConfig{
		Proto: "http", URL: "/", Method: "GET", ResponseCodes: []int{200},
		Headers: map[string]string{"X-Custom": "test-value"},
	})
	result := c.Check(context.TODO())
	s.True(result.Success)
	s.Equal("test-value", receivedHeader)
}

func (s *HTTPCheckerSuite) TestHEADMethod() {
	var receivedMethod string
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
	})
	c := s.checker(ts, &config.HTTPCheckConfig{Proto: "http", URL: "/", Method: "HEAD", ResponseCodes: []int{200}})
	c.Check(context.TODO())
	s.Equal("HEAD", receivedMethod)
}

func (s *HTTPCheckerSuite) TestTLSInsecure() {
	ts := s.tlsServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "tls ok")
	})
	c := s.checker(ts, &config.HTTPCheckConfig{Proto: "https", URL: "/", Method: "GET", ResponseCodes: []int{200}, TLSInsecure: true})
	result := c.Check(context.TODO())
	s.True(result.Success)
}

// TestTLSResolve verifies curl --resolve semantics: when tls_server_name is set
// and cfg.Host is a bare IP (the VIP), the checker connects to the IP via a
// custom DialContext while presenting tls_server_name as TLS SNI.
// TLSInsecure is used so the self-signed test cert (valid for 127.0.0.1, not the
// fake hostname) does not block the connection.
func (s *HTTPCheckerSuite) TestTLSResolve() {
	ts := s.tlsServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "tls resolve ok")
	})
	host, portStr, err := net.SplitHostPort(ts.Listener.Addr().String())
	s.Require().NoError(err)
	port, err := strconv.Atoi(portStr)
	s.Require().NoError(err)

	c, err := NewHTTPChecker(&config.HTTPCheckConfig{
		Proto:         "https",
		Host:          host, // bare IP (127.0.0.1)
		Port:          uint16(port),
		URL:           "/",
		Method:        "GET",
		ResponseCodes: []int{200},
		TLSInsecure:   true,
		TLSServerName: "example.test",
	})
	s.Require().NoError(err)
	s.Equal("example.test", c.sniHost)

	result := c.Check(context.TODO())
	s.True(result.Success)
	s.Contains(result.Detail, "200")
}

// TestTLSResolve_NoSNIForIPHost verifies that sniHost is NOT set when
// tls_server_name is not configured.
func (s *HTTPCheckerSuite) TestTLSResolve_NoSNIForIPHost() {
	c, err := NewHTTPChecker(&config.HTTPCheckConfig{
		Proto:  "https",
		Host:   "example.test",
		Port:   443,
		URL:    "/",
		Method: "GET",
		// TLSServerName intentionally not set
	})
	s.Require().NoError(err)
	s.Empty(c.sniHost)
}

func (s *HTTPCheckerSuite) TestTLSCACert() {
	ts := s.tlsServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "tls ca ok")
	})

	cert := ts.Certificate()
	s.Require().NotNil(cert)
	pemData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	s.Require().NotEmpty(pemData)

	caPath := s.T().TempDir() + "/ca.pem"
	err := os.WriteFile(caPath, pemData, 0o600)
	s.Require().NoError(err)

	c := s.checker(ts, &config.HTTPCheckConfig{
		Proto:         "https",
		URL:           "/",
		Method:        "GET",
		ResponseCodes: []int{200},
		TLSCACert:     caPath,
	})

	result := c.Check(context.TODO())
	s.True(result.Success)
}

func (s *HTTPCheckerSuite) TestNewHTTPChecker_InvalidTLSCACertPath() {
	_, err := NewHTTPChecker(&config.HTTPCheckConfig{
		Proto:     "https",
		Host:      "127.0.0.1",
		Port:      443,
		URL:       "/",
		Method:    "GET",
		TLSCACert: "/does/not/exist-ca.pem",
	})
	s.Error(err)
	s.Contains(err.Error(), "reading tls_ca_cert")
}

func (s *HTTPCheckerSuite) TestNewHTTPChecker_InvalidTLSCACertPEM() {
	caPath := s.T().TempDir() + "/bad-ca.pem"
	err := os.WriteFile(caPath, []byte("not a pem cert"), 0o600)
	s.Require().NoError(err)

	_, err = NewHTTPChecker(&config.HTTPCheckConfig{
		Proto:     "https",
		Host:      "127.0.0.1",
		Port:      443,
		URL:       "/",
		Method:    "GET",
		TLSCACert: caPath,
	})
	s.Error(err)
	s.Contains(err.Error(), "parsing tls_ca_cert")
}

func (s *HTTPCheckerSuite) TestResponseJQ_True() {
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	})
	c := s.checker(ts, &config.HTTPCheckConfig{
		Proto: "http", URL: "/", Method: "GET", ResponseCodes: []int{200},
		ResponseJQ: `.status == "ok"`,
	})
	result := c.Check(context.TODO())
	s.True(result.Success)
}

func (s *HTTPCheckerSuite) TestResponseJQ_False() {
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"degraded"}`)
	})
	c := s.checker(ts, &config.HTTPCheckConfig{
		Proto: "http", URL: "/", Method: "GET", ResponseCodes: []int{200},
		ResponseJQ: `.status == "ok"`,
	})
	result := c.Check(context.TODO())
	s.False(result.Success)
	s.Contains(result.Detail, "jq expression is not true")
}

func (s *HTTPCheckerSuite) TestResponseJQ_NotJSON() {
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `not json`)
	})
	c := s.checker(ts, &config.HTTPCheckConfig{
		Proto: "http", URL: "/", Method: "GET", ResponseCodes: []int{200},
		ResponseJQ: `.status == "ok"`,
	})
	result := c.Check(context.TODO())
	s.False(result.Success)
	s.Contains(result.Detail, "not valid JSON")
}

func (s *HTTPCheckerSuite) TestResponseRegex_Match() {
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `status: healthy (v1.2.3)`)
	})
	c := s.checker(ts, &config.HTTPCheckConfig{
		Proto: "http", URL: "/", Method: "GET", ResponseCodes: []int{200},
		ResponseRegex: `healthy \(v\d+\.\d+\.\d+\)`,
	})
	result := c.Check(context.TODO())
	s.True(result.Success)
}

func (s *HTTPCheckerSuite) TestResponseRegex_NoMatch() {
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `status: degraded`)
	})
	c := s.checker(ts, &config.HTTPCheckConfig{
		Proto: "http", URL: "/", Method: "GET", ResponseCodes: []int{200},
		ResponseRegex: `healthy`,
	})
	result := c.Check(context.TODO())
	s.False(result.Success)
	s.Contains(result.Detail, "does not match regex")
}

// forwardProxy starts a minimal HTTP forward proxy that forwards requests to
// a fixed target host:port and records whether it was used.
func (s *HTTPCheckerSuite) forwardProxy(target string, hit *atomic.Bool) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		r2 := r.Clone(r.Context())
		r2.RequestURI = ""
		r2.URL.Scheme = "http"
		r2.URL.Host = target
		resp, err := http.DefaultTransport.RoundTrip(r2)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	s.T().Cleanup(ts.Close)
	return ts
}

// splitAddr splits host and port from an httptest server and returns them.
func (s *HTTPCheckerSuite) splitAddr(ts *httptest.Server) (host string, port uint16) {
	s.T().Helper()
	h, portStr, err := net.SplitHostPort(ts.Listener.Addr().String())
	s.Require().NoError(err)
	p, err := strconv.Atoi(portStr)
	s.Require().NoError(err)
	return h, uint16(p)
}

// TestProxyRequest verifies a check is routed through the configured forward
// proxy rather than directly to the origin.
func (s *HTTPCheckerSuite) TestProxyRequest() {
	origin := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "proxied ok")
	})
	var hit atomic.Bool
	proxy := s.forwardProxy(origin.Listener.Addr().String(), &hit)

	host, port := s.splitAddr(origin)
	c, err := NewHTTPChecker(&config.HTTPCheckConfig{
		Proxy: proxy.URL,
		Host:  host, Port: port,
		URL: "/healthz", Proto: "http", Method: "GET", ResponseCodes: []int{200},
	})
	s.Require().NoError(err)

	result := c.Check(context.TODO())
	s.True(result.Success)
	s.True(hit.Load(), "request should have been routed through the proxy")
}

// TestProxyAuth verifies credentials embedded in the proxy URL are sent as
// Proxy-Authorization.
func (s *HTTPCheckerSuite) TestProxyAuth() {
	origin := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "ok")
	})
	var seenAuth atomic.Value
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth.Store(r.Header.Get("Proxy-Authorization"))
		r2 := r.Clone(r.Context())
		r2.RequestURI = ""
		r2.URL.Scheme = "http"
		r2.URL.Host = origin.Listener.Addr().String()
		resp, err := http.DefaultTransport.RoundTrip(r2)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	s.T().Cleanup(proxy.Close)

	proxyURL := strings.Replace(proxy.URL, "http://", "http://user:pass@", 1)
	host, port := s.splitAddr(origin)
	c, err := NewHTTPChecker(&config.HTTPCheckConfig{
		Proxy: proxyURL,
		Host:  host, Port: port,
		URL: "/", Proto: "http", Method: "GET", ResponseCodes: []int{200},
	})
	s.Require().NoError(err)

	result := c.Check(context.TODO())
	s.True(result.Success)
	auth, _ := seenAuth.Load().(string)
	s.Contains(auth, "Basic ")
}

// TestProxyDown verifies a check fails when the proxy is unreachable.
func (s *HTTPCheckerSuite) TestProxyDown() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	s.Require().NoError(err)
	deadAddr := ln.Addr().String()
	s.Require().NoError(ln.Close())

	c, err := NewHTTPChecker(&config.HTTPCheckConfig{
		Proxy: "http://" + deadAddr,
		Host:  "example.test", Port: 80,
		URL: "/", Proto: "http", Method: "GET", ResponseCodes: []int{200},
	})
	s.Require().NoError(err)

	result := c.Check(context.TODO())
	s.False(result.Success)
}

func (s *HTTPCheckerSuite) TestContextTimeout() {
	ts := s.plainServer(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client disconnects or a long timeout.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusGatewayTimeout)
	})
	c := s.checker(ts, &config.HTTPCheckConfig{Proto: "http", URL: "/", Method: "GET", ResponseCodes: []int{200}})

	ctx, cancel := context.WithTimeout(context.TODO(), 100*time.Millisecond)
	defer cancel()

	result := c.Check(ctx)
	s.False(result.Success)
	s.True(result.TimedOut)
}

// TestHTTPCheckerSuite is the entry point that runs all suite methods.
func TestHTTPCheckerSuite(t *testing.T) {
	suite.Run(t, new(HTTPCheckerSuite))
}
