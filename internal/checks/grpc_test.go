package checks

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/vooon/pathosd/internal/config"
)

// startGRPCHealthServer starts a gRPC server with the standard health service
// registered and returns the health server (to configure statuses) and port.
func startGRPCHealthServer(t *testing.T) (*health.Server, uint16) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := uint16(ln.Addr().(*net.TCPAddr).Port)

	healthSrv := health.NewServer()
	grpcSrv := grpc.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcSrv, healthSrv)

	t.Cleanup(func() { grpcSrv.GracefulStop() })
	go func() { _ = grpcSrv.Serve(ln) }()

	return healthSrv, port
}

// startGRPCCustomServer starts a gRPC server that routes all unregistered
// methods to handler and returns the listening port.
func startGRPCCustomServer(t *testing.T, handler grpc.StreamHandler) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := uint16(ln.Addr().(*net.TCPAddr).Port)

	grpcSrv := grpc.NewServer(grpc.UnknownServiceHandler(handler))
	t.Cleanup(func() { grpcSrv.GracefulStop() })
	go func() { _ = grpcSrv.Serve(ln) }()

	return port
}

// grpcOKHandler reads the incoming frame and replies with an empty OK response.
func grpcOKHandler(_ any, stream grpc.ServerStream) error {
	var dummy emptypb.Empty
	if err := stream.RecvMsg(&dummy); err != nil {
		return err
	}
	return stream.SendMsg(&emptypb.Empty{})
}

// grpcStatusHandler returns a handler that always replies with the given code.
func grpcStatusHandler(code codes.Code) grpc.StreamHandler {
	return func(_ any, stream grpc.ServerStream) error {
		var dummy emptypb.Empty
		_ = stream.RecvMsg(&dummy)
		if code == codes.OK {
			return stream.SendMsg(&emptypb.Empty{})
		}
		return status.Error(code, code.String())
	}
}

// grpcSlowHandler blocks until the stream context is canceled, simulating a
// hung server to trigger client-side deadline expiry.
func grpcSlowHandler(_ any, stream grpc.ServerStream) error {
	<-stream.Context().Done()
	return status.Error(codes.DeadlineExceeded, "slow")
}

// --- Type ---

func TestGRPCChecker_Type(t *testing.T) {
	c, err := NewGRPCChecker(&config.GRPCCheckConfig{Host: "127.0.0.1", Port: 9999})
	require.NoError(t, err)
	assert.Equal(t, "grpc", c.Type())
}

// --- Standard health protocol ---

func TestGRPCChecker_Health_Serving(t *testing.T) {
	healthSrv, port := startGRPCHealthServer(t)
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	c, err := NewGRPCChecker(&config.GRPCCheckConfig{Host: "127.0.0.1", Port: port})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.True(t, result.Success, "SERVING should pass; detail: %s", result.Detail)
	assert.Contains(t, result.Detail, "SERVING")
	assert.NotZero(t, result.Duration)
}

func TestGRPCChecker_Health_NotServing(t *testing.T) {
	healthSrv, port := startGRPCHealthServer(t)
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)

	c, err := NewGRPCChecker(&config.GRPCCheckConfig{Host: "127.0.0.1", Port: port})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success, "NOT_SERVING should fail")
	assert.NotNil(t, result.Err)
	assert.Contains(t, result.Detail, "NOT_SERVING")
}

func TestGRPCChecker_Health_NamedService(t *testing.T) {
	healthSrv, port := startGRPCHealthServer(t)
	healthSrv.SetServingStatus("my.svc.v1.Service", grpc_health_v1.HealthCheckResponse_SERVING)

	c, err := NewGRPCChecker(&config.GRPCCheckConfig{
		Host:    "127.0.0.1",
		Port:    port,
		Service: "my.svc.v1.Service",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.True(t, result.Success, "named service SERVING should pass; detail: %s", result.Detail)
}

func TestGRPCChecker_Health_ServiceNotFound(t *testing.T) {
	// health.Server returns NOT_FOUND for an unregistered service name.
	_, port := startGRPCHealthServer(t)

	c, err := NewGRPCChecker(&config.GRPCCheckConfig{
		Host:    "127.0.0.1",
		Port:    port,
		Service: "not.registered.Service",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success, "unregistered service should fail")
	assert.NotNil(t, result.Err)
}

// --- Custom method ---

func TestGRPCChecker_CustomMethod_OK(t *testing.T) {
	port := startGRPCCustomServer(t, grpcOKHandler)

	c, err := NewGRPCChecker(&config.GRPCCheckConfig{
		Host:   "127.0.0.1",
		Port:   port,
		Method: "test.Svc/Ping",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.True(t, result.Success, "OK response should pass; detail: %s", result.Detail)
	assert.Contains(t, result.Detail, "OK")
	assert.NotZero(t, result.Duration)
}

func TestGRPCChecker_CustomMethod_NonOK_Fails(t *testing.T) {
	port := startGRPCCustomServer(t, grpcStatusHandler(codes.NotFound))

	c, err := NewGRPCChecker(&config.GRPCCheckConfig{
		Host:   "127.0.0.1",
		Port:   port,
		Method: "test.Svc/Ping",
		// OKCodes unset → default is [OK]
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success, "NotFound should fail when ok_codes is default")
	assert.NotNil(t, result.Err)
	assert.Contains(t, result.Detail, "NotFound")
}

func TestGRPCChecker_CustomMethod_OKCodes_Match(t *testing.T) {
	port := startGRPCCustomServer(t, grpcStatusHandler(codes.NotFound))

	c, err := NewGRPCChecker(&config.GRPCCheckConfig{
		Host:    "127.0.0.1",
		Port:    port,
		Method:  "test.Svc/Ping",
		OKCodes: []string{"NOT_FOUND"},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.True(t, result.Success, "NotFound in ok_codes should pass; detail: %s", result.Detail)
}

func TestGRPCChecker_CustomMethod_OKCodes_PascalCase(t *testing.T) {
	// Verify that PascalCase code names ("NotFound") work in addition to SCREAMING_SNAKE_CASE.
	port := startGRPCCustomServer(t, grpcStatusHandler(codes.NotFound))

	c, err := NewGRPCChecker(&config.GRPCCheckConfig{
		Host:    "127.0.0.1",
		Port:    port,
		Method:  "test.Svc/Ping",
		OKCodes: []string{"NotFound"},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.True(t, result.Success, "PascalCase ok_codes should pass; detail: %s", result.Detail)
}

func TestGRPCChecker_CustomMethod_MethodNormalization(t *testing.T) {
	// Both "foo/Bar" and "/foo/Bar" must reach the same handler.
	port := startGRPCCustomServer(t, grpcOKHandler)

	for _, method := range []string{"test.Svc/Ping", "/test.Svc/Ping"} {
		t.Run(method, func(t *testing.T) {
			c, err := NewGRPCChecker(&config.GRPCCheckConfig{
				Host:   "127.0.0.1",
				Port:   port,
				Method: method,
			})
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			result := c.Check(ctx)
			assert.True(t, result.Success, "method %q should pass; detail: %s", method, result.Detail)
		})
	}
}

// --- Metadata ---

func TestGRPCChecker_Metadata(t *testing.T) {
	var capturedMD metadata.MD
	handler := func(_ any, stream grpc.ServerStream) error {
		capturedMD, _ = metadata.FromIncomingContext(stream.Context())
		var dummy emptypb.Empty
		_ = stream.RecvMsg(&dummy)
		return stream.SendMsg(&emptypb.Empty{})
	}
	port := startGRPCCustomServer(t, handler)

	c, err := NewGRPCChecker(&config.GRPCCheckConfig{
		Host:   "127.0.0.1",
		Port:   port,
		Method: "test.Svc/Ping",
		Metadata: map[string]string{
			"x-custom-header": "test-value",
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	require.True(t, result.Success, "check should pass; detail: %s", result.Detail)
	assert.Equal(t, []string{"test-value"}, capturedMD.Get("x-custom-header"))
}

// --- Timeout ---

func TestGRPCChecker_Timeout(t *testing.T) {
	port := startGRPCCustomServer(t, grpcSlowHandler)

	c, err := NewGRPCChecker(&config.GRPCCheckConfig{
		Host:   "127.0.0.1",
		Port:   port,
		Method: "test.Svc/Slow",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success)
	assert.True(t, result.TimedOut)
}

// --- Connection error ---

func TestGRPCChecker_ConnectionRefused(t *testing.T) {
	port := reserveTCPPortForChecker(t)

	c, err := NewGRPCChecker(&config.GRPCCheckConfig{Host: "127.0.0.1", Port: port})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := c.Check(ctx)
	assert.False(t, result.Success)
	assert.NotEmpty(t, result.Detail)
	assert.NotNil(t, result.Err)
}

// --- Constructor errors ---

func TestNewGRPCChecker_InvalidOKCode(t *testing.T) {
	_, err := NewGRPCChecker(&config.GRPCCheckConfig{
		Host:    "127.0.0.1",
		Port:    9999,
		OKCodes: []string{"BOGUS_CODE"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown status code")
}

func TestNewGRPCChecker_TLSConflict(t *testing.T) {
	_, err := NewGRPCChecker(&config.GRPCCheckConfig{
		Host:        "127.0.0.1",
		Port:        9999,
		TLS:         true,
		TLSInsecure: true,
		TLSCACert:   "/tmp/nonexistent.pem",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tls_ca_cert")
}

func TestNewGRPCChecker_TLSCACert_Missing(t *testing.T) {
	_, err := NewGRPCChecker(&config.GRPCCheckConfig{
		Host:      "127.0.0.1",
		Port:      9999,
		TLS:       true,
		TLSCACert: "/nonexistent/path/ca.pem",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading tls_ca_cert")
}
