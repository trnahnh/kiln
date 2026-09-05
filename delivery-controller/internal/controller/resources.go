package controller

import (
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	platformv1 "github.com/trnahnh/kiln/delivery-controller/api/v1"
)

func primaryName(target string) string { return target + "-primary" }
func canaryServiceName(target string) string {
	return target + "-canary"
}

// templateHash identifies the version under rollout. The controller's own labels are
// stripped first so adding them never reads as a new version.
func templateHash(t corev1.PodTemplateSpec) string {
	t = *t.DeepCopy()
	delete(t.Labels, platformv1.LabelRole)
	delete(t.Labels, platformv1.LabelRolloutRef)
	b, _ := json.Marshal(t)
	h := fnv.New64a()
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func withRole(labels map[string]string, rollout, role string) map[string]string {
	out := maps.Clone(labels)
	if out == nil {
		out = map[string]string{}
	}
	out[platformv1.LabelRole] = role
	out[platformv1.LabelRolloutRef] = rollout
	return out
}

func hasRole(labels map[string]string, rollout, role string) bool {
	return labels[platformv1.LabelRole] == role && labels[platformv1.LabelRolloutRef] == rollout
}

// primaryFrom clones the target into the primary Deployment: same spec, its own selector
// and role label, replicas restored from what the target had before it was parked at zero.
func primaryFrom(target *appsv1.Deployment, rollout string, replicas int32) *appsv1.Deployment {
	p := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        primaryName(target.Name),
			Namespace:   target.Namespace,
			Labels:      withRole(target.Labels, rollout, platformv1.RolePrimary),
			Annotations: maps.Clone(target.Annotations),
		},
		Spec: *target.Spec.DeepCopy(),
	}
	applyPrimaryTemplate(p, target, rollout, replicas)
	p.Spec.Selector = &metav1.LabelSelector{MatchLabels: withRole(target.Spec.Selector.MatchLabels, rollout, platformv1.RolePrimary)}
	return p
}

func applyPrimaryTemplate(primary, target *appsv1.Deployment, rollout string, replicas int32) {
	selector := primary.Spec.Selector
	primary.Spec = *target.Spec.DeepCopy()
	primary.Spec.Selector = selector
	primary.Spec.Replicas = ptr.To(replicas)
	primary.Spec.Template.Labels = withRole(target.Spec.Template.Labels, rollout, platformv1.RolePrimary)
	primary.Spec.Paused = false
}

func roleService(name string, base *corev1.Service, target *appsv1.Deployment, rollout, role string) *corev1.Service {
	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: base.Namespace,
			Labels:    withRole(base.Labels, rollout, role),
		},
	}
	applyRoleServiceSpec(s, base, target, rollout, role)
	return s
}

func applyRoleServiceSpec(s, base *corev1.Service, target *appsv1.Deployment, rollout, role string) {
	s.Spec.Selector = withRole(target.Spec.Selector.MatchLabels, rollout, role)
	s.Spec.Ports = nil
	for _, p := range base.Spec.Ports {
		p.NodePort = 0
		s.Spec.Ports = append(s.Spec.Ports, p)
	}
	s.Spec.Type = corev1.ServiceTypeClusterIP
}

func rolledOut(d *appsv1.Deployment) bool {
	if d.Spec.Replicas == nil || d.Status.ObservedGeneration < d.Generation {
		return false
	}
	n := *d.Spec.Replicas
	return d.Status.Replicas == n && d.Status.UpdatedReplicas == n && d.Status.AvailableReplicas == n
}
