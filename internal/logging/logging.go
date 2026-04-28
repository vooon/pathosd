package logging

import (
	"context"
	"io"
	"log/slog"
	"os"

	charmlog "github.com/charmbracelet/log"
)

// Setup creates a structured slog.Logger and installs it as the global default.
// Any additional slog.Handlers (e.g. an OTEL log bridge) can be appended via
// the extra variadic parameter.  Log records are sent to all handlers.
func Setup(level, format string, extra ...slog.Handler) *slog.Logger {
	var primary slog.Handler
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(level))

	switch format {
	case "json":
		primary = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	default:
		primary = newCharmHandler(os.Stderr, lvl)
	}

	var handler slog.Handler = primary
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

func newCharmHandler(w io.Writer, level slog.Level) slog.Handler {
	l := charmlog.NewWithOptions(w, charmlog.Options{
		Level:           charmlog.Level(level),
		ReportTimestamp: true,
	})
	return l
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
