// Package agent runs on every node. It watches ChaosExperiments, and for the pods the
// controller selected on its own node it re-derives the blast radius from a fresh cluster
// read before touching anything, so containment holds even if the controller is wrong or
// compromised. It reverts on the experiment ending, on the CR being deleted, and on each
// fault's own deadline, independently.
package agent

import "sort"

// Allowed is the largest number of pods a fault may touch: the cap applied to the pods
// currently matching, floored. Thirty percent of two pods is zero, not one.
func Allowed(maxReplicaPercentage int32, matching int) int {
	if matching <= 0 {
		return 0
	}
	return int(int64(maxReplicaPercentage) * int64(matching) / 100)
}

// InScope is the agent's own answer to which of the controller's selected pods it will
// actually fault: only pods that still match the selector, capped at Allowed even if the
// controller selected more, chosen deterministically so every agent and every reconcile
// agree without coordination. A selection that exceeds the cap is contained here, not
// trusted.
func InScope(selected []string, matching map[string]bool, maxReplicaPercentage int32) map[string]bool {
	var eligible []string
	for _, pod := range selected {
		if matching[pod] {
			eligible = append(eligible, pod)
		}
	}
	sort.Strings(eligible)
	limit := min(Allowed(maxReplicaPercentage, len(matching)), len(eligible))
	out := make(map[string]bool, limit)
	for _, pod := range eligible[:limit] {
		out[pod] = true
	}
	return out
}
