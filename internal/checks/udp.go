package checks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/vooon/pathosd/internal/config"
)

// UDPChecker probes a UDP port by sending a small datagram and waiting for an
// ICMP port-unreachable reply. On Linux, when a UDP socket is connected and a
// datagram is sent to a port with no listener, the kernel delivers the ICMP
// error back to the socket as ECONNREFUSED on the next I/O call — without
// requiring elevated privileges or raw sockets.
//
// Decision logic:
//   - ECONNREFUSED on read → nothing is listening → check fails
//   - Read timeout (no ICMP received) → datagram was accepted → check passes
//   - Actual data received → server responded → check passes
type UDPChecker struct {
	cfg config.UDPCheckConfig
}

func NewUDPChecker(cfg *config.UDPCheckConfig) *UDPChecker {
	return &UDPChecker{cfg: *cfg}
}

func (c *UDPChecker) Type() string { return "udp" }

func (c *UDPChecker) Check(ctx context.Context) Result {
	start := time.Now()

	if c.cfg.Host == "" {
		err := fmt.Errorf("udp check: host is required")
		return Result{Duration: time.Since(start), Err: err, Detail: err.Error()}
	}
	if c.cfg.Port == 0 {
		err := fmt.Errorf("udp check: port is required")
		return Result{Duration: time.Since(start), Err: err, Detail: err.Error()}
	}

	addr := fmt.Sprintf("%s:%d", c.cfg.Host, c.cfg.Port)

	// Resolve address.
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return Result{Duration: time.Since(start), Err: err, Detail: fmt.Sprintf("resolve %s: %v", addr, err)}
	}

	// Connect UDP socket — sets destination so ICMP errors are delivered back.
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return Result{Duration: time.Since(start), Err: err, Detail: fmt.Sprintf("dial udp %s: %v", addr, err)}
	}
	defer func() { _ = conn.Close() }()

	// Propagate context deadline to the socket.
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return Result{Duration: time.Since(start), Err: err, Detail: err.Error()}
		}
	}

	// Send probe datagram.
	payload := c.cfg.Payload
	if len(payload) == 0 {
		payload = []byte{0x00}
	}
	if _, err := conn.Write(payload); err != nil {
		return Result{Duration: time.Since(start), Err: err, Detail: fmt.Sprintf("write: %v", err)}
	}

	// Wait for ICMP port-unreachable (ECONNREFUSED) or a real response.
	// Use a short read window: 500 ms or until the context deadline, whichever comes first.
	readTimeout := 500 * time.Millisecond
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < readTimeout {
			readTimeout = remaining
		}
	}
	if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return Result{Duration: time.Since(start), Err: err, Detail: err.Error()}
	}

	buf := make([]byte, 256)
	_, err = conn.Read(buf)
	dur := time.Since(start)

	if err == nil {
		// Received an actual response — server is alive.
		return Result{Success: true, Duration: dur, Detail: fmt.Sprintf("udp %s: received response", addr)}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		// Read timed out with no ICMP — datagram was consumed, port is listening.
		return Result{Success: true, Duration: dur, Detail: fmt.Sprintf("udp %s: no ICMP port-unreachable (port open)", addr)}
	}

	// ECONNREFUSED → ICMP port-unreachable → nothing is listening.
	if isConnRefused(err) {
		return Result{Duration: dur, Detail: fmt.Sprintf("udp %s: ICMP port unreachable (port closed)", addr)}
	}

	// Any other error.
	return Result{Duration: dur, Err: err, Detail: fmt.Sprintf("udp %s: %v", addr, err)}
}
