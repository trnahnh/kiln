package controller

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/trnahnh/kiln/audit"
	platformv1 "github.com/trnahnh/kiln/operator/api/v1"
	"github.com/trnahnh/kiln/tracing"
)

const controllerName = "tenantdatabase"

// publish records one transition of tdb as an audit event and as one span of the trace the
// CR was requested under, backdated to began so the span's duration is the transition's
// (ADR-0021); idParts distinguish it from the next transition of the same action on the
// same resource so a repeated reconcile stores one entry.
func (r *TenantDatabaseReconciler) publish(ctx context.Context, tdb *platformv1.TenantDatabase, action string, began time.Time, details map[string]any, idParts ...string) {
	pub := r.Audit
	if pub == nil {
		pub = audit.Discard{}
	}
	resource := audit.ResourceRef("TenantDatabase", tdb.Namespace, tdb.Name)
	outcome, _ := details["outcome"].(string)
	ctx, span := tracing.Span(ctx, controllerName, tdb.Annotations, action, began,
		attribute.String("kiln.resource", resource),
		attribute.String("kiln.outcome", outcome),
		attribute.String("kiln.phase", string(tdb.Status.Phase)),
	)
	defer span.End()
	pub.Publish(ctx, audit.Event{
		EventID:   audit.DeterministicID(append([]string{resource, action}, idParts...)...),
		Actor:     audit.ActorOf(tdb.Annotations, controllerName),
		Action:    action,
		Resource:  resource,
		Timestamp: r.now(),
		Details:   details,
	})
}
