package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	platformv1 "github.com/trnahnh/kiln/chaos/api/v1"
	"github.com/trnahnh/kiln/chaos/internal/analysis"
	"github.com/trnahnh/kiln/slo"
)

const proxyContainer = "istio-proxy"

func podReady(p *corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// selectTargets fixes the blast radius: the lowest-named `allowed` matching pods, so the
// choice is stable across reconciles. For resource-exhaustion it resolves the container a
// burner will join and rejects a target whose container has no CPU limit, since a burner
// there would contend node-wide rather than being throttled with the app.
func selectTargets(cr *platformv1.ChaosExperiment, matching []corev1.Pod, allowed int) (targets []platformv1.TargetStatus, reason, msg string, ok bool) {
	chosen := matching
	if len(chosen) > allowed {
		chosen = chosen[:allowed]
	}
	for i := range chosen {
		p := &chosen[i]
		t := platformv1.TargetStatus{Pod: p.Name, UID: string(p.UID), Node: p.Spec.NodeName, State: platformv1.TargetSelected}
		if cr.Spec.FaultType == platformv1.FaultResourceExhaustion {
			container, found := limitedContainer(p)
			if !found {
				return nil, platformv1.ReasonInvalidSpec, "resource-exhaustion needs an app container with a CPU limit; pod " + p.Name + " has none", false
			}
			t.Container = container
		}
		targets = append(targets, t)
	}
	return targets, "", "", true
}

func limitedContainer(p *corev1.Pod) (string, bool) {
	for _, c := range p.Spec.Containers {
		if c.Name == proxyContainer {
			continue
		}
		if _, has := c.Resources.Limits[corev1.ResourceCPU]; has {
			return c.Name, true
		}
	}
	return "", false
}

// pickPods is the pod-kill victim set: up to `allowed` matching pods, lowest-named first.
// A fresh selection every interval is fine because killed pods are replaced under new
// names, so the count in flight never exceeds the cap.
func pickPods(cr *platformv1.ChaosExperiment, matching []corev1.Pod, allowed int) []corev1.Pod {
	if len(matching) > allowed {
		return matching[:allowed]
	}
	return matching
}

// workloadName is the Istio canonical service the SLO is read for: the pods' `app` label,
// which Istio reports as destination_workload for a standard Deployment.
func workloadName(cr *platformv1.ChaosExperiment, matching []corev1.Pod) string {
	for i := range matching {
		if app := matching[i].Labels["app"]; app != "" {
			return app
		}
	}
	return ""
}

func snapshot(s *platformv1.CounterSnapshot) slo.Counters {
	if s == nil {
		return slo.Counters{}
	}
	return slo.Counters{Requests: s.Requests, Errors: s.Errors, Slow: s.Slow}
}

func stateFromStatus(s *platformv1.AnalysisState) analysis.State {
	st := analysis.State{RecoveredAfter: -1}
	if s == nil {
		return st
	}
	st.FaultWindows = int(s.FaultWindows)
	st.HeadroomTotal = s.HeadroomTotal
	st.WorstErrorRate = s.WorstErrorRate
	st.WorstSlowFraction = s.WorstSlowFraction
	st.RecoveryWindows = int(s.RecoveryWindows)
	if s.RecoveredAfter != nil {
		st.RecoveredAfter = int(*s.RecoveredAfter)
	}
	if s.LastWindowAt != nil {
		st.LastWindowAt = s.LastWindowAt.Time
	}
	return st
}

func writeState(cr *platformv1.ChaosExperiment, st analysis.State, now metav1.Time) {
	if cr.Status.Analysis == nil {
		cr.Status.Analysis = &platformv1.AnalysisState{}
	}
	a := cr.Status.Analysis
	a.FaultWindows = int32(st.FaultWindows)
	a.HeadroomTotal = st.HeadroomTotal
	a.WorstErrorRate = st.WorstErrorRate
	a.WorstSlowFraction = st.WorstSlowFraction
	a.RecoveryWindows = int32(st.RecoveryWindows)
	if st.RecoveredAfter >= 0 {
		a.RecoveredAfter = ptr.To(int32(st.RecoveredAfter))
	}
	if !st.LastWindowAt.IsZero() {
		a.LastWindowAt = ptr.To(metav1.NewTime(st.LastWindowAt))
	}
}
