package agent

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	platformv1 "github.com/trnahnh/kiln/chaos/api/v1"
)

const proxyContainer = "istio-proxy"

func podReady(p *corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// allowFrom keeps the pod reachable from the node (kubelet probes, exec) and from its own
// sidecar's loopback while it is otherwise partitioned.
func allowFrom(nodeIP string, p *corev1.Pod) []string {
	var out []string
	if nodeIP != "" {
		out = append(out, nodeIP)
	}
	if p.Status.HostIP != "" && p.Status.HostIP != nodeIP {
		out = append(out, p.Status.HostIP)
	}
	return out
}

// containerName is the app container a resource-exhaustion burner joins: the one the
// controller named, else the first non-sidecar container.
func containerName(cr *platformv1.ChaosExperiment, p *corev1.Pod) string {
	for _, t := range cr.Status.Targets {
		if t.Pod == p.Name && t.Container != "" {
			return t.Container
		}
	}
	for _, c := range p.Spec.Containers {
		if c.Name != proxyContainer {
			return c.Name
		}
	}
	return ""
}

// containerID returns the runtime id of the named container, or of any running app
// container when the name is empty (network faults act on the shared pod namespace, so any
// one will do).
func containerID(p *corev1.Pod, name string) string {
	fallback := ""
	for _, cs := range p.Status.ContainerStatuses {
		if cs.ContainerID == "" || cs.State.Running == nil {
			continue
		}
		if cs.Name == name {
			return cs.ContainerID
		}
		if cs.Name != proxyContainer && fallback == "" {
			fallback = cs.ContainerID
		}
	}
	if name == "" {
		return fallback
	}
	return ""
}

// podToExperiments wakes every experiment in a changed pod's namespace, but only for pods
// on this node, so a pod coming or going is reflected without a full resync.
func podToExperiments(c client.Client, nodeName string) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		p, ok := obj.(*corev1.Pod)
		if !ok || p.Spec.NodeName != nodeName {
			return nil
		}
		list := &platformv1.ChaosExperimentList{}
		if err := c.List(ctx, list, client.InNamespace(p.Namespace)); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(list.Items))
		for i := range list.Items {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: list.Items[i].Namespace, Name: list.Items[i].Name}})
		}
		return out
	})
}

// sweeper is the hard backstop: independently of any CR event it reverts every fault whose
// lease deadline has passed, so a dead or partitioned controller cannot leave a fault live.
type sweeper struct {
	reconciler *Reconciler
	interval   time.Duration
}

func (s *sweeper) Start(ctx context.Context) error {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s.sweep(ctx)
		}
	}
}

func (s *sweeper) sweep(ctx context.Context) {
	entries, err := s.reconciler.Ledger.List()
	if err != nil {
		return
	}
	now := s.reconciler.now()
	for _, e := range entries {
		if now.After(e.Deadline) {
			_ = s.reconciler.revert(ctx, e)
		}
	}
}
