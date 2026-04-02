package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_RegistryNotNil(t *testing.T) {
	m := New(nil)
	require.NotNil(t, m)
	assert.NotNil(t, m.Registry)
}

func TestNew_RegistryGatherSucceeds(t *testing.T) {
	m := New(GenerateCheckBuckets(2 * time.Second))

	// Seed one observation on each vec so Gather returns all families.
	m.VIPState.WithLabelValues("v", "10.0.0.1/32").Set(0)
	m.VIPTransitions.WithLabelValues("v", "withdrawn").Add(0)
	m.VIPLastTransition.WithLabelValues("v").Set(0)
	m.VIPPriority.WithLabelValues("v", "10.0.0.1/32").Set(0)
	m.RouteState.WithLabelValues("10.0.0.1/32", "10.0.0.254", "65000", "", "", "100", "0", "ipv4-unicast").Set(0)
	m.CheckTotal.WithLabelValues("v", "http", "success").Add(0)
	m.CheckAbsorbed.WithLabelValues("v").Add(0)
	m.CheckDuration.WithLabelValues("v", "http").Observe(0)
	m.CheckLastResult.WithLabelValues("v").Set(0)
	m.CheckTimeoutExceeded.WithLabelValues("v").Add(0)

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)

	names := make(map[string]struct{}, len(mfs))
	for _, mf := range mfs {
		names[mf.GetName()] = struct{}{}
	}

	expected := []string{
		"pathosd_vip_state",
		"pathosd_vip_transitions_total",
		"pathosd_vip_last_transition_timestamp",
		"pathosd_vip_priority_status",
		"pathosd_route_state",
		"pathosd_check_total",
		"pathosd_check_rise_fall_absorbed_total",
		"pathosd_check_duration_seconds",
		"pathosd_check_last_result",
		"pathosd_check_timeout_exceeded_total",
	}
	for _, name := range expected {
		assert.Contains(t, names, name, "metric %q not found in registry", name)
	}
}

func TestNew_VIPStateUpdatable(t *testing.T) {
	m := New(nil)
	m.VIPState.WithLabelValues("web-vip", "10.0.0.1/32").Set(1)

	value := testutil.ToFloat64(m.VIPState.WithLabelValues("web-vip", "10.0.0.1/32"))
	assert.Equal(t, float64(1), value)
}

func TestNew_CheckTotalIncrementable(t *testing.T) {
	m := New(nil)
	m.CheckTotal.WithLabelValues("web-vip", "http", "success").Inc()

	value := testutil.ToFloat64(m.CheckTotal.WithLabelValues("web-vip", "http", "success"))
	assert.Equal(t, float64(1), value)
}

func TestGenerateCheckBuckets(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
	}{
		{"100ms", 100 * time.Millisecond},
		{"500ms", 500 * time.Millisecond},
		{"2s", 2 * time.Second},
		{"10s", 10 * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buckets := GenerateCheckBuckets(tc.timeout)
			require.NotEmpty(t, buckets)

			// Buckets must be strictly increasing.
			for i := 1; i < len(buckets); i++ {
				assert.Greater(t, buckets[i], buckets[i-1], "bucket[%d] <= bucket[%d]", i, i-1)
			}

			// Last bucket must be >= timeout seconds.
			assert.GreaterOrEqual(t, buckets[len(buckets)-1], tc.timeout.Seconds())
		})
	}
}
