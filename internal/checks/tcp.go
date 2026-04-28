package checks

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/vooon/pathosd/internal/config"
)

// TCPChecker dials host:port over TCP. A successful connection means the port
// is open and accepting connections; the connection is closed immediately after.
type TCPChecker struct {
	cfg config.TCPCheckConfig
}

func NewTCPChecker(cfg *config.TCPCheckConfig) *TCPChecker {
	return &TCPChecker{cfg: *cfg}
}

func (c *TCPChecker) Type() string { return "tcp" }

func (c *TCPChecker) Check(ctx context.Context) Result {
	start := time.Now()

	if c.cfg.Host == "" {
		err := fmt.Errorf("tcp check: host is required")
		return Result{Duration: time.Since(start), Err: err, Detail: err.Error()}
	}
	if c.cfg.Port == 0 {
		err := fmt.Errorf("tcp check: port is required")
		return Result{Duration: time.Since(start), Err: err, Detail: err.Error()}
	}

	addr := fmt.Sprintf("%s:%d", c.cfg.Host, c.cfg.Port)

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	dur := time.Since(start)

	if err != nil {
		timedOut := ctx.Err() != nil
		return Result{Duration: dur, Err: err, Detail: fmt.Sprintf("dial tcp %s: %v", addr, err), TimedOut: timedOut}
	}
	_ = conn.Close()

	return Result{Success: true, Duration: dur, Detail: fmt.Sprintf("tcp %s: connection accepted", addr)}
}
