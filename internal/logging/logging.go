package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync/atomic"

	charmlog "github.com/charmbracelet/log"
)

// Setup creates a structured slog.Logger and installs it as the global default.
// Any additional slog.Handlers (e.g. an OTEL log bridge) can be appended via
// the extra variadic parameter.  Log records are sent to all handlers.
// timestamps controls whether console records carry a time; disable it under
// journald, which records its own.
func Setup(level, format string, timestamps bool, extra ...slog.Handler) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(level))
	levelVar.Set(lvl)
	primary := newPrimaryHandler(os.Stderr, format, timestamps)

	handler := primary
	if len(extra) > 0 {
		all := make([]slog.Handler, 0, 1+len(extra))
		all = append(all, primary)
		all = append(all, extra...)
		handler = &multiHandler{handlers: all}
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}

// levelVar is the console level; SetLevel changes it at runtime (config
// reload of logging.level). The charmbracelet handler keeps its own copy,
// so SetLevel also updates the most recently built one.
var (
	levelVar    = new(slog.LevelVar)
	charmLogger atomic.Pointer[charmlog.Logger]
)

// SetLevel changes the console log level of the logger installed by Setup.
func SetLevel(level string) error {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return err
	}
	levelVar.Set(lvl)
	if l := charmLogger.Load(); l != nil {
		l.SetLevel(charmlog.Level(lvl))
	}
	return nil
}

func newPrimaryHandler(w io.Writer, format string, timestamps bool) slog.Handler {
	switch format {
	case "json":
		opts := &slog.HandlerOptions{Level: levelVar}
		if !timestamps {
			opts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
				if len(groups) == 0 && a.Key == slog.TimeKey {
					return slog.Attr{}
				}
				return a
			}
		}
		return slog.NewJSONHandler(w, opts)
	default:
		h := charmlog.NewWithOptions(w, charmlog.Options{
			Level:           charmlog.Level(levelVar.Level()),
			ReportTimestamp: timestamps,
		})
		charmLogger.Store(h)
		return h
	}
}

// multiHandler fans out slog records to multiple handlers.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var last error
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				last = err
			}
		}
	}
	return last
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}

// NewLevelFilter wraps inner, silently dropping records below minLevel.
// Use this to give a specific handler (e.g. the OTEL log bridge) a different
// minimum level than the console handler.
func NewLevelFilter(minLevel slog.Level, inner slog.Handler) slog.Handler {
	return &levelFilterHandler{minLevel: minLevel, inner: inner}
}

type levelFilterHandler struct {
	minLevel slog.Level
	inner    slog.Handler
}

func (f *levelFilterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= f.minLevel && f.inner.Enabled(ctx, level)
}

func (f *levelFilterHandler) Handle(ctx context.Context, r slog.Record) error {
	return f.inner.Handle(ctx, r)
}

func (f *levelFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelFilterHandler{minLevel: f.minLevel, inner: f.inner.WithAttrs(attrs)}
}

func (f *levelFilterHandler) WithGroup(name string) slog.Handler {
	return &levelFilterHandler{minLevel: f.minLevel, inner: f.inner.WithGroup(name)}
}
