package checks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/config"
)

func TestNewChecker_HTTP(t *testing.T) {
	cfg := &config.CheckConfig{Type: "http", HTTP: &config.HTTPCheckConfig{}}
	c, err := NewChecker(cfg)
	require.NoError(t, err)
	assert.Equal(t, "http", c.Type())
}

func TestNewChecker_DNS(t *testing.T) {
	cfg := &config.CheckConfig{Type: "dns", DNS: &config.DNSCheckConfig{}}
	c, err := NewChecker(cfg)
	require.NoError(t, err)
	assert.Equal(t, "dns", c.Type())
}

func TestNewChecker_Ping(t *testing.T) {
	cfg := &config.CheckConfig{Type: "ping", Ping: &config.PingCheckConfig{}}
	c, err := NewChecker(cfg)
	require.NoError(t, err)
	assert.Equal(t, "ping", c.Type())
}

func TestNewChecker_UnknownType(t *testing.T) {
	cfg := &config.CheckConfig{Type: "unknown"}
	_, err := NewChecker(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported check type "unknown"`)
}

func TestNewChecker_HTTP_NilConfig(t *testing.T) {
	cfg := &config.CheckConfig{Type: "http"}
	_, err := NewChecker(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "http check config is nil")
}

func TestNewChecker_DNS_NilConfig(t *testing.T) {
	cfg := &config.CheckConfig{Type: "dns"}
	_, err := NewChecker(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dns check config is nil")
}

func TestNewChecker_Ping_NilConfig(t *testing.T) {
	cfg := &config.CheckConfig{Type: "ping"}
	_, err := NewChecker(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ping check config is nil")
}
