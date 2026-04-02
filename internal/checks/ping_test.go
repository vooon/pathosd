package checks

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vooon/pathosd/internal/config"
)

func TestPingChecker_Type(t *testing.T) {
	c := NewPingChecker(&config.PingCheckConfig{})
	assert.Equal(t, "ping", c.Type())
}

func TestPingChecker_EmptyDstIP(t *testing.T) {
	c := NewPingChecker(&config.PingCheckConfig{})
	result := c.Check(context.TODO())
	assert.False(t, result.Success)
	assert.Contains(t, result.Detail, "dst_ip is required")
}
