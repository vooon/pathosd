package logging

import (
	"io"
	"log/slog"
	"os"

	charmlog "github.com/charmbracelet/log"
)

func Setup(level, format string) *slog.Logger {
	var handler slog.Handler
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(level))

	switch format {
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	default:
		handler = newCharmHandler(os.Stderr, lvl)
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
