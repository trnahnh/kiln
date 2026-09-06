package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/trnahnh/kiln/audit"
	platformv1 "github.com/trnahnh/kiln/operator/api/v1"
	"github.com/trnahnh/kiln/operator/internal/lifecycle"
)

const (
	backupIDLayout = "20060102T150405Z"

	pollInterval  = 10 * time.Second
	sweepInterval = 5 * time.Minute
)

type TenantDatabaseReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Commands Commands
	Now      func() time.Time
	Audit    audit.Publisher
}

// +kubebuilder:rbac:groups=platform.internal,resources=tenantdatabases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.internal,resources=tenantdatabases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.internal,resources=tenantdatabases/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims;services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=create
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *TenantDatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tdb := &platformv1.TenantDatabase{}
	if err := r.Get(ctx, req.NamespacedName, tdb); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !tdb.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, tdb)
	}
	if !controllerutil.ContainsFinalizer(tdb, finalizerName) {
		controllerutil.AddFinalizer(tdb, finalizerName)
		return ctrl.Result{}, r.Update(ctx, tdb)
	}

	before := tdb.DeepCopy()
	var result ctrl.Result
	var err error
	switch lifecycle.Normalize(tdb.Status.Phase) {
	case platformv1.PhaseProvisioning:
		result, err = r.reconcileProvisioning(ctx, tdb)
	case platformv1.PhaseReady:
		result, err = r.reconcileReady(ctx, tdb)
	case platformv1.PhaseBackingUp:
		result, err = r.reconcileOperation(ctx, tdb, operationBackup)
	case platformv1.PhaseRestoring:
		result, err = r.reconcileOperation(ctx, tdb, operationRestore)
	case platformv1.PhaseFailed:
		return ctrl.Result{}, nil
	}
	if statusErr := r.patchStatus(ctx, before, tdb); statusErr != nil {
		return ctrl.Result{}, errors.Join(err, statusErr)
	}
	return result, err
}

func (r *TenantDatabaseReconciler) reconcileProvisioning(ctx context.Context, tdb *platformv1.TenantDatabase) (ctrl.Result, error) {
	if tdb.Spec.Engine != platformv1.EnginePostgres {
		r.fail(tdb, platformv1.ReasonUnsupportedEngine, fmt.Sprintf("engine %q is not supported by this operator", tdb.Spec.Engine))
		return ctrl.Result{}, nil
	}
	if err := r.ensureDependents(ctx, tdb); err != nil {
		if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
			r.fail(tdb, platformv1.ReasonProvisionFailed, err.Error())
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: tdb.Namespace, Name: statefulSetName(tdb)}, sts); err != nil {
		if apierrors.IsNotFound(err) {
			// Just created; the informer cache has not caught up yet.
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	if !statefulSetReady(sts) {
		setCondition(tdb, platformv1.ConditionReady, metav1.ConditionFalse, platformv1.ReasonProvisioning, "waiting for the database pod to become ready")
		setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionTrue, platformv1.ReasonProvisioning, "creating owned resources")
		tdb.Status.Phase = platformv1.PhaseProvisioning
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	if err := r.transition(tdb, lifecycle.EventProvisioned); err != nil {
		return ctrl.Result{}, err
	}
	timeToReady.Observe(r.now().Sub(tdb.CreationTimestamp.Time).Seconds())
	r.markSettled(tdb)
	r.publish(tdb, audit.ActionProvision, map[string]any{"outcome": "Ready"}, string(tdb.UID))
	return ctrl.Result{Requeue: true}, nil
}

func (r *TenantDatabaseReconciler) reconcileReady(ctx context.Context, tdb *platformv1.TenantDatabase) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if err := r.ensureDependents(ctx, tdb); err != nil {
		return ctrl.Result{}, err
	}

	// A Job that exists while the phase still reads Ready means the status write after
	// creating it was lost; adopt it instead of starting a second one.
	for _, op := range []string{operationBackup, operationRestore} {
		job, err := r.activeJob(ctx, tdb, op)
		if err != nil {
			return ctrl.Result{}, err
		}
		if job != nil {
			return ctrl.Result{Requeue: true}, r.enterOperation(tdb, op)
		}
	}

	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: tdb.Namespace, Name: dataPVCName(tdb)}, pvc); err != nil {
		return ctrl.Result{}, err
	}
	desired := storageQuantity(tdb.Spec.StorageGB)
	if current := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; current.Cmp(desired) < 0 {
		pvc.Spec.Resources.Requests[corev1.ResourceStorage] = desired
		if err := r.Update(ctx, pvc); err != nil {
			if !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
				return ctrl.Result{}, fmt.Errorf("grow data volume: %w", err)
			}
			// The StorageClass refuses expansion or the claim is not bound yet: report it and
			// keep retrying rather than backing off with a stack trace.
			r.Recorder.Eventf(tdb, corev1.EventTypeWarning, platformv1.ReasonScaleFailed, "data volume resize rejected: %v", err)
			r.publish(tdb, audit.ActionScale, map[string]any{"outcome": "Rejected", "from": current.String(), "to": desired.String(), "reason": err.Error()}, "Rejected", current.String(), desired.String())
			setCondition(tdb, platformv1.ConditionReady, metav1.ConditionFalse, platformv1.ReasonScaling, "data volume resize pending")
			setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionFalse, platformv1.ReasonScaleFailed, err.Error())
			return ctrl.Result{RequeueAfter: pollInterval}, nil
		}
		log.Info("growing data volume", "from", current.String(), "to", desired.String())
		r.Recorder.Eventf(tdb, corev1.EventTypeNormal, platformv1.ReasonScaling, "growing data volume from %s to %s", current.String(), desired.String())
		r.publish(tdb, audit.ActionScale, map[string]any{"outcome": "Applied", "from": current.String(), "to": desired.String()}, "Applied", current.String(), desired.String())
	}

	wantsRestore := tdb.Annotations[platformv1.AnnotationRestoreFrom] != ""
	wantsBackup := tdb.Annotations[platformv1.AnnotationBackup] == platformv1.AnnotationBackupNow

	if !pvcSettled(pvc, desired) {
		setCondition(tdb, platformv1.ConditionReady, metav1.ConditionFalse, platformv1.ReasonScaling, "data volume resize in progress")
		if wantsRestore || wantsBackup {
			r.conflict(tdb, "requested operation deferred until the data volume resize completes")
		} else {
			setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionTrue, platformv1.ReasonScaling, "data volume resize in progress")
		}
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}
	r.markSettled(tdb)

	if wantsRestore {
		return ctrl.Result{Requeue: true}, r.startOperation(ctx, tdb, operationRestore, tdb.Annotations[platformv1.AnnotationRestoreFrom], platformv1.AnnotationRestoreFrom)
	}
	if wantsBackup {
		return ctrl.Result{Requeue: true}, r.startOperation(ctx, tdb, operationBackup, r.newBackupID(), platformv1.AnnotationBackup)
	}

	due, wait, err := r.scheduledBackup(tdb)
	if err != nil {
		r.Recorder.Eventf(tdb, corev1.EventTypeWarning, "InvalidBackupSchedule", "%v", err)
		return ctrl.Result{RequeueAfter: sweepInterval}, nil
	}
	if due {
		return ctrl.Result{Requeue: true}, r.startOperation(ctx, tdb, operationBackup, r.newBackupID(), "")
	}
	if wait == 0 || wait > sweepInterval {
		wait = sweepInterval
	}
	return ctrl.Result{RequeueAfter: wait}, nil
}

func (r *TenantDatabaseReconciler) reconcileOperation(ctx context.Context, tdb *platformv1.TenantDatabase, operation string) (ctrl.Result, error) {
	job, err := r.latestJob(ctx, tdb, operation)
	if err != nil {
		return ctrl.Result{}, err
	}
	if job == nil {
		r.Recorder.Eventf(tdb, corev1.EventTypeWarning, "JobMissing", "%s Job disappeared before it finished", operation)
		return ctrl.Result{Requeue: true}, r.finishOperation(tdb, operation, false, "")
	}

	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: tdb.Namespace, Name: dataPVCName(tdb)}, pvc); err != nil {
		return ctrl.Result{}, err
	}
	current := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	scalePending := current.Cmp(storageQuantity(tdb.Spec.StorageGB)) < 0

	if !jobFinished(job) {
		if scalePending {
			r.conflict(tdb, fmt.Sprintf("storage change deferred until the %s completes", operation))
		} else {
			reason := platformv1.ReasonBackingUp
			if operation == operationRestore {
				reason = platformv1.ReasonRestoring
			}
			setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionTrue, reason, fmt.Sprintf("Job %s is running", job.Name))
		}
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	return ctrl.Result{Requeue: true}, r.finishOperation(tdb, operation, jobSucceeded(job), job.Annotations[annotationBackupID])
}

func (r *TenantDatabaseReconciler) reconcileDelete(ctx context.Context, tdb *platformv1.TenantDatabase) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(tdb, finalizerName) {
		return ctrl.Result{}, nil
	}
	running := 0
	for _, op := range []string{operationBackup, operationRestore} {
		job, err := r.activeJob(ctx, tdb, op)
		if err != nil {
			return ctrl.Result{}, err
		}
		if job == nil {
			continue
		}
		running++
		if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
	}
	if running > 0 {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	controllerutil.RemoveFinalizer(tdb, finalizerName)
	return ctrl.Result{}, r.Update(ctx, tdb)
}

func (r *TenantDatabaseReconciler) ensureDependents(ctx context.Context, tdb *platformv1.TenantDatabase) error {
	if err := r.createCredentials(ctx, tdb); err != nil {
		return err
	}
	objects := []client.Object{
		desiredPVC(tdb, dataPVCName(tdb), tdb.Spec.StorageGB),
		desiredPVC(tdb, backupsPVCName(tdb), tdb.Spec.StorageGB),
		desiredService(tdb),
		desiredStatefulSet(tdb),
	}
	for _, obj := range objects {
		if err := r.createIfMissing(ctx, tdb, obj); err != nil {
			return err
		}
	}
	return nil
}

// createCredentials never reads the Secret back: the operator's only Secret verb is create,
// so it cannot read any Secret anywhere, the audit writer credential included (ADR-0019).
func (r *TenantDatabaseReconciler) createCredentials(ctx context.Context, tdb *platformv1.TenantDatabase) error {
	secret, err := desiredSecret(tdb)
	if err != nil {
		return err
	}
	if err := controllerutil.SetControllerReference(tdb, secret, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference on %s: %w", secret.Name, err)
	}
	if err := r.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create credentials %s: %w", secret.Name, err)
	}
	return nil
}

// Every owned object carries a controller reference before it is created; nothing is
// provisioned without one (CLAUDE.md invariant).
func (r *TenantDatabaseReconciler) createIfMissing(ctx context.Context, tdb *platformv1.TenantDatabase, obj client.Object) error {
	existing := obj.DeepCopyObject().(client.Object)
	err := r.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	if err := controllerutil.SetControllerReference(tdb, obj, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference on %s: %w", obj.GetName(), err)
	}
	if err := r.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create %T %s: %w", obj, obj.GetName(), err)
	}
	return nil
}

func (r *TenantDatabaseReconciler) startOperation(ctx context.Context, tdb *platformv1.TenantDatabase, operation, backupID, annotation string) error {
	var script string
	if operation == operationBackup {
		script = r.commands().BackupScript(backupID)
	} else {
		script = r.commands().RestoreScript(backupID)
	}
	if err := r.createIfMissing(ctx, tdb, desiredJob(tdb, operation, backupID, script)); err != nil {
		return err
	}
	if annotation != "" {
		if err := r.clearAnnotation(ctx, tdb, annotation); err != nil {
			return err
		}
	}
	r.Recorder.Eventf(tdb, corev1.EventTypeNormal, "OperationStarted", "%s started (%s)", operation, backupID)
	r.publish(tdb, auditAction(operation), map[string]any{"outcome": "Started", "backupId": backupID}, "Started", backupID)
	return r.enterOperation(tdb, operation)
}

func (r *TenantDatabaseReconciler) enterOperation(tdb *platformv1.TenantDatabase, operation string) error {
	if operation == operationBackup {
		if err := r.transition(tdb, lifecycle.EventBackupStarted); err != nil {
			return err
		}
		setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionTrue, platformv1.ReasonBackingUp, "backup Job running")
		return nil
	}
	if err := r.transition(tdb, lifecycle.EventRestoreStarted); err != nil {
		return err
	}
	setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionTrue, platformv1.ReasonRestoring, "restore Job running")
	return nil
}

func (r *TenantDatabaseReconciler) finishOperation(tdb *platformv1.TenantDatabase, operation string, succeeded bool, backupID string) error {
	outcome := "Failed"
	if succeeded {
		outcome = "Succeeded"
	}
	idKey := backupID
	if idKey == "" {
		idKey = "unknown"
	}
	r.publish(tdb, auditAction(operation), map[string]any{"outcome": outcome, "backupId": backupID}, outcome, idKey)
	switch {
	case operation == operationBackup && succeeded:
		if t, err := time.Parse(backupIDLayout, backupID); err == nil {
			tdb.Status.LastBackupTime = &metav1.Time{Time: t}
		}
		setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionFalse, platformv1.ReasonReconciled, "backup "+backupID+" completed")
		return r.transition(tdb, lifecycle.EventBackupSucceeded)
	case operation == operationBackup:
		r.Recorder.Event(tdb, corev1.EventTypeWarning, platformv1.ReasonBackupFailed, "backup Job failed; the database is unaffected")
		setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionFalse, platformv1.ReasonBackupFailed, "backup Job failed")
		return r.transition(tdb, lifecycle.EventBackupFailed)
	case succeeded:
		setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionFalse, platformv1.ReasonReconciled, "restore "+backupID+" completed")
		return r.transition(tdb, lifecycle.EventRestoreSucceeded)
	default:
		r.Recorder.Event(tdb, corev1.EventTypeWarning, platformv1.ReasonRestoreFailed, "restore Job failed; data state is unknown")
		if err := r.transition(tdb, lifecycle.EventRestoreFailed); err != nil {
			return err
		}
		setCondition(tdb, platformv1.ConditionReady, metav1.ConditionFalse, platformv1.ReasonRestoreFailed, "restore Job failed; delete and recreate to recover")
		setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionFalse, platformv1.ReasonRestoreFailed, "restore Job failed")
		return nil
	}
}

func (r *TenantDatabaseReconciler) transition(tdb *platformv1.TenantDatabase, ev lifecycle.Event) error {
	next, err := lifecycle.Next(tdb.Status.Phase, ev)
	if err != nil {
		return fmt.Errorf("%s: %w", tdb.Name, err)
	}
	tdb.Status.Phase = next
	return nil
}

func (r *TenantDatabaseReconciler) fail(tdb *platformv1.TenantDatabase, reason, message string) {
	tdb.Status.Phase = platformv1.PhaseFailed
	setCondition(tdb, platformv1.ConditionReady, metav1.ConditionFalse, reason, message)
	setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionFalse, reason, message)
	r.Recorder.Event(tdb, corev1.EventTypeWarning, reason, message)
	r.publish(tdb, audit.ActionProvision, map[string]any{"outcome": "Failed", "reason": reason}, string(tdb.UID), reason)
}

func auditAction(operation string) string {
	if operation == operationBackup {
		return audit.ActionBackup
	}
	return audit.ActionRestore
}

// conflict records RECONCILE_CONFLICT (API_REFERENCE.md error codes); the event fires once
// per conflict episode rather than on every requeue.
func (r *TenantDatabaseReconciler) conflict(tdb *platformv1.TenantDatabase, message string) {
	current := meta.FindStatusCondition(tdb.Status.Conditions, platformv1.ConditionProgressing)
	if current == nil || current.Reason != platformv1.ReasonReconcileConflict {
		r.Recorder.Event(tdb, corev1.EventTypeWarning, platformv1.ReasonReconcileConflict, message)
	}
	setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionFalse, platformv1.ReasonReconcileConflict, message)
}

func (r *TenantDatabaseReconciler) markSettled(tdb *platformv1.TenantDatabase) {
	setCondition(tdb, platformv1.ConditionReady, metav1.ConditionTrue, platformv1.ReasonReconciled, "database is available")
	current := meta.FindStatusCondition(tdb.Status.Conditions, platformv1.ConditionProgressing)
	if current == nil || current.Status == metav1.ConditionTrue || current.Reason == platformv1.ReasonReconcileConflict {
		setCondition(tdb, platformv1.ConditionProgressing, metav1.ConditionFalse, platformv1.ReasonReconciled, "spec applied")
	}
	tdb.Status.ObservedGeneration = tdb.Generation
}

func (r *TenantDatabaseReconciler) scheduledBackup(tdb *platformv1.TenantDatabase) (due bool, wait time.Duration, err error) {
	if tdb.Spec.BackupSchedule == "" {
		return false, 0, nil
	}
	schedule, err := cron.ParseStandard(tdb.Spec.BackupSchedule)
	if err != nil {
		return false, 0, fmt.Errorf("backupSchedule %q: %w", tdb.Spec.BackupSchedule, err)
	}
	last := tdb.CreationTimestamp.Time
	if tdb.Status.LastBackupTime != nil {
		last = tdb.Status.LastBackupTime.Time
	}
	now := r.now()
	next := schedule.Next(last)
	if !next.After(now) {
		return true, 0, nil
	}
	return false, next.Sub(now), nil
}

func (r *TenantDatabaseReconciler) activeJob(ctx context.Context, tdb *platformv1.TenantDatabase, operation string) (*batchv1.Job, error) {
	jobs, err := r.listJobs(ctx, tdb, operation)
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		if !jobFinished(&jobs[i]) {
			return &jobs[i], nil
		}
	}
	return nil, nil
}

// latestJob prefers a running Job, then the most recently created finished one.
func (r *TenantDatabaseReconciler) latestJob(ctx context.Context, tdb *platformv1.TenantDatabase, operation string) (*batchv1.Job, error) {
	jobs, err := r.listJobs(ctx, tdb, operation)
	if err != nil {
		return nil, err
	}
	var latest *batchv1.Job
	for i := range jobs {
		job := &jobs[i]
		if !jobFinished(job) {
			return job, nil
		}
		if latest == nil || job.CreationTimestamp.After(latest.CreationTimestamp.Time) {
			latest = job
		}
	}
	return latest, nil
}

func (r *TenantDatabaseReconciler) listJobs(ctx context.Context, tdb *platformv1.TenantDatabase, operation string) ([]batchv1.Job, error) {
	jobs := &batchv1.JobList{}
	err := r.List(ctx, jobs, client.InNamespace(tdb.Namespace),
		client.MatchingLabels{labelTenantDatabase: tdb.Name, labelOperation: operation})
	if err != nil {
		return nil, fmt.Errorf("list %s jobs: %w", operation, err)
	}
	return jobs.Items, nil
}

// clearAnnotation patches a copy so the response does not overwrite the status
// mutations accumulated on tdb during this reconcile.
func (r *TenantDatabaseReconciler) clearAnnotation(ctx context.Context, tdb *platformv1.TenantDatabase, key string) error {
	obj := tdb.DeepCopy()
	base := obj.DeepCopy()
	delete(obj.Annotations, key)
	if err := r.Patch(ctx, obj, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("clear annotation %s: %w", key, err)
	}
	delete(tdb.Annotations, key)
	tdb.ResourceVersion = obj.ResourceVersion
	return nil
}

func (r *TenantDatabaseReconciler) patchStatus(ctx context.Context, before, after *platformv1.TenantDatabase) error {
	if equality.Semantic.DeepEqual(before.Status, after.Status) {
		return nil
	}
	return r.Status().Patch(ctx, after, client.MergeFrom(before))
}

func (r *TenantDatabaseReconciler) newBackupID() string {
	return r.now().UTC().Format(backupIDLayout)
}

func (r *TenantDatabaseReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *TenantDatabaseReconciler) commands() Commands {
	if r.Commands == nil {
		return PostgresCommands{}
	}
	return r.Commands
}

func setCondition(tdb *platformv1.TenantDatabase, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&tdb.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: tdb.Generation,
	})
}

func statefulSetReady(sts *appsv1.StatefulSet) bool {
	return sts.Status.ObservedGeneration >= sts.Generation && sts.Status.ReadyReplicas >= 1
}

// pvcSettled: the requested size is applied and no resize is still in flight. Provisioners
// that never report capacity (kind's local-path) settle as soon as the spec is accepted.
func pvcSettled(pvc *corev1.PersistentVolumeClaim, desired resource.Quantity) bool {
	current := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if current.Cmp(desired) < 0 {
		return false
	}
	for _, c := range pvc.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		if c.Type == corev1.PersistentVolumeClaimResizing || c.Type == corev1.PersistentVolumeClaimFileSystemResizePending {
			return false
		}
	}
	return true
}

func (r *TenantDatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1.TenantDatabase{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.Service{}).
		Named("tenantdatabase").
		Complete(r)
}
