package bgp

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"

	gobgplog "github.com/osrg/gobgp/v3/pkg/log"
)

type goBGPLogger struct {
	logger *slog.Logger
	level  atomic.Uint32
}

func newGoBGPLogger(logger *slog.Logger, level string) *goBGPLogger {
	if logger == nil {
		logger = slog.Default()
	}
	l := &goBGPLogger{logger: logger}
	l.SetLevel(goBGPLevelFromString(level))
	return l
}

func goBGPLevelFromString(level string) gobgplog.LogLevel {
	switch level {
	case "debug":
		return gobgplog.DebugLevel
	case "warn":
		return gobgplog.WarnLevel
	case "error":
		return gobgplog.ErrorLevel
	default:
		return gobgplog.InfoLevel
	}
}

func (l *goBGPLogger) Panic(msg string, fields gobgplog.Fields) {
	l.log(gobgplog.PanicLevel, msg, fields)
	panic(msg)
}

func (l *goBGPLogger) Fatal(msg string, fields gobgplog.Fields) {
	l.log(gobgplog.FatalLevel, msg, fields)
	os.Exit(1)
}

func (l *goBGPLogger) Error(msg string, fields gobgplog.Fields) {
	l.log(gobgplog.ErrorLevel, msg, fields)
}

func (l *goBGPLogger) Warn(msg string, fields gobgplog.Fields) {
	l.log(gobgplog.WarnLevel, msg, fields)
}

func (l *goBGPLogger) Info(msg string, fields gobgplog.Fields) {
	l.log(gobgplog.InfoLevel, msg, fields)
}

func (l *goBGPLogger) Debug(msg string, fields gobgplog.Fields) {
	l.log(gobgplog.DebugLevel, msg, fields)
}

func (l *goBGPLogger) SetLevel(level gobgplog.LogLevel) {
	l.level.Store(uint32(level))
}

func (l *goBGPLogger) GetLevel() gobgplog.LogLevel {
	return gobgplog.LogLevel(l.level.Load())
}

func (l *goBGPLogger) log(level gobgplog.LogLevel, msg string, fields gobgplog.Fields) {
	if level > l.GetLevel() {
		return
	}

	attrs := make([]any, 0, len(fields)*2)
	for k, v := range fields {
		attrs = append(attrs, normalizeFieldKey(k), v)
	}
	l.logger.Log(context.Background(), slogLevel(level), msg, attrs...)
}

func normalizeFieldKey(k string) string {
	if k == "" {
		return "field"
	}
	return strings.ToLower(k)
}

func slogLevel(level gobgplog.LogLevel) slog.Level {
	switch level {
	case gobgplog.PanicLevel, gobgplog.FatalLevel, gobgplog.ErrorLevel:
		return slog.LevelError
	case gobgplog.WarnLevel:
		return slog.LevelWarn
	case gobgplog.InfoLevel:
		return slog.LevelInfo
	default:
		return slog.LevelDebug
	}
}
