package tracing

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/syncronix-labs/aegora/internal/proxy"
)

type stubRouter struct {
	matchFn func(r *http.Request) *proxy.RouteConfig
}

func (s *stubRouter) Match(r *http.Request) *proxy.RouteConfig {
	if s.matchFn != nil {
		return s.matchFn(r)
	}
	return nil
}

func initTestTracer(t *testing.T) (*tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return exp, tp
}

func TestTracing_Middleware_CreatesSpanWithAttributes(t *testing.T) {
	exp, tp := initTestTracer(t)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	tracer := tp.Tracer("test-tracer")
	router := &stubRouter{
		matchFn: func(r *http.Request) *proxy.RouteConfig {
			if r.URL.Path == "/api/users" {
				return &proxy.RouteConfig{Name: "users-service"}
			}
			return nil
		},
	}

	handler := Middleware(tracer, router)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected exactly 1 span, got %d", len(spans))
	}

	span := spans[0]
	if span.Name != "aegora.request" {
		t.Errorf("expected span name %q, got %q", "aegora.request", span.Name)
	}

	attrs := make(map[string]string)
	for _, a := range span.Attributes {
		attrs[string(a.Key)] = a.Value.String()
	}

	if attrs["http.method"] != "GET" {
		t.Errorf("expected http.method = GET, got %q", attrs["http.method"])
	}
	if attrs["http.route"] != "users-service" {
		t.Errorf("expected http.route = users-service, got %q", attrs["http.route"])
	}
	if attrs["http.status_code"] != "200" {
		t.Errorf("expected http.status_code = 200, got %q", attrs["http.status_code"])
	}
	if attrs["http.target"] != "/api/users" {
		t.Errorf("expected http.target = /api/users, got %q", attrs["http.target"])
	}
}

func TestTracing_Middleware_ParentPropagation(t *testing.T) {
	exp, tp := initTestTracer(t)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	tracer := tp.Tracer("test-tracer")
	handler := Middleware(tracer, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	const parentTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	const parentSpanID = "00f067aa0ba902b7"
	traceparent := fmt.Sprintf("00-%s-%s-01", parentTraceID, parentSpanID)

	req := httptest.NewRequest(http.MethodGet, "/test/parent", nil)
	req.Header.Set("traceparent", traceparent)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	span := spans[0]
	if span.SpanContext.TraceID().String() != parentTraceID {
		t.Errorf("expected span trace ID %s, got %s", parentTraceID, span.SpanContext.TraceID().String())
	}
	if span.Parent.SpanID().String() != parentSpanID {
		t.Errorf("expected parent span ID %s, got %s", parentSpanID, span.Parent.SpanID().String())
	}
}

func TestTracing_Middleware_500MarksStatusError(t *testing.T) {
	exp, tp := initTestTracer(t)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	tracer := tp.Tracer("test-tracer")
	handler := Middleware(tracer, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/fail", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	span := spans[0]
	if span.Status.Code != codes.Error {
		t.Errorf("expected status code Error (%v), got %v", codes.Error, span.Status.Code)
	}
	if span.Status.Description != "500" {
		t.Errorf("expected status description %q, got %q", "500", span.Status.Description)
	}

	attrs := make(map[string]string)
	for _, a := range span.Attributes {
		attrs[string(a.Key)] = a.Value.String()
	}
	if attrs["http.route"] != "unmatched" {
		t.Errorf("expected unmatched route when router is nil, got %q", attrs["http.route"])
	}
}

func TestTracing_Setup_Stdout(t *testing.T) {
	tp, shutdown, err := Setup("test-service", Config{
		Enabled:     true,
		Exporter:    "stdout",
		SampleRatio: 1.0,
	})
	if err != nil {
		t.Fatalf("Setup stdout failed: %v", err)
	}
	if tp == nil {
		t.Fatal("expected non-nil TracerProvider")
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown function")
	}

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
}
