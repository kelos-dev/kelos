package controller

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/types"
)

// reconcileTracer is the shared tracer for controller reconcile spans. It is
// obtained from the global TracerProvider, so it delegates to the OTLP provider
// installed by observability.Setup once that runs; when OTLP export is
// disabled the spans are cheap no-ops.
var reconcileTracer = otel.Tracer("github.com/kelos-dev/kelos/internal/controller")

// startReconcileSpan starts a span named "reconcile.<kind>" that records the
// reconciled object's namespace and name. Callers must end the span, typically
// with defer span.End(). The returned context carries the span so downstream
// operations (and, where propagated, agent pods) join the same trace.
func startReconcileSpan(ctx context.Context, kind string, key types.NamespacedName) (context.Context, trace.Span) {
	return reconcileTracer.Start(ctx, "reconcile."+kind,
		trace.WithAttributes(
			attribute.String("k8s.resource.kind", kind),
			attribute.String("k8s.namespace.name", key.Namespace),
			attribute.String("k8s.resource.name", key.Name),
		),
	)
}
