// Package plugin adapts the pure scoring model to the scheduler framework.
package plugin

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	resourcehelper "k8s.io/component-helpers/resource"
	fwk "k8s.io/kube-scheduler/framework"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"

	"github.com/trnahnh/kiln/scheduler-plugin/internal/pricing"
	"github.com/trnahnh/kiln/scheduler-plugin/internal/scoring"
)

const (
	Name = "CostAware"

	// Pod label contract (ADR-0009, API_REFERENCE.md).
	LabelWorkloadClass = "kiln.platform.internal/workload-class"

	gpuResource = v1.ResourceName("nvidia.com/gpu")

	stateKey fwk.StateKey = "CostAware"
)

// Args is the pluginConfig payload; all three weights must be given and sum to 100.
type Args struct {
	metav1.TypeMeta `json:",inline"`
	Weights         scoring.Weights `json:"weights"`
}

type CostAware struct {
	source  pricing.Source
	weights scoring.Weights
}

var (
	_ fwk.FilterPlugin      = &CostAware{}
	_ fwk.PreScorePlugin    = &CostAware{}
	_ fwk.ScorePlugin       = &CostAware{}
	_ fwk.EnqueueExtensions = &CostAware{}
)

func New(_ context.Context, obj runtime.Object, _ fwk.Handle) (fwk.Plugin, error) {
	args := Args{Weights: scoring.DefaultWeights()}
	if obj != nil {
		if err := frameworkruntime.DecodeInto(obj, &args); err != nil {
			return nil, fmt.Errorf("decode %s args: %w", Name, err)
		}
	}
	if err := args.Weights.Validate(); err != nil {
		return nil, fmt.Errorf("%s args: %w", Name, err)
	}
	return NewWithSource(pricing.NodeLabels{}, args.Weights), nil
}

func NewWithSource(source pricing.Source, weights scoring.Weights) *CostAware {
	return &CostAware{source: source, weights: weights}
}

func (p *CostAware) Name() string { return Name }

// Filter is the workload-class gate: it runs before any score, so no weighting can place
// a latency-sensitive pod on reclaimable capacity. Unknown nodes count as on-demand.
func (p *CostAware) Filter(_ context.Context, _ fwk.CycleState, pod *v1.Pod, nodeInfo fwk.NodeInfo) *fwk.Status {
	economics, _ := p.source.Economics(nodeInfo.Node())
	if !scoring.Feasible(podModel(pod), scoring.Node{Economics: economics}) {
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, "latency-sensitive workloads are not placed on spot capacity")
	}
	return nil
}

type preScoreState struct {
	costs     scoring.CostRange
	economics map[string]scoring.Economics
	fallback  scoring.Economics
}

func (s *preScoreState) Clone() fwk.StateData { return s }

// PreScore resolves every candidate's economics once so Score can normalise cost across
// the candidate set rather than against a single node.
func (p *CostAware) PreScore(_ context.Context, state fwk.CycleState, _ *v1.Pod, nodes []fwk.NodeInfo) *fwk.Status {
	state.Write(stateKey, p.resolve(nodes))
	return nil
}

func (p *CostAware) resolve(nodes []fwk.NodeInfo) *preScoreState {
	s := &preScoreState{economics: make(map[string]scoring.Economics, len(nodes))}
	var known []scoring.Economics
	for _, n := range nodes {
		if e, ok := p.source.Economics(n.Node()); ok {
			s.economics[n.Node().Name] = e
			known = append(known, e)
		}
	}
	s.fallback = pricing.Fallback(known)
	all := make([]scoring.Node, 0, len(nodes))
	for _, n := range nodes {
		all = append(all, scoring.Node{Economics: s.economicsOf(n.Node().Name)})
	}
	s.costs = scoring.CostRangeOf(all)
	return s
}

func (s *preScoreState) economicsOf(name string) scoring.Economics {
	if e, ok := s.economics[name]; ok {
		return e
	}
	return s.fallback
}

func (p *CostAware) Score(_ context.Context, state fwk.CycleState, pod *v1.Pod, nodeInfo fwk.NodeInfo) (int64, *fwk.Status) {
	var s *preScoreState
	if data, err := state.Read(stateKey); err == nil {
		s, _ = data.(*preScoreState)
	}
	if s == nil {
		s = p.resolve([]fwk.NodeInfo{nodeInfo})
	}
	node := nodeModel(nodeInfo, s.economicsOf(nodeInfo.Node().Name))
	return scoring.Score(podModel(pod), node, s.costs, p.weights), nil
}

func (p *CostAware) ScoreExtensions() fwk.ScoreExtensions { return nil }

// EventsToRegister: a node whose economics labels change may become feasible or cheaper.
func (p *CostAware) EventsToRegister(_ context.Context) ([]fwk.ClusterEventWithHint, error) {
	return []fwk.ClusterEventWithHint{
		{Event: fwk.ClusterEvent{Resource: fwk.Node, ActionType: fwk.Add | fwk.UpdateNodeLabel}},
	}, nil
}

func podModel(pod *v1.Pod) scoring.Pod {
	requests := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
	return scoring.Pod{
		Class: scoring.ParseClass(pod.Labels[LabelWorkloadClass]),
		Requests: scoring.Resources{
			MilliCPU:    requests.Cpu().MilliValue(),
			MemoryBytes: requests.Memory().Value(),
			GPUs:        quantityOf(requests, gpuResource),
		},
	}
}

func nodeModel(nodeInfo fwk.NodeInfo, economics scoring.Economics) scoring.Node {
	alloc := nodeInfo.GetAllocatable()
	req := nodeInfo.GetRequested()
	return scoring.Node{
		Name:        nodeInfo.Node().Name,
		Allocatable: scoring.Resources{MilliCPU: alloc.GetMilliCPU(), MemoryBytes: alloc.GetMemory(), GPUs: alloc.GetScalarResources()[gpuResource]},
		Requested:   scoring.Resources{MilliCPU: req.GetMilliCPU(), MemoryBytes: req.GetMemory(), GPUs: req.GetScalarResources()[gpuResource]},
		Economics:   economics,
	}
}

func quantityOf(list v1.ResourceList, name v1.ResourceName) int64 {
	q, ok := list[name]
	if !ok {
		return 0
	}
	return q.Value()
}
