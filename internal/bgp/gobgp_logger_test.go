package bgp

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGoBGPLevelFromString(t *testing.T) {
	assert.Equal(t, slog.LevelDebug, goBGPLevelFromString("debug"))
	assert.Equal(t, slog.LevelWarn, goBGPLevelFromString("warn"))
	assert.Equal(t, slog.LevelError, goBGPLevelFromString("error"))
	assert.Equal(t, slog.LevelInfo, goBGPLevelFromString("info"))
	assert.Equal(t, slog.LevelInfo, goBGPLevelFromString("unknown"))
}

func TestNewGoBGPLevelVar(t *testing.T) {
	t.Run("initializes to parsed level", func(t *testing.T) {
		lv := newGoBGPLevelVar("warn")
		assert.Equal(t, slog.LevelWarn, lv.Level())
	})

	t.Run("defaults to info for unknown levels", func(t *testing.T) {
		lv := newGoBGPLevelVar("bad-level")
		assert.Equal(t, slog.LevelInfo, lv.Level())
	})
}

// recordingHandler keeps every record it is asked to handle.
type recordingHandler struct {
	level   slog.Level
	records *[]slog.Record
	attrs   []slog.Attr
}

func (h *recordingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	r.AddAttrs(h.attrs...)
	*h.records = append(*h.records, r)
	return nil
}

func (h *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &recordingHandler{level: h.level, records: h.records, attrs: append(append([]slog.Attr{}, h.attrs...), attrs...)}
}

func (h *recordingHandler) WithGroup(string) slog.Handler { return h }

func recordAttrs(r slog.Record) map[string]string {
	out := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value.String()
		return true
	})
	return out
}

func TestGoBGPHandler(t *testing.T) {
	newLogger := func(level slog.Level) (*slog.Logger, *[]slog.Record) {
		records := &[]slog.Record{}
		inner := &recordingHandler{level: level, records: records}
		return slog.New(newGoBGPHandler(inner)).With("component", "gobgp"), records
	}

	t.Run("promotes connect failure to info", func(t *testing.T) {
		logger, records := newLogger(slog.LevelInfo)
		logger.With("Topic", "Peer", "Key", "10.0.0.2").Debug(goBGPConnectFailedMsg, slog.String(goBGPErrorKey, "dial tcp 10.0.0.2:179: connect: connection refused"))

		if assert.Len(t, *records, 1) {
			r := (*records)[0]
			assert.Equal(t, slog.LevelInfo, r.Level)
			assert.Equal(t, goBGPConnectFailedMsg, r.Message)
			attrs := recordAttrs(r)
			assert.Equal(t, "dial tcp 10.0.0.2:179: connect: connection refused", attrs[goBGPErrorKey])
			assert.Equal(t, "10.0.0.2", attrs["Key"])
			assert.Equal(t, "gobgp", attrs["component"])
			assert.NotContains(t, attrs, "hint")
		}
	})

	t.Run("adds the TTL hint on no route to host", func(t *testing.T) {
		logger, records := newLogger(slog.LevelInfo)
		logger.Debug(goBGPConnectFailedMsg, slog.String(goBGPErrorKey, "dial tcp 10.0.0.2:179: connect: no route to host"))

		if assert.Len(t, *records, 1) {
			assert.Equal(t, ttlHint, recordAttrs((*records)[0])["hint"])
		}
	})

	t.Run("drops other debug records at info level", func(t *testing.T) {
		logger, records := newLogger(slog.LevelInfo)
		logger.Debug("try to connect")
		logger.Debug("sent update")
		assert.Empty(t, *records)
	})

	t.Run("drops the connect failure at warn level", func(t *testing.T) {
		logger, records := newLogger(slog.LevelWarn)
		logger.Debug(goBGPConnectFailedMsg, slog.String(goBGPErrorKey, "x"))
		assert.Empty(t, *records)
	})

	t.Run("passes everything through at debug level", func(t *testing.T) {
		logger, records := newLogger(slog.LevelDebug)
		logger.Debug("try to connect")
		logger.Debug(goBGPConnectFailedMsg, slog.String(goBGPErrorKey, "x"))
		logger.Info("Peer Up")
		if assert.Len(t, *records, 3) {
			assert.Equal(t, slog.LevelDebug, (*records)[1].Level)
		}
	})

	t.Run("info and above are untouched", func(t *testing.T) {
		logger, records := newLogger(slog.LevelInfo)
		logger.Info("Peer Up")
		logger.Warn("cannot set TTL")
		if assert.Len(t, *records, 2) {
			assert.Equal(t, "Peer Up", (*records)[0].Message)
			assert.Equal(t, slog.LevelWarn, (*records)[1].Level)
		}
	})
}
