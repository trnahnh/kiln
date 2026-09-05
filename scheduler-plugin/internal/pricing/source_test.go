package pricing

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/trnahnh/kiln/scheduler-plugin/internal/scoring"
)

func node(labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n", Labels: labels}}
}

func TestNodeLabelsReadsTheContract(t *testing.T) {
	e, ok := NodeLabels{}.Economics(node(map[string]string{
		LabelCapacityType: "spot", LabelHourlyCost: "0.03", LabelPreemptionRisk: "0.2",
	}))
	if !ok || e != (scoring.Economics{HourlyCost: 0.03, Spot: true, PreemptionRisk: 0.2}) {
		t.Fatalf("got %+v ok=%v", e, ok)
	}
	e, ok = NodeLabels{}.Economics(node(map[string]string{LabelCapacityType: "on-demand", LabelHourlyCost: "0.10"}))
	if !ok || e != (scoring.Economics{HourlyCost: 0.10}) {
		t.Fatalf("on-demand: got %+v ok=%v", e, ok)
	}
}

func TestNodeLabelsDefaultsSpotRiskAndIgnoresRiskOnDemand(t *testing.T) {
	e, ok := NodeLabels{}.Economics(node(map[string]string{LabelCapacityType: "spot", LabelHourlyCost: "0.03"}))
	if !ok || e.PreemptionRisk != DefaultSpotRisk {
		t.Fatalf("spot without a risk label gets the default, got %+v", e)
	}
	e, ok = NodeLabels{}.Economics(node(map[string]string{LabelCapacityType: "on-demand", LabelHourlyCost: "0.10", LabelPreemptionRisk: "0.9"}))
	if !ok || e.PreemptionRisk != 0 {
		t.Fatalf("on-demand capacity has no reclaim risk whatever the label says, got %+v", e)
	}
}

func TestNodeLabelsRejectsMissingOrMalformedFacts(t *testing.T) {
	cases := []map[string]string{
		{},
		{LabelHourlyCost: "0.10"},
		{LabelCapacityType: "reserved", LabelHourlyCost: "0.10"},
		{LabelCapacityType: "spot"},
		{LabelCapacityType: "spot", LabelHourlyCost: "cheap"},
		{LabelCapacityType: "spot", LabelHourlyCost: "-1"},
		{LabelCapacityType: "spot", LabelHourlyCost: "0.03", LabelPreemptionRisk: "1.5"},
	}
	for _, labels := range cases {
		if _, ok := (NodeLabels{}).Economics(node(labels)); ok {
			t.Errorf("labels %v must not resolve", labels)
		}
	}
}

func TestFallbackIsTheMostExpensiveOnDemand(t *testing.T) {
	known := []scoring.Economics{{HourlyCost: 0.03, Spot: true}, {HourlyCost: 0.10}, {HourlyCost: 0.07}}
	got := Fallback(known)
	if got != (scoring.Economics{HourlyCost: 0.10}) {
		t.Fatalf("got %+v", got)
	}
	none := Fallback(nil)
	if none != (scoring.Economics{}) {
		t.Fatalf("no known nodes: got %+v", none)
	}
}

type fixed struct {
	e  scoring.Economics
	ok bool
}

func (f fixed) Economics(*corev1.Node) (scoring.Economics, bool) { return f.e, f.ok }

func TestChainReturnsFirstAnswer(t *testing.T) {
	c := Chain{fixed{ok: false}, fixed{e: scoring.Economics{HourlyCost: 0.5}, ok: true}, fixed{e: scoring.Economics{HourlyCost: 9}, ok: true}}
	e, ok := c.Economics(node(nil))
	if !ok || e.HourlyCost != 0.5 {
		t.Fatalf("got %+v ok=%v", e, ok)
	}
	if _, ok := (Chain{fixed{}}).Economics(node(nil)); ok {
		t.Fatal("a chain with no answers must report !ok")
	}
}
