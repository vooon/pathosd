package bgp

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	gobgplog "github.com/osrg/gobgp/v3/pkg/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type logRecord struct {
	message string
	level   slog.Level
}

type captureHandler struct {
	mu      sync.Mutex
	records []logRecord
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, logRecord{message: r.Message, level: r.Level})
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *captureHandler) WithGroup(string) slog.Handler {
	return h
}

func (h *captureHandler) snapshot() []logRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]logRecord, len(h.records))
	copy(out, h.records)
	return out
}

func TestGoBGPLevelFromString(t *testing.T) {
	assert.Equal(t, gobgplog.DebugLevel, goBGPLevelFromString("debug"))
	assert.Equal(t, gobgplog.WarnLevel, goBGPLevelFromString("warn"))
	assert.Equal(t, gobgplog.ErrorLevel, goBGPLevelFromString("error"))
	assert.Equal(t, gobgplog.InfoLevel, goBGPLevelFromString("info"))
	assert.Equal(t, gobgplog.InfoLevel, goBGPLevelFromString("unknown"))
}

func TestGoBGPLoggerHonorsLevel(t *testing.T) {
	h := &captureHandler{}
	l := newGoBGPLogger(slog.New(h), "info")

	l.Debug("debug-message", nil)
	l.Info("info-message", gobgplog.Fields{"Topic": "Peer"})

	records := h.snapshot()
	require.Len(t, records, 1)
	assert.Equal(t, "info-message", records[0].message)
	assert.Equal(t, slog.LevelInfo, records[0].level)

	l.SetLevel(gobgplog.DebugLevel)
	l.Debug("debug-message-2", nil)

	records = h.snapshot()
	require.Len(t, records, 2)
	assert.Equal(t, "debug-message-2", records[1].message)
	assert.Equal(t, slog.LevelDebug, records[1].level)
}

func TestNormalizeFieldKey(t *testing.T) {
	assert.Equal(t, "topic", normalizeFieldKey("Topic"))
	assert.Equal(t, "key", normalizeFieldKey("Key"))
	assert.Equal(t, "field", normalizeFieldKey(""))
}
