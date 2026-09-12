package controller

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"k8s.io/apimachinery/pkg/api/meta"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/trnahnh/kiln/audit"
	"github.com/trnahnh/kiln/audit/outbox"
	platformv1 "github.com/trnahnh/kiln/delivery-controller/api/v1"
	"github.com/trnahnh/kiln/tracing"
)

const controllerName = "canaryrollout"

// publish records one transition of cr as an audit event committed in its status, in the
// same patch as the transition (ADR-0022), and as one span of the trace the rollout was
// requested under, backdated to began so the span's duration is the transition's
// (ADR-0021); idParts distinguish it from the next transition of the same action on the
// same rollout so a repeated reconcile stores one entry.
func (r *CanaryRolloutReconciler) publish(ctx context.Context, cr *platformv1.CanaryRollout, action string, began time.Time, details map[string]any, idParts ...string) {
	resource := audit.ResourceRef("CanaryRollout", cr.Namespace, cr.Name)
	outcome, _ := details["outcome"].(string)
	hash, _ := details["templateHash"].(string)
	ctx, span := tracing.Span(ctx, controllerName, cr.Annotations, action, began,
		attribute.String("kiln.resource", resource),
		attribute.String("kiln.outcome", outcome),
		attribute.String("kiln.template_hash", hash),
	)
	defer span.End()
	pending, err := audit.NewPending(ctx, audit.Event{
		EventID:   audit.DeterministicID(append([]string{resource, action}, idParts...)...),
		Actor:     audit.ActorOf(cr.Annotations, controllerName),
		Action:    action,
		Resource:  resource,
		Timestamp: r.now().Time,
		Details:   details,
	})
	if err != nil {
		logf.FromContext(ctx).Error(err, "audit event not committed", "action", action)
		return
	}
	cr.Status.Audit.Pending = append(cr.Status.Audit.Pending, pending)
}

// signalOutbox hands the committed events to the drainer; called only after the status
// patch succeeded, and on every reconcile so a restart resumes what was left.
func (r *CanaryRolloutReconciler) signalOutbox(cr *platformv1.CanaryRollout) {
	if r.Outbox != nil && len(cr.Status.Audit.Pending) > 0 {
		r.Outbox.Signal(outbox.Key(cr))
	}
}

// rolloutBegan is when the current rollout started: the Progressing condition flips to
// true at the start and stays there until the rollout settles.
func rolloutBegan(cr *platformv1.CanaryRollout) time.Time {
	c := meta.FindStatusCondition(cr.Status.Conditions, platformv1.ConditionProgressing)
	if c == nil {
		return time.Time{}
	}
	return c.LastTransitionTime.Time
}
