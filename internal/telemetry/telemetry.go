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
	if cfg.Endpoint == "" || !cfg.IsEnabled() {
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
	if cfg.Traces.IsEnabled() {
		traceExp, err := buildTraceExporter(ctx, cfg, cfg.Traces)
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
	}

	// ── Metrics (bridge existing Prometheus registry → OTLP) ────────────────
	if cfg.Metrics.IsEnabled() {
		metricsExp, err := buildMetricsExporter(ctx, cfg, cfg.Metrics)
		if err != nil {
			_ = p.Shutdown(ctx)
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
	}

	// ── Logs ────────────────────────────────────────────────────────────────
	if cfg.Logs.IsEnabled() {
		logExp, err := buildLogExporter(ctx, cfg, cfg.Logs)
		if err != nil {
			_ = p.Shutdown(ctx)
			return nil, err
		}
		lp := sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		)
		p.lp = lp
		p.shutdown = append(p.shutdown, lp.Shutdown)
		global.SetLoggerProvider(lp)
	}

	return p, nil
}

func buildTraceExporter(ctx context.Context, cfg config.OTelConfig, sig config.OTelSignalConfig) (sdktrace.SpanExporter, error) {
	proto, normURL, err := config.ParseOTelEndpoint(sig.EffectiveEndpoint(cfg.Endpoint))
	if err != nil {
		return nil, fmt.Errorf("otel: trace endpoint: %w", err)
	}
	insecure := sig.EffectiveInsecure(cfg.Insecure)
	headers := sig.EffectiveHeaders(cfg.Headers)
	if proto == config.OTelProtocolGRPC {
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(normURL)}
		if insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		if len(headers) > 0 {
			opts = append(opts, otlptracegrpc.WithHeaders(headers))
		}
		exp, err := otlptracegrpc.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otel: creating gRPC trace exporter: %w", err)
		}
		return exp, nil
	}
	// OTLP/HTTP
	opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(normURL)}
	if insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	if len(headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(headers))
	}
	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otel: creating HTTP trace exporter: %w", err)
	}
	return exp, nil
}

func buildMetricsExporter(ctx context.Context, cfg config.OTelConfig, sig config.OTelSignalConfig) (sdkmetric.Exporter, error) {
	proto, normURL, err := config.ParseOTelEndpoint(sig.EffectiveEndpoint(cfg.Endpoint))
	if err != nil {
		return nil, fmt.Errorf("otel: metrics endpoint: %w", err)
	}
	insecure := sig.EffectiveInsecure(cfg.Insecure)
	headers := sig.EffectiveHeaders(cfg.Headers)
	if proto == config.OTelProtocolGRPC {
		opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpointURL(normURL)}
		if insecure {
			opts = append(opts, otlpmetricgrpc.WithInsecure())
		}
		if len(headers) > 0 {
			opts = append(opts, otlpmetricgrpc.WithHeaders(headers))
		}
		exp, err := otlpmetricgrpc.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otel: creating gRPC metrics exporter: %w", err)
		}
		return exp, nil
	}
	// OTLP/HTTP
	opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(normURL)}
	if insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	if len(headers) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(headers))
	}
	exp, err := otlpmetrichttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otel: creating HTTP metrics exporter: %w", err)
	}
	return exp, nil
}

func buildLogExporter(ctx context.Context, cfg config.OTelConfig, sig config.OTelSignalConfig) (sdklog.Exporter, error) {
	proto, normURL, err := config.ParseOTelEndpoint(sig.EffectiveEndpoint(cfg.Endpoint))
	if err != nil {
		return nil, fmt.Errorf("otel: log endpoint: %w", err)
	}
	insecure := sig.EffectiveInsecure(cfg.Insecure)
	headers := sig.EffectiveHeaders(cfg.Headers)
	if proto == config.OTelProtocolGRPC {
		opts := []otlploggrpc.Option{otlploggrpc.WithEndpointURL(normURL)}
		if insecure {
			opts = append(opts, otlploggrpc.WithInsecure())
		}
		if len(headers) > 0 {
			opts = append(opts, otlploggrpc.WithHeaders(headers))
		}
		exp, err := otlploggrpc.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otel: creating gRPC log exporter: %w", err)
		}
		return exp, nil
	}
	// OTLP/HTTP
	opts := []otlploghttp.Option{otlploghttp.WithEndpointURL(normURL)}
	if insecure {
		opts = append(opts, otlploghttp.WithInsecure())
	}
	if len(headers) > 0 {
		opts = append(opts, otlploghttp.WithHeaders(headers))
	}
	exp, err := otlploghttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otel: creating HTTP log exporter: %w", err)
	}
	return exp, nil
}
