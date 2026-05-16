package checks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/vooon/pathosd/internal/config"
)

// grpcCodesByName maps lower-cased, underscore-stripped gRPC status code names
// to their codes.Code value. Accepts both PascalCase ("NotFound") and
// SCREAMING_SNAKE_CASE ("NOT_FOUND") after normalisation.
var grpcCodesByName = map[string]codes.Code{
	"ok":                 codes.OK,
	"canceled":           codes.Canceled,
	"unknown":            codes.Unknown,
	"invalidargument":    codes.InvalidArgument,
	"deadlineexceeded":   codes.DeadlineExceeded,
	"notfound":           codes.NotFound,
	"alreadyexists":      codes.AlreadyExists,
	"permissiondenied":   codes.PermissionDenied,
	"resourceexhausted":  codes.ResourceExhausted,
	"failedprecondition": codes.FailedPrecondition,
	"aborted":            codes.Aborted,
	"outofrange":         codes.OutOfRange,
	"unimplemented":      codes.Unimplemented,
	"internal":           codes.Internal,
	"unavailable":        codes.Unavailable,
	"dataloss":           codes.DataLoss,
	"unauthenticated":    codes.Unauthenticated,
}

func parseGRPCCode(name string) (codes.Code, bool) {
	c, ok := grpcCodesByName[strings.ToLower(strings.ReplaceAll(name, "_", ""))]
	return c, ok
}

// GRPCChecker performs a gRPC health check.
//
// When cfg.Method is empty it uses the standard gRPC Health Checking Protocol
// (grpc.health.v1.Health/Check) and requires the response status to be SERVING.
//
// When cfg.Method is set it calls that unary RPC with an empty proto3 request
// and considers the call passing if the returned gRPC status code is in OKCodes
// (default: OK). This mirrors the HTTP checker's response_codes semantics.
type GRPCChecker struct {
	cfg     config.GRPCCheckConfig
	conn    *grpc.ClientConn
	okCodes map[codes.Code]bool
	method  string // normalised: always starts with "/"
}

func NewGRPCChecker(cfg *config.GRPCCheckConfig) (*GRPCChecker, error) {
	if cfg.TLSInsecure && cfg.TLSCACert != "" {
		return nil, fmt.Errorf("tls_ca_cert cannot be set together with tls_insecure")
	}

	var creds credentials.TransportCredentials
	if cfg.TLS {
		tlsCfg := &tls.Config{}
		if cfg.TLSInsecure {
			tlsCfg.InsecureSkipVerify = true // #nosec G402 — user-opted-in
		}
		if cfg.TLSServerName != "" {
			tlsCfg.ServerName = cfg.TLSServerName
		}
		if cfg.TLSCACert != "" {
			pemData, err := os.ReadFile(cfg.TLSCACert)
			if err != nil {
				return nil, fmt.Errorf("reading tls_ca_cert: %w", err)
			}
			pool, err := x509.SystemCertPool()
			if err != nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(pemData) {
				return nil, fmt.Errorf("parsing tls_ca_cert %q: no certificates found", cfg.TLSCACert)
			}
			tlsCfg.RootCAs = pool
		}
		creds = credentials.NewTLS(tlsCfg)
	} else {
		creds = insecure.NewCredentials()
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("grpc: creating client for %s: %w", addr, err)
	}

	// Parse ok_codes; default to OK when none supplied.
	okCodes := make(map[codes.Code]bool, len(cfg.OKCodes))
	for _, name := range cfg.OKCodes {
		c, ok := parseGRPCCode(name)
		if !ok {
			_ = conn.Close()
			return nil, fmt.Errorf("grpc: unknown status code %q in ok_codes", name)
		}
		okCodes[c] = true
	}
	if len(okCodes) == 0 {
		okCodes[codes.OK] = true
	}

	// Normalise method path: gRPC convention requires a leading "/".
	method := cfg.Method
	if method != "" && !strings.HasPrefix(method, "/") {
		method = "/" + method
	}

	return &GRPCChecker{
		cfg:     *cfg,
		conn:    conn,
		okCodes: okCodes,
		method:  method,
	}, nil
}

func (c *GRPCChecker) Type() string { return "grpc" }

func (c *GRPCChecker) Check(ctx context.Context) Result {
	start := time.Now()

	// Attach caller-supplied metadata to the outgoing context.
	if len(c.cfg.Metadata) > 0 {
		ctx = metadata.NewOutgoingContext(ctx, metadata.New(c.cfg.Metadata))
	}

	if c.method == "" {
		return c.checkStandardHealth(ctx, start)
	}
	return c.checkCustomMethod(ctx, start)
}

// checkStandardHealth uses the gRPC Health Checking Protocol
// (grpc.health.v1.Health/Check). The check passes only when the server
// responds with status SERVING.
func (c *GRPCChecker) checkStandardHealth(ctx context.Context, start time.Time) Result {
	client := grpc_health_v1.NewHealthClient(c.conn)
	resp, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: c.cfg.Service})
	dur := time.Since(start)

	if err != nil {
		st, _ := status.FromError(err)
		detail := fmt.Sprintf("grpc health check %s:%d service=%q: %s", c.cfg.Host, c.cfg.Port, c.cfg.Service, st.Message())
		return Result{Duration: dur, Err: err, Detail: detail, TimedOut: ctx.Err() != nil}
	}

	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		detail := fmt.Sprintf("grpc health check %s:%d service=%q: status=%s (want SERVING)", c.cfg.Host, c.cfg.Port, c.cfg.Service, resp.Status)
		return Result{Duration: dur, Err: fmt.Errorf("not serving: %s", resp.Status), Detail: detail}
	}

	return Result{
		Success:  true,
		Duration: dur,
		Detail:   fmt.Sprintf("grpc health check %s:%d service=%q: SERVING", c.cfg.Host, c.cfg.Port, c.cfg.Service),
	}
}

// checkCustomMethod calls an arbitrary unary RPC with an empty proto3 request.
// The check passes when the returned gRPC status code is in cfg.OKCodes.
func (c *GRPCChecker) checkCustomMethod(ctx context.Context, start time.Time) Result {
	var reply emptypb.Empty
	err := c.conn.Invoke(ctx, c.method, &emptypb.Empty{}, &reply)
	dur := time.Since(start)

	st, _ := status.FromError(err)
	code := st.Code()

	if !c.okCodes[code] {
		detail := fmt.Sprintf("grpc %s:%d%s: code=%s", c.cfg.Host, c.cfg.Port, c.method, code)
		if st.Message() != "" {
			detail += ": " + st.Message()
		}
		checkErr := err
		if checkErr == nil {
			checkErr = fmt.Errorf("unexpected status %s", code)
		}
		return Result{Duration: dur, Err: checkErr, Detail: detail, TimedOut: ctx.Err() != nil}
	}

	return Result{
		Success:  true,
		Duration: dur,
		Detail:   fmt.Sprintf("grpc %s:%d%s: code=%s", c.cfg.Host, c.cfg.Port, c.method, code),
	}
}
