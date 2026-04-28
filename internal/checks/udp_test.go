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

func listenUDP(t *testing.T) (*net.UDPConn, uint16) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	port := uint16(conn.LocalAddr().(*net.UDPAddr).Port)
	t.Cleanup(func() { _ = conn.Close() })
	return conn, port
}

func reserveUDPPort(t *testing.T) uint16 {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	port := uint16(conn.LocalAddr().(*net.UDPAddr).Port)
	_ = conn.Close() // close immediately so nothing is listening on this port
	return port
}

func TestUDPChecker_Type(t *testing.T) {
	c := NewUDPChecker(&config.UDPCheckConfig{Host: "127.0.0.1", Port: 514})
	assert.Equal(t, "udp", c.Type())
}

func TestUDPChecker_PortOpen(t *testing.T) {
	_, port := listenUDP(t)

	c := NewUDPChecker(&config.UDPCheckConfig{
		Host:    "127.0.0.1",
		Port:    port,
		Payload: []byte{0x00},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.True(t, result.Success, "expected pass for open UDP port; detail: %s", result.Detail)
	assert.NotZero(t, result.Duration)
}

func TestUDPChecker_PortClosed(t *testing.T) {
	port := reserveUDPPort(t)

	c := NewUDPChecker(&config.UDPCheckConfig{
		Host:    "127.0.0.1",
		Port:    port,
		Payload: []byte{0x00},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success, "expected fail for closed UDP port; detail: %s", result.Detail)
	assert.Contains(t, result.Detail, "port closed")
}

func TestUDPChecker_MissingHost(t *testing.T) {
	c := NewUDPChecker(&config.UDPCheckConfig{Port: 514})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success)
	assert.Contains(t, result.Detail, "host is required")
}

func TestUDPChecker_MissingPort(t *testing.T) {
	c := NewUDPChecker(&config.UDPCheckConfig{Host: "127.0.0.1"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success)
	assert.Contains(t, result.Detail, "port is required")
}

func TestUDPChecker_ContextCancelled(t *testing.T) {
	_, port := listenUDP(t)

	c := NewUDPChecker(&config.UDPCheckConfig{
		Host:    "127.0.0.1",
		Port:    port,
		Payload: []byte{0x00},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	result := c.Check(ctx)
	// Either success (open port detected before deadline) or a timeout/deadline error.
	// What matters is it doesn't panic or hang.
	assert.NotZero(t, result.Duration)
}
