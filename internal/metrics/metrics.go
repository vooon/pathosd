package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
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

	// Build info
	BuildInfo *prometheus.GaugeVec
}

// DefaultCheckBuckets provides sensible histogram buckets for check durations.
var DefaultCheckBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0}

// New creates a new Metrics instance with all collectors registered.
func New() *Metrics {
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
			Buckets: DefaultCheckBuckets,
		}, []string{"vip", "type"}),

		CheckLastResult: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_check_last_result",
			Help: "Last check result: 0=fail, 1=success.",
		}, []string{"vip"}),

		CheckTimeoutExceeded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pathosd_check_timeout_exceeded_total",
			Help: "Total checks that exceeded their timeout.",
		}, []string{"vip"}),

		BuildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_build_info",
			Help: "Build information.",
		}, []string{"version", "commit", "go_version"}),
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
		m.BuildInfo,
	)

	return m
}

// SetBuildInfo records version metadata.
func (m *Metrics) SetBuildInfo(version, commit, goVersion string) {
	m.BuildInfo.WithLabelValues(version, commit, goVersion).Set(1)
}
