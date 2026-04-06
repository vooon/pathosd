package bgp

import (
	"log/slog"
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
