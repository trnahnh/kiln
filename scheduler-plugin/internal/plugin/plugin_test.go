package plugin

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/trnahnh/kiln/scheduler-plugin/internal/pricing"
	"github.com/trnahnh/kiln/scheduler-plugin/internal/scoring"
)

func nodeInfo(name string, labels map[string]string, used ...*v1.Pod) fwk.NodeInfo {
	n := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: v1.NodeStatus{Allocatable: v1.ResourceList{
			v1.ResourceCPU:    resource.MustParse("4"),
			v1.ResourceMemory: resource.MustParse("8Gi"),
			v1.ResourcePods:   resource.MustParse("110"),
		}},
	}
	info := framework.NewNodeInfo(used...)
	info.SetNode(n)
	return info
}

func spotLabels(cost string) map[string]string {
	return map[string]string{pricing.LabelCapacityType: "spot", pricing.LabelHourlyCost: cost, pricing.LabelPreemptionRisk: "0.2"}
}

func onDemandLabels(cost string) map[string]string {
	return map[string]string{pricing.LabelCapacityType: "on-demand", pricing.LabelHourlyCost: cost}
}

func testPod(class string, cpu, mem string) *v1.Pod {
	p := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
		Spec: v1.PodSpec{Containers: []v1.Container{{Name: "c", Resources: v1.ResourceRequirements{
			Requests: v1.ResourceList{v1.ResourceCPU: resource.MustParse(cpu), v1.ResourceMemory: resource.MustParse(mem)},
		}}}},
	}
	if class != "" {
		p.Labels = map[string]string{LabelWorkloadClass: class}
	}
	return p
}

func scoreAll(t *testing.T, p *CostAware, pod *v1.Pod, nodes ...fwk.NodeInfo) map[string]int64 {
	t.Helper()
	state := framework.NewCycleState()
	var feasible []fwk.NodeInfo
	for _, n := range nodes {
		if st := p.Filter(context.Background(), state, pod, n); st.IsSuccess() {
			feasible = append(feasible, n)
		}
	}
	if st := p.PreScore(context.Background(), state, pod, feasible); !st.IsSuccess() {
		t.Fatalf("PreScore: %v", st)
	}
	out := map[string]int64{}
	for _, n := range feasible {
		s, st := p.Score(context.Background(), state, pod, n)
		if !st.IsSuccess() {
			t.Fatalf("Score %s: %v", n.Node().Name, st)
		}
		out[n.Node().Name] = s
	}
	return out
}

func TestNewRejectsBadWeightsAndDefaultsWhenUnset(t *testing.T) {
	if _, err := New(context.Background(), nil, nil); err != nil {
		t.Fatalf("nil args must use the defaults: %v", err)
	}
	bad := &runtime.Unknown{Raw: []byte(`{"weights":{"cost":90,"fragmentation":30,"preemption":20}}`), ContentType: runtime.ContentTypeJSON}
	if _, err := New(context.Background(), bad, nil); err == nil {
		t.Fatal("weights summing to 140 must be rejected")
	}
	good := &runtime.Unknown{Raw: []byte(`{"weights":{"cost":70,"fragmentation":20,"preemption":10}}`), ContentType: runtime.ContentTypeJSON}
	p, err := New(context.Background(), good, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.(*CostAware).weights != (scoring.Weights{Cost: 70, Fragmentation: 20, Preemption: 10}) {
		t.Fatalf("weights not applied: %+v", p.(*CostAware).weights)
	}
}

func TestFilterKeepsLatencySensitiveAndUnlabelledPodsOffSpot(t *testing.T) {
	p := NewWithSource(pricing.NodeLabels{}, scoring.DefaultWeights())
	spot := nodeInfo("spot", spotLabels("0.03"))
	od := nodeInfo("od", onDemandLabels("0.10"))
	for _, class := range []string{"", "latency-sensitive", "typo"} {
		pod := testPod(class, "500m", "1Gi")
		if st := p.Filter(context.Background(), framework.NewCycleState(), pod, spot); st.Code() != fwk.UnschedulableAndUnresolvable {
			t.Errorf("class %q on spot: got %v, want UnschedulableAndUnresolvable", class, st)
		}
		if st := p.Filter(context.Background(), framework.NewCycleState(), pod, od); !st.IsSuccess() {
			t.Errorf("class %q on on-demand: got %v, want success", class, st)
		}
	}
	for _, class := range []string{"standard", "batch"} {
		if st := p.Filter(context.Background(), framework.NewCycleState(), testPod(class, "500m", "1Gi"), spot); !st.IsSuccess() {
			t.Errorf("class %q on spot: got %v, want success", class, st)
		}
	}
}

func TestUnknownNodeIsTreatedAsOnDemand(t *testing.T) {
	p := NewWithSource(pricing.NodeLabels{}, scoring.DefaultWeights())
	unknown := nodeInfo("mystery", nil)
	if st := p.Filter(context.Background(), framework.NewCycleState(), testPod("", "500m", "1Gi"), unknown); !st.IsSuccess() {
		t.Fatalf("a node with no economics must stay feasible for latency-sensitive pods: %v", st)
	}
	scores := scoreAll(t, p, testPod("batch", "500m", "1Gi"), nodeInfo("spot", spotLabels("0.03")), unknown, nodeInfo("od", onDemandLabels("0.10")))
	if scores["mystery"] != scores["od"] {
		t.Fatalf("unknown node must score like the dearest on-demand node: %v", scores)
	}
}

func TestBatchPodPrefersCheapSpotAndLatencyPodPacksOnDemand(t *testing.T) {
	p := NewWithSource(pricing.NodeLabels{}, scoring.DefaultWeights())
	spotA := nodeInfo("spot-a", spotLabels("0.03"))
	spotB := nodeInfo("spot-b", spotLabels("0.03"), testPod("batch", "2", "4Gi"))
	odA := nodeInfo("od-a", onDemandLabels("0.10"))
	odB := nodeInfo("od-b", onDemandLabels("0.10"), testPod("latency-sensitive", "2", "4Gi"))

	batch := scoreAll(t, p, testPod("batch", "500m", "1Gi"), spotA, spotB, odA, odB)
	if batch["spot-b"] <= batch["spot-a"] || batch["spot-a"] <= batch["od-a"] {
		t.Fatalf("batch pod: fuller spot > empty spot > on-demand, got %v", batch)
	}

	latency := scoreAll(t, p, testPod("latency-sensitive", "500m", "1Gi"), spotA, spotB, odA, odB)
	if _, ok := latency["spot-a"]; ok {
		t.Fatalf("latency-sensitive pod must never be scored on spot: %v", latency)
	}
	if latency["od-b"] <= latency["od-a"] {
		t.Fatalf("latency-sensitive pod should pack onto the fuller on-demand node, got %v", latency)
	}
	for _, s := range latency {
		if s < 0 || s > scoring.MaxScore {
			t.Fatalf("score out of framework bounds: %v", latency)
		}
	}
}

func TestScoreWithoutPreScoreStillWorks(t *testing.T) {
	p := NewWithSource(pricing.NodeLabels{}, scoring.DefaultWeights())
	s, st := p.Score(context.Background(), framework.NewCycleState(), testPod("batch", "500m", "1Gi"), nodeInfo("spot", spotLabels("0.03")))
	if !st.IsSuccess() || s <= 0 {
		t.Fatalf("got score=%d status=%v", s, st)
	}
}
