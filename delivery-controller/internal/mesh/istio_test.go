package mesh

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func route() Route {
	return Route{
		Namespace:      "shop",
		Host:           "checkout",
		PrimaryService: "checkout-primary",
		CanaryService:  "checkout-canary",
		Labels:         map[string]string{"platform.internal/canary-rollout": "checkout"},
		Owner:          metav1.OwnerReference{APIVersion: "platform.internal/v1", Kind: "CanaryRollout", Name: "checkout", UID: "u1"},
	}
}

func newIstio() *Istio {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(VirtualServiceGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(VirtualServiceGVK.GroupVersion().WithKind("VirtualServiceList"), &unstructured.UnstructuredList{})
	return &Istio{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
}

func TestEnsureCreatesAWeightedVirtualServiceOwnedByTheRollout(t *testing.T) {
	ctx := context.Background()
	i := newIstio()
	if err := i.Ensure(ctx, route(), 20); err != nil {
		t.Fatal(err)
	}
	vs := &unstructured.Unstructured{}
	vs.SetGroupVersionKind(VirtualServiceGVK)
	if err := i.Client.Get(ctx, types.NamespacedName{Namespace: "shop", Name: "checkout"}, vs); err != nil {
		t.Fatal(err)
	}
	hosts, _, _ := unstructured.NestedStringSlice(vs.Object, "spec", "hosts")
	if len(hosts) != 1 || hosts[0] != "checkout" {
		t.Fatalf("hosts %v", hosts)
	}
	if refs := vs.GetOwnerReferences(); len(refs) != 1 || refs[0].UID != "u1" {
		t.Fatalf("owner refs %v", refs)
	}
	p, c, err := i.Weights(ctx, route())
	if err != nil || p != 80 || c != 20 {
		t.Fatalf("weights %d/%d %v", p, c, err)
	}
}

func TestEnsureUpdatesWeightsInPlace(t *testing.T) {
	ctx := context.Background()
	i := newIstio()
	for _, w := range []int{5, 5, 50, 0} {
		if err := i.Ensure(ctx, route(), w); err != nil {
			t.Fatal(err)
		}
		p, c, err := i.Weights(ctx, route())
		if err != nil || p != 100-w || c != w {
			t.Fatalf("after %d: weights %d/%d %v", w, p, c, err)
		}
	}
}

func TestEnsureRejectsWeightsOutOfRange(t *testing.T) {
	i := newIstio()
	if err := i.Ensure(context.Background(), route(), 101); err == nil {
		t.Fatal("expected an error")
	}
}

func TestWeightsOfSingleDestinationWithoutWeightIsAll(t *testing.T) {
	vs := virtualService(route(), 0)
	vs.Object["spec"].(map[string]any)["http"] = []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": "checkout-primary"}}}}}
	p, c, err := weightsOf(vs, route())
	if err != nil || p != 100 || c != 0 {
		t.Fatalf("weights %d/%d %v", p, c, err)
	}
}
