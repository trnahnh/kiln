// Package plugin adapts the pure scoring model to the scheduler framework.
package plugin

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/component-base/metrics/legacyregistry"
	resourcehelper "k8s.io/component-helpers/resource"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"

	"github.com/trnahnh/kiln/audit"
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

// Args is the pluginConfig payload; all three weights must be given and sum to 100. Audit
// names the Kafka brokers every binding is published to; empty disables publishing.
type Args struct {
	metav1.TypeMeta `json:",inline"`
	Weights         scoring.Weights `json:"weights"`
	Audit           AuditArgs       `json:"audit"`
}

type AuditArgs struct {
	Brokers []string `json:"brokers"`
	Topic   string   `json:"topic"`
}

const controllerName = "kiln-scheduler"

type CostAware struct {
	source  pricing.Source
	weights scoring.Weights
	audit   audit.Publisher
}

var (
	_ fwk.FilterPlugin      = &CostAware{}
	_ fwk.PreScorePlugin    = &CostAware{}
	_ fwk.ScorePlugin       = &CostAware{}
	_ fwk.PostBindPlugin    = &CostAware{}
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
	publisher, err := newPublisher(args.Audit)
	if err != nil {
		return nil, fmt.Errorf("%s audit: %w", Name, err)
	}
	p := NewWithSource(pricing.NodeLabels{}, args.Weights)
	p.audit = publisher
	return p, nil
}

func NewWithSource(source pricing.Source, weights scoring.Weights) *CostAware {
	return &CostAware{source: source, weights: weights, audit: audit.Discard{}}
}

// WithAudit routes SCHEDULE events to pub; tests pass an audit.Recorder.
func (p *CostAware) WithAudit(pub audit.Publisher) *CostAware {
	p.audit = pub
	return p
}

// newPublisher wires the non-blocking audit producer (ADR-0017). The scheduler's own
// registry is Prometheus-backed, so the publish counters land next to its metrics.
func newPublisher(a AuditArgs) (audit.Publisher, error) {
	if len(a.Brokers) == 0 {
		klog.InfoS("audit publishing disabled: no audit.brokers in the CostAware args")
		return audit.Discard{}, nil
	}
	var registerer prometheus.Registerer
	if r, ok := legacyregistry.DefaultGatherer.(prometheus.Registerer); ok {
		registerer = r
	}
	return audit.NewKafka(audit.Options{
		Brokers:    a.Brokers,
		Topic:      a.Topic,
		Registerer: registerer,
		OnFailure: func(e audit.Event, err error) {
			klog.ErrorS(err, "audit event not published", "action", e.Action, "resource", e.Resource)
		},
	})
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

// PostBind runs only for a pod this scheduler actually bound, so every SCHEDULE event is a
// placement that happened rather than one that was scored.
func (p *CostAware) PostBind(_ context.Context, _ fwk.CycleState, pod *v1.Pod, nodeName string) {
	resource := audit.ResourceRef("Pod", pod.Namespace, pod.Name)
	p.audit.Publish(audit.Event{
		EventID:   audit.DeterministicID(resource, audit.ActionSchedule, string(pod.UID), nodeName),
		Actor:     audit.ActorOf(pod.Annotations, controllerName),
		Action:    audit.ActionSchedule,
		Resource:  resource,
		Timestamp: time.Now(),
		Details: map[string]any{
			"outcome":       "Bound",
			"node":          nodeName,
			"workloadClass": string(scoring.ParseClass(pod.Labels[LabelWorkloadClass])),
		},
	})
}

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
