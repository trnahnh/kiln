package scoring

import (
	"fmt"
	"testing"
)

const gi = 1 << 30

func spot(name string, cost, risk float64) Node {
	return Node{Name: name, Allocatable: Resources{MilliCPU: 4000, MemoryBytes: 8 * gi},
		Economics: Economics{HourlyCost: cost, Spot: true, PreemptionRisk: risk}}
}

func onDemand(name string, cost float64) Node {
	return Node{Name: name, Allocatable: Resources{MilliCPU: 4000, MemoryBytes: 8 * gi},
		Economics: Economics{HourlyCost: cost}}
}

func pod(class WorkloadClass, milliCPU, memBytes int64) Pod {
	return Pod{Class: class, Requests: Resources{MilliCPU: milliCPU, MemoryBytes: memBytes}}
}

func feasibleNames(p Pod, nodes []Node) []string {
	var out []string
	for _, n := range nodes {
		if Feasible(p, n) {
			out = append(out, n.Name)
		}
	}
	return out
}

func best(p Pod, nodes []Node, w Weights) string {
	var feasible []Node
	for _, n := range nodes {
		if Feasible(p, n) {
			feasible = append(feasible, n)
		}
	}
	costs := CostRangeOf(feasible)
	bestName, bestScore := "", int64(-1)
	for _, n := range feasible {
		if s := Score(p, n, costs, w); s > bestScore {
			bestName, bestScore = n.Name, s
		}
	}
	return bestName
}

func TestParseClassDefaultsToLatencySensitive(t *testing.T) {
	for _, in := range []string{"", "latency-sensitive", "gold", "BATCH"} {
		if got := ParseClass(in); got != ClassLatencySensitive {
			t.Errorf("ParseClass(%q) = %s, want latency-sensitive", in, got)
		}
	}
	if ParseClass("standard") != ClassStandard || ParseClass("batch") != ClassBatch {
		t.Error("explicit standard/batch must be honoured")
	}
}

func TestLatencySensitivePodsNeverSeeSpotNodes(t *testing.T) {
	nodes := []Node{spot("spot-a", 0.03, 0.2), onDemand("od-a", 0.10), spot("spot-b", 0.01, 0.05)}
	got := feasibleNames(pod(ClassLatencySensitive, 500, gi), nodes)
	if len(got) != 1 || got[0] != "od-a" {
		t.Fatalf("latency-sensitive pod feasible on %v, want only od-a", got)
	}
	if got := feasibleNames(pod(ClassBatch, 500, gi), nodes); len(got) != 3 {
		t.Fatalf("batch pod feasible on %v, want all three", got)
	}
}

func TestAllSpotClusterStarvesLatencySensitiveRatherThanPlacingIt(t *testing.T) {
	nodes := []Node{spot("spot-a", 0.03, 0.2), spot("spot-b", 0.02, 0.3)}
	if got := feasibleNames(pod(ClassLatencySensitive, 500, gi), nodes); got != nil {
		t.Fatalf("no spot node may be feasible for a latency-sensitive pod, got %v", got)
	}
	if got := best(pod(ClassStandard, 500, gi), nodes, DefaultWeights()); got != "spot-b" {
		t.Fatalf("standard pod on an all-spot cluster should take the cheapest node, got %s", got)
	}
}

func TestNoSpotCapacityFallsBackToPackingOnDemand(t *testing.T) {
	fuller := onDemand("od-full", 0.10)
	fuller.Requested = Resources{MilliCPU: 3000, MemoryBytes: 6 * gi}
	emptier := onDemand("od-empty", 0.10)
	nodes := []Node{emptier, fuller}
	p := pod(ClassBatch, 500, gi)
	costs := CostRangeOf(nodes)
	if CostScore(fuller.Economics, costs) != 1 || CostScore(emptier.Economics, costs) != 1 {
		t.Fatal("equal costs must not create a preference")
	}
	if got := best(p, nodes, DefaultWeights()); got != "od-full" {
		t.Fatalf("with no spot capacity fragmentation decides; want od-full, got %s", got)
	}
}

func TestCheaperSpotWinsForBatchUnlessRiskIsExtreme(t *testing.T) {
	nodes := []Node{spot("spot-cheap", 0.03, 0.2), onDemand("od", 0.10)}
	if got := best(pod(ClassBatch, 500, gi), nodes, DefaultWeights()); got != "spot-cheap" {
		t.Fatalf("batch pod should prefer the cheap spot node, got %s", got)
	}
	risky := []Node{spot("spot-risky", 0.03, 1.0), onDemand("od", 0.10)}
	costs := CostRangeOf(risky)
	spotScore := Score(pod(ClassBatch, 500, gi), risky[0], costs, DefaultWeights())
	odScore := Score(pod(ClassBatch, 500, gi), risky[1], costs, DefaultWeights())
	if spotScore <= odScore {
		t.Fatalf("even at maximal risk the 50-point cost lead keeps spot ahead (spot=%d od=%d); this pins the documented weighting", spotScore, odScore)
	}
	if got := PreemptionScore(risky[0].Economics); got != 0 {
		t.Fatalf("maximal risk must zero the preemption sub-score, got %v", got)
	}
}

func TestFragmentationPrefersTheNodeItFillsBest(t *testing.T) {
	a := onDemand("a", 0.10)
	a.Requested = Resources{MilliCPU: 3500, MemoryBytes: gi}
	b := onDemand("b", 0.10)
	b.Requested = Resources{MilliCPU: 1000, MemoryBytes: gi}
	p := pod(ClassStandard, 500, gi)
	if FragmentationScore(p, a) <= FragmentationScore(p, b) {
		t.Fatal("the node left fuller after placement must score higher")
	}
	if FragmentationScore(p, a) != 1.0 {
		t.Fatalf("a: 3500m+500m of 4000m is fully packed, want 1.0, got %v", FragmentationScore(p, a))
	}
	over := onDemand("over", 0.10)
	over.Requested = Resources{MilliCPU: 3800, MemoryBytes: gi}
	if FragmentationScore(p, over) != 0 {
		t.Fatal("a placement that does not fit scores 0, never panics")
	}
}

func TestGPUNodesArePenalisedForNonGPUPods(t *testing.T) {
	gpu := onDemand("gpu", 0.10)
	gpu.Allocatable.GPUs = 4
	plain := onDemand("plain", 0.10)
	p := pod(ClassStandard, 1000, gi)
	if FragmentationScore(p, gpu) >= FragmentationScore(p, plain) {
		t.Fatal("a non-GPU pod must score lower on a GPU node than on an equivalent plain node")
	}
	gpuPod := Pod{Class: ClassBatch, Requests: Resources{MilliCPU: 1000, MemoryBytes: gi, GPUs: 4}}
	if got := FragmentationScore(gpuPod, gpu); got != 1.0 {
		t.Fatalf("a pod taking every GPU packs the node completely, want 1.0, got %v", got)
	}
	if got := FragmentationScore(gpuPod, plain); got != 0 {
		t.Fatalf("a GPU pod cannot fit a node without GPUs, want 0, got %v", got)
	}
}

func TestScoreStaysWithinFrameworkBounds(t *testing.T) {
	nodes := []Node{spot("s", 0.01, 0.9), onDemand("o", 1.00), onDemand("free", 0)}
	costs := CostRangeOf(nodes)
	for _, n := range nodes {
		for _, c := range []WorkloadClass{ClassLatencySensitive, ClassStandard, ClassBatch} {
			for _, req := range []Resources{{}, {MilliCPU: 4000, MemoryBytes: 8 * gi}, {MilliCPU: 9000}} {
				s := Score(Pod{Class: c, Requests: req}, n, costs, DefaultWeights())
				if s < 0 || s > MaxScore {
					t.Fatalf("score %d out of [0,%d] for %s/%s/%+v", s, MaxScore, n.Name, c, req)
				}
			}
		}
	}
}

func TestWeightsValidation(t *testing.T) {
	if err := DefaultWeights().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, w := range []Weights{{50, 30, 10}, {-10, 60, 50}, {100, 0, 1}} {
		if err := w.Validate(); err == nil {
			t.Errorf("%+v must be rejected", w)
		}
	}
	if err := (Weights{Cost: 100}).Validate(); err != nil {
		t.Errorf("a single 100 weight is valid: %v", err)
	}
}

func BenchmarkScore(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		nodes := make([]Node, n)
		for i := range nodes {
			if i%2 == 0 {
				nodes[i] = spot(fmt.Sprintf("spot-%d", i), 0.03, 0.2)
			} else {
				nodes[i] = onDemand(fmt.Sprintf("od-%d", i), 0.10)
			}
			nodes[i].Requested = Resources{MilliCPU: int64(i % 4000), MemoryBytes: gi}
		}
		p := pod(ClassBatch, 500, gi)
		costs := CostRangeOf(nodes)
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			for b.Loop() {
				for i := range nodes {
					if Feasible(p, nodes[i]) {
						_ = Score(p, nodes[i], costs, DefaultWeights())
					}
				}
			}
		})
	}
}
