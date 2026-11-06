package bgp

import (
	"context"
	"log/slog"
	"strings"
)

func newGoBGPLevelVar(level string) *slog.LevelVar {
	lv := new(slog.LevelVar)
	lv.Set(goBGPLevelFromString(level))
	return lv
}

func goBGPLevelFromString(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// goBGPConnectFailedMsg is the message GoBGP's connect loop logs, at debug
// level, after every failed dial of an active peer.
const goBGPConnectFailedMsg = "failed to connect"

// goBGPErrorKey is the attribute GoBGP attaches the dial error under.
const goBGPErrorKey = "Error"

// ttlHint explains the one dial failure an operator cannot see from the
// peer state alone: an eBGP SYN leaves with TTL 1 unless multihop is set,
// so a peer two hops away answers with ICMP time-exceeded, the kernel
// aborts the connect with EHOSTUNREACH and the FSM just stays in active.
const ttlHint = "eBGP sessions dial with TTL 1 unless enable_multihop is set; a peer more than one hop away cannot answer"

// newGoBGPHandler wraps the daemon's handler for the embedded GoBGP logger.
// GoBGP reports a failed connection attempt only at debug level, which hides
// the dial error (no route to host, connection refused, timeout) behind a
// peer that merely stays in active. The wrapper lets debug records through
// to Handle, re-emits the connect failure at info and drops the rest unless
// the inner handler wants debug anyway.
func newGoBGPHandler(inner slog.Handler) slog.Handler {
	return &goBGPHandler{inner: inner}
}

type goBGPHandler struct {
	inner slog.Handler
}

func (h *goBGPHandler) Enabled(ctx context.Context, level slog.Level) bool {
	// Debug must pass so the connect failure reaches Handle; everything
	// else follows the inner handler.
	return level <= slog.LevelDebug || h.inner.Enabled(ctx, level)
}

func (h *goBGPHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level > slog.LevelDebug || h.inner.Enabled(ctx, slog.LevelDebug) {
		return h.inner.Handle(ctx, r)
	}
	if r.Message != goBGPConnectFailedMsg || !h.inner.Enabled(ctx, slog.LevelInfo) {
		return nil
	}
	promoted := slog.NewRecord(r.Time, slog.LevelInfo, r.Message, r.PC)
	var dialErr string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == goBGPErrorKey {
			dialErr = a.Value.String()
		}
		promoted.AddAttrs(a)
		return true
	})
	if strings.Contains(dialErr, "no route to host") {
		promoted.AddAttrs(slog.String("hint", ttlHint))
	}
	return h.inner.Handle(ctx, promoted)
}

func (h *goBGPHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &goBGPHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *goBGPHandler) WithGroup(name string) slog.Handler {
	return &goBGPHandler{inner: h.inner.WithGroup(name)}
}
