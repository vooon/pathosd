package checks

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestPingChecker_NewConfiguredPinger(t *testing.T) {
	t.Run("unprivileged mode by default", func(t *testing.T) {
		c := NewPingChecker(&config.PingCheckConfig{
			DstIP:   "127.0.0.1",
			Count:   3,
			Timeout: &config.Duration{Duration: 500 * time.Millisecond},
		})

		pinger, err := c.newConfiguredPinger()
		require.NoError(t, err)
		assert.False(t, pinger.Privileged())
		assert.Equal(t, 3, pinger.Count)
		assert.Equal(t, 500*time.Millisecond, pinger.Timeout)
	})

	t.Run("privileged mode enabled from config", func(t *testing.T) {
		c := NewPingChecker(&config.PingCheckConfig{
			DstIP:        "127.0.0.1",
			Count:        1,
			Privileged:   true,
			SrcIP:        "127.0.0.1",
			Interval:     &config.Duration{Duration: 100 * time.Millisecond},
			Timeout:      &config.Duration{Duration: 300 * time.Millisecond},
			MaxLossRatio: 0.2,
		})

		pinger, err := c.newConfiguredPinger()
		require.NoError(t, err)
		assert.True(t, pinger.Privileged())
		assert.Equal(t, "127.0.0.1", pinger.Source)
		assert.Equal(t, 100*time.Millisecond, pinger.Interval)
		assert.Equal(t, 300*time.Millisecond, pinger.Timeout)
	})
}
