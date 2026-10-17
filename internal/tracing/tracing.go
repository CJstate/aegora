// Package tracing provides OpenTelemetry distributed tracing setup,
// W3C traceparent propagation, and HTTP server middleware for Aegora.
package tracing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/syncronix-labs/aegora/internal/middleware"
	"github.com/syncronix-labs/aegora/internal/proxy"
)

// Config specifies OpenTelemetry distributed tracing initialization settings.
type Config struct {
	Enabled      bool
	Exporter     string
	OTLPEndpoint string
	SampleRatio  float64
}

// RouteMatcher defines the route-matching interface used by tracing middleware.
// It matches incoming HTTP requests to their configured RouteConfig.
// *proxy.Router satisfies this interface.
type RouteMatcher interface {
	Match(r *http.Request) *proxy.RouteConfig
}

// Setup initializes the OpenTelemetry TracerProvider, configures the requested span
// exporter (stdout or OTLP), applies parent-based ratio sampling, and sets global
// trace providers and W3C traceparent propagators.
//
// Sampling Ratio Rationale:
// The default ratio is 1.0 (100% trace capture), suitable for development and staging.
// Production deployments with high request throughput should configure sample_ratio
// between 0.01 and 0.1 (1% - 10%) to balance visibility against telemetry ingestion cost.
//
// Exporter Choices:
//   - "stdout" (stdouttrace, default): zero external infrastructure, prints spans to logs.
//   - "otlp" (otlptracehttp): exports spans to an OpenTelemetry collector endpoint.
func Setup(serviceName string, cfg Config) (*sdktrace.TracerProvider, func(context.Context) error, error) {
	if strings.TrimSpace(serviceName) == "" {
		serviceName = "aegora"
	}

	ctx := context.Background()

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("tracing: create resource: %w", err)
	}

	var exporter sdktrace.SpanExporter
	switch cfg.Exporter {
	case "otlp":
		endpoint := cfg.OTLPEndpoint
		if strings.TrimSpace(endpoint) == "" {
			endpoint = "localhost:4318"
		}
		endpoint = strings.TrimPrefix(endpoint, "http://")
		endpoint = strings.TrimPrefix(endpoint, "https://")

		otlpExp, err := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(endpoint),
			otlptracehttp.WithInsecure(),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("tracing: init otlp exporter: %w", err)
		}
		exporter = otlpExp
	case "stdout", "":
		stdoutExp, err := stdouttrace.New()
		if err != nil {
			return nil, nil, fmt.Errorf("tracing: init stdout exporter: %w", err)
		}
		exporter = stdoutExp
	default:
		return nil, nil, fmt.Errorf("tracing: unsupported exporter %q (must be stdout or otlp)", cfg.Exporter)
	}

	ratio := cfg.SampleRatio
	if ratio <= 0.0 || ratio > 1.0 {
		ratio = 1.0
	}
	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sampler),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown := func(shutdownCtx context.Context) error {
		var errs []error
		if err := tp.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("tracer provider shutdown: %w", err))
		}
		return errors.Join(errs...)
	}

	return tp, shutdown, nil
}

// Middleware constructs an HTTP middleware that extracts parent W3C trace contexts,
// starts a server span ("aegora.request"), attaches route/method/target attributes,
// and records the final response status.
//
// Middleware Placement Rationale:
// Positioned next to the metrics middleware (after RequestID) so that the trace span
// covers shedding, rate-limiting, response caching, queue admission, and upstream proxying.
func Middleware(tracer trace.Tracer, router RouteMatcher) middleware.Middleware {
	if tracer == nil {
		tracer = otel.GetTracerProvider().Tracer("github.com/syncronix-labs/aegora")
	}

	propagator := otel.GetTextMapPropagator()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Extract parent SpanContext from incoming W3C TraceContext headers
			ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

			// 2. Resolve route name using the router's matching logic
			routeName := "unmatched"
			if router != nil {
				if matched := router.Match(r); matched != nil && matched.Name != "" {
					routeName = matched.Name
				}
			}

			target := r.URL.EscapedPath()
			if target == "" {
				target = r.URL.Path
			}
			if target == "" {
				target = "/"
			}

			// 3. Start server span
			ctx, span := tracer.Start(ctx, "aegora.request",
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.route", routeName),
					attribute.String("http.target", target),
				),
			)
			defer span.End()

			rw := newStatusResponseWriter(w)
			r = r.WithContext(ctx)

			next.ServeHTTP(rw, r)

			// 4. Record response status and flag 5xx server errors
			span.SetAttributes(attribute.Int("http.status_code", rw.status))
			if rw.status >= 500 {
				span.SetStatus(codes.Error, strconv.Itoa(rw.status))
			}
		})
	}
}

// statusResponseWriter captures the HTTP status code written by downstream handlers.
//
// Streaming Safety Rationale:
// This wrapper implements ONLY http.ResponseWriter (Header, Write, WriteHeader).
// It deliberately does NOT implement http.Flusher, http.Hijacker, or http.ResponseController's
// Unwrap() method. Advertising http.Flusher or exposing Unwrap causes httputil.ReverseProxy
// to treat the downstream response as streaming and trigger mid-body Flushes, which prematurely
// commits response headers or breaks buffering middleware.
type statusResponseWriter struct {
	w           http.ResponseWriter
	status      int
	wroteHeader bool
}

func newStatusResponseWriter(w http.ResponseWriter) *statusResponseWriter {
	return &statusResponseWriter{
		w:      w,
		status: http.StatusOK,
	}
}

func (rw *statusResponseWriter) Header() http.Header {
	return rw.w.Header()
}

func (rw *statusResponseWriter) WriteHeader(code int) {
	if rw.wroteHeader {
		return
	}
	rw.status = code
	rw.wroteHeader = true
	rw.w.WriteHeader(code)
}

func (rw *statusResponseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.w.Write(b)
}
