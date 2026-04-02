package checks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/config"
)

func TestNewChecker(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.CheckConfig
		wantType    string
		wantErrContains string
	}{
		{
			name:     "http",
			cfg:      &config.CheckConfig{Type: "http", HTTP: &config.HTTPCheckConfig{}},
			wantType: "http",
		},
		{
			name:     "dns",
			cfg:      &config.CheckConfig{Type: "dns", DNS: &config.DNSCheckConfig{}},
			wantType: "dns",
		},
		{
			name:     "ping",
			cfg:      &config.CheckConfig{Type: "ping", Ping: &config.PingCheckConfig{}},
			wantType: "ping",
		},
		{
			name:            "unknown type",
			cfg:             &config.CheckConfig{Type: "unknown"},
			wantErrContains: `unsupported check type "unknown"`,
		},
		{
			name:            "http nil sub-config",
			cfg:             &config.CheckConfig{Type: "http"},
			wantErrContains: "http check config is nil",
		},
		{
			name:            "dns nil sub-config",
			cfg:             &config.CheckConfig{Type: "dns"},
			wantErrContains: "dns check config is nil",
		},
		{
			name:            "ping nil sub-config",
			cfg:             &config.CheckConfig{Type: "ping"},
			wantErrContains: "ping check config is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewChecker(tt.cfg)
			if tt.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, c.Type())
		})
	}
}
