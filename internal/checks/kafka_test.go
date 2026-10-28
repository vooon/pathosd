package checks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"

	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

func startFakeKafka(t *testing.T, opts ...kfake.Opt) (string, uint16) {
	t.Helper()
	c, err := kfake.NewCluster(append([]kfake.Opt{kfake.NumBrokers(1), kfake.ClusterID("fake-cluster")}, opts...)...)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	host, portStr, err := net.SplitHostPort(c.ListenAddrs()[0])
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, uint16(port)
}

func kafkaDetails(t *testing.T, r Result) *model.KafkaDetails {
	t.Helper()
	d, ok := r.Details.(*model.KafkaDetails)
	require.True(t, ok, "KafkaDetails expected")
	return d
}

func TestKafkaChecker_BrokerMetadata(t *testing.T) {
	host, port := startFakeKafka(t)
	c, err := NewKafkaChecker(&config.KafkaCheckConfig{Host: host, Port: port})
	require.NoError(t, err)
	assert.Equal(t, "kafka", c.Type())

	r := c.Check(context.Background())
	require.True(t, r.Success, r.Detail)
	assert.Equal(t, "kafka "+net.JoinHostPort(host, strconv.Itoa(int(port)))+": 1 brokers", r.Detail)
	d := kafkaDetails(t, r)
	assert.Equal(t, 1, d.Brokers)
	assert.Equal(t, "fake-cluster", d.ClusterID)
	assert.Empty(t, d.Topic)

	t.Run("min_brokers not met", func(t *testing.T) {
		c, err := NewKafkaChecker(&config.KafkaCheckConfig{Host: host, Port: port, MinBrokers: 3})
		require.NoError(t, err)
		r := c.Check(context.Background())
		assert.False(t, r.Success)
		assert.Contains(t, r.Detail, "1 brokers in metadata, need 3")
	})
}

func TestKafkaChecker_Topic(t *testing.T) {
	host, port := startFakeKafka(t, kfake.SeedTopics(3, "events"))

	t.Run("present with leaders", func(t *testing.T) {
		c, err := NewKafkaChecker(&config.KafkaCheckConfig{Host: host, Port: port, Topic: "events"})
		require.NoError(t, err)
		r := c.Check(context.Background())
		require.True(t, r.Success, r.Detail)
		assert.Contains(t, r.Detail, "topic events: 3 partitions with leader")
		d := kafkaDetails(t, r)
		assert.Equal(t, 3, d.Partitions)
		assert.Equal(t, 0, d.PartitionsWithoutLeader)
	})

	t.Run("missing", func(t *testing.T) {
		c, err := NewKafkaChecker(&config.KafkaCheckConfig{Host: host, Port: port, Topic: "nope"})
		require.NoError(t, err)
		r := c.Check(context.Background())
		assert.False(t, r.Success)
		assert.Contains(t, r.Detail, `topic "nope"`)
		assert.Equal(t, 1, kafkaDetails(t, r).Brokers, "broker count is still reported")
	})
}

func TestEvalKafkaTopic(t *testing.T) {
	part := func(id, leader int32, err error) kadm.PartitionDetail {
		return kadm.PartitionDetail{Topic: "t", Partition: id, Leader: leader, Err: err}
	}
	td := kadm.TopicDetail{Topic: "t", Partitions: kadm.PartitionDetails{0: part(0, 1, nil), 1: part(1, 2, nil)}}
	n, l, err := evalKafkaTopic(td)
	require.NoError(t, err)
	assert.Equal(t, []int{2, 0}, []int{n, l})

	td.Partitions[1] = part(1, -1, nil)
	td.Partitions[2] = part(2, -1, errors.New("LEADER_NOT_AVAILABLE"))
	n, l, err = evalKafkaTopic(td)
	require.Error(t, err)
	assert.Equal(t, []int{3, 2}, []int{n, l})
	assert.Contains(t, err.Error(), "2 of 3 partitions without leader (partition 1 has no leader)")

	_, _, err = evalKafkaTopic(kadm.TopicDetail{Topic: "t", Err: errors.New("UNKNOWN_TOPIC_OR_PARTITION")})
	assert.Contains(t, err.Error(), `topic "t": UNKNOWN_TOPIC_OR_PARTITION`)
	_, _, err = evalKafkaTopic(kadm.TopicDetail{Topic: "t"})
	assert.Contains(t, err.Error(), "has no partitions")
}

func TestKafkaChecker_SASL(t *testing.T) {
	for _, mech := range []struct{ cfg, fake string }{
		{config.KafkaSASLPlain, "PLAIN"},
		{config.KafkaSASLScramSHA256, "SCRAM-SHA-256"},
		{config.KafkaSASLScramSHA512, "SCRAM-SHA-512"},
	} {
		t.Run(mech.cfg, func(t *testing.T) {
			host, port := startFakeKafka(t, kfake.EnableSASL(), kfake.Superuser(mech.fake, "pathosd", "secret"))

			c, err := NewKafkaChecker(&config.KafkaCheckConfig{Host: host, Port: port, SASL: &config.KafkaSASLConfig{Mechanism: mech.cfg, User: "pathosd", Password: "secret"}})
			require.NoError(t, err)
			r := c.Check(context.Background())
			assert.True(t, r.Success, r.Detail)

			c, err = NewKafkaChecker(&config.KafkaCheckConfig{Host: host, Port: port, SASL: &config.KafkaSASLConfig{Mechanism: mech.cfg, User: "pathosd", Password: "wrong"}})
			require.NoError(t, err)
			r = c.Check(context.Background())
			assert.False(t, r.Success)
			assert.NotContains(t, r.Detail, "wrong", "password must not leak into details")

			c, err = NewKafkaChecker(&config.KafkaCheckConfig{Host: host, Port: port})
			require.NoError(t, err)
			r = c.Check(context.Background())
			assert.False(t, r.Success, "unauthenticated client against a SASL listener")
		})
	}

	_, err := NewKafkaChecker(&config.KafkaCheckConfig{Host: "h", Port: 1, SASL: &config.KafkaSASLConfig{Mechanism: "gssapi"}})
	require.Error(t, err)
}

func writeSelfSignedCert(t *testing.T, host string) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "kafka-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
		IPAddresses:  []net.IP{net.ParseIP(host)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	cert, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, certPEM, 0o600))
	return cert, path
}

func TestKafkaChecker_TLS(t *testing.T) {
	cert, caPath := writeSelfSignedCert(t, "127.0.0.1")
	host, port := startFakeKafka(t, kfake.TLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}))

	t.Run("ca cert", func(t *testing.T) {
		c, err := NewKafkaChecker(&config.KafkaCheckConfig{Host: host, Port: port, TLS: true, TLSCACert: caPath})
		require.NoError(t, err)
		r := c.Check(context.Background())
		assert.True(t, r.Success, r.Detail)
	})
	t.Run("insecure", func(t *testing.T) {
		c, err := NewKafkaChecker(&config.KafkaCheckConfig{Host: host, Port: port, TLS: true, TLSInsecure: true})
		require.NoError(t, err)
		r := c.Check(context.Background())
		assert.True(t, r.Success, r.Detail)
	})
	t.Run("unknown ca", func(t *testing.T) {
		c, err := NewKafkaChecker(&config.KafkaCheckConfig{Host: host, Port: port, TLS: true})
		require.NoError(t, err)
		r := c.Check(context.Background())
		assert.False(t, r.Success)
	})
	t.Run("plaintext against tls listener", func(t *testing.T) {
		c, err := NewKafkaChecker(&config.KafkaCheckConfig{Host: host, Port: port})
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		r := c.Check(ctx)
		assert.False(t, r.Success)
	})
	t.Run("missing ca file", func(t *testing.T) {
		_, err := NewKafkaChecker(&config.KafkaCheckConfig{Host: host, Port: port, TLS: true, TLSCACert: "/nonexistent.pem"})
		require.Error(t, err)
	})
}

func TestKafkaChecker_Unreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	require.NoError(t, ln.Close())

	c, err := NewKafkaChecker(&config.KafkaCheckConfig{Host: "127.0.0.1", Port: port})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := c.Check(ctx)
	assert.False(t, r.Success)
	assert.Contains(t, r.Detail, "metadata:")
	assert.Equal(t, 0, kafkaDetails(t, r).Brokers)
}
