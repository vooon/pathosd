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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/config"
)

// serverHostPort splits the test server's address into host and port.
func serverHostPort(t *testing.T, ts *httptest.Server) (string, uint16) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(ts.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, uint16(port)
}

func TestHTTPChecker_Type(t *testing.T) {
	c := NewHTTPChecker(&config.HTTPCheckConfig{})
	assert.Equal(t, "http", c.Type())
}

func TestHTTPChecker_Success200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "healthy")
	}))
	defer ts.Close()

	host, port := serverHostPort(t, ts)
	cfg := &config.HTTPCheckConfig{
		Proto:         "http",
		Host:          host,
		Port:          port,
		URL:           "/",
		Method:        "GET",
		ResponseCodes: []int{200},
	}
	c := NewHTTPChecker(cfg)
	result := c.Check(context.Background())
	assert.True(t, result.Success)
	assert.Contains(t, result.Detail, "200")
}

func TestHTTPChecker_WrongStatusCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	host, port := serverHostPort(t, ts)
	cfg := &config.HTTPCheckConfig{
		Proto:         "http",
		Host:          host,
		Port:          port,
		URL:           "/",
		Method:        "GET",
		ResponseCodes: []int{200},
	}
	c := NewHTTPChecker(cfg)
	result := c.Check(context.Background())
	assert.False(t, result.Success)
	assert.Contains(t, result.Detail, "unexpected status")
}

func TestHTTPChecker_MultipleResponseCodes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	host, port := serverHostPort(t, ts)
	cfg := &config.HTTPCheckConfig{
		Proto:         "http",
		Host:          host,
		Port:          port,
		URL:           "/",
		Method:        "GET",
		ResponseCodes: []int{200, 204},
	}
	c := NewHTTPChecker(cfg)
	result := c.Check(context.Background())
	assert.True(t, result.Success)
}

func TestHTTPChecker_ResponseTextMatch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "status: healthy")
	}))
	defer ts.Close()

	host, port := serverHostPort(t, ts)
	cfg := &config.HTTPCheckConfig{
		Proto:         "http",
		Host:          host,
		Port:          port,
		URL:           "/",
		Method:        "GET",
		ResponseCodes: []int{200},
		ResponseText:  "healthy",
	}
	c := NewHTTPChecker(cfg)
	result := c.Check(context.Background())
	assert.True(t, result.Success)
}

func TestHTTPChecker_ResponseTextMismatch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "status: degraded")
	}))
	defer ts.Close()

	host, port := serverHostPort(t, ts)
	cfg := &config.HTTPCheckConfig{
		Proto:         "http",
		Host:          host,
		Port:          port,
		URL:           "/",
		Method:        "GET",
		ResponseCodes: []int{200},
		ResponseText:  "healthy",
	}
	c := NewHTTPChecker(cfg)
	result := c.Check(context.Background())
	assert.False(t, result.Success)
	assert.Contains(t, result.Detail, `body missing`)
}

func TestHTTPChecker_CustomHeaders(t *testing.T) {
	var receivedHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-Custom")
		fmt.Fprint(w, "ok")
	}))
	defer ts.Close()

	host, port := serverHostPort(t, ts)
	cfg := &config.HTTPCheckConfig{
		Proto:         "http",
		Host:          host,
		Port:          port,
		URL:           "/",
		Method:        "GET",
		ResponseCodes: []int{200},
		Headers:       map[string]string{"X-Custom": "test-value"},
	}
	c := NewHTTPChecker(cfg)
	result := c.Check(context.Background())
	assert.True(t, result.Success)
	assert.Equal(t, "test-value", receivedHeader)
}

func TestHTTPChecker_HEADMethod(t *testing.T) {
	var receivedMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
	}))
	defer ts.Close()

	host, port := serverHostPort(t, ts)
	cfg := &config.HTTPCheckConfig{
		Proto:         "http",
		Host:          host,
		Port:          port,
		URL:           "/",
		Method:        "HEAD",
		ResponseCodes: []int{200},
	}
	c := NewHTTPChecker(cfg)
	c.Check(context.Background())
	assert.Equal(t, "HEAD", receivedMethod)
}

func TestHTTPChecker_TLSInsecure(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "tls ok")
	}))
	defer ts.Close()

	host, port := serverHostPort(t, ts)
	cfg := &config.HTTPCheckConfig{
		Proto:         "https",
		Host:          host,
		Port:          port,
		URL:           "/",
		Method:        "GET",
		ResponseCodes: []int{200},
		TLSInsecure:   true,
	}
	c := NewHTTPChecker(cfg)
	result := c.Check(context.Background())
	assert.True(t, result.Success)
}

func TestHTTPChecker_ContextTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client disconnects or a long timeout.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer ts.Close()

	host, port := serverHostPort(t, ts)
	cfg := &config.HTTPCheckConfig{
		Proto:         "http",
		Host:          host,
		Port:          port,
		URL:           "/",
		Method:        "GET",
		ResponseCodes: []int{200},
	}
	c := NewHTTPChecker(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success)
	assert.True(t, result.TimedOut)
}
