package lifecycle

import (
	"errors"
	"testing"

	platformv1 "github.com/trnahnh/kiln/operator/api/v1"
)

func TestNextAllowsDocumentedTransitions(t *testing.T) {
	cases := []struct {
		from  platformv1.Phase
		event Event
		want  platformv1.Phase
	}{
		{platformv1.PhaseProvisioning, EventProvisioned, platformv1.PhaseReady},
		{platformv1.PhaseReady, EventBackupStarted, platformv1.PhaseBackingUp},
		{platformv1.PhaseBackingUp, EventBackupSucceeded, platformv1.PhaseReady},
		{platformv1.PhaseBackingUp, EventBackupFailed, platformv1.PhaseReady},
		{platformv1.PhaseReady, EventRestoreStarted, platformv1.PhaseRestoring},
		{platformv1.PhaseRestoring, EventRestoreSucceeded, platformv1.PhaseReady},
		{platformv1.PhaseRestoring, EventRestoreFailed, platformv1.PhaseFailed},
		{platformv1.PhaseProvisioning, EventFailed, platformv1.PhaseFailed},
		{platformv1.PhaseReady, EventFailed, platformv1.PhaseFailed},
		{platformv1.PhaseBackingUp, EventFailed, platformv1.PhaseFailed},
		{platformv1.PhaseRestoring, EventFailed, platformv1.PhaseFailed},
	}
	for _, c := range cases {
		got, err := Next(c.from, c.event)
		if err != nil {
			t.Errorf("%s + %s: unexpected error %v", c.from, c.event, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s + %s: got %s, want %s", c.from, c.event, got, c.want)
		}
	}
}

func TestNextRejectsConflictingTransitions(t *testing.T) {
	cases := []struct {
		from  platformv1.Phase
		event Event
	}{
		{platformv1.PhaseBackingUp, EventBackupStarted},
		{platformv1.PhaseBackingUp, EventRestoreStarted},
		{platformv1.PhaseRestoring, EventBackupStarted},
		{platformv1.PhaseRestoring, EventRestoreStarted},
		{platformv1.PhaseProvisioning, EventBackupStarted},
		{platformv1.PhaseProvisioning, EventRestoreStarted},
		{platformv1.PhaseReady, EventProvisioned},
		{platformv1.PhaseReady, EventBackupSucceeded},
		{platformv1.PhaseReady, EventRestoreSucceeded},
		{platformv1.PhaseBackingUp, EventRestoreSucceeded},
		{platformv1.PhaseRestoring, EventBackupSucceeded},
	}
	for _, c := range cases {
		got, err := Next(c.from, c.event)
		if !errors.Is(err, ErrConflict) {
			t.Errorf("%s + %s: got (%s, %v), want ErrConflict", c.from, c.event, got, err)
		}
		if got != c.from {
			t.Errorf("%s + %s: rejected transition must leave phase unchanged, got %s", c.from, c.event, got)
		}
	}
}

func TestFailedIsTerminal(t *testing.T) {
	for _, ev := range []Event{EventProvisioned, EventBackupStarted, EventBackupSucceeded, EventBackupFailed,
		EventRestoreStarted, EventRestoreSucceeded, EventRestoreFailed, EventFailed} {
		got, err := Next(platformv1.PhaseFailed, ev)
		if !errors.Is(err, ErrTerminal) {
			t.Errorf("Failed + %s: got (%s, %v), want ErrTerminal", ev, got, err)
		}
		if got != platformv1.PhaseFailed {
			t.Errorf("Failed + %s: phase changed to %s", ev, got)
		}
	}
}

func TestEmptyPhaseIsProvisioning(t *testing.T) {
	if got := Normalize(""); got != platformv1.PhaseProvisioning {
		t.Errorf("Normalize(\"\") = %s, want Provisioning", got)
	}
	if got := Normalize(platformv1.PhaseReady); got != platformv1.PhaseReady {
		t.Errorf("Normalize(Ready) = %s", got)
	}
}

func TestSpecChangesApplyOnlyInReady(t *testing.T) {
	for _, p := range []platformv1.Phase{platformv1.PhaseProvisioning, platformv1.PhaseBackingUp, platformv1.PhaseRestoring, platformv1.PhaseFailed} {
		if CanApplySpec(p) {
			t.Errorf("CanApplySpec(%s) = true", p)
		}
	}
	if !CanApplySpec(platformv1.PhaseReady) {
		t.Error("CanApplySpec(Ready) = false")
	}
}

func TestOperationsStartOnlyWhenReadyAndSettled(t *testing.T) {
	if CanStartOperation(platformv1.PhaseReady, false) {
		t.Error("an operation must not start while a scale is still settling")
	}
	if !CanStartOperation(platformv1.PhaseReady, true) {
		t.Error("CanStartOperation(Ready, settled) = false")
	}
	for _, p := range []platformv1.Phase{platformv1.PhaseProvisioning, platformv1.PhaseBackingUp, platformv1.PhaseRestoring, platformv1.PhaseFailed} {
		if CanStartOperation(p, true) {
			t.Errorf("CanStartOperation(%s, settled) = true", p)
		}
	}
}
