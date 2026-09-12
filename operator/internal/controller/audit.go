package controller

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/trnahnh/kiln/audit"
	"github.com/trnahnh/kiln/audit/outbox"
	platformv1 "github.com/trnahnh/kiln/operator/api/v1"
	"github.com/trnahnh/kiln/tracing"
)

const controllerName = "tenantdatabase"

// publish records one transition of tdb as an audit event committed in its status, in the
// same patch as the transition (ADR-0022), and as one span of the trace the CR was
// requested under, backdated to began so the span's duration is the transition's
// (ADR-0021); idParts distinguish it from the next transition of the same action on the
// same resource so a repeated reconcile stores one entry.
func (r *TenantDatabaseReconciler) publish(ctx context.Context, tdb *platformv1.TenantDatabase, action string, began time.Time, details map[string]any, idParts ...string) {
	resource := audit.ResourceRef("TenantDatabase", tdb.Namespace, tdb.Name)
	outcome, _ := details["outcome"].(string)
	ctx, span := tracing.Span(ctx, controllerName, tdb.Annotations, action, began,
		attribute.String("kiln.resource", resource),
		attribute.String("kiln.outcome", outcome),
		attribute.String("kiln.phase", string(tdb.Status.Phase)),
	)
	defer span.End()
	pending, err := audit.NewPending(ctx, audit.Event{
		EventID:   audit.DeterministicID(append([]string{resource, action}, idParts...)...),
		Actor:     audit.ActorOf(tdb.Annotations, controllerName),
		Action:    action,
		Resource:  resource,
		Timestamp: r.now(),
		Details:   details,
	})
	if err != nil {
		logf.FromContext(ctx).Error(err, "audit event not committed", "action", action)
		return
	}
	tdb.Status.Audit.Pending = append(tdb.Status.Audit.Pending, pending)
}

// signalOutbox hands the committed events to the drainer; called only after the status
// patch succeeded, and on every reconcile so a restart resumes what was left.
func (r *TenantDatabaseReconciler) signalOutbox(tdb *platformv1.TenantDatabase) {
	if r.Outbox != nil && len(tdb.Status.Audit.Pending) > 0 {
		r.Outbox.Signal(outbox.Key(tdb))
	}
}
