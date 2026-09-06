// Package audit is what every subsystem publishes its actions through. It sends wire
// events to the Kafka topic the Audit/RBAC service consumes; the chain, the dedup and the
// table are the service's alone (ADR-0018).
package audit

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	Topic = "kiln.audit"

	// Stamped by the audit service's REST path on every CR it applies; controllers copy it
	// into the actor of every event about that CR.
	AnnotationRequestedBy = "platform.internal/requested-by"

	ActionProvisionRequest = "PROVISION_REQUEST"
	ActionPolicyDeny       = "POLICY_DENY"
	ActionProvision        = "PROVISION"
	ActionBackup           = "BACKUP"
	ActionRestore          = "RESTORE"
	ActionScale            = "SCALE"
	ActionSchedule         = "SCHEDULE"
	ActionDeploy           = "DEPLOY"
	ActionRollback         = "ROLLBACK"
	ActionChaosExperiment  = "CHAOS_EXPERIMENT"
)

// Event is the wire shape defined in API_REFERENCE.md ("Audit event schema").
type Event struct {
	EventID   string         `json:"eventId"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Resource  string         `json:"resource"`
	Timestamp time.Time      `json:"timestamp"`
	Details   map[string]any `json:"details"`
}

func (e Event) Validate() error {
	if _, err := uuid.Parse(e.EventID); err != nil {
		return fmt.Errorf("eventId %q: %w", e.EventID, err)
	}
	for name, v := range map[string]string{"actor": e.Actor, "action": e.Action, "resource": e.Resource} {
		if v == "" {
			return fmt.Errorf("%s is empty", name)
		}
	}
	if e.Timestamp.IsZero() {
		return fmt.Errorf("timestamp is zero")
	}
	return nil
}

// MarshalJSON pins the timestamp to RFC 3339 UTC at microsecond precision, the precision
// the service stores and hashes, and never omits details.
func (e Event) MarshalJSON() ([]byte, error) {
	type wire struct {
		EventID   string         `json:"eventId"`
		Actor     string         `json:"actor"`
		Action    string         `json:"action"`
		Resource  string         `json:"resource"`
		Timestamp string         `json:"timestamp"`
		Details   map[string]any `json:"details"`
	}
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	return json.Marshal(wire{
		EventID:   e.EventID,
		Actor:     e.Actor,
		Action:    e.Action,
		Resource:  e.Resource,
		Timestamp: e.Timestamp.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z"),
		Details:   details,
	})
}

var idNamespace = uuid.MustParse("3b1d8c2e-6f0a-4c9b-9d3e-7a5f1e2c4b60")

// DeterministicID gives a retried reconcile and a redelivered record the same eventId, so
// the service's unique constraint stores the transition once (ADR-0017). Callers pass the
// resource, the action and whatever distinguishes this transition from the next one of the
// same kind on the same resource.
func DeterministicID(parts ...string) string {
	return uuid.NewSHA1(idNamespace, []byte(strings.Join(parts, "\x00"))).String()
}

// ResourceRef renders <Kind>/<namespace>/<name>.
func ResourceRef(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

// ActorOf reads the requested-by annotation and otherwise attributes the action to the
// controller itself (ADR-0018).
func ActorOf(annotations map[string]string, controller string) string {
	if actor := annotations[AnnotationRequestedBy]; actor != "" {
		return actor
	}
	return "system:" + controller
}
