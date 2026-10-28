package checks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

// KafkaChecker connects to a Kafka broker, negotiates API versions (and TLS
// and SASL when configured) and asks for cluster metadata. The check passes
// when the broker answers with at least min_brokers live brokers and, if a
// topic is configured, that topic exists and every partition has a leader.
type KafkaChecker struct {
	cfg    config.KafkaCheckConfig
	opts   []kgo.Opt
	target string
}

func NewKafkaChecker(cfg *config.KafkaCheckConfig) (*KafkaChecker, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	clientID := cfg.ClientID
	if clientID == "" {
		clientID = "pathosd"
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(addr),
		kgo.ClientID(clientID),
		// One attempt per check; the scheduler retries on its own cadence.
		kgo.RequestRetries(0),
		kgo.DialTimeout(5 * time.Second),
		kgo.RequestTimeoutOverhead(5 * time.Second),
		kgo.ConnIdleTimeout(time.Second),
	}
	if cfg.TLS {
		tc, err := kafkaTLSConfig(cfg)
		if err != nil {
			return nil, err
		}
		opts = append(opts, kgo.DialTLSConfig(tc))
	}
	if cfg.SASL != nil {
		mech, err := kafkaSASL(cfg.SASL)
		if err != nil {
			return nil, err
		}
		opts = append(opts, kgo.SASL(mech))
	}
	return &KafkaChecker{cfg: *cfg, opts: opts, target: "kafka " + addr}, nil
}

func kafkaTLSConfig(cfg *config.KafkaCheckConfig) (*tls.Config, error) {
	tc := &tls.Config{InsecureSkipVerify: cfg.TLSInsecure, ServerName: cfg.TLSServerName} //nolint:gosec // operator opt-in, validated as exclusive with tls_ca_cert
	if tc.ServerName == "" {
		tc.ServerName = cfg.Host
	}
	if cfg.TLSCACert != "" {
		pem, err := os.ReadFile(cfg.TLSCACert)
		if err != nil {
			return nil, fmt.Errorf("reading tls_ca_cert: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("parsing tls_ca_cert %q: no certificates found", cfg.TLSCACert)
		}
		tc.RootCAs = pool
	}
	return tc, nil
}

func kafkaSASL(s *config.KafkaSASLConfig) (sasl.Mechanism, error) {
	switch s.Mechanism {
	case config.KafkaSASLPlain:
		return plain.Plain(func(context.Context) (plain.Auth, error) {
			return plain.Auth{User: s.User, Pass: s.Password}, nil
		}), nil
	case config.KafkaSASLScramSHA256:
		return scram.Sha256(func(context.Context) (scram.Auth, error) {
			return scram.Auth{User: s.User, Pass: s.Password}, nil
		}), nil
	case config.KafkaSASLScramSHA512:
		return scram.Sha512(func(context.Context) (scram.Auth, error) {
			return scram.Auth{User: s.User, Pass: s.Password}, nil
		}), nil
	default:
		return nil, fmt.Errorf("unsupported sasl mechanism %q", s.Mechanism)
	}
}

func (c *KafkaChecker) Type() string { return "kafka" }

func (c *KafkaChecker) Check(ctx context.Context) Result {
	start := time.Now()
	details := &model.KafkaDetails{Topic: c.cfg.Topic}
	fail := func(err error, detail string) Result {
		return Result{Duration: time.Since(start), Err: err, Detail: c.target + ": " + detail, Details: details, TimedOut: ctx.Err() != nil}
	}

	// A fresh client per check so every run exercises connect, TLS and
	// authentication; kgo dials lazily on the first request.
	cl, err := kgo.NewClient(c.opts...)
	if err != nil {
		return fail(err, fmt.Sprintf("client: %v", err))
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)

	var meta kadm.Metadata
	if c.cfg.Topic != "" {
		meta, err = adm.Metadata(ctx, c.cfg.Topic)
	} else {
		meta, err = adm.BrokerMetadata(ctx)
	}
	if err != nil {
		return fail(err, fmt.Sprintf("metadata: %v", err))
	}
	details.ClusterID = meta.Cluster
	details.Controller = meta.Controller
	details.Brokers = len(meta.Brokers)

	minBrokers := c.cfg.MinBrokers
	if minBrokers <= 0 {
		minBrokers = 1
	}
	if details.Brokers < minBrokers {
		err := fmt.Errorf("%d brokers in metadata, need %d", details.Brokers, minBrokers)
		return fail(err, err.Error())
	}

	if c.cfg.Topic != "" {
		td, ok := meta.Topics[c.cfg.Topic]
		if !ok {
			err := fmt.Errorf("topic %q not in metadata", c.cfg.Topic)
			return fail(err, err.Error())
		}
		partitions, leaderless, err := evalKafkaTopic(td)
		details.Partitions = partitions
		details.PartitionsWithoutLeader = leaderless
		if err != nil {
			return fail(err, err.Error())
		}
	}

	detail := fmt.Sprintf("%s: %d brokers", c.target, details.Brokers)
	if c.cfg.Topic != "" {
		detail += fmt.Sprintf(", topic %s: %d partitions with leader", c.cfg.Topic, details.Partitions)
	}
	return Result{Success: true, Duration: time.Since(start), Detail: detail, Details: details}
}

// evalKafkaTopic checks a topic's metadata: no topic error, at least one
// partition, and a leader for every partition. It returns the partition
// count and how many partitions lack a leader.
func evalKafkaTopic(td kadm.TopicDetail) (int, int, error) {
	if td.Err != nil {
		return 0, 0, fmt.Errorf("topic %q: %w", td.Topic, td.Err)
	}
	if len(td.Partitions) == 0 {
		return 0, 0, fmt.Errorf("topic %q has no partitions", td.Topic)
	}
	leaderless := 0
	var firstErr error
	for _, p := range td.Partitions.Sorted() {
		if p.Err != nil || p.Leader < 0 {
			leaderless++
			if firstErr == nil {
				if p.Err != nil {
					firstErr = fmt.Errorf("partition %d: %w", p.Partition, p.Err)
				} else {
					firstErr = fmt.Errorf("partition %d has no leader", p.Partition)
				}
			}
		}
	}
	if leaderless > 0 {
		return len(td.Partitions), leaderless, fmt.Errorf("topic %q: %d of %d partitions without leader (%w)", td.Topic, leaderless, len(td.Partitions), firstErr)
	}
	return len(td.Partitions), 0, nil
}
