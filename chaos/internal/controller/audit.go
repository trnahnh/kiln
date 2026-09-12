package controller

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/trnahnh/kiln/audit"
	"github.com/trnahnh/kiln/audit/outbox"
	platformv1 "github.com/trnahnh/kiln/chaos/api/v1"
	"github.com/trnahnh/kiln/tracing"
)

const controllerName = "chaos-controller"

// publish records one transition of cr as an audit event committed in its status, in the
// same patch as the transition (ADR-0022), and as one span of the trace the experiment
// was requested under, backdated to began so the span's duration is the transition's
// (ADR-0021); idParts distinguish it from the next transition of the same experiment so a
// repeated reconcile stores one entry.
func (r *Reconciler) publish(ctx context.Context, cr *platformv1.ChaosExperiment, began time.Time, details map[string]any, idParts ...string) {
	resource := audit.ResourceRef("ChaosExperiment", cr.Namespace, cr.Name)
	outcome, _ := details["outcome"].(string)
	ctx, span := tracing.Span(ctx, controllerName, cr.Annotations, audit.ActionChaosExperiment, began,
		attribute.String("kiln.resource", resource),
		attribute.String("kiln.outcome", outcome),
		attribute.String("kiln.fault_type", string(cr.Spec.FaultType)),
	)
	defer span.End()
	pending, err := audit.NewPending(ctx, audit.Event{
		EventID:   audit.DeterministicID(append([]string{resource, audit.ActionChaosExperiment}, idParts...)...),
		Actor:     audit.ActorOf(cr.Annotations, controllerName),
		Action:    audit.ActionChaosExperiment,
		Resource:  resource,
		Timestamp: r.now().Time,
		Details:   details,
	})
	if err != nil {
		logf.FromContext(ctx).Error(err, "audit event not committed")
		return
	}
	cr.Status.Audit.Pending = append(cr.Status.Audit.Pending, pending)
}

// signalOutbox hands the committed events to the drainer; called only after the status
// patch succeeded, and on every reconcile so a restart resumes what was left.
func (r *Reconciler) signalOutbox(cr *platformv1.ChaosExperiment) {
	if r.Outbox != nil && len(cr.Status.Audit.Pending) > 0 {
		r.Outbox.Signal(outbox.Key(cr))
	}
}

// experimentBegan is when the fault went live, or zero before it has.
func experimentBegan(cr *platformv1.ChaosExperiment) time.Time {
	if cr.Status.StartedAt == nil {
		return time.Time{}
	}
	return cr.Status.StartedAt.Time
}
