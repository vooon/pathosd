package telemetry

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	otelslogbridge "go.opentelemetry.io/contrib/bridges/otelslog"
	prometheusbridge "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/vooon/pathosd/internal/config"
)

// Provider holds all OTEL SDK providers and shuts them down gracefully.
// All methods are safe to call on a zero-value Provider (OTEL disabled).
type Provider struct {
	lp       *sdklog.LoggerProvider
	shutdown []func(context.Context) error
}

// LogHandler returns a slog.Handler that ships records to the OTEL
// LoggerProvider. Returns nil when OTEL is disabled.
func (p *Provider) LogHandler() slog.Handler {
	if p.lp == nil {
		return nil
	}
	return otelslogbridge.NewHandler("pathosd", otelslogbridge.WithLoggerProvider(p.lp))
}

// Shutdown flushes and stops all OTEL providers.
func (p *Provider) Shutdown(ctx context.Context) error {
	var last error
	for _, fn := range p.shutdown {
		if err := fn(ctx); err != nil {
			last = err
		}
	}
	return last
}

// Setup initialises OTEL TracerProvider, MeterProvider (bridged from the
// Prometheus registry), and LoggerProvider, then registers them as globals.
//
// When cfg.Endpoint is empty all three signals remain as no-ops (the OTEL SDK
// default), and a zero-value Provider is returned.
func Setup(
	ctx context.Context,
	cfg config.OTelConfig,
	promReg *prometheus.Registry,
	svcVersion string,
) (*Provider, error) {
	if cfg.Endpoint == "" {
		return &Provider{}, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(svcVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel: building resource: %w", err)
	}

	p := &Provider{}

	// ── Traces ──────────────────────────────────────────────────────────────
	traceExp, err := buildTraceExporter(ctx, cfg)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	p.shutdown = append(p.shutdown, tp.Shutdown)
	otel.SetTracerProvider(tp)

	// ── Metrics (bridge existing Prometheus registry → OTLP) ────────────────
	metricsExp, err := buildMetricsExporter(ctx, cfg)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, err
	}
	producer := prometheusbridge.NewMetricProducer(
		prometheusbridge.WithGatherer(promReg),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
			metricsExp,
			sdkmetric.WithProducer(producer),
		)),
	)
	p.shutdown = append(p.shutdown, mp.Shutdown)
	otel.SetMeterProvider(mp)

	// ── Logs ────────────────────────────────────────────────────────────────
	logExp, err := buildLogExporter(ctx, cfg)
	if err != nil {
		_ = mp.Shutdown(ctx)
		_ = tp.Shutdown(ctx)
		return nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)
	p.lp = lp
	p.shutdown = append(p.shutdown, lp.Shutdown)
	global.SetLoggerProvider(lp)

	return p, nil
}

func buildTraceExporter(ctx context.Context, cfg config.OTelConfig) (sdktrace.SpanExporter, error) {
	switch cfg.Protocol {
	case "grpc":
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlptracegrpc.WithHeaders(cfg.Headers))
		}
		exp, err := otlptracegrpc.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otel: creating gRPC trace exporter: %w", err)
		}
		return exp, nil
	default: // "http"
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
		}
		exp, err := otlptracehttp.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otel: creating HTTP trace exporter: %w", err)
		}
		return exp, nil
	}
}

func buildMetricsExporter(ctx context.Context, cfg config.OTelConfig) (sdkmetric.Exporter, error) {
	switch cfg.Protocol {
	case "grpc":
		opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpointURL(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlpmetricgrpc.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlpmetricgrpc.WithHeaders(cfg.Headers))
		}
		exp, err := otlpmetricgrpc.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otel: creating gRPC metrics exporter: %w", err)
		}
		return exp, nil
	default: // "http"
		opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlpmetrichttp.WithHeaders(cfg.Headers))
		}
		exp, err := otlpmetrichttp.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otel: creating HTTP metrics exporter: %w", err)
		}
		return exp, nil
	}
}

func buildLogExporter(ctx context.Context, cfg config.OTelConfig) (sdklog.Exporter, error) {
	switch cfg.Protocol {
	case "grpc":
		opts := []otlploggrpc.Option{otlploggrpc.WithEndpointURL(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlploggrpc.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlploggrpc.WithHeaders(cfg.Headers))
		}
		exp, err := otlploggrpc.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otel: creating gRPC log exporter: %w", err)
		}
		return exp, nil
	default: // "http"
		opts := []otlploghttp.Option{otlploghttp.WithEndpointURL(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlploghttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlploghttp.WithHeaders(cfg.Headers))
		}
		exp, err := otlploghttp.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otel: creating HTTP log exporter: %w", err)
		}
		return exp, nil
	}
}
