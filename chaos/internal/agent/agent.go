package agent

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	platformv1 "github.com/trnahnh/kiln/chaos/api/v1"
	"github.com/trnahnh/kiln/chaos/internal/fault"
)

// +kubebuilder:rbac:groups=platform.internal,resources=chaosexperiments,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconciler enforces one node's share of every experiment. It never writes the CR: the
// controller owns status, and what the agent did is proven from the node.
type Reconciler struct {
	client.Client
	Injector Injector
	Ledger   fault.Ledger
	Recorder record.EventRecorder
	NodeName string
	NodeIP   string
	Now      func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cr := &platformv1.ChaosExperiment{}
	if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
		if apierrors.IsNotFound(err) {
			// The CR is gone; revert anything this node still holds for it.
			return ctrl.Result{}, r.revertExperiment(ctx, req.Namespace, req.Name)
		}
		return ctrl.Result{}, err
	}
	if !r.faultsShouldBeLive(cr) {
		return ctrl.Result{}, r.revertByUID(ctx, string(cr.UID))
	}
	return r.enforce(ctx, cr)
}

// The agent injects the network and cgroup faults; pod-kill is the controller's to do
// through the API, so an agent holds nothing for it.
func (r *Reconciler) faultsShouldBeLive(cr *platformv1.ChaosExperiment) bool {
	if cr.DeletionTimestamp != nil || cr.Status.Phase != platformv1.PhaseRunning {
		return false
	}
	if cr.Spec.FaultType == platformv1.FaultPodKill {
		return false
	}
	now := r.now()
	if cr.Status.LeaseExpiresAt == nil || !now.Before(cr.Status.LeaseExpiresAt.Time) {
		return false
	}
	if cr.Status.FaultEndsAt == nil || !now.Before(cr.Status.FaultEndsAt.Time) {
		return false
	}
	return true
}

func (r *Reconciler) enforce(ctx context.Context, cr *platformv1.ChaosExperiment) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	sel, err := labels.Parse(cr.Spec.Target.LabelSelector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("selector: %w", err)
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(cr.Namespace), client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return ctrl.Result{}, err
	}

	matching := map[string]bool{}
	mine := map[string]*corev1.Pod{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || !podReady(p) {
			continue
		}
		matching[p.Name] = true
		if p.Spec.NodeName == r.NodeName {
			mine[p.Name] = p
		}
	}

	var selected []string
	for _, t := range cr.Status.Targets {
		if t.Node == r.NodeName {
			selected = append(selected, t.Pod)
		}
	}
	scope := InScope(selected, matching, cr.Spec.Target.MaxReplicaPercentage)

	live, err := r.Ledger.List()
	if err != nil {
		return ctrl.Result{}, err
	}
	held := map[string]fault.Entry{}
	for _, e := range live {
		if e.ExperimentUID == string(cr.UID) {
			held[e.Pod] = e
		}
	}

	// Revert anything held that is no longer in scope (pod gone, moved, or over the cap).
	for pod, e := range held {
		if scope[pod] {
			continue
		}
		if err := r.revert(ctx, e); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("reverted an out-of-scope fault", "pod", pod)
	}

	lease := cr.Status.LeaseExpiresAt.Time
	burnUntil := cr.Status.FaultEndsAt.Time
	for pod := range scope {
		if e, ok := held[pod]; ok {
			// Extend the dead-man switch to the renewed lease.
			e.Deadline = lease
			if err := r.Ledger.Put(e); err != nil {
				return ctrl.Result{}, err
			}
			continue
		}
		p := mine[pod]
		if p == nil {
			continue
		}
		req, err := r.request(cr, p, lease, burnUntil)
		if err != nil {
			r.event(cr, corev1.EventTypeWarning, platformv1.ReasonInjectionFailed, fmt.Sprintf("pod %s: %v", pod, err))
			continue
		}
		e, err := r.Injector.Apply(ctx, req)
		if err != nil {
			r.event(cr, corev1.EventTypeWarning, platformv1.ReasonInjectionFailed, fmt.Sprintf("pod %s: %v", pod, err))
			// Tell the controller the fault did not take effect so it aborts rather than
			// scoring a run that never happened; this is on metadata, not status.
			r.reportInjectionError(ctx, cr, fmt.Sprintf("pod %s: %v", pod, err))
			return ctrl.Result{}, err
		}
		if err := r.Ledger.Put(e); err != nil {
			return ctrl.Result{}, err
		}
		r.event(cr, corev1.EventTypeNormal, platformv1.ReasonInjecting, fmt.Sprintf("%s injected on %s", cr.Spec.FaultType, pod))
	}
	// Re-check around the lease so its lapse is noticed even if the controller has stopped
	// sending events; the second-by-second sweeper is the hard backstop.
	return ctrl.Result{RequeueAfter: cr.Spec.AnalysisInterval()}, nil
}

func (r *Reconciler) request(cr *platformv1.ChaosExperiment, p *corev1.Pod, lease, burnUntil time.Time) (Request, error) {
	req := Request{
		Namespace: cr.Namespace, Experiment: cr.Name, ExperimentUID: string(cr.UID),
		Pod: p.Name, PodUID: string(p.UID), FaultType: cr.Spec.FaultType,
		LatencyMs: cr.Spec.LatencyMs(), JitterMs: cr.Spec.JitterMs(),
		CPUPercent: cr.Spec.CPUPercent(), MemoryMiB: cr.Spec.MemoryMiB(),
		AllowFrom: allowFrom(r.NodeIP, p), Deadline: lease, BurnUntil: burnUntil,
	}
	container := containerName(cr, p)
	id := containerID(p, container)
	if id == "" {
		return Request{}, fmt.Errorf("no running container id yet")
	}
	req.ContainerID = id
	return req, nil
}

func (r *Reconciler) revert(ctx context.Context, e fault.Entry) error {
	if err := r.Injector.Revert(ctx, e); err != nil {
		return fmt.Errorf("reverting %s on %s: %w", e.Kind, e.Pod, err)
	}
	return r.Ledger.Delete(e.Key())
}

func (r *Reconciler) revertByUID(ctx context.Context, uid string) error {
	live, err := r.Ledger.List()
	if err != nil {
		return err
	}
	for _, e := range live {
		if e.ExperimentUID == uid {
			if err := r.revert(ctx, e); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Reconciler) revertExperiment(ctx context.Context, namespace, name string) error {
	live, err := r.Ledger.List()
	if err != nil {
		return err
	}
	for _, e := range live {
		if e.Namespace == namespace && e.Experiment == name {
			if err := r.revert(ctx, e); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Reconciler) reportInjectionError(ctx context.Context, cr *platformv1.ChaosExperiment, msg string) {
	if cr.Annotations[platformv1.AnnotationInjectionError] == msg {
		return
	}
	patched := cr.DeepCopy()
	if patched.Annotations == nil {
		patched.Annotations = map[string]string{}
	}
	patched.Annotations[platformv1.AnnotationInjectionError] = msg
	_ = r.Patch(ctx, patched, client.MergeFrom(cr))
}

func (r *Reconciler) event(cr *platformv1.ChaosExperiment, kind, reason, msg string) {
	if r.Recorder != nil {
		r.Recorder.Event(cr, kind, reason, msg)
	}
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.Add(&sweeper{reconciler: r, interval: time.Second}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1.ChaosExperiment{}).
		Watches(&corev1.Pod{}, podToExperiments(mgr.GetClient(), r.NodeName)).
		Named("chaos-agent").
		Complete(r)
}
