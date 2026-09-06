package controller

import (
	"github.com/trnahnh/kiln/audit"

	platformv1 "github.com/trnahnh/kiln/chaos/api/v1"
)

const controllerName = "chaos-controller"

// publish records one transition of cr; idParts distinguish it from the next transition of
// the same action on the same experiment so a repeated reconcile stores one entry.
func (r *Reconciler) publish(cr *platformv1.ChaosExperiment, details map[string]any, idParts ...string) {
	pub := r.Audit
	if pub == nil {
		pub = audit.Discard{}
	}
	resource := audit.ResourceRef("ChaosExperiment", cr.Namespace, cr.Name)
	pub.Publish(audit.Event{
		EventID:   audit.DeterministicID(append([]string{resource, audit.ActionChaosExperiment}, idParts...)...),
		Actor:     audit.ActorOf(cr.Annotations, controllerName),
		Action:    audit.ActionChaosExperiment,
		Resource:  resource,
		Timestamp: r.now().Time,
		Details:   details,
	})
}
