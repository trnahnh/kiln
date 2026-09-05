// Package scoring is the pure placement model: no Kubernetes types, no I/O, so every
// tradeoff can be tested against synthetic cluster states (TESTING.md).
package scoring

import (
	"fmt"
	"math"
)

type WorkloadClass string

const (
	ClassLatencySensitive WorkloadClass = "latency-sensitive"
	ClassStandard         WorkloadClass = "standard"
	ClassBatch            WorkloadClass = "batch"
)

// ParseClass treats anything unrecognised, including no label at all, as latency-sensitive:
// a workload that has not opted into reclaimable capacity never gets it (ADR-0009).
func ParseClass(label string) WorkloadClass {
	switch WorkloadClass(label) {
	case ClassStandard, ClassBatch:
		return WorkloadClass(label)
	default:
		return ClassLatencySensitive
	}
}

type Resources struct {
	MilliCPU    int64
	MemoryBytes int64
	GPUs        int64
}

type Economics struct {
	HourlyCost     float64
	Spot           bool
	PreemptionRisk float64
}

type Node struct {
	Name        string
	Allocatable Resources
	Requested   Resources
	Economics   Economics
}

type Pod struct {
	Class    WorkloadClass
	Requests Resources
}

// Weights are percentages summing to 100; see SYSTEM_DESIGN.md section 3 for why cost
// leads and preemption trails.
type Weights struct {
	Cost          int64
	Fragmentation int64
	Preemption    int64
}

func DefaultWeights() Weights {
	return Weights{Cost: 50, Fragmentation: 30, Preemption: 20}
}

func (w Weights) Validate() error {
	if w.Cost < 0 || w.Fragmentation < 0 || w.Preemption < 0 {
		return fmt.Errorf("weights must be non-negative: %+v", w)
	}
	if sum := w.Cost + w.Fragmentation + w.Preemption; sum != 100 {
		return fmt.Errorf("weights must sum to 100, got %d", sum)
	}
	return nil
}

// MaxScore matches the scheduler framework's MaxNodeScore.
const MaxScore int64 = 100

// gpuStrandingPenalty halves the fragmentation score of a pod that requests no GPU but
// would occupy a GPU node: the accelerator stays idle while CPU and memory fill up.
const gpuStrandingPenalty = 0.5

// Feasible is the workload-class filter. It runs before scoring, so no weighting can put
// a latency-sensitive pod on a reclaimable node (CLAUDE.md, SYSTEM_DESIGN.md section 3).
func Feasible(pod Pod, node Node) bool {
	return !(pod.Class == ClassLatencySensitive && node.Economics.Spot)
}

type CostRange struct {
	Min float64
	Max float64
}

func CostRangeOf(nodes []Node) CostRange {
	if len(nodes) == 0 {
		return CostRange{}
	}
	r := CostRange{Min: nodes[0].Economics.HourlyCost, Max: nodes[0].Economics.HourlyCost}
	for _, n := range nodes[1:] {
		r.Min = math.Min(r.Min, n.Economics.HourlyCost)
		r.Max = math.Max(r.Max, n.Economics.HourlyCost)
	}
	return r
}

// Score combines the three sub-scores, each in [0,1], into [0,MaxScore].
func Score(pod Pod, node Node, costs CostRange, w Weights) int64 {
	total := float64(w.Cost)*CostScore(node.Economics, costs) +
		float64(w.Fragmentation)*FragmentationScore(pod, node) +
		float64(w.Preemption)*PreemptionScore(node.Economics)
	return int64(math.Round(clamp01(total/100) * float64(MaxScore)))
}

// CostScore is 1 for the cheapest feasible node and 0 for the most expensive. When every
// node costs the same there is nothing to prefer, so all score 1.
func CostScore(e Economics, costs CostRange) float64 {
	span := costs.Max - costs.Min
	if span <= 0 {
		return 1
	}
	return clamp01(1 - (e.HourlyCost-costs.Min)/span)
}

// FragmentationScore rewards placements that fill a node rather than leaving slivers on
// many nodes: the post-placement utilisation of the pod's most demanding resource. A pod
// that requests nothing scores neutrally.
func FragmentationScore(pod Pod, node Node) float64 {
	after := Resources{
		MilliCPU:    node.Requested.MilliCPU + pod.Requests.MilliCPU,
		MemoryBytes: node.Requested.MemoryBytes + pod.Requests.MemoryBytes,
		GPUs:        node.Requested.GPUs + pod.Requests.GPUs,
	}
	dominant := 0.0
	requested := false
	for _, dim := range []struct{ req, after, alloc int64 }{
		{pod.Requests.MilliCPU, after.MilliCPU, node.Allocatable.MilliCPU},
		{pod.Requests.MemoryBytes, after.MemoryBytes, node.Allocatable.MemoryBytes},
		{pod.Requests.GPUs, after.GPUs, node.Allocatable.GPUs},
	} {
		if dim.req <= 0 {
			continue
		}
		requested = true
		if dim.alloc <= 0 || dim.after > dim.alloc {
			return 0
		}
		dominant = math.Max(dominant, float64(dim.after)/float64(dim.alloc))
	}
	if !requested {
		return 0.5
	}
	if node.Allocatable.GPUs > 0 && pod.Requests.GPUs == 0 {
		dominant *= gpuStrandingPenalty
	}
	return clamp01(dominant)
}

// PreemptionScore is 1 minus the reclaim risk; on-demand capacity carries none.
func PreemptionScore(e Economics) float64 {
	if !e.Spot {
		return 1
	}
	return clamp01(1 - e.PreemptionRisk)
}

func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}
