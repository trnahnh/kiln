package aws

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/trnahnh/kiln/scheduler-plugin/internal/scoring"
)

// FakeEC2 mirrors the response shape of DescribeSpotPriceHistory: a history list whose
// newest entry is the current price. The liveaws test checks this shape against AWS.
type FakeEC2 struct {
	History []types.SpotPrice
	Err     error
	Calls   int
}

func (f *FakeEC2) DescribeSpotPriceHistory(_ context.Context, _ *ec2.DescribeSpotPriceHistoryInput, _ ...func(*ec2.Options)) (*ec2.DescribeSpotPriceHistoryOutput, error) {
	f.Calls++
	if f.Err != nil {
		return nil, f.Err
	}
	return &ec2.DescribeSpotPriceHistoryOutput{SpotPriceHistory: f.History}, nil
}

type fakeAdvisor struct {
	doc   *Advisor
	err   error
	calls int
}

func (f *fakeAdvisor) Fetch(context.Context) (*Advisor, error) {
	f.calls++
	return f.doc, f.err
}

// SampleAdvisorJSON is the documented shape of spot-advisor-data.json, reduced to two types.
const SampleAdvisorJSON = `{
  "ranges": [
    {"index": 0, "label": "<5%", "dots": 0, "max": 5},
    {"index": 1, "label": "5-10%", "dots": 1, "max": 10},
    {"index": 2, "label": "10-15%", "dots": 2, "max": 15},
    {"index": 3, "label": "15-20%", "dots": 3, "max": 20},
    {"index": 4, "label": ">20%", "dots": 4, "max": 100}
  ],
  "spot_advisor": {
    "us-east-1": {
      "Linux": {"m5.large": {"s": 70, "r": 0}, "c5.4xlarge": {"s": 60, "r": 4}}
    }
  }
}`

func advisorDoc(t *testing.T) *Advisor {
	t.Helper()
	a, err := ParseAdvisor(strings.NewReader(SampleAdvisorJSON))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func spotNode(instanceType string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n", Labels: map[string]string{
		LabelEKSCapacityType: "SPOT", LabelInstanceType: instanceType,
		LabelZone: "us-east-1a", LabelRegion: "us-east-1",
	}}}
}

func history(prices ...string) []types.SpotPrice {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	out := make([]types.SpotPrice, len(prices))
	for i, p := range prices {
		ts := base.Add(time.Duration(i) * time.Hour)
		out[i] = types.SpotPrice{SpotPrice: awssdk.String(p), Timestamp: &ts, InstanceType: types.InstanceTypeM5Large}
	}
	return out
}

func newSource(fake *FakeEC2, adv *fakeAdvisor) *Source {
	return &Source{EC2: fake, Advisor: adv, OnDemand: map[string]float64{"m5.large": 0.096}, Now: func() time.Time { return time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC) }}
}

func TestSpotNodeGetsLatestPriceAndAdvisorRisk(t *testing.T) {
	s := newSource(&FakeEC2{History: history("0.0400", "0.0350")}, &fakeAdvisor{doc: advisorDoc(t)})
	e, ok := s.Economics(spotNode("m5.large"))
	if !ok {
		t.Fatal("expected economics")
	}
	if e != (scoring.Economics{HourlyCost: 0.035, Spot: true, PreemptionRisk: 0.05}) {
		t.Fatalf("got %+v", e)
	}
}

func TestHighInterruptionBucketMapsToFullRisk(t *testing.T) {
	s := newSource(&FakeEC2{History: history("0.30")}, &fakeAdvisor{doc: advisorDoc(t)})
	e, ok := s.Economics(spotNode("c5.4xlarge"))
	if !ok || e.PreemptionRisk != 1.0 {
		t.Fatalf("got %+v ok=%v", e, ok)
	}
}

func TestOnDemandNodeUsesTheTableAndNeverCallsEC2(t *testing.T) {
	fake := &FakeEC2{Err: errors.New("must not be called")}
	s := newSource(fake, &fakeAdvisor{err: errors.New("must not be called")})
	n := spotNode("m5.large")
	n.Labels[LabelEKSCapacityType] = "ON_DEMAND"
	e, ok := s.Economics(n)
	if !ok || e != (scoring.Economics{HourlyCost: 0.096}) {
		t.Fatalf("got %+v ok=%v", e, ok)
	}
	if fake.Calls != 0 {
		t.Fatal("on-demand pricing must not touch the spot API")
	}
	n.Labels[LabelInstanceType] = "x9.unknown"
	if _, ok := s.Economics(n); ok {
		t.Fatal("an instance type missing from the table must not resolve")
	}
}

func TestKarpenterLabelsAreAccepted(t *testing.T) {
	s := newSource(&FakeEC2{History: history("0.05")}, &fakeAdvisor{doc: advisorDoc(t)})
	n := spotNode("m5.large")
	delete(n.Labels, LabelEKSCapacityType)
	n.Labels[LabelKarpenterCapacityType] = "spot"
	if e, ok := s.Economics(n); !ok || !e.Spot || e.HourlyCost != 0.05 {
		t.Fatalf("got %+v ok=%v", e, ok)
	}
}

func TestFailuresAndUnknownNodesDoNotResolve(t *testing.T) {
	adv := advisorDoc(t)
	cases := map[string]*Source{
		"ec2 error":         newSource(&FakeEC2{Err: errors.New("throttled")}, &fakeAdvisor{doc: adv}),
		"empty history":     newSource(&FakeEC2{}, &fakeAdvisor{doc: adv}),
		"malformed price":   newSource(&FakeEC2{History: history("free")}, &fakeAdvisor{doc: adv}),
		"advisor error":     newSource(&FakeEC2{History: history("0.04")}, &fakeAdvisor{err: errors.New("503")}),
		"unknown in region": newSource(&FakeEC2{History: history("0.04")}, &fakeAdvisor{doc: adv}),
	}
	for name, s := range cases {
		n := spotNode("m5.large")
		if name == "unknown in region" {
			n.Labels[LabelRegion] = "eu-north-1"
		}
		if _, ok := s.Economics(n); ok {
			t.Errorf("%s: must not resolve", name)
		}
	}
	plain := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{LabelInstanceType: "m5.large"}}}
	if _, ok := newSource(&FakeEC2{}, &fakeAdvisor{}).Economics(plain); ok {
		t.Error("a node with no capacity-type label must not resolve")
	}
}

func TestPricesAndAdvisorAreCachedWithinTTL(t *testing.T) {
	fake := &FakeEC2{History: history("0.04")}
	adv := &fakeAdvisor{doc: advisorDoc(t)}
	s := newSource(fake, adv)
	for i := 0; i < 5; i++ {
		if _, ok := s.Economics(spotNode("m5.large")); !ok {
			t.Fatal("expected economics")
		}
	}
	if fake.Calls != 1 || adv.calls != 1 {
		t.Fatalf("expected one upstream call each within the TTL, got ec2=%d advisor=%d", fake.Calls, adv.calls)
	}
	s.Now = func() time.Time { return time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC) }
	if _, ok := s.Economics(spotNode("m5.large")); !ok {
		t.Fatal("expected economics")
	}
	if fake.Calls != 2 || adv.calls != 2 {
		t.Fatalf("expected a refresh after the TTL, got ec2=%d advisor=%d", fake.Calls, adv.calls)
	}
}

func TestParseAdvisorRejectsEmptyDocuments(t *testing.T) {
	if _, err := ParseAdvisor(strings.NewReader(`{"ranges": [], "spot_advisor": {}}`)); err == nil {
		t.Fatal("empty document must be rejected")
	}
	if _, err := ParseAdvisor(strings.NewReader(`not json`)); err == nil {
		t.Fatal("garbage must be rejected")
	}
}
