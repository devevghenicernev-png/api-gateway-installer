// Package tracing wires apigw to an OpenTelemetry collector via OTLP/HTTP.
//
// Why OTel: distributed tracing across webhook receive → deploy worker →
// nginx → upstream is the single biggest debugging unlock for production
// gateways. Datadog, New Relic, Honeycomb, Tempo, Jaeger — they all speak
// OTLP. One exporter, every backend.
//
// Init() should be called once at process start (typically from
// `apigw dashboard serve`); Shutdown() must run on graceful stop to flush
// pending spans.
//
// Why OTLP/HTTP not gRPC: HTTP works through cheap reverse proxies and
// doesn't need keepalive tuning. Modern collectors accept both — HTTP is
// simpler for the common cluster-local pattern.
package tracing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

// Config controls the OTel exporter. Leave Endpoint empty to disable
// tracing entirely (NoopTracer is installed — instrumentation costs
// almost nothing).
type Config struct {
	// ServiceName is the OTel resource attribute. Defaults to "apigw".
	ServiceName string

	// ServiceVersion is the apigw version string (passed in from main).
	ServiceVersion string

	// Endpoint is the OTLP/HTTP collector URL, e.g.
	// "https://otel-collector.internal:4318/v1/traces" or
	// "http://localhost:4318/v1/traces" for a sidecar.
	//
	// Standard env: OTEL_EXPORTER_OTLP_ENDPOINT (we use it as fallback).
	Endpoint string

	// Insecure skips TLS verification. Only for dev/local.
	Insecure bool

	// SampleRatio is 0..1. 0 = sample nothing; 1 = sample every span;
	// 0.1 = 10%. Default 1.0 (sample-all). Production gateways with
	// 10K+ RPS typically drop to 0.01.
	SampleRatio float64

	// Headers added to every OTLP POST (e.g. {"x-honeycomb-team": "..."}).
	Headers map[string]string
}

// Shutdown returns a func the caller must invoke at process exit to flush
// the exporter. No-op when tracing is disabled.
type Shutdown func(ctx context.Context) error

// Init installs an OTel TracerProvider. If cfg.Endpoint is empty AND the
// OTEL_EXPORTER_OTLP_ENDPOINT env is unset, tracing is no-op (no exporter
// configured, no spans exported — instrumentation calls survive as
// zero-cost branch-not-taken).
func Init(ctx context.Context, cfg Config) (Shutdown, error) {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if endpoint == "" {
		// No exporter configured — install a no-op so callers can still
		// call otel.Tracer().Start() without nil checks.
		return func(context.Context) error { return nil }, nil
	}

	if cfg.ServiceName == "" {
		cfg.ServiceName = "apigw"
	}
	if cfg.SampleRatio == 0 {
		cfg.SampleRatio = 1.0
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			attribute.String("apigw.host", hostname()),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(endpoint),
		otlptracehttp.WithTimeout(5 * time.Second),
	}
	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
	}
	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp,
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithMaxExportBatchSize(512),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	otel.SetTracerProvider(tp)
	// W3C trace-context + baggage propagation: the standard set every
	// modern collector groks.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return errors.Join(tp.ForceFlush(shutdownCtx), tp.Shutdown(shutdownCtx))
	}, nil
}

// Tracer returns the named tracer; thin wrapper so call sites don't
// import otel.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
