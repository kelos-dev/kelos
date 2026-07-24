// Package observability wires OpenTelemetry (OTLP) metrics and traces into the
// Kelos controller and spawner binaries.
//
// Setup is a no-op unless an OTLP endpoint is configured via the standard
// OTEL_EXPORTER_OTLP_ENDPOINT (or the signal-specific
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT / OTEL_EXPORTER_OTLP_METRICS_ENDPOINT)
// environment variables. When unset, the existing Prometheus /metrics endpoint
// remains the only observability surface, so existing operators need no
// migration.
//
// Metrics are exported by bridging the controller-runtime Prometheus registry
// (which both binaries already register their metrics on) to an OTLP metric
// exporter. This means every metric already scraped from /metrics is also
// pushed over OTLP, without re-instrumenting any call sites.
package observability

import (
	"context"
	"fmt"
	"os"
	"strings"

	prombridge "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Annotation keys used to carry W3C trace context across the spawner -> Task
// boundary. The spawner stamps these onto the Tasks it creates so the Task
// controller (and the agent pod) can join the discovery trace.
const (
	AnnotationTraceparent = "kelos.dev/traceparent"
	AnnotationTracestate  = "kelos.dev/tracestate"
)

// annotationCarrier adapts a Kubernetes annotations map to a
// propagation.TextMapCarrier, mapping the W3C "traceparent"/"tracestate" header
// names to the kelos.dev/ annotation keys.
type annotationCarrier struct {
	annotations map[string]string
}

func annotationKey(headerKey string) string {
	switch headerKey {
	case "traceparent":
		return AnnotationTraceparent
	case "tracestate":
		return AnnotationTracestate
	default:
		return ""
	}
}

func (c annotationCarrier) Get(key string) string {
	if k := annotationKey(key); k != "" {
		return c.annotations[k]
	}
	return ""
}

func (c annotationCarrier) Set(key, value string) {
	if k := annotationKey(key); k != "" {
		c.annotations[k] = value
	}
}

func (c annotationCarrier) Keys() []string {
	keys := make([]string, 0, 2)
	if _, ok := c.annotations[AnnotationTraceparent]; ok {
		keys = append(keys, "traceparent")
	}
	if _, ok := c.annotations[AnnotationTracestate]; ok {
		keys = append(keys, "tracestate")
	}
	return keys
}

// InjectTraceContext writes the trace context from ctx into annotations using
// the kelos.dev/ trace annotation keys. It is a no-op when there is no active
// span context. The annotations map must be non-nil.
func InjectTraceContext(ctx context.Context, annotations map[string]string) {
	otel.GetTextMapPropagator().Inject(ctx, annotationCarrier{annotations: annotations})
}

// ExtractTraceContext returns a context carrying the trace context encoded in
// annotations, or ctx unchanged when none is present.
func ExtractTraceContext(ctx context.Context, annotations map[string]string) context.Context {
	if annotations == nil {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, annotationCarrier{annotations: annotations})
}

// TraceparentFromAnnotations returns the W3C traceparent value stored in
// annotations, or "" when absent. It is used to seed the TRACEPARENT env var of
// agent pods so agents that support OTEL can continue the trace.
func TraceparentFromAnnotations(annotations map[string]string) string {
	return annotations[AnnotationTraceparent]
}

// noopShutdown is returned when OTLP export is disabled so callers can always
// defer the returned function unconditionally.
func noopShutdown(context.Context) error { return nil }

// envSet reports whether any of the given environment variables is set to a
// non-blank value (after trimming surrounding whitespace).
func envSet(envs ...string) bool {
	for _, env := range envs {
		if strings.TrimSpace(os.Getenv(env)) != "" {
			return true
		}
	}
	return false
}

// tracesEnabled reports whether an OTLP trace endpoint is configured, via the
// generic OTEL_EXPORTER_OTLP_ENDPOINT or the trace-specific
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT.
func tracesEnabled() bool {
	return envSet("OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
}

// metricsEnabled reports whether an OTLP metric endpoint is configured, via the
// generic OTEL_EXPORTER_OTLP_ENDPOINT or the metric-specific
// OTEL_EXPORTER_OTLP_METRICS_ENDPOINT.
func metricsEnabled() bool {
	return envSet("OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
}

// Enabled reports whether any OTLP endpoint is configured. When false, Setup
// installs nothing and the caller keeps the Prometheus-only behavior.
func Enabled() bool {
	return tracesEnabled() || metricsEnabled()
}

// Setup installs global OTLP TracerProvider and MeterProvider when an OTLP
// endpoint is configured, and returns a shutdown function that flushes and
// closes both providers. When no endpoint is configured it returns a no-op
// shutdown and a nil error.
//
// serviceName seeds the service.name resource attribute; OTEL_SERVICE_NAME (via
// resource.WithFromEnv) overrides it when set. serviceVersion populates
// service.version.
//
// Setup fails fast: if an endpoint is configured but an exporter cannot be
// constructed, it returns an error rather than silently degrading to no export.
func Setup(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	if !Enabled() {
		return noopShutdown, nil
	}

	res, err := buildResource(ctx, serviceName, serviceVersion)
	if err != nil {
		return noopShutdown, fmt.Errorf("building OTEL resource: %w", err)
	}

	// W3C trace context + baggage so spans propagate across the
	// spawner -> Task -> agent pod boundary via traceparent.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	var shutdowns []func(context.Context) error

	// rollback shuts down everything already installed, in reverse install
	// order, so a partial failure does not leak a live exporter.
	rollback := func() {
		for i := len(shutdowns) - 1; i >= 0; i-- {
			_ = shutdowns[i](ctx)
		}
	}

	if tracesEnabled() {
		traceExporter, err := newTraceExporter(ctx)
		if err != nil {
			rollback()
			return noopShutdown, fmt.Errorf("creating OTLP trace exporter: %w", err)
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		shutdowns = append(shutdowns, tp.Shutdown)
	}

	if metricsEnabled() {
		metricExporter, err := newMetricExporter(ctx)
		if err != nil {
			rollback()
			return noopShutdown, fmt.Errorf("creating OTLP metric exporter: %w", err)
		}
		// Bridge the controller-runtime Prometheus registry into the OTLP push
		// pipeline: the PeriodicReader collects from the bridge producer, which
		// gathers every metric registered on ctrlmetrics.Registry.
		reader := sdkmetric.NewPeriodicReader(
			metricExporter,
			sdkmetric.WithProducer(prombridge.NewMetricProducer(
				prombridge.WithGatherer(ctrlmetrics.Registry),
			)),
		)
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(reader),
			sdkmetric.WithResource(res),
		)
		otel.SetMeterProvider(mp)
		shutdowns = append(shutdowns, mp.Shutdown)
	}

	return func(ctx context.Context) error {
		var errs []error
		// Shut down in reverse install order.
		for i := len(shutdowns) - 1; i >= 0; i-- {
			if err := shutdowns[i](ctx); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("shutting down OTEL providers: %v", errs)
		}
		return nil
	}, nil
}

func buildResource(ctx context.Context, serviceName, serviceVersion string) (*resource.Resource, error) {
	// resource.New applies options in order, and later options win on key
	// conflicts. Set the explicit attributes first as the baseline, then
	// resource.WithFromEnv last so operator-provided OTEL_SERVICE_NAME and
	// OTEL_RESOURCE_ATTRIBUTES override/extend them.
	return resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
	)
}

// otlpProtocol returns the configured OTLP protocol, honoring the standard
// OTEL_EXPORTER_OTLP_PROTOCOL (and its signal-specific variants). It defaults to
// grpc, matching the OpenTelemetry specification default.
func otlpProtocol(signalEnv string) string {
	for _, env := range []string{signalEnv, "OTEL_EXPORTER_OTLP_PROTOCOL"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return strings.ToLower(v)
		}
	}
	return "grpc"
}

func newTraceExporter(ctx context.Context) (*otlptrace.Exporter, error) {
	// The exporter constructors read endpoint, headers, TLS, and timeout from
	// the standard OTEL_EXPORTER_OTLP_* environment variables themselves; we
	// only select the wire protocol here.
	switch proto := otlpProtocol("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"); proto {
	case "grpc":
		return otlptracegrpc.New(ctx)
	case "http/protobuf", "http":
		return otlptracehttp.New(ctx)
	default:
		return nil, fmt.Errorf("unsupported OTLP protocol %q (want grpc or http/protobuf)", proto)
	}
}

func newMetricExporter(ctx context.Context) (sdkmetric.Exporter, error) {
	switch proto := otlpProtocol("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"); proto {
	case "grpc":
		return otlpmetricgrpc.New(ctx)
	case "http/protobuf", "http":
		return otlpmetrichttp.New(ctx)
	default:
		return nil, fmt.Errorf("unsupported OTLP protocol %q (want grpc or http/protobuf)", proto)
	}
}
