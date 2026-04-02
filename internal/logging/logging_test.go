package logging

import (
	"log/slog"
	"testing"

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
			logger := Setup(tc.level, tc.format)
			require.NotNil(t, logger)
			assert.Equal(t, logger, slog.Default())
		})
	}
}
