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
	// Use a routable but non-responsive address to force a dial timeout.
	// 192.0.2.1 is TEST-NET-1 (RFC 5737) — no host will answer.
	c := NewTCPChecker(&config.TCPCheckConfig{Host: "192.0.2.1", Port: 9999})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success)
	assert.True(t, result.TimedOut)
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
