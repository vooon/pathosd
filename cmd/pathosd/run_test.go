package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vooon/pathosd/internal/config"
)

func TestApplyRunOverrides(t *testing.T) {
	tests := []struct {
		name      string
		initial   string
		debug     bool
		wantLevel string
	}{
		{
			name:      "debug flag disabled keeps configured level",
			initial:   "info",
			debug:     false,
			wantLevel: "info",
		},
		{
			name:      "debug flag enabled overrides configured level",
			initial:   "warn",
			debug:     true,
			wantLevel: "debug",
		},
		{
			name:      "debug flag enabled also overrides empty level",
			initial:   "",
			debug:     true,
			wantLevel: "debug",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Logging: config.LoggingConfig{
					Level: tc.initial,
				},
			}

			applyRunOverrides(cfg, tc.debug)
			assert.Equal(t, tc.wantLevel, cfg.Logging.Level)
		})
	}
}
