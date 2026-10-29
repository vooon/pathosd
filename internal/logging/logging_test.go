package logging

import (
	"bytes"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetup(t *testing.T) {
	tests := []struct {
		level  string
		format string
	}{
		{"debug", "text"},
		{"info", "json"},
		{"warn", "text"},
		{"error", "json"},
		{"", "text"},    // invalid level → INFO
		{"info", "xml"}, // unknown format → charm handler
	}

	for _, tc := range tests {
		t.Run(tc.level+"/"+tc.format, func(t *testing.T) {
			logger := Setup(tc.level, tc.format, true)
			require.NotNil(t, logger)
			assert.Equal(t, logger, slog.Default())
		})
	}
}

func TestNewPrimaryHandler_Timestamps(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, ts := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/timestamps=%v", format, ts), func(t *testing.T) {
				var buf bytes.Buffer
				require.NoError(t, SetLevel("info"))
				logger := slog.New(newPrimaryHandler(&buf, format, ts))
				logger.Info("hello", "k", "v")
				out := buf.String()
				assert.Contains(t, out, "hello")
				year := time.Now().UTC().Format("2006")
				if ts {
					assert.Contains(t, out, year, "timestamp expected")
				} else {
					assert.NotContains(t, out, year, "no timestamp expected")
					assert.NotContains(t, out, `"time"`)
				}
			})
		}
	}
}

func TestSetLevel_Live(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, SetLevel("info"))
			logger := slog.New(newPrimaryHandler(&buf, format, false))
			logger.Debug("hidden")
			assert.NotContains(t, buf.String(), "hidden")
			require.NoError(t, SetLevel("debug"))
			logger.Debug("shown")
			assert.Contains(t, buf.String(), "shown")
			require.NoError(t, SetLevel("info"))
		})
	}
	assert.Error(t, SetLevel("loud"))
}
