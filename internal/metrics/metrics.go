package metrics

import (
	"maps"
	"math"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	pver "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/vooon/pathosd/internal/model"
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
	// VIP priority: AS-path prepend count currently applied (base while
	// announced, base + lower_priority while pessimized)
	VIPPriority *prometheus.GaugeVec
	// VIPLocalAddress is 1 while the VIP address is configured on a local
	// interface; only populated for VIPs with require_local_address.
	VIPLocalAddress *prometheus.GaugeVec

	// Route state: per-peer per-VIP (1=advertised, 0=withdrawn)
	RouteState *prometheus.GaugeVec

	// Check counters by result (success/fail/stale)
	CheckTotal *prometheus.CounterVec
	// Check duration histogram
	CheckDuration *prometheus.HistogramVec
	// Check last result (0=fail, 1=ok)
	CheckLastResult *prometheus.GaugeVec
	// Check timeout exceeded
	CheckTimeoutExceeded *prometheus.CounterVec

	// Per-check-type details, populated only for VIPs with
	// check.detailed_metrics enabled (see ObserveCheckDetails).
	PingRTT            *prometheus.HistogramVec
	PingPacketLoss     *prometheus.GaugeVec
	HTTPResponseStatus *prometheus.GaugeVec
	HTTPTLSCertExpiry  *prometheus.GaugeVec
	DNSRcode           *prometheus.GaugeVec
	DNSAnswers         *prometheus.GaugeVec
	GRPCStatusCode     *prometheus.GaugeVec
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

	bucketIncrement := nominal / float64(bucketCount)

	// Linear buckets from increment to nominal timeout.
	var buckets []float64
	for i := 1; i <= bucketCount; i++ {
		buckets = append(buckets, roundTo(bucketIncrement*float64(i), 6))
	}

	// Exponential subdivisions for fine resolution near zero (or near timeout).
	if bucketIncrement <= 1 {
		for i := 1; i <= bucketCount; i++ {
			buckets = append(buckets, roundTo(math.Pow(bucketIncrement, float64(i)), 6))
		}
	} else {
		bucketFraction := 1.0 / float64(bucketCount)
		for i := 1; i <= bucketCount; i++ {
			buckets = append(buckets, roundTo(checkTimeout*math.Pow(bucketFraction, float64(i)), 6))
		}
	}

	buckets = append(buckets, nominal, effective)

	// Deduplicate and sort.
	seen := make(map[float64]struct{}, len(buckets))
	for _, v := range buckets {
		seen[v] = struct{}{}
	}
	result := slices.Collect(maps.Keys(seen))
	slices.Sort(result)
	return result
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
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m := &Metrics{
		Registry: reg,

		VIPState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_vip_state",
			Help: "Current VIP state: 0=withdrawn, 1=announced, 2=pessimized.",
		}, []string{"vip", "nlri"}),

		VIPTransitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pathosd_vip_transitions_total",
			Help: "Total VIP state transitions.",
		}, []string{"vip", "nlri", "to"}),

		VIPLastTransition: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_vip_last_transition_timestamp",
			Help: "Unix timestamp of last VIP state transition.",
		}, []string{"vip", "nlri"}),

		VIPPriority: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_vip_priority_status",
			Help: "AS-path prepend count applied to the VIP route: announce.as_path_prepend while announced, plus lower_priority.as_path_prepend while pessimized (0 = full priority).",
		}, []string{"vip", "nlri"}),

		VIPLocalAddress: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_vip_local_address_assigned",
			Help: "1 when the VIP address is configured on a local interface, 0 when missing (VIPs with require_local_address only).",
		}, []string{"vip", "nlri"}),

		RouteState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_route_state",
			Help: "Per-peer per-VIP route state (1=advertised, 0=withdrawn).",
		}, []string{"nlri", "peer_ip", "peer_asn", "as_path", "communities", "local_preference", "med", "family"}),

		CheckTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pathosd_check_total",
			Help: "Total health checks by result.",
		}, []string{"vip", "nlri", "check", "type", "result"}),

		CheckDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pathosd_check_duration_seconds",
			Help:    "Histogram of health check durations in seconds.",
			Buckets: checkBuckets,
		}, []string{"vip", "nlri", "check", "type"}),

		CheckLastResult: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_check_last_result",
			Help: "Last check result: 0=fail, 1=success.",
		}, []string{"vip", "nlri", "check"}),

		CheckTimeoutExceeded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pathosd_check_timeout_exceeded_total",
			Help: "Total checks that exceeded their timeout.",
		}, []string{"vip", "nlri", "check"}),

		PingRTT: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pathosd_ping_rtt_seconds",
			Help:    "Round-trip time of individual ICMP echo replies (check.detailed_metrics only).",
			Buckets: checkBuckets,
		}, []string{"vip", "nlri", "check"}),
		PingPacketLoss: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_ping_packet_loss_ratio",
			Help: "Packet loss of the last ping check, 0..1 (check.detailed_metrics only).",
		}, []string{"vip", "nlri", "check"}),
		HTTPResponseStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_http_response_status",
			Help: "HTTP status code of the last check response (check.detailed_metrics only).",
		}, []string{"vip", "nlri", "check"}),
		HTTPTLSCertExpiry: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_http_tls_cert_expiry_timestamp_seconds",
			Help: "NotAfter of the leaf TLS certificate seen by the last HTTPS check, as Unix time (check.detailed_metrics only).",
		}, []string{"vip", "nlri", "check"}),
		DNSRcode: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_dns_rcode",
			Help: "DNS response code of the last query per name, 0 = NOERROR (check.detailed_metrics only).",
		}, []string{"vip", "nlri", "check", "name"}),
		DNSAnswers: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_dns_answers",
			Help: "Answer records in the last DNS reply per name (check.detailed_metrics only).",
		}, []string{"vip", "nlri", "check", "name"}),
		GRPCStatusCode: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pathosd_grpc_status_code",
			Help: "gRPC status code of the last check call, 0 = OK (check.detailed_metrics only).",
		}, []string{"vip", "nlri", "check"}),
	}

	reg.MustRegister(
		m.VIPState,
		m.VIPTransitions,
		m.VIPLastTransition,
		m.VIPPriority,
		m.VIPLocalAddress,
		m.RouteState,
		m.CheckTotal,
		m.CheckDuration,
		m.CheckLastResult,
		m.CheckTimeoutExceeded,
		m.PingRTT,
		m.PingPacketLoss,
		m.HTTPResponseStatus,
		m.HTTPTLSCertExpiry,
		m.DNSRcode,
		m.DNSAnswers,
		m.GRPCStatusCode,
		pver.NewCollector("pathosd"),
	)

	return m
}

// ObserveCheckDetails exports check-type-specific observations for a VIP.
// Callers gate it on check.detailed_metrics; nil details are ignored.
func (m *Metrics) ObserveCheckDetails(vip, nlri, check string, details model.CheckDetails) {
	switch d := details.(type) {
	case *model.PingDetails:
		for _, rtt := range d.RTTs {
			m.PingRTT.WithLabelValues(vip, nlri, check).Observe(rtt.Seconds())
		}
		m.PingPacketLoss.WithLabelValues(vip, nlri, check).Set(d.PacketLoss)
	case *model.HTTPDetails:
		m.HTTPResponseStatus.WithLabelValues(vip, nlri, check).Set(float64(d.StatusCode))
		if !d.TLSCertNotAfter.IsZero() {
			m.HTTPTLSCertExpiry.WithLabelValues(vip, nlri, check).Set(float64(d.TLSCertNotAfter.Unix()))
		}
	case *model.DNSDetails:
		for _, n := range d.Names {
			m.DNSRcode.WithLabelValues(vip, nlri, check, n.Name).Set(float64(n.Rcode))
			m.DNSAnswers.WithLabelValues(vip, nlri, check, n.Name).Set(float64(n.Answers))
		}
	case *model.GRPCDetails:
		m.GRPCStatusCode.WithLabelValues(vip, nlri, check).Set(float64(d.StatusCode))
	}
}
