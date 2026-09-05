// Package pricing turns node metadata into the economics the scoring model needs.
package pricing

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/trnahnh/kiln/scheduler-plugin/internal/scoring"
)

// Source resolves a node's economics. ok is false when the node carries no usable facts;
// the plugin then falls back to the conservative default in Fallback.
type Source interface {
	Economics(node *corev1.Node) (e scoring.Economics, ok bool)
}

// Node economics contract (ADR-0010, API_REFERENCE.md): labels so both kind and cloud node
// templates can set them.
const (
	LabelCapacityType   = "kiln.platform.internal/capacity-type"
	LabelHourlyCost     = "kiln.platform.internal/hourly-cost"
	LabelPreemptionRisk = "kiln.platform.internal/preemption-risk"

	CapacitySpot     = "spot"
	CapacityOnDemand = "on-demand"

	// Spot capacity that states no risk still carries some; this is the Spot Advisor's
	// lowest bucket.
	DefaultSpotRisk = 0.05
)

// NodeLabels reads the contract labels directly. It is the source for kind, CI and any
// cloud whose node templates stamp the labels themselves.
type NodeLabels struct{}

func (NodeLabels) Economics(node *corev1.Node) (scoring.Economics, bool) {
	labels := node.GetLabels()
	capacity, ok := labels[LabelCapacityType]
	if !ok || (capacity != CapacitySpot && capacity != CapacityOnDemand) {
		return scoring.Economics{}, false
	}
	cost, err := strconv.ParseFloat(labels[LabelHourlyCost], 64)
	if err != nil || cost < 0 {
		return scoring.Economics{}, false
	}
	e := scoring.Economics{HourlyCost: cost, Spot: capacity == CapacitySpot}
	if e.Spot {
		e.PreemptionRisk = DefaultSpotRisk
	}
	if raw, ok := labels[LabelPreemptionRisk]; ok {
		risk, err := strconv.ParseFloat(raw, 64)
		if err != nil || risk < 0 || risk > 1 {
			return scoring.Economics{}, false
		}
		if e.Spot {
			e.PreemptionRisk = risk
		}
	}
	return e, true
}

// Fallback is what an unknown node is assumed to be: on-demand at the highest cost seen
// among known nodes, so ignorance never makes a node look cheap or safe to reclaim onto.
func Fallback(known []scoring.Economics) scoring.Economics {
	max := 0.0
	for _, e := range known {
		if e.HourlyCost > max {
			max = e.HourlyCost
		}
	}
	return scoring.Economics{HourlyCost: max}
}

// Chain asks each source in order and returns the first answer.
type Chain []Source

func (c Chain) Economics(node *corev1.Node) (scoring.Economics, bool) {
	for _, s := range c {
		if e, ok := s.Economics(node); ok {
			return e, true
		}
	}
	return scoring.Economics{}, false
}
