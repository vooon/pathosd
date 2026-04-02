package metrics

import (
	"math"
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	pver "github.com/prometheus/client_golang/prometheus/collectors/version"
)

// Metrics holds all pathosd-specific Prometheus metrics.
type Metrics struct {
	Registry *prometheus.Registry

	// VIP state: 0=withdrawn, 1=announced, 2=pessimized
	VIPState *prometheus.GaugeVec
	// VIP transitions counter
	VIPTransitions *prometheus.CounterVec
	// VIP last transition timestamp
	VIPLastTransition *prometheus.GaugeVec
	// VIP priority status (AS-path multiplier: 1=normal, N=prepend count)
	VIPPriority *prometheus.GaugeVec

	// Route state: per-peer per-VIP (1=advertised, 0=withdrawn)
	RouteState *prometheus.GaugeVec

	// Check counters by result (success/fail/stale)
	CheckTotal *prometheus.CounterVec
	// Checks absorbed by rise/fall (did not trigger state change)
	CheckAbsorbed *prometheus.CounterVec
	// Check duration histogram
	CheckDuration *prometheus.HistogramVec
	// Check last result (0=fail, 1=ok)
	CheckLastResult *prometheus.GaugeVec
	// Check timeout exceeded
	CheckTimeoutExceeded *prometheus.CounterVec
}

const (
	// maxCheckOverrun accounts for goroutine scheduling jitter beyond the nominal timeout.
	maxCheckOverrun = 50 * time.Millisecond
	// defaultBucketCount is the number of linear/exponential subdivisions.
	defaultBucketCount = 10
)

// GenerateCheckBuckets computes histogram buckets for check durations based on the
// check timeout. Buckets cover 0 to timeout with linear spacing, plus exponential
// subdivisions near zero for fine-grained latency visibility. A small overrun
// bucket beyond nominal timeout captures scheduling jitter.
func GenerateCheckBuckets(timeout time.Duration) []float64 {
	return generateCheckBuckets(timeout.Seconds(), defaultBucketCount)
}

func generateCheckBuckets(checkTimeout float64, bucketCount int) []float64 {
	nominal := roundTo(checkTimeout, 6)
	effective := roundTo(checkTimeout+maxCheckOverrun.Seconds(), 6)

	increment := nominal / float64(bucketCount)

	seen := make(map[float64]struct{})
	var buckets []float64
	add := func(v float64) {
		v = roundTo(v, 6)
		if _, ok := seen[v]; !ok && v > 0 {
			seen[v] = struct{}{}
			buckets = append(buckets, v)
		}
	}

	// Linear buckets from increment to timeout.
	for i := 1; i <= bucketCount; i++ {
		add(increment * float64(i))
	}

	// Exponential subdivisions for fine resolution.
	if increment <= 1 {
		for i := 1; i <= bucketCount; i++ {
			add(math.Pow(increment, float64(i)))
		}
	} else {
		fraction := 1.0 / float64(bucketCount)
		for i := 1; i <= bucketCount; i++ {
			add(checkTimeout * math.Pow(fraction, float64(i)))
		}
	}

	add(nominal)
	add(effective)

	sort.Float64s(buckets)
	return buckets
}

func roundTo(v float64, decimals int) float64 {
	mul := math.Pow10(decimals)
	return math.Round(v*mul) / mul
}

// New creates a new Metrics instance with all collectors registered.
// checkBuckets are the histogram bucket boundaries for check duration; pass nil to skip histogram creation.
func New(checkBuckets []float64) *Metrics {
	reg := prometheus.NewRegistry()

	// Include Go runtime and process collectors
	reg.MustRegister(prometheus.NewGoCollector())
	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	m := &Metrics{
		Registry: reg,

		VIPState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_vip_state",
			Help: "Current VIP state: 0=withdrawn, 1=announced, 2=pessimized.",
		}, []string{"vip", "prefix"}),

		VIPTransitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pathosd_vip_transitions_total",
			Help: "Total VIP state transitions.",
		}, []string{"vip", "to"}),

		VIPLastTransition: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_vip_last_transition_timestamp",
			Help: "Unix timestamp of last VIP state transition.",
		}, []string{"vip"}),

		VIPPriority: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_vip_priority_status",
			Help: "Current AS-path multiplier for VIP (1=normal, N=prepend count when pessimized).",
		}, []string{"vip", "prefix"}),

		RouteState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_route_state",
			Help: "Per-peer per-VIP route state (1=advertised, 0=withdrawn).",
		}, []string{"nlri", "peer_ip", "peer_asn", "as_path", "communities", "local_preference", "med", "family"}),

		CheckTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pathosd_check_total",
			Help: "Total health checks by result.",
		}, []string{"vip", "type", "result"}),

		CheckAbsorbed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pathosd_check_rise_fall_absorbed_total",
			Help: "Checks whose result was absorbed by rise/fall hysteresis (did not trigger state change).",
		}, []string{"vip"}),

		CheckDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pathosd_check_duration_seconds",
			Help:    "Histogram of health check durations in seconds.",
			Buckets: checkBuckets,
		}, []string{"vip", "type"}),

		CheckLastResult: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_check_last_result",
			Help: "Last check result: 0=fail, 1=success.",
		}, []string{"vip"}),

		CheckTimeoutExceeded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pathosd_check_timeout_exceeded_total",
			Help: "Total checks that exceeded their timeout.",
		}, []string{"vip"}),
	}

	reg.MustRegister(
		m.VIPState,
		m.VIPTransitions,
		m.VIPLastTransition,
		m.VIPPriority,
		m.RouteState,
		m.CheckTotal,
		m.CheckAbsorbed,
		m.CheckDuration,
		m.CheckLastResult,
		m.CheckTimeoutExceeded,
		pver.NewCollector("pathosd"),
	)

	return m
}
