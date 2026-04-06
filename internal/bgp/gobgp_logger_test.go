package bgp

import (
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
