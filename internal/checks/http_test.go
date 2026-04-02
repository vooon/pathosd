package checks

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
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
