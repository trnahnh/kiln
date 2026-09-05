// Package mesh moves traffic between the primary and canary Services. The reconciler only
// sees Router; Istio's VirtualService is an implementation detail (ADR-0011).
package mesh

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Route struct {
	Namespace      string
	Host           string
	PrimaryService string
	CanaryService  string
	Labels         map[string]string
	Owner          metav1.OwnerReference
}

type Router interface {
	// Ensure makes the mesh route the host's traffic canaryPercent to the canary Service and
	// the rest to the primary Service, creating the routing object if needed.
	Ensure(ctx context.Context, r Route, canaryPercent int) error
	// Weights reads back what the mesh currently enforces.
	Weights(ctx context.Context, r Route) (primary, canary int, err error)
}

var VirtualServiceGVK = schema.GroupVersionKind{Group: "networking.istio.io", Version: "v1", Kind: "VirtualService"}

type Istio struct {
	Client client.Client
}

func (i *Istio) Ensure(ctx context.Context, r Route, canaryPercent int) error {
	if canaryPercent < 0 || canaryPercent > 100 {
		return fmt.Errorf("canary weight %d out of range", canaryPercent)
	}
	desired := virtualService(r, canaryPercent)
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(VirtualServiceGVK)
	err := i.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: r.Host}, existing)
	switch {
	case apierrors.IsNotFound(err):
		return i.Client.Create(ctx, desired)
	case err != nil:
		return err
	}
	if p, c, err := weightsOf(existing, r); err == nil && p == 100-canaryPercent && c == canaryPercent {
		return nil
	}
	existing.Object["spec"] = desired.Object["spec"]
	existing.SetLabels(r.Labels)
	existing.SetOwnerReferences([]metav1.OwnerReference{r.Owner})
	return i.Client.Update(ctx, existing)
}

func (i *Istio) Weights(ctx context.Context, r Route) (int, int, error) {
	vs := &unstructured.Unstructured{}
	vs.SetGroupVersionKind(VirtualServiceGVK)
	if err := i.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: r.Host}, vs); err != nil {
		return 0, 0, err
	}
	return weightsOf(vs, r)
}

func virtualService(r Route, canaryPercent int) *unstructured.Unstructured {
	vs := &unstructured.Unstructured{}
	vs.SetGroupVersionKind(VirtualServiceGVK)
	vs.SetNamespace(r.Namespace)
	vs.SetName(r.Host)
	vs.SetLabels(r.Labels)
	vs.SetOwnerReferences([]metav1.OwnerReference{r.Owner})
	vs.Object["spec"] = map[string]any{
		"hosts": []any{r.Host},
		"http": []any{map[string]any{
			"route": []any{
				map[string]any{"destination": map[string]any{"host": r.PrimaryService}, "weight": int64(100 - canaryPercent)},
				map[string]any{"destination": map[string]any{"host": r.CanaryService}, "weight": int64(canaryPercent)},
			},
		}},
	}
	return vs
}

func weightsOf(vs *unstructured.Unstructured, r Route) (int, int, error) {
	http, found, err := unstructured.NestedSlice(vs.Object, "spec", "http")
	if err != nil || !found || len(http) == 0 {
		return 0, 0, fmt.Errorf("virtualservice %s/%s has no http routes", vs.GetNamespace(), vs.GetName())
	}
	first, ok := http[0].(map[string]any)
	if !ok {
		return 0, 0, fmt.Errorf("virtualservice %s/%s: malformed http route", vs.GetNamespace(), vs.GetName())
	}
	routes, found, err := unstructured.NestedSlice(first, "route")
	if err != nil || !found {
		return 0, 0, fmt.Errorf("virtualservice %s/%s: missing route destinations", vs.GetNamespace(), vs.GetName())
	}
	weights := map[string]int{}
	for _, item := range routes {
		dest, ok := item.(map[string]any)
		if !ok {
			continue
		}
		host, _, _ := unstructured.NestedString(dest, "destination", "host")
		w, found, _ := unstructured.NestedInt64(dest, "weight")
		if !found && len(routes) == 1 {
			w = 100
		}
		weights[host] += int(w)
	}
	return weights[r.PrimaryService], weights[r.CanaryService], nil
}
