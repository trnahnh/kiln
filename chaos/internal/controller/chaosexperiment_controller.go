package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/trnahnh/kiln/audit"
	platformv1 "github.com/trnahnh/kiln/chaos/api/v1"
	"github.com/trnahnh/kiln/chaos/internal/agent"
	"github.com/trnahnh/kiln/chaos/internal/analysis"
	"github.com/trnahnh/kiln/slo"
)

// The lease outlives a few analysis ticks so a single slow reconcile does not lapse it;
// the agents revert within a second of it lapsing for real.
func leaseTTL(interval time.Duration) time.Duration {
	if ttl := 3 * interval; ttl > 15*time.Second {
		return ttl
	}
	return 15 * time.Second
}

// +kubebuilder:rbac:groups=platform.internal,resources=chaosexperiments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=platform.internal,resources=chaosexperiments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.internal,resources=chaosexperiments/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

type Reconciler struct {
	client.Client
	Recorder record.EventRecorder
	Metrics  slo.Source
	Now      func() time.Time
	// LeaseTTL overrides the computed lease lifetime; tests set it short.
	LeaseTTL time.Duration
	Audit    audit.Publisher
}

func (r *Reconciler) lease(interval time.Duration) time.Duration {
	if r.LeaseTTL > 0 {
		return r.LeaseTTL
	}
	return leaseTTL(interval)
}

func (r *Reconciler) now() metav1.Time {
	if r.Now != nil {
		return metav1.NewTime(r.Now())
	}
	return metav1.Now()
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cr := &platformv1.ChaosExperiment{}
	if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := cr.DeepCopy()
	res, err := r.reconcile(ctx, cr)
	cr.Status.ObservedGeneration = cr.Generation
	if patchErr := r.Status().Patch(ctx, cr, client.MergeFrom(before)); patchErr != nil && err == nil {
		err = fmt.Errorf("updating status: %w", patchErr)
	}
	return res, err
}

func (r *Reconciler) reconcile(ctx context.Context, cr *platformv1.ChaosExperiment) (ctrl.Result, error) {
	if cr.DeletionTimestamp != nil {
		return r.finalize(ctx, cr)
	}
	if err := r.ensureFinalizer(ctx, cr); err != nil {
		return ctrl.Result{}, err
	}

	switch cr.Status.Phase {
	case platformv1.PhaseAborted, platformv1.PhaseCompleted:
		return r.settle(ctx, cr)
	}

	if reason, msg, ok := r.validate(cr); !ok {
		r.setReady(cr, false, reason, msg)
		if cr.Status.Phase == "" {
			cr.Status.Phase = platformv1.PhaseScheduled
		}
		return ctrl.Result{}, nil
	}

	matching, err := r.matchingPods(ctx, cr)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch cr.Status.Phase {
	case "", platformv1.PhaseScheduled:
		return r.schedule(ctx, cr, matching)
	case platformv1.PhaseRunning:
		return r.run(ctx, cr, matching)
	}
	return ctrl.Result{}, nil
}

// validate rejects what can be judged from the spec alone; blast-radius and CPU-limit
// checks that need live pods happen during scheduling.
func (r *Reconciler) validate(cr *platformv1.ChaosExperiment) (reason, msg string, ok bool) {
	if ns := cr.Spec.Target.Namespace; ns != "" && ns != cr.Namespace {
		return platformv1.ReasonInvalidSpec, fmt.Sprintf("target.namespace %q must equal the experiment's namespace %q", ns, cr.Namespace), false
	}
	if _, err := labels.Parse(cr.Spec.Target.LabelSelector); err != nil {
		return platformv1.ReasonInvalidSpec, fmt.Sprintf("labelSelector: %v", err), false
	}
	if cr.Spec.Duration.Duration <= 0 {
		return platformv1.ReasonInvalidSpec, "duration must be positive", false
	}
	return "", "", true
}

func (r *Reconciler) schedule(ctx context.Context, cr *platformv1.ChaosExperiment, matching []corev1.Pod) (ctrl.Result, error) {
	cr.Status.Phase = platformv1.PhaseScheduled
	interval := cr.Spec.AnalysisInterval()

	if len(matching) == 0 {
		r.setReady(cr, false, platformv1.ReasonWaiting, "no ready pods match the selector yet")
		return ctrl.Result{RequeueAfter: interval}, nil
	}
	allowed := agent.Allowed(cr.Spec.Target.MaxReplicaPercentage, len(matching))
	if allowed == 0 {
		r.setReady(cr, false, platformv1.ReasonInvalidSpec, fmt.Sprintf("maxReplicaPercentage %d%% of %d matching pods floors to zero pods", cr.Spec.Target.MaxReplicaPercentage, len(matching)))
		return ctrl.Result{}, nil
	}
	targets, reason, msg, ok := selectTargets(cr, matching, allowed)
	if !ok {
		r.setReady(cr, false, reason, msg)
		return ctrl.Result{}, nil
	}
	if blocking, err := r.overlappingExperiment(ctx, cr, targets); err != nil {
		return ctrl.Result{}, err
	} else if blocking != "" {
		r.setReady(cr, false, platformv1.ReasonWaiting, fmt.Sprintf("waiting for experiment %q to finish with an overlapping pod set", blocking))
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	// Fail closed: no injection until a baseline SLO snapshot has been read.
	cur, err := r.Metrics.Counters(ctx, r.metricTarget(cr, matching))
	if err != nil {
		r.setReady(cr, false, platformv1.ReasonMetricsUnavailable, fmt.Sprintf("waiting for a baseline metric snapshot: %v", err))
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	now := r.now()
	cr.Status.Phase = platformv1.PhaseRunning
	cr.Status.Reason = platformv1.ReasonRunning
	cr.Status.StartedAt = &now
	cr.Status.FaultEndsAt = ptr.To(metav1.NewTime(now.Add(cr.Spec.Duration.Duration)))
	cr.Status.LeaseExpiresAt = ptr.To(metav1.NewTime(now.Add(r.lease(interval))))
	cr.Status.Targets = targets
	cr.Status.Analysis = &platformv1.AnalysisState{
		LastCounters:   &platformv1.CounterSnapshot{Requests: cur.Requests, Errors: cur.Errors, Slow: cur.Slow, At: now},
		LastWindowAt:   &now,
		RecoveredAfter: nil,
	}
	r.setReady(cr, true, platformv1.ReasonRunning, "fault injection started")
	r.event(cr, corev1.EventTypeNormal, platformv1.ReasonRunning, fmt.Sprintf("%s on %d pod(s) for %s", cr.Spec.FaultType, len(targets), cr.Spec.Duration.Duration))
	r.publish(cr, map[string]any{"outcome": "Started", "faultType": string(cr.Spec.FaultType), "targets": len(targets)}, "Started", string(cr.UID))
	return ctrl.Result{RequeueAfter: interval}, nil
}

func (r *Reconciler) run(ctx context.Context, cr *platformv1.ChaosExperiment, matching []corev1.Pod) (ctrl.Result, error) {
	interval := cr.Spec.AnalysisInterval()
	now := r.now()

	// An agent could not apply the fault; the run tested nothing, so abort rather than score
	// it as a clean pass.
	if msg := cr.Annotations[platformv1.AnnotationInjectionError]; msg != "" {
		r.event(cr, corev1.EventTypeWarning, platformv1.ReasonInjectionFailed, msg)
		return r.stop(ctx, cr, platformv1.PhaseAborted, platformv1.ReasonInjectionFailed, now)
	}

	allowed := agent.Allowed(cr.Spec.Target.MaxReplicaPercentage, len(matching))
	targets, _, _, ok := selectTargets(cr, matching, allowed)
	if ok {
		cr.Status.Targets = targets
	}
	// Renew the lease so the agents keep the faults; if this controller stops here, the
	// lease lapses and they revert.
	cr.Status.LeaseExpiresAt = ptr.To(metav1.NewTime(now.Add(r.lease(interval))))

	faultOver := !now.Time.Before(cr.Status.FaultEndsAt.Time)
	stage := analysis.StageFault
	if faultOver {
		stage = analysis.StageRecovery
	}

	if cr.Spec.FaultType == platformv1.FaultPodKill && !faultOver {
		if err := r.maybeKill(ctx, cr, matching, allowed, now); err != nil {
			return ctrl.Result{}, err
		}
	}

	st := stateFromStatus(cr.Status.Analysis)
	prev := cr.Status.Analysis.LastCounters
	cur, readErr := r.Metrics.Counters(ctx, r.metricTarget(cr, matching))
	ok = readErr == nil
	var window analysis.Window
	if ok {
		d := slo.Delta(snapshot(prev), cur)
		window = analysis.Window{Requests: d.Requests, Errors: d.Errors, Slow: d.Slow}
	}
	dec := analysis.Tick(r.cfg(cr), &st, now.Time, stage, window, ok)
	if dec.Judged && ok {
		st.LastWindowAt = now.Time
		cr.Status.Analysis.LastCounters = &platformv1.CounterSnapshot{Requests: cur.Requests, Errors: cur.Errors, Slow: cur.Slow, At: now}
	}
	writeState(cr, st, now)

	switch dec.Action {
	case analysis.Abort:
		return r.stop(ctx, cr, platformv1.PhaseAborted, string(dec.Reason), now)
	case analysis.Complete:
		return r.stop(ctx, cr, platformv1.PhaseCompleted, string(dec.Reason), now)
	}
	if faultOver {
		cr.Status.Reason = platformv1.ReasonRecovering
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// stop ends the fault: it stops renewing the lease (setting it to now so agents revert at
// once), records the terminal phase and score, and hands off to settle to confirm the
// faults are gone before the experiment is done.
func (r *Reconciler) stop(ctx context.Context, cr *platformv1.ChaosExperiment, phase platformv1.Phase, reason string, now metav1.Time) (ctrl.Result, error) {
	cr.Status.Phase = phase
	cr.Status.Reason = reason
	cr.Status.LeaseExpiresAt = &now
	cr.Status.FaultEndsAt = &now
	score := analysis.Score(r.cfg(cr), stateFromStatus(cr.Status.Analysis))
	cr.Status.ResilienceScore = &score
	if phase == platformv1.PhaseAborted {
		cr.Status.AbortReason = reason
		cr.Status.AbortedAt = &now
		zero := 0.0
		cr.Status.ResilienceScore = &zero
		r.event(cr, corev1.EventTypeWarning, platformv1.ReasonSLOBreach, fmt.Sprintf("aborted (%s): traffic-affecting faults reverted", reason))
		r.publish(cr, map[string]any{"outcome": "Aborted", "abortReason": reason, "faultType": string(cr.Spec.FaultType), "resilienceScore": 0.0}, "Aborted", string(cr.UID))
	} else {
		cr.Status.CompletedAt = &now
		r.event(cr, corev1.EventTypeNormal, platformv1.ReasonCompleted, fmt.Sprintf("completed, resilience score %.1f", score))
		r.publish(cr, map[string]any{"outcome": "Completed", "resilienceScore": score, "faultType": string(cr.Spec.FaultType)}, "Completed", string(cr.UID))
	}
	return r.settle(ctx, cr)
}

// settle waits out the lease so every agent has provably reverted, then records that the
// faults are cleared. It never reports cleared before the lease has lapsed.
func (r *Reconciler) settle(ctx context.Context, cr *platformv1.ChaosExperiment) (ctrl.Result, error) {
	now := r.now()
	if cr.Status.LeaseExpiresAt != nil && now.Time.Before(cr.Status.LeaseExpiresAt.Time) {
		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{Type: platformv1.ConditionFaultsCleared, Status: metav1.ConditionFalse, Reason: platformv1.ReasonFaultsLive, Message: "waiting for the fault lease to lapse"})
		return ctrl.Result{RequeueAfter: time.Until(cr.Status.LeaseExpiresAt.Time) + time.Second}, nil
	}
	if cr.Status.FaultEndedAt == nil {
		cr.Status.FaultEndedAt = &now
	}
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{Type: platformv1.ConditionFaultsCleared, Status: metav1.ConditionTrue, Reason: platformv1.ReasonFaultsCleared, Message: "the fault lease has lapsed; every agent has reverted"})
	return ctrl.Result{}, nil
}

func (r *Reconciler) finalize(ctx context.Context, cr *platformv1.ChaosExperiment) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cr, platformv1.Finalizer) {
		return ctrl.Result{}, nil
	}
	// Stop renewing so the agents revert, and wait out the lease before letting the object go.
	now := r.now()
	if cr.Status.LeaseExpiresAt == nil || cr.Status.LeaseExpiresAt.Time.After(now.Time) {
		cr.Status.LeaseExpiresAt = &now
	}
	if now.Time.Before(cr.Status.LeaseExpiresAt.Time) {
		return ctrl.Result{RequeueAfter: time.Until(cr.Status.LeaseExpiresAt.Time) + time.Second}, nil
	}
	patched := cr.DeepCopy()
	controllerutil.RemoveFinalizer(patched, platformv1.Finalizer)
	if err := r.Patch(ctx, patched, client.MergeFrom(cr)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) ensureFinalizer(ctx context.Context, cr *platformv1.ChaosExperiment) error {
	if controllerutil.ContainsFinalizer(cr, platformv1.Finalizer) {
		return nil
	}
	patched := cr.DeepCopy()
	controllerutil.AddFinalizer(patched, platformv1.Finalizer)
	if err := r.Patch(ctx, patched, client.MergeFrom(cr)); err != nil {
		return err
	}
	cr.Finalizers = patched.Finalizers
	return nil
}

func (r *Reconciler) maybeKill(ctx context.Context, cr *platformv1.ChaosExperiment, matching []corev1.Pod, allowed int, now metav1.Time) error {
	if cr.Status.LastKillAt != nil && now.Sub(cr.Status.LastKillAt.Time) < cr.Spec.KillInterval() {
		return nil
	}
	victims := pickPods(cr, matching, allowed)
	for i := range victims {
		p := victims[i]
		if err := r.Delete(ctx, &p, client.GracePeriodSeconds(0)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("killing pod %s: %w", p.Name, err)
		}
		cr.Status.Kills++
	}
	cr.Status.LastKillAt = &now
	if len(victims) > 0 {
		r.event(cr, corev1.EventTypeNormal, platformv1.ReasonInjecting, fmt.Sprintf("killed %d pod(s)", len(victims)))
	}
	return nil
}

func (r *Reconciler) matchingPods(ctx context.Context, cr *platformv1.ChaosExperiment) ([]corev1.Pod, error) {
	sel, err := labels.Parse(cr.Spec.Target.LabelSelector)
	if err != nil {
		return nil, err
	}
	list := &corev1.PodList{}
	if err := r.List(ctx, list, client.InNamespace(cr.Namespace), client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return nil, err
	}
	var out []corev1.Pod
	for i := range list.Items {
		if podReady(&list.Items[i]) {
			out = append(out, list.Items[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r *Reconciler) overlappingExperiment(ctx context.Context, cr *platformv1.ChaosExperiment, targets []platformv1.TargetStatus) (string, error) {
	list := &platformv1.ChaosExperimentList{}
	if err := r.List(ctx, list, client.InNamespace(cr.Namespace)); err != nil {
		return "", err
	}
	mine := map[string]bool{}
	for _, t := range targets {
		mine[t.Pod] = true
	}
	for i := range list.Items {
		other := &list.Items[i]
		if other.Name == cr.Name || other.Status.Phase != platformv1.PhaseRunning {
			continue
		}
		for _, t := range other.Status.Targets {
			if mine[t.Pod] {
				return other.Name, nil
			}
		}
	}
	return "", nil
}

func (r *Reconciler) cfg(cr *platformv1.ChaosExperiment) analysis.Config {
	return analysis.Config{
		ErrorRateMax:    cr.Spec.AbortOnSLOBreach.ErrorRateMax,
		MinSampleSize:   cr.Spec.MinSampleSize(),
		RecoveryWindows: cr.Spec.RecoveryWindows(),
		MetricsTimeout:  platformv1.MetricsTimeout,
	}
}

func (r *Reconciler) metricTarget(cr *platformv1.ChaosExperiment, matching []corev1.Pod) slo.Target {
	return slo.Target{
		Namespace:    cr.Namespace,
		Workload:     workloadName(cr, matching),
		LatencyMaxMs: float64(cr.Spec.AbortOnSLOBreach.LatencyP99MaxMs),
		Reporter:     slo.ReporterSource,
	}
}

func (r *Reconciler) setReady(cr *platformv1.ChaosExperiment, ok bool, reason, msg string) {
	cr.Status.Reason = reason
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{Type: platformv1.ConditionReady, Status: status, Reason: reason, Message: msg})
}

func (r *Reconciler) event(cr *platformv1.ChaosExperiment, kind, reason, msg string) {
	if r.Recorder != nil {
		r.Recorder.Event(cr, kind, reason, msg)
	}
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1.ChaosExperiment{}).
		Named("chaos-controller").
		Complete(r)
}
