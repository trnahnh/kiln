// Package lifecycle is the TenantDatabase phase machine, free of any Kubernetes client
// so every transition can be tested without a cluster.
package lifecycle

import (
	"errors"
	"fmt"

	platformv1 "github.com/trnahnh/kiln/operator/api/v1"
)

type Event string

const (
	EventProvisioned      Event = "Provisioned"
	EventBackupStarted    Event = "BackupStarted"
	EventBackupSucceeded  Event = "BackupSucceeded"
	EventBackupFailed     Event = "BackupFailed"
	EventRestoreStarted   Event = "RestoreStarted"
	EventRestoreSucceeded Event = "RestoreSucceeded"
	EventRestoreFailed    Event = "RestoreFailed"
	EventFailed           Event = "Failed"
)

// ErrConflict is the RECONCILE_CONFLICT error code from API_REFERENCE.md: the event is
// legal in some phase but not the current one, so the caller requeues rather than acts.
var ErrConflict = errors.New("RECONCILE_CONFLICT")

// ErrTerminal: nothing leaves Failed except deletion (ADR-0003 discussion, API_REFERENCE.md).
var ErrTerminal = errors.New("phase Failed is terminal")

var transitions = map[platformv1.Phase]map[Event]platformv1.Phase{
	platformv1.PhaseProvisioning: {
		EventProvisioned: platformv1.PhaseReady,
	},
	platformv1.PhaseReady: {
		EventBackupStarted:  platformv1.PhaseBackingUp,
		EventRestoreStarted: platformv1.PhaseRestoring,
	},
	platformv1.PhaseBackingUp: {
		EventBackupSucceeded: platformv1.PhaseReady,
		EventBackupFailed:    platformv1.PhaseReady,
	},
	platformv1.PhaseRestoring: {
		EventRestoreSucceeded: platformv1.PhaseReady,
		EventRestoreFailed:    platformv1.PhaseFailed,
	},
}

func Normalize(p platformv1.Phase) platformv1.Phase {
	if p == "" {
		return platformv1.PhaseProvisioning
	}
	return p
}

func Next(from platformv1.Phase, ev Event) (platformv1.Phase, error) {
	from = Normalize(from)
	if from == platformv1.PhaseFailed {
		return from, ErrTerminal
	}
	if ev == EventFailed {
		return platformv1.PhaseFailed, nil
	}
	to, ok := transitions[from][ev]
	if !ok {
		return from, fmt.Errorf("%w: %s while %s", ErrConflict, ev, from)
	}
	return to, nil
}

// CanApplySpec: spec changes (storage growth) are applied only in Ready, never under a
// running backup or restore.
func CanApplySpec(p platformv1.Phase) bool {
	return Normalize(p) == platformv1.PhaseReady
}

// CanStartOperation: a backup or restore starts only in Ready and only once a prior spec
// change has settled (Ready condition True), so operations never overlap a scale.
func CanStartOperation(p platformv1.Phase, settled bool) bool {
	return CanApplySpec(p) && settled
}
