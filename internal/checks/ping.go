package checks

import (
	"context"
	"fmt"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	"github.com/vooon/pathosd/internal/config"
)

type PingChecker struct {
	cfg config.PingCheckConfig
}

func NewPingChecker(cfg *config.PingCheckConfig) *PingChecker {
	return &PingChecker{cfg: *cfg}
}

func (c *PingChecker) Type() string { return "ping" }

func (c *PingChecker) Check(ctx context.Context) Result {
	start := time.Now()

	if c.cfg.DstIP == "" {
		return Result{Duration: time.Since(start), Detail: "dst_ip is required"}
	}

	pinger, err := probing.NewPinger(c.cfg.DstIP)
	if err != nil {
		return Result{Duration: time.Since(start), Err: err, Detail: err.Error()}
	}
	pinger.Count = c.cfg.Count
	pinger.SetPrivileged(false)

	if c.cfg.SrcIP != "" {
		pinger.Source = c.cfg.SrcIP
	}
	if c.cfg.Timeout != nil {
		pinger.Timeout = c.cfg.Timeout.Duration
	}
	if c.cfg.Interval != nil {
		pinger.Interval = c.cfg.Interval.Duration
	}

	done := make(chan error, 1)
	go func() { done <- pinger.Run() }()

	select {
	case <-ctx.Done():
		pinger.Stop()
		return Result{Duration: time.Since(start), Detail: "ping timed out", TimedOut: true}
	case err := <-done:
		dur := time.Since(start)
		if err != nil {
			return Result{Duration: dur, Err: err, Detail: err.Error()}
		}
		stats := pinger.Statistics()
		lossRatio := stats.PacketLoss / 100.0
		if lossRatio > c.cfg.MaxLossRatio {
			return Result{Duration: dur, Detail: fmt.Sprintf("loss %.1f%% > max %.1f%%", stats.PacketLoss, c.cfg.MaxLossRatio*100)}
		}
		return Result{Success: true, Duration: dur, Detail: fmt.Sprintf("ping OK %d/%d loss=%.1f%%", stats.PacketsRecv, stats.PacketsSent, stats.PacketLoss)}
	}
}
