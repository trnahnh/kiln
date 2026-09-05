package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	platformv1 "github.com/trnahnh/kiln/delivery-controller/api/v1"
	"github.com/trnahnh/kiln/delivery-controller/internal/analysis"
	"github.com/trnahnh/kiln/delivery-controller/internal/mesh"
	"github.com/trnahnh/kiln/delivery-controller/internal/metrics"
)

const (
	defaultInterval        = 15 * time.Second
	defaultMaxStepDuration = 30 * time.Minute
	defaultAlpha           = 0.05
	defaultBeta            = 0.10
	defaultRegression      = 2.0
	defaultDrainGrace      = 10 * time.Second
	waitRequeue            = 5 * time.Second
	idleRequeue            = time.Minute
)

// +kubebuilder:rbac:groups=platform.internal,resources=canaryrollouts,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=platform.internal,resources=canaryrollouts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=networking.istio.io,resources=virtualservices,verbs=get;list;watch;create;update;patch

type CanaryRolloutReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Metrics  metrics.Source
	Router   mesh.Router
	Now      func() time.Time

	// Resource versions each status patch superseded. A reconcile queued by a watch event can
	// run before the cache has the patched object; acting on that stale copy would repeat a
	// terminal transition, so such reads are requeued instead.
	superseded sync.Map
}

func (r *CanaryRolloutReconciler) now() metav1.Time {
	if r.Now != nil {
		return metav1.NewTime(r.Now())
	}
	return metav1.Now()
}

func (r *CanaryRolloutReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cr := &platformv1.CanaryRollout{}
	if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if rv, ok := r.superseded.Load(req.NamespacedName); ok && rv == cr.ResourceVersion {
		return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
	}
	before := cr.DeepCopy()
	res, err := r.reconcile(ctx, cr)
	cr.Status.ObservedGeneration = cr.Generation
	if patchErr := r.Status().Patch(ctx, cr, client.MergeFrom(before)); patchErr != nil {
		if err == nil {
			err = fmt.Errorf("updating status: %w", patchErr)
		}
	} else if cr.ResourceVersion != before.ResourceVersion {
		r.superseded.Store(req.NamespacedName, before.ResourceVersion)
	}
	return res, err
}

func (r *CanaryRolloutReconciler) reconcile(ctx context.Context, cr *platformv1.CanaryRollout) (ctrl.Result, error) {
	cfg, err := configFrom(cr)
	if err != nil {
		r.setReady(cr, false, platformv1.ReasonInvalidSpec, err.Error())
		return ctrl.Result{}, nil
	}

	target := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Spec.TargetDeployment}, target); err != nil {
		if apierrors.IsNotFound(err) {
			r.setReady(cr, false, platformv1.ReasonTargetMissing, fmt.Sprintf("deployment %q not found", cr.Spec.TargetDeployment))
			return ctrl.Result{RequeueAfter: idleRequeue}, nil
		}
		return ctrl.Result{}, err
	}
	if !hasRole(target.Spec.Template.Labels, cr.Name, platformv1.RoleCanary) {
		patched := target.DeepCopy()
		patched.Spec.Template.Labels = withRole(target.Spec.Template.Labels, cr.Name, platformv1.RoleCanary)
		if err := r.Patch(ctx, patched, client.MergeFrom(target)); err != nil {
			return ctrl.Result{}, fmt.Errorf("labelling target pods: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if target.Spec.Replicas != nil && *target.Spec.Replicas > 0 {
		cr.Status.TargetReplicas = ptr.To(*target.Spec.Replicas)
	}
	if cr.Status.TargetReplicas == nil {
		cr.Status.TargetReplicas = ptr.To(int32(1))
	}

	if err := r.ensureServices(ctx, cr, target); err != nil {
		if apierrors.IsNotFound(err) {
			r.setReady(cr, false, platformv1.ReasonServiceMissing, fmt.Sprintf("service %q not found", target.Name))
			return ctrl.Result{RequeueAfter: idleRequeue}, nil
		}
		return ctrl.Result{}, err
	}

	hash := templateHash(target.Spec.Template)
	switch cr.Status.Phase {
	case "", platformv1.PhaseInitializing:
		return r.initialize(ctx, cr, target, hash)
	case platformv1.PhaseSucceeded, platformv1.PhaseRolledBack:
		return r.idle(ctx, cr, cfg, target, hash)
	case platformv1.PhaseProgressing:
		return r.progress(ctx, cr, cfg, target, hash)
	case platformv1.PhaseAnalyzing:
		return r.analyze(ctx, cr, cfg, target, hash)
	case platformv1.PhasePromoting:
		return r.promote(ctx, cr, cfg, target, hash)
	case platformv1.PhaseDraining:
		return r.drain(ctx, cr, cfg, target)
	}
	return ctrl.Result{}, fmt.Errorf("unknown phase %q", cr.Status.Phase)
}

func (r *CanaryRolloutReconciler) initialize(ctx context.Context, cr *platformv1.CanaryRollout, target *appsv1.Deployment, hash string) (ctrl.Result, error) {
	cr.Status.Phase = platformv1.PhaseInitializing
	primary, err := r.ensurePrimary(ctx, cr, target)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !rolledOut(primary) {
		r.setReady(cr, false, platformv1.ReasonInitializing, "waiting for the primary Deployment to roll out")
		return ctrl.Result{RequeueAfter: waitRequeue}, nil
	}
	if err := r.Router.Ensure(ctx, r.route(cr, target), 0); err != nil {
		return ctrl.Result{}, fmt.Errorf("routing all traffic to primary: %w", err)
	}
	if err := r.scaleTarget(ctx, target, 0); err != nil {
		return ctrl.Result{}, err
	}
	cr.Status.PromotedTemplateHash = hash
	cr.Status.ObservedTemplateHash = hash
	r.settle(cr, platformv1.PhaseSucceeded, platformv1.ReasonIdle, platformv1.AnalysisPending)
	return ctrl.Result{}, nil
}

func (r *CanaryRolloutReconciler) idle(ctx context.Context, cr *platformv1.CanaryRollout, cfg rolloutConfig, target *appsv1.Deployment, hash string) (ctrl.Result, error) {
	switch hash {
	case cr.Status.ObservedTemplateHash:
		if err := r.Router.Ensure(ctx, r.route(cr, target), 0); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: idleRequeue}, r.scaleTarget(ctx, target, 0)
	case cr.Status.PromotedTemplateHash:
		// The target was put back to the version primary already runs; nothing to prove.
		cr.Status.ObservedTemplateHash = hash
		r.settle(cr, platformv1.PhaseSucceeded, platformv1.ReasonIdle, cr.Status.LastAnalysisResult)
		return ctrl.Result{}, r.scaleTarget(ctx, target, 0)
	}
	return r.startRollout(ctx, cr, cfg, target, hash)
}

func (r *CanaryRolloutReconciler) startRollout(ctx context.Context, cr *platformv1.CanaryRollout, cfg rolloutConfig, target *appsv1.Deployment, hash string) (ctrl.Result, error) {
	if err := r.Router.Ensure(ctx, r.route(cr, target), 0); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.scaleTarget(ctx, target, *cr.Status.TargetReplicas); err != nil {
		return ctrl.Result{}, err
	}
	now := r.now()
	st := analysis.Start(cfg.Config, now.Time)
	cr.Status.ObservedTemplateHash = hash
	cr.Status.Analysis = stateToStatus(st, nil, now)
	cr.Status.Phase = platformv1.PhaseProgressing
	cr.Status.Reason = platformv1.ReasonRolloutStarted
	cr.Status.LastAnalysisResult = platformv1.AnalysisPending
	cr.Status.CanaryWeight = 0
	cr.Status.CurrentStep = 1
	r.setReady(cr, false, platformv1.ReasonWaitingForCanary, "waiting for the canary Deployment to roll out")
	r.setProgressing(cr, true, platformv1.ReasonRolloutStarted, "template "+hash)
	r.Recorder.Eventf(cr, corev1.EventTypeNormal, platformv1.ReasonRolloutStarted, "rolling out template %s", hash)
	return ctrl.Result{RequeueAfter: waitRequeue}, nil
}

func (r *CanaryRolloutReconciler) progress(ctx context.Context, cr *platformv1.CanaryRollout, cfg rolloutConfig, target *appsv1.Deployment, hash string) (ctrl.Result, error) {
	if hash != cr.Status.ObservedTemplateHash {
		return r.startRollout(ctx, cr, cfg, target, hash)
	}
	now := r.now()
	startedAt := cr.Status.Analysis.CheckpointStartedAt.Time
	if now.Sub(startedAt) > cfg.MaxStepDuration {
		reason := platformv1.ReasonCanaryUnavailable
		if rolledOut(target) {
			reason = platformv1.ReasonMetricsUnavailable
		}
		return r.rollback(ctx, cr, cfg, target, reason, "")
	}
	if !rolledOut(target) {
		return ctrl.Result{RequeueAfter: waitRequeue}, nil
	}
	// The baseline snapshot excludes everything the workload served before this rollout;
	// without it the first window would count stale history, so traffic waits for it.
	baseline, err := r.Metrics.Counters(ctx, r.target(cr, target))
	if err != nil {
		r.setReady(cr, false, platformv1.ReasonMetricsUnavailable, err.Error())
		return ctrl.Result{RequeueAfter: cfg.interval(cr)}, nil
	}
	st := analysis.Start(cfg.Config, now.Time)
	if err := r.Router.Ensure(ctx, r.route(cr, target), st.Weight); err != nil {
		return ctrl.Result{}, err
	}
	cr.Status.Analysis = stateToStatus(st, &baseline, now)
	cr.Status.CanaryWeight = int32(st.Weight)
	cr.Status.Phase = platformv1.PhaseAnalyzing
	cr.Status.Reason = platformv1.ReasonAnalyzing
	r.setReady(cr, false, platformv1.ReasonAnalyzing, fmt.Sprintf("canary at %d%%", st.Weight))
	r.Recorder.Eventf(cr, corev1.EventTypeNormal, platformv1.ReasonTrafficShifted, "canary receives %d%% of traffic", st.Weight)
	canaryWeight.WithLabelValues(cr.Namespace, cr.Name).Set(float64(st.Weight))
	return ctrl.Result{RequeueAfter: cfg.interval(cr)}, nil
}

func (r *CanaryRolloutReconciler) analyze(ctx context.Context, cr *platformv1.CanaryRollout, cfg rolloutConfig, target *appsv1.Deployment, hash string) (ctrl.Result, error) {
	if hash != cr.Status.ObservedTemplateHash {
		return r.startRollout(ctx, cr, cfg, target, hash)
	}
	now := r.now()
	interval := cfg.interval(cr)
	if last := cr.Status.Analysis.LastTickAt; last != nil && now.Sub(last.Time) < interval {
		return ctrl.Result{RequeueAfter: interval - now.Sub(last.Time)}, nil
	}
	st := stateFromStatus(cr.Status.Analysis, int(cr.Status.CanaryWeight))
	prev := cr.Status.Analysis.LastCounters
	cur, err := r.Metrics.Counters(ctx, r.target(cr, target))
	ok := err == nil
	var sample analysis.Sample
	snapshot := prev
	if ok {
		sample = metrics.Delta(countersFromStatus(prev), cur)
		snapshot = &platformv1.CounterSnapshot{Requests: cur.Requests, Errors: cur.Errors, Slow: cur.Slow, At: now}
	}
	d := analysis.Tick(cfg.Config, &st, now.Time, sample, ok)
	cr.Status.Analysis = stateToStatus(st, nil, now)
	cr.Status.Analysis.LastCounters = snapshot
	cr.Status.Analysis.Confidence = d.Confidence
	cr.Status.CurrentStep = int32(st.Checkpoint) + 1
	decisions.WithLabelValues(d.Action.String(), string(d.Reason)).Inc()

	switch d.Action {
	case analysis.Rollback:
		return r.rollback(ctx, cr, cfg, target, string(d.Reason), d.Criterion)
	case analysis.Promote:
		cr.Status.Phase = platformv1.PhasePromoting
		cr.Status.Reason = platformv1.ReasonPromoting
		cr.Status.LastAnalysisResult = platformv1.AnalysisPass
		r.setReady(cr, false, platformv1.ReasonPromoting, "canary accepted at 100%, updating primary")
		r.Recorder.Eventf(cr, corev1.EventTypeNormal, platformv1.ReasonPromoting, "canary accepted after %d requests", st.TotalSamples)
		return ctrl.Result{Requeue: true}, nil
	case analysis.Shift:
		if err := r.Router.Ensure(ctx, r.route(cr, target), d.Weight); err != nil {
			return ctrl.Result{}, err
		}
		cr.Status.CanaryWeight = int32(d.Weight)
		canaryWeight.WithLabelValues(cr.Namespace, cr.Name).Set(float64(d.Weight))
		r.setReady(cr, false, platformv1.ReasonAnalyzing, fmt.Sprintf("canary at %d%%, confidence %.2f", d.Weight, d.Confidence))
		r.Recorder.Eventf(cr, corev1.EventTypeNormal, platformv1.ReasonTrafficShifted, "canary receives %d%% of traffic (confidence %.2f)", d.Weight, d.Confidence)
	default:
		msg := fmt.Sprintf("canary at %d%%, confidence %.2f", st.Weight, d.Confidence)
		if !ok {
			msg = "metrics unavailable: " + err.Error()
		} else if d.Anomaly {
			msg += ", holding after an anomalous window"
		}
		r.setReady(cr, false, platformv1.ReasonAnalyzing, msg)
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

func (r *CanaryRolloutReconciler) promote(ctx context.Context, cr *platformv1.CanaryRollout, cfg rolloutConfig, target *appsv1.Deployment, hash string) (ctrl.Result, error) {
	if hash != cr.Status.ObservedTemplateHash {
		return r.startRollout(ctx, cr, cfg, target, hash)
	}
	primary := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: primaryName(target.Name)}, primary); err != nil {
		return ctrl.Result{}, err
	}
	if templateHash(primary.Spec.Template) != hash {
		before := primary.DeepCopy()
		applyPrimaryTemplate(primary, target, cr.Name, *cr.Status.TargetReplicas)
		if err := r.Patch(ctx, primary, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating primary template: %w", err)
		}
		return ctrl.Result{RequeueAfter: waitRequeue}, nil
	}
	if !rolledOut(primary) {
		return ctrl.Result{RequeueAfter: waitRequeue}, nil
	}
	cr.Status.PromotedTemplateHash = hash
	r.Recorder.Eventf(cr, corev1.EventTypeNormal, platformv1.ReasonPromoted, "primary now runs template %s", hash)
	return r.beginDrain(ctx, cr, cfg, target, platformv1.ReasonPromoted, platformv1.AnalysisPass)
}

func (r *CanaryRolloutReconciler) rollback(ctx context.Context, cr *platformv1.CanaryRollout, cfg rolloutConfig, target *appsv1.Deployment, reason, criterion string) (ctrl.Result, error) {
	msg := reason
	if criterion != "" {
		msg = fmt.Sprintf("%s on %s", reason, criterion)
	}
	r.Recorder.Eventf(cr, corev1.EventTypeWarning, "RolledBack", "%s: traffic returned to primary, canary drains for %s", msg, cfg.drainGrace(cr))
	return r.beginDrain(ctx, cr, cfg, target, reason, platformv1.AnalysisFail)
}

// beginDrain routes everything to primary but leaves the canary running for the grace:
// the API server shows the new route before every client sidecar has it, and a canary
// parked in the same instant would fail the requests still routed to it.
func (r *CanaryRolloutReconciler) beginDrain(ctx context.Context, cr *platformv1.CanaryRollout, cfg rolloutConfig, target *appsv1.Deployment, reason string, result platformv1.AnalysisResult) (ctrl.Result, error) {
	if err := r.Router.Ensure(ctx, r.route(cr, target), 0); err != nil {
		return ctrl.Result{}, fmt.Errorf("routing all traffic to primary: %w", err)
	}
	now := r.now()
	cr.Status.Phase = platformv1.PhaseDraining
	cr.Status.Reason = reason
	cr.Status.LastAnalysisResult = result
	cr.Status.CanaryWeight = 0
	cr.Status.TrafficFlippedAt = &now
	canaryWeight.WithLabelValues(cr.Namespace, cr.Name).Set(0)
	r.setReady(cr, false, reason, fmt.Sprintf("traffic on primary, canary draining for %s", cfg.drainGrace(cr)))
	return r.drain(ctx, cr, cfg, target)
}

func (r *CanaryRolloutReconciler) drain(ctx context.Context, cr *platformv1.CanaryRollout, cfg rolloutConfig, target *appsv1.Deployment) (ctrl.Result, error) {
	if cr.Status.TrafficFlippedAt != nil {
		if left := cfg.drainGrace(cr) - r.now().Sub(cr.Status.TrafficFlippedAt.Time); left > 0 {
			return ctrl.Result{RequeueAfter: left}, nil
		}
	}
	if err := r.scaleTarget(ctx, target, 0); err != nil {
		return ctrl.Result{}, err
	}
	phase := platformv1.PhaseRolledBack
	if cr.Status.Reason == platformv1.ReasonPromoted {
		phase = platformv1.PhaseSucceeded
	}
	r.settle(cr, phase, cr.Status.Reason, cr.Status.LastAnalysisResult)
	return ctrl.Result{}, nil
}

// settle records a terminal state for this rollout: traffic is entirely on primary and
// the canary Deployment is parked at zero.
func (r *CanaryRolloutReconciler) settle(cr *platformv1.CanaryRollout, phase platformv1.Phase, reason string, result platformv1.AnalysisResult) {
	cr.Status.Phase = phase
	cr.Status.Reason = reason
	cr.Status.LastAnalysisResult = result
	cr.Status.CanaryWeight = 0
	cr.Status.TrafficFlippedAt = nil
	canaryWeight.WithLabelValues(cr.Namespace, cr.Name).Set(0)
	r.setReady(cr, phase == platformv1.PhaseSucceeded, reason, "traffic on primary, canary parked at zero replicas")
	r.setProgressing(cr, false, reason, "")
}

func (r *CanaryRolloutReconciler) ensurePrimary(ctx context.Context, cr *platformv1.CanaryRollout, target *appsv1.Deployment) (*appsv1.Deployment, error) {
	primary := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: primaryName(target.Name)}, primary)
	if apierrors.IsNotFound(err) {
		primary = primaryFrom(target, cr.Name, *cr.Status.TargetReplicas)
		if err := controllerutil.SetControllerReference(cr, primary, r.Scheme); err != nil {
			return nil, err
		}
		if err := r.Create(ctx, primary); err != nil {
			return nil, fmt.Errorf("creating primary: %w", err)
		}
		r.Recorder.Eventf(cr, corev1.EventTypeNormal, platformv1.ReasonInitializing, "created primary Deployment %s", primary.Name)
		return primary, nil
	}
	return primary, err
}

func (r *CanaryRolloutReconciler) ensureServices(ctx context.Context, cr *platformv1.CanaryRollout, target *appsv1.Deployment) error {
	base := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: target.Name}, base); err != nil {
		return err
	}
	for name, role := range map[string]string{primaryName(target.Name): platformv1.RolePrimary, canaryServiceName(target.Name): platformv1.RoleCanary} {
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cr.Namespace}}
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
			if svc.CreationTimestamp.IsZero() {
				*svc = *roleService(name, base, target, cr.Name, role)
			} else {
				applyRoleServiceSpec(svc, base, target, cr.Name, role)
			}
			return controllerutil.SetControllerReference(cr, svc, r.Scheme)
		})
		if err != nil {
			return fmt.Errorf("ensuring service %s: %w", name, err)
		}
	}
	return nil
}

func (r *CanaryRolloutReconciler) scaleTarget(ctx context.Context, target *appsv1.Deployment, replicas int32) error {
	if target.Spec.Replicas != nil && *target.Spec.Replicas == replicas {
		return nil
	}
	before := target.DeepCopy()
	target.Spec.Replicas = ptr.To(replicas)
	if err := r.Patch(ctx, target, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("scaling %s to %d: %w", target.Name, replicas, err)
	}
	return nil
}

func (r *CanaryRolloutReconciler) route(cr *platformv1.CanaryRollout, target *appsv1.Deployment) mesh.Route {
	return mesh.Route{
		Namespace:      cr.Namespace,
		Host:           target.Name,
		PrimaryService: primaryName(target.Name),
		CanaryService:  canaryServiceName(target.Name),
		Labels:         map[string]string{platformv1.LabelRolloutRef: cr.Name},
		Owner:          *metav1.NewControllerRef(cr, platformv1.SchemeGroupVersion.WithKind("CanaryRollout")),
	}
}

func (r *CanaryRolloutReconciler) target(cr *platformv1.CanaryRollout, target *appsv1.Deployment) metrics.Target {
	return metrics.Target{Namespace: cr.Namespace, Workload: target.Name, LatencyMaxMs: float64(cr.Spec.SuccessCriteria.LatencyP99MaxMs)}
}

func (r *CanaryRolloutReconciler) setReady(cr *platformv1.CanaryRollout, ready bool, reason, msg string) {
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{Type: platformv1.ConditionReady, Status: status, Reason: reason, Message: msg, ObservedGeneration: cr.Generation})
}

func (r *CanaryRolloutReconciler) setProgressing(cr *platformv1.CanaryRollout, progressing bool, reason, msg string) {
	status := metav1.ConditionFalse
	if progressing {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{Type: platformv1.ConditionProgressing, Status: status, Reason: reason, Message: msg, ObservedGeneration: cr.Generation})
}

type rolloutConfig struct {
	analysis.Config
}

func (c rolloutConfig) drainGrace(cr *platformv1.CanaryRollout) time.Duration {
	if a := cr.Spec.Analysis; a != nil && a.DrainGrace != nil {
		return max(0, a.DrainGrace.Duration)
	}
	return defaultDrainGrace
}

func (c rolloutConfig) interval(cr *platformv1.CanaryRollout) time.Duration {
	if a := cr.Spec.Analysis; a != nil && a.Interval.Duration > 0 {
		return a.Interval.Duration
	}
	return defaultInterval
}

func configFrom(cr *platformv1.CanaryRollout) (rolloutConfig, error) {
	cfg := analysis.Config{
		ErrorRateMax:     cr.Spec.SuccessCriteria.ErrorRateMax,
		LatencyTailMax:   analysis.DefaultLatencyTailMax,
		MinSampleSize:    int64(cr.Spec.SuccessCriteria.MinSampleSize),
		Alpha:            defaultAlpha,
		Beta:             defaultBeta,
		RegressionFactor: defaultRegression,
		MaxStepDuration:  defaultMaxStepDuration,
	}
	for _, p := range cr.Spec.StepPercentages {
		cfg.Checkpoints = append(cfg.Checkpoints, int(p))
	}
	if a := cr.Spec.Analysis; a != nil {
		if a.Alpha > 0 {
			cfg.Alpha = a.Alpha
		}
		if a.Beta > 0 {
			cfg.Beta = a.Beta
		}
		if a.RegressionFactor > 0 {
			cfg.RegressionFactor = a.RegressionFactor
		}
		if a.MaxStepDuration.Duration > 0 {
			cfg.MaxStepDuration = a.MaxStepDuration.Duration
		}
	}
	if err := cfg.Validate(); err != nil {
		return rolloutConfig{}, err
	}
	return rolloutConfig{cfg}, nil
}

func stateToStatus(st analysis.State, baseline *metrics.Counters, now metav1.Time) *platformv1.AnalysisState {
	out := &platformv1.AnalysisState{
		Checkpoint:             int32(st.Checkpoint),
		Errors:                 platformv1.CriterionState{Cumulative: st.Errors.Cumulative, SinceCheckpoint: st.Errors.SinceCheckpoint},
		Latency:                platformv1.CriterionState{Cumulative: st.Latency.Cumulative, SinceCheckpoint: st.Latency.SinceCheckpoint},
		SamplesSinceCheckpoint: st.SamplesSinceCheckpoint,
		TotalSamples:           st.TotalSamples,
		Shrink:                 int32(st.Shrink),
		Anomalies:              int32(st.Anomalies),
		CheckpointStartedAt:    ptr.To(metav1.NewTime(st.CheckpointStartedAt)),
		LastTickAt:             ptr.To(now),
	}
	if baseline != nil {
		out.LastCounters = &platformv1.CounterSnapshot{Requests: baseline.Requests, Errors: baseline.Errors, Slow: baseline.Slow, At: now}
	}
	return out
}

func stateFromStatus(s *platformv1.AnalysisState, weight int) analysis.State {
	st := analysis.State{
		Checkpoint:             int(s.Checkpoint),
		Weight:                 weight,
		Errors:                 analysis.Criterion{Cumulative: s.Errors.Cumulative, SinceCheckpoint: s.Errors.SinceCheckpoint},
		Latency:                analysis.Criterion{Cumulative: s.Latency.Cumulative, SinceCheckpoint: s.Latency.SinceCheckpoint},
		SamplesSinceCheckpoint: s.SamplesSinceCheckpoint,
		TotalSamples:           s.TotalSamples,
		Shrink:                 int(s.Shrink),
		Anomalies:              int(s.Anomalies),
	}
	if s.CheckpointStartedAt != nil {
		st.CheckpointStartedAt = s.CheckpointStartedAt.Time
	}
	return st
}

func countersFromStatus(s *platformv1.CounterSnapshot) metrics.Counters {
	if s == nil {
		return metrics.Counters{}
	}
	return metrics.Counters{Requests: s.Requests, Errors: s.Errors, Slow: s.Slow}
}

func (r *CanaryRolloutReconciler) SetupWithManager(mgr ctrl.Manager) error {
	vs := &unstructured.Unstructured{}
	vs.SetGroupVersionKind(mesh.VirtualServiceGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1.CanaryRollout{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(vs).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(r.rolloutsTargeting)).
		Complete(r)
}

func (r *CanaryRolloutReconciler) rolloutsTargeting(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &platformv1.CanaryRolloutList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, cr := range list.Items {
		if cr.Spec.TargetDeployment == obj.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}})
		}
	}
	return reqs
}
