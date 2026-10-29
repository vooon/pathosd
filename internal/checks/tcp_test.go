package checks

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/config"
)

func listenTCP(t *testing.T) (*net.TCPListener, uint16) {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	t.Cleanup(func() { _ = ln.Close() })
	return ln, port
}

func reserveTCPPortForChecker(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	return port
}

func TestTCPChecker_Type(t *testing.T) {
	c := NewTCPChecker(&config.TCPCheckConfig{Host: "127.0.0.1", Port: 80})
	assert.Equal(t, "tcp", c.Type())
}

func TestTCPChecker_PortOpen(t *testing.T) {
	_, port := listenTCP(t)

	c := NewTCPChecker(&config.TCPCheckConfig{Host: "127.0.0.1", Port: port})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.True(t, result.Success, "expected pass for open TCP port; detail: %s", result.Detail)
	assert.Contains(t, result.Detail, "connection accepted")
	assert.NotZero(t, result.Duration)
}

func TestTCPChecker_PortClosed(t *testing.T) {
	port := reserveTCPPortForChecker(t)

	c := NewTCPChecker(&config.TCPCheckConfig{Host: "127.0.0.1", Port: port})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success, "expected fail for closed TCP port; detail: %s", result.Detail)
	assert.NotEmpty(t, result.Detail)
}

func TestTCPChecker_Timeout(t *testing.T) {
	// An already-expired deadline makes the dialer give up before any packet
	// leaves, so the outcome does not depend on the network. Dialing a
	// black-hole address with a short live deadline was flaky on CI: an ICMP
	// unreachable arriving before the deadline is a plain dial error, not a
	// timeout.
	_, port := listenTCP(t)
	c := NewTCPChecker(&config.TCPCheckConfig{Host: "127.0.0.1", Port: port})

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success, "detail: %s", result.Detail)
	assert.True(t, result.TimedOut, "detail: %s", result.Detail)
}

func TestTCPChecker_MissingHost(t *testing.T) {
	c := NewTCPChecker(&config.TCPCheckConfig{Port: 80})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success)
	assert.Contains(t, result.Detail, "host is required")
}

func TestTCPChecker_MissingPort(t *testing.T) {
	c := NewTCPChecker(&config.TCPCheckConfig{Host: "127.0.0.1"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success)
	assert.Contains(t, result.Detail, "port is required")
}

// TestTCPChecker_IPv6Host verifies that a bare IPv6 host is bracketed when
// forming the dial address, so IPv6 /128 VIPs (host defaults to the VIP IP)
// can be checked.
func TestTCPChecker_IPv6Host(t *testing.T) {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("::1"), Port: 0})
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := uint16(ln.Addr().(*net.TCPAddr).Port)

	c := NewTCPChecker(&config.TCPCheckConfig{Host: "::1", Port: port})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.True(t, result.Success, "expected pass for open IPv6 TCP port; detail: %s", result.Detail)
	assert.Contains(t, result.Detail, "[::1]:")
}
