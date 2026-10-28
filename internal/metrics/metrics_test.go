package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/model"
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
	m.VIPTransitions.WithLabelValues("v", "10.0.0.1/32", "withdrawn").Add(0)
	m.VIPLastTransition.WithLabelValues("v", "10.0.0.1/32").Set(0)
	m.VIPPriority.WithLabelValues("v", "10.0.0.1/32").Set(0)
	m.RouteState.WithLabelValues("10.0.0.1/32", "10.0.0.254", "65000", "", "", "100", "0", "ipv4-unicast").Set(0)
	m.CheckTotal.WithLabelValues("v", "10.0.0.1/32", "http", "http", "success").Add(0)
	m.CheckDuration.WithLabelValues("v", "10.0.0.1/32", "http", "http").Observe(0)
	m.CheckLastResult.WithLabelValues("v", "10.0.0.1/32", "http").Set(0)
	m.CheckTimeoutExceeded.WithLabelValues("v", "10.0.0.1/32", "http").Add(0)

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
	m.CheckTotal.WithLabelValues("web-vip", "10.0.0.1/32", "http", "http", "success").Inc()

	value := testutil.ToFloat64(m.CheckTotal.WithLabelValues("web-vip", "10.0.0.1/32", "http", "http", "success"))
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

func TestObserveCheckDetails(t *testing.T) {
	m := New([]float64{0.001, 0.01, 0.1})
	const vip, nlri = "vip-1", "10.0.0.1/32"

	t.Run("nil details are ignored", func(t *testing.T) {
		m.ObserveCheckDetails(vip, nlri, "http", nil)
		assert.Equal(t, 0, testutil.CollectAndCount(m.HTTPResponseStatus))
	})

	t.Run("ping", func(t *testing.T) {
		m.ObserveCheckDetails(vip, nlri, "http", &model.PingDetails{
			RTTs: []time.Duration{2 * time.Millisecond, 3 * time.Millisecond}, PacketLoss: 0.25,
		})
		assert.Equal(t, 0.25, testutil.ToFloat64(m.PingPacketLoss.WithLabelValues(vip, nlri, "http")))
		assert.Equal(t, 1, testutil.CollectAndCount(m.PingRTT))
	})

	t.Run("http", func(t *testing.T) {
		exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
		m.ObserveCheckDetails(vip, nlri, "http", &model.HTTPDetails{StatusCode: 503, TLSCertNotAfter: exp})
		assert.Equal(t, float64(503), testutil.ToFloat64(m.HTTPResponseStatus.WithLabelValues(vip, nlri, "http")))
		assert.Equal(t, float64(exp.Unix()), testutil.ToFloat64(m.HTTPTLSCertExpiry.WithLabelValues(vip, nlri, "http")))
	})

	t.Run("http without TLS leaves expiry untouched", func(t *testing.T) {
		m2 := New(nil)
		m2.ObserveCheckDetails(vip, nlri, "http", &model.HTTPDetails{StatusCode: 200})
		assert.Equal(t, 0, testutil.CollectAndCount(m2.HTTPTLSCertExpiry))
	})

	t.Run("dns", func(t *testing.T) {
		m.ObserveCheckDetails(vip, nlri, "http", &model.DNSDetails{Names: []model.DNSNameDetails{
			{Name: "a.example", Rcode: 0, Answers: 2},
			{Name: "b.example", Rcode: 3, Answers: 0},
		}})
		assert.Equal(t, float64(2), testutil.ToFloat64(m.DNSAnswers.WithLabelValues(vip, nlri, "http", "a.example")))
		assert.Equal(t, float64(3), testutil.ToFloat64(m.DNSRcode.WithLabelValues(vip, nlri, "http", "b.example")))
	})

	t.Run("listen", func(t *testing.T) {
		m.ObserveCheckDetails(vip, nlri, "listen", &model.ListenDetails{Sockets: []string{"0.0.0.0:4739", "[::]:4739"}})
		assert.Equal(t, float64(2), testutil.ToFloat64(m.ListenSockets.WithLabelValues(vip, nlri, "listen")))
	})

	t.Run("kafka", func(t *testing.T) {
		m.ObserveCheckDetails(vip, nlri, "kafka", &model.KafkaDetails{Brokers: 3, Topic: "t", Partitions: 6, PartitionsWithoutLeader: 2})
		assert.Equal(t, float64(3), testutil.ToFloat64(m.KafkaBrokers.WithLabelValues(vip, nlri, "kafka")))
		assert.Equal(t, float64(2), testutil.ToFloat64(m.KafkaLeaderless.WithLabelValues(vip, nlri, "kafka")))
	})

	t.Run("sql", func(t *testing.T) {
		m.ObserveCheckDetails(vip, nlri, "sql", &model.SQLDetails{Queries: []model.SQLQueryDetails{
			{Name: "q1", Rows: 1, Duration: 20 * time.Millisecond, Passed: true},
			{Name: "lag", Rows: 1, Duration: 5 * time.Millisecond, Passed: false},
		}})
		assert.Equal(t, float64(1), testutil.ToFloat64(m.SQLQueryPassed.WithLabelValues(vip, nlri, "sql", "q1")))
		assert.Equal(t, float64(0), testutil.ToFloat64(m.SQLQueryPassed.WithLabelValues(vip, nlri, "sql", "lag")))
		assert.Equal(t, 0.02, testutil.ToFloat64(m.SQLQueryDuration.WithLabelValues(vip, nlri, "sql", "q1")))
		assert.Equal(t, float64(1), testutil.ToFloat64(m.SQLQueryRows.WithLabelValues(vip, nlri, "sql", "lag")))
	})

	t.Run("grpc", func(t *testing.T) {
		m.ObserveCheckDetails(vip, nlri, "http", &model.GRPCDetails{StatusCode: 14})
		assert.Equal(t, float64(14), testutil.ToFloat64(m.GRPCStatusCode.WithLabelValues(vip, nlri, "http")))
	})

	_, err := m.Registry.Gather()
	require.NoError(t, err)
}
