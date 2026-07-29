package observability

import (
	"context"
	"net"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
)

// fakeMetricsServer is a minimal in-process OTLP gRPC metrics collector that
// accepts every export. It lets the MeterProvider's shutdown flush succeed
// without a real collector, so the positive Setup test exercises the full
// install + shutdown happy path.
type fakeMetricsServer struct {
	collectormetricspb.UnimplementedMetricsServiceServer
}

func (fakeMetricsServer) Export(context.Context, *collectormetricspb.ExportMetricsServiceRequest) (*collectormetricspb.ExportMetricsServiceResponse, error) {
	return &collectormetricspb.ExportMetricsServiceResponse{}, nil
}

// startFakeOTLPServer starts the fake collector on a random loopback port and
// returns its "host:port" address. The server is stopped via t.Cleanup.
func startFakeOTLPServer(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	collectormetricspb.RegisterMetricsServiceServer(srv, fakeMetricsServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func TestEnabled(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "no env", env: nil, want: false},
		{name: "generic endpoint", env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4317"}, want: true},
		{name: "traces endpoint", env: map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://c:4317"}, want: true},
		{name: "metrics endpoint", env: map[string]string{"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": "http://c:4317"}, want: true},
		{name: "blank endpoint", env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "  "}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all endpoint vars first so t.Setenv restores cleanly.
			for _, k := range []string{
				"OTEL_EXPORTER_OTLP_ENDPOINT",
				"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
				"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
			} {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if got := Enabled(); got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetupNoopWhenDisabled(t *testing.T) {
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	} {
		t.Setenv(k, "")
	}
	shutdown, err := Setup(context.Background(), "test", "v0")
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if shutdown == nil {
		t.Fatal("Setup() returned nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown() error = %v", err)
	}
}

func TestSetupInstallsProvidersWhenEnabled(t *testing.T) {
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	} {
		t.Setenv(k, "")
	}
	// Point at an in-process OTLP gRPC collector. The http:// scheme selects
	// insecure transport; the grpc exporter does not dial eagerly, and the
	// fake server accepts the shutdown flush so shutdown returns cleanly.
	addr := startFakeOTLPServer(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+addr)
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")

	shutdown, err := Setup(context.Background(), "test-svc", "v1.2.3")
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if shutdown == nil {
		t.Fatal("Setup() returned nil shutdown")
	}

	// Confirm the real SDK providers were installed, not the default no-op /
	// global-delegate providers.
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Errorf("TracerProvider = %T, want *sdktrace.TracerProvider", otel.GetTracerProvider())
	}
	if _, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider); !ok {
		t.Errorf("MeterProvider = %T, want *sdkmetric.MeterProvider", otel.GetMeterProvider())
	}

	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown() error = %v", err)
	}
}

func TestSignalEnabledMetricsOnly(t *testing.T) {
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://c:4317")

	if !metricsEnabled() {
		t.Error("metricsEnabled() = false, want true for a metrics-only endpoint")
	}
	if tracesEnabled() {
		t.Error("tracesEnabled() = true, want false for a metrics-only endpoint")
	}
}

func TestTraceContextRoundTrip(t *testing.T) {
	// InjectTraceContext/ExtractTraceContext rely on the global propagator.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	traceID, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	spanID, _ := trace.SpanIDFromHex("0102030405060708")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	annotations := map[string]string{}
	InjectTraceContext(ctx, annotations)

	tp, ok := annotations[AnnotationTraceparent]
	if !ok || tp == "" {
		t.Fatalf("expected %s annotation to be set, got %v", AnnotationTraceparent, annotations)
	}
	if got := TraceparentFromAnnotations(annotations); got != tp {
		t.Errorf("TraceparentFromAnnotations() = %q, want %q", got, tp)
	}

	// Extract into a fresh context and confirm the trace/span IDs survive.
	extracted := ExtractTraceContext(context.Background(), annotations)
	gotSC := trace.SpanContextFromContext(extracted)
	if gotSC.TraceID() != traceID {
		t.Errorf("extracted TraceID = %s, want %s", gotSC.TraceID(), traceID)
	}
	if gotSC.SpanID() != spanID {
		t.Errorf("extracted SpanID = %s, want %s", gotSC.SpanID(), spanID)
	}
}

func TestExtractTraceContextNilAnnotations(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	ctx := context.Background()
	if got := ExtractTraceContext(ctx, nil); got != ctx {
		t.Error("ExtractTraceContext(nil) should return the context unchanged")
	}
}
