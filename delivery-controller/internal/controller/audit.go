package controller

import (
	"github.com/trnahnh/kiln/audit"

	platformv1 "github.com/trnahnh/kiln/delivery-controller/api/v1"
)

const controllerName = "canaryrollout"

// publish records one transition of cr; idParts distinguish it from the next transition of
// the same action on the same rollout so a repeated reconcile stores one entry.
func (r *CanaryRolloutReconciler) publish(cr *platformv1.CanaryRollout, action string, details map[string]any, idParts ...string) {
	pub := r.Audit
	if pub == nil {
		pub = audit.Discard{}
	}
	resource := audit.ResourceRef("CanaryRollout", cr.Namespace, cr.Name)
	pub.Publish(audit.Event{
		EventID:   audit.DeterministicID(append([]string{resource, action}, idParts...)...),
		Actor:     audit.ActorOf(cr.Annotations, controllerName),
		Action:    action,
		Resource:  resource,
		Timestamp: r.now().Time,
		Details:   details,
	})
}
