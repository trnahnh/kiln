package controller

import (
	"github.com/trnahnh/kiln/audit"

	platformv1 "github.com/trnahnh/kiln/operator/api/v1"
)

const controllerName = "tenantdatabase"

// publish records one transition of tdb; idParts distinguish it from the next transition of
// the same action on the same resource so a repeated reconcile stores one entry.
func (r *TenantDatabaseReconciler) publish(tdb *platformv1.TenantDatabase, action string, details map[string]any, idParts ...string) {
	pub := r.Audit
	if pub == nil {
		pub = audit.Discard{}
	}
	resource := audit.ResourceRef("TenantDatabase", tdb.Namespace, tdb.Name)
	pub.Publish(audit.Event{
		EventID:   audit.DeterministicID(append([]string{resource, action}, idParts...)...),
		Actor:     audit.ActorOf(tdb.Annotations, controllerName),
		Action:    action,
		Resource:  resource,
		Timestamp: r.now(),
		Details:   details,
	})
}
