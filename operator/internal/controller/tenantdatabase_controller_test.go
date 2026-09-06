package controller

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/trnahnh/kiln/audit"
	platformv1 "github.com/trnahnh/kiln/operator/api/v1"
)

const (
	timeout  = 20 * time.Second
	interval = 200 * time.Millisecond
)

var nsCounter int

func newNamespace() string {
	nsCounter++
	name := fmt.Sprintf("tdb-test-%d", nsCounter)
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
	return name
}

func newTenantDatabase(ns, name string) *platformv1.TenantDatabase {
	return &platformv1.TenantDatabase{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: platformv1.TenantDatabaseSpec{
			Engine:    platformv1.EnginePostgres,
			Version:   "16",
			StorageGB: 20,
			Tier:      platformv1.TierStandard,
		},
	}
}

func get(obj client.Object, ns, name string) error {
	return k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, obj)
}

func fetch(tdb *platformv1.TenantDatabase) *platformv1.TenantDatabase {
	out := &platformv1.TenantDatabase{}
	Expect(get(out, tdb.Namespace, tdb.Name)).To(Succeed())
	return out
}

func phaseOf(tdb *platformv1.TenantDatabase) func() platformv1.Phase {
	return func() platformv1.Phase {
		out := &platformv1.TenantDatabase{}
		if err := get(out, tdb.Namespace, tdb.Name); err != nil {
			return ""
		}
		return out.Status.Phase
	}
}

func conditionReason(tdb *platformv1.TenantDatabase, condType string) func() string {
	return func() string {
		c := meta.FindStatusCondition(fetch(tdb).Status.Conditions, condType)
		if c == nil {
			return ""
		}
		return c.Reason
	}
}

func conditionStatus(tdb *platformv1.TenantDatabase, condType string) func() metav1.ConditionStatus {
	return func() metav1.ConditionStatus {
		c := meta.FindStatusCondition(fetch(tdb).Status.Conditions, condType)
		if c == nil {
			return ""
		}
		return c.Status
	}
}

func pvcStorage(ns, name string) func() string {
	return func() string {
		pvc := &corev1.PersistentVolumeClaim{}
		if err := get(pvc, ns, name); err != nil {
			return ""
		}
		q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		return q.String()
	}
}

func markStatefulSetReady(tdb *platformv1.TenantDatabase) {
	sts := &appsv1.StatefulSet{}
	Eventually(func() error { return get(sts, tdb.Namespace, statefulSetName(tdb)) }, timeout, interval).Should(Succeed())
	sts.Status.Replicas = 1
	sts.Status.ReadyReplicas = 1
	sts.Status.AvailableReplicas = 1
	sts.Status.ObservedGeneration = sts.Generation
	Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
}

func provisionToReady(ns, name string) *platformv1.TenantDatabase {
	tdb := newTenantDatabase(ns, name)
	Expect(k8sClient.Create(ctx, tdb)).To(Succeed())
	Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseProvisioning))
	markPVCBound(ns, dataPVCName(tdb))
	markStatefulSetReady(tdb)
	Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseReady))
	Eventually(conditionStatus(tdb, platformv1.ConditionReady), timeout, interval).Should(Equal(metav1.ConditionTrue))
	return tdb
}

func activeJob(tdb *platformv1.TenantDatabase, operation string) func() *batchv1.Job {
	return func() *batchv1.Job {
		jobs := &batchv1.JobList{}
		if err := k8sClient.List(ctx, jobs, client.InNamespace(tdb.Namespace),
			client.MatchingLabels{labelTenantDatabase: tdb.Name, labelOperation: operation}); err != nil {
			return nil
		}
		for i := range jobs.Items {
			if !jobFinished(&jobs.Items[i]) {
				return &jobs.Items[i]
			}
		}
		return nil
	}
}

// The API server validates Job status the way the Job controller writes it: a start time,
// and the target condition (SuccessCriteriaMet / FailureTarget) before the terminal one.
func finishJob(job *batchv1.Job, condType batchv1.JobConditionType) {
	now := metav1.Now()
	job.Status.StartTime = &now
	target := batchv1.JobSuccessCriteriaMet
	if condType == batchv1.JobFailed {
		target = batchv1.JobFailureTarget
	}
	for _, t := range []batchv1.JobConditionType{target, condType} {
		job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
			Type: t, Status: corev1.ConditionTrue, LastTransitionTime: now, Reason: "Test",
		})
	}
	if condType == batchv1.JobComplete {
		job.Status.Succeeded = 1
		job.Status.CompletionTime = &now
	} else {
		job.Status.Failed = 1
	}
	Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
}

// Simulates the PV controller: the API server only accepts a size change on a Bound claim.
func markPVCBound(ns, name string) {
	pvc := &corev1.PersistentVolumeClaim{}
	Eventually(func() error { return get(pvc, ns, name) }, timeout, interval).Should(Succeed())
	pvc.Status.Phase = corev1.ClaimBound
	pvc.Status.Capacity = pvc.Spec.Resources.Requests
	Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())
}

func annotate(tdb *platformv1.TenantDatabase, key, value string) {
	Eventually(func() error {
		cur := fetch(tdb)
		if cur.Annotations == nil {
			cur.Annotations = map[string]string{}
		}
		cur.Annotations[key] = value
		return k8sClient.Update(ctx, cur)
	}, timeout, interval).Should(Succeed())
}

func setStorage(tdb *platformv1.TenantDatabase, gb int32) {
	Eventually(func() error {
		cur := fetch(tdb)
		cur.Spec.StorageGB = gb
		return k8sClient.Update(ctx, cur)
	}, timeout, interval).Should(Succeed())
}

func ownedByTenantDatabase(obj client.Object, tdb *platformv1.TenantDatabase) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == tdb.UID && ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	return false
}

func eventsWithReason(ns, reason string) func() int {
	return func() int {
		events := &corev1.EventList{}
		if err := k8sClient.List(ctx, events, client.InNamespace(ns)); err != nil {
			return 0
		}
		n := 0
		for _, e := range events.Items {
			if e.Reason == reason {
				n++
			}
		}
		return n
	}
}

// auditOutcomes lists the outcomes published for one action on tdb, in order, with the
// eventIds so a test can assert a transition was published once even when reconciled twice.
func auditOutcomes(tdb *platformv1.TenantDatabase, action string) func() []string {
	resource := audit.ResourceRef("TenantDatabase", tdb.Namespace, tdb.Name)
	return func() []string {
		var out []string
		for _, e := range auditLog.Events() {
			if e.Resource == resource && e.Action == action {
				out = append(out, e.Details["outcome"].(string)+"@"+e.EventID)
			}
		}
		return out
	}
}

func auditEvent(tdb *platformv1.TenantDatabase, action, outcome string) audit.Event {
	resource := audit.ResourceRef("TenantDatabase", tdb.Namespace, tdb.Name)
	for _, e := range auditLog.Events() {
		if e.Resource == resource && e.Action == action && e.Details["outcome"] == outcome {
			return e
		}
	}
	return audit.Event{}
}

var _ = Describe("TenantDatabase reconciler", func() {
	Context("provisioning", func() {
		It("creates every owned resource, then becomes Ready when the StatefulSet is ready", func() {
			ns := newNamespace()
			tdb := newTenantDatabase(ns, "checkout")
			Expect(k8sClient.Create(ctx, tdb)).To(Succeed())

			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseProvisioning))
			Eventually(conditionStatus(tdb, platformv1.ConditionReady), timeout, interval).Should(Equal(metav1.ConditionFalse))
			Expect(conditionReason(tdb, platformv1.ConditionReady)()).To(Equal(platformv1.ReasonProvisioning))

			tdb = fetch(tdb)
			Expect(controllerutil.ContainsFinalizer(tdb, finalizerName)).To(BeTrue())

			secret := &corev1.Secret{}
			Eventually(func() error { return get(secret, ns, secretName(tdb)) }, timeout, interval).Should(Succeed())
			Expect(secret.Data).To(HaveKey("POSTGRES_PASSWORD"))
			Expect(ownedByTenantDatabase(secret, tdb)).To(BeTrue())

			data := &corev1.PersistentVolumeClaim{}
			Expect(get(data, ns, dataPVCName(tdb))).To(Succeed())
			Expect(ownedByTenantDatabase(data, tdb)).To(BeTrue())
			Expect(data.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("20Gi")))

			backups := &corev1.PersistentVolumeClaim{}
			Expect(get(backups, ns, backupsPVCName(tdb))).To(Succeed())
			Expect(ownedByTenantDatabase(backups, tdb)).To(BeTrue())

			svc := &corev1.Service{}
			Expect(get(svc, ns, serviceName(tdb))).To(Succeed())
			Expect(ownedByTenantDatabase(svc, tdb)).To(BeTrue())
			Expect(svc.Spec.Ports[0].Port).To(BeEquivalentTo(5432))

			sts := &appsv1.StatefulSet{}
			Expect(get(sts, ns, statefulSetName(tdb))).To(Succeed())
			Expect(ownedByTenantDatabase(sts, tdb)).To(BeTrue())
			Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal("postgres:16"))
			Expect(sts.Spec.VolumeClaimTemplates).To(BeEmpty(), "the operator owns the PVC directly so it can be resized and garbage-collected")
			Expect(sts.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName).To(Equal(dataPVCName(tdb)))

			Consistently(phaseOf(tdb), 2*time.Second, interval).Should(Equal(platformv1.PhaseProvisioning))

			markStatefulSetReady(tdb)
			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseReady))
			ready := fetch(tdb)
			Expect(meta.IsStatusConditionTrue(ready.Status.Conditions, platformv1.ConditionReady)).To(BeTrue())
			Expect(ready.Status.ObservedGeneration).To(Equal(ready.Generation))

			Eventually(auditOutcomes(tdb, audit.ActionProvision), timeout, interval).Should(HaveLen(1))
			ev := auditEvent(tdb, audit.ActionProvision, "Ready")
			Expect(ev.Actor).To(Equal("system:tenantdatabase"), "no requested-by annotation attributes the action to the controller")
			Expect(ev.EventID).To(Equal(audit.DeterministicID(ev.Resource, audit.ActionProvision, string(ready.UID))))
		})

		It("attributes the provision to the requested-by annotation when present", func() {
			ns := newNamespace()
			tdb := newTenantDatabase(ns, "attributed")
			tdb.Annotations = map[string]string{audit.AnnotationRequestedBy: "dev@company.com"}
			Expect(k8sClient.Create(ctx, tdb)).To(Succeed())
			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseProvisioning))
			markStatefulSetReady(tdb)
			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseReady))
			Eventually(func() string { return auditEvent(tdb, audit.ActionProvision, "Ready").Actor }, timeout, interval).Should(Equal("dev@company.com"))
		})

		It("sends an unsupported engine to Failed without creating anything", func() {
			ns := newNamespace()
			tdb := newTenantDatabase(ns, "cache")
			tdb.Spec.Engine = platformv1.EngineRedis
			Expect(k8sClient.Create(ctx, tdb)).To(Succeed())

			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseFailed))
			Expect(conditionReason(tdb, platformv1.ConditionReady)()).To(Equal(platformv1.ReasonUnsupportedEngine))

			sts := &appsv1.StatefulSetList{}
			Expect(k8sClient.List(ctx, sts, client.InNamespace(ns))).To(Succeed())
			Expect(sts.Items).To(BeEmpty())
			pvcs := &corev1.PersistentVolumeClaimList{}
			Expect(k8sClient.List(ctx, pvcs, client.InNamespace(ns))).To(Succeed())
			Expect(pvcs.Items).To(BeEmpty())
		})
	})

	Context("idempotency", func() {
		It("re-creates a deleted dependent and otherwise leaves settled resources untouched", func() {
			ns := newNamespace()
			tdb := provisionToReady(ns, "checkout")

			sts := &appsv1.StatefulSet{}
			Expect(get(sts, ns, statefulSetName(tdb))).To(Succeed())
			before := sts.ResourceVersion

			svc := &corev1.Service{}
			Expect(get(svc, ns, serviceName(tdb))).To(Succeed())
			Expect(k8sClient.Delete(ctx, svc)).To(Succeed())
			annotate(tdb, "test/poke", "1")
			Eventually(func() error { return get(&corev1.Service{}, ns, serviceName(tdb)) }, timeout, interval).Should(Succeed())

			Consistently(func() string {
				cur := &appsv1.StatefulSet{}
				Expect(get(cur, ns, statefulSetName(tdb))).To(Succeed())
				return cur.ResourceVersion
			}, 2*time.Second, interval).Should(Equal(before))
		})
	})

	Context("scaling", func() {
		It("grows the data PVC when storageGB grows and settles back to Ready", func() {
			ns := newNamespace()
			tdb := provisionToReady(ns, "checkout")

			setStorage(tdb, 30)
			Eventually(pvcStorage(ns, dataPVCName(tdb)), timeout, interval).Should(Equal("30Gi"))
			Eventually(func() int64 { return fetch(tdb).Status.ObservedGeneration }, timeout, interval).Should(Equal(fetch(tdb).Generation))
			Expect(phaseOf(tdb)()).To(Equal(platformv1.PhaseReady))
			Expect(conditionStatus(tdb, platformv1.ConditionReady)()).To(Equal(metav1.ConditionTrue))
		})

		It("holds a requested backup while the PVC is still resizing", func() {
			ns := newNamespace()
			tdb := provisionToReady(ns, "checkout")

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(get(pvc, ns, dataPVCName(tdb))).To(Succeed())
			pvc.Status.Phase = corev1.ClaimBound
			pvc.Status.Conditions = []corev1.PersistentVolumeClaimCondition{{
				Type: corev1.PersistentVolumeClaimResizing, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now(),
			}}
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())

			setStorage(tdb, 30)
			Eventually(pvcStorage(ns, dataPVCName(tdb)), timeout, interval).Should(Equal("30Gi"))
			Eventually(conditionReason(tdb, platformv1.ConditionReady), timeout, interval).Should(Equal(platformv1.ReasonScaling))

			annotate(tdb, platformv1.AnnotationBackup, platformv1.AnnotationBackupNow)
			Consistently(activeJob(tdb, operationBackup), 3*time.Second, interval).Should(BeNil())
			Expect(phaseOf(tdb)()).To(Equal(platformv1.PhaseReady))
			Eventually(conditionReason(tdb, platformv1.ConditionProgressing), timeout, interval).Should(Equal(platformv1.ReasonReconcileConflict))

			Expect(get(pvc, ns, dataPVCName(tdb))).To(Succeed())
			pvc.Status.Conditions = nil
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())

			Eventually(activeJob(tdb, operationBackup), timeout, interval).ShouldNot(BeNil())
			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseBackingUp))
		})
	})

	Context("backup", func() {
		It("runs an on-demand backup through Backing Up and records lastBackupTime", func() {
			ns := newNamespace()
			tdb := provisionToReady(ns, "checkout")

			annotate(tdb, platformv1.AnnotationBackup, platformv1.AnnotationBackupNow)
			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseBackingUp))
			Eventually(func() string { return fetch(tdb).Annotations[platformv1.AnnotationBackup] }, timeout, interval).Should(BeEmpty())

			var job *batchv1.Job
			Eventually(func() *batchv1.Job { job = activeJob(tdb, operationBackup)(); return job }, timeout, interval).ShouldNot(BeNil())
			Expect(ownedByTenantDatabase(job, tdb)).To(BeTrue())
			container := job.Spec.Template.Spec.Containers[0]
			Expect(container.Image).To(Equal("postgres:16"))
			Expect(container.Args).To(HaveLen(1))
			Expect(container.Args[0]).To(ContainSubstring("pg_dump"))
			Expect(job.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName).To(Equal(backupsPVCName(tdb)))

			svc := &corev1.Service{}
			Expect(get(svc, ns, serviceName(tdb))).To(Succeed())
			Expect(labels.SelectorFromSet(svc.Spec.Selector).Matches(labels.Set(job.Spec.Template.Labels))).To(BeFalse(),
				"a Job pod must never be selected by the database Service")
			sts := &appsv1.StatefulSet{}
			Expect(get(sts, ns, statefulSetName(tdb))).To(Succeed())
			Expect(labels.SelectorFromSet(svc.Spec.Selector).Matches(labels.Set(sts.Spec.Template.Labels))).To(BeTrue())

			finishJob(job, batchv1.JobComplete)
			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseReady))
			done := fetch(tdb)
			Expect(done.Status.LastBackupTime).NotTo(BeNil())
			Expect(done.Status.LastBackupTime.Time).To(BeTemporally("~", time.Now(), time.Minute))
			Expect(meta.FindStatusCondition(done.Status.Conditions, platformv1.ConditionProgressing).Reason).To(Equal(platformv1.ReasonReconciled))

			Eventually(auditOutcomes(tdb, audit.ActionBackup), timeout, interval).Should(HaveLen(2))
			started := auditEvent(tdb, audit.ActionBackup, "Started")
			succeeded := auditEvent(tdb, audit.ActionBackup, "Succeeded")
			Expect(started.Details["backupId"]).To(Equal(job.Annotations[annotationBackupID]))
			Expect(succeeded.Details["backupId"]).To(Equal(started.Details["backupId"]))
			Expect(started.EventID).NotTo(Equal(succeeded.EventID))
		})

		It("returns to Ready with BackupFailed when the backup Job fails", func() {
			ns := newNamespace()
			tdb := provisionToReady(ns, "checkout")

			annotate(tdb, platformv1.AnnotationBackup, platformv1.AnnotationBackupNow)
			var job *batchv1.Job
			Eventually(func() *batchv1.Job { job = activeJob(tdb, operationBackup)(); return job }, timeout, interval).ShouldNot(BeNil())
			finishJob(job, batchv1.JobFailed)

			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseReady))
			Eventually(conditionReason(tdb, platformv1.ConditionProgressing), timeout, interval).Should(Equal(platformv1.ReasonBackupFailed))
			Expect(fetch(tdb).Status.LastBackupTime).To(BeNil())
			Expect(conditionStatus(tdb, platformv1.ConditionReady)()).To(Equal(metav1.ConditionTrue))
		})

		It("starts a scheduled backup when the cron schedule falls due", func() {
			ns := newNamespace()
			tdb := newTenantDatabase(ns, "checkout")
			tdb.Spec.BackupSchedule = "0 3 * * *"
			Expect(k8sClient.Create(ctx, tdb)).To(Succeed())
			markStatefulSetReady(tdb)
			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseReady))

			Consistently(activeJob(tdb, operationBackup), 2*time.Second, interval).Should(BeNil())

			testClock.Advance(25 * time.Hour)
			annotate(tdb, "test/poke", "1")
			Eventually(activeJob(tdb, operationBackup), timeout, interval).ShouldNot(BeNil())
			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseBackingUp))
		})
	})

	Context("restore", func() {
		It("runs a restore through Restoring and back to Ready", func() {
			ns := newNamespace()
			tdb := provisionToReady(ns, "checkout")

			annotate(tdb, platformv1.AnnotationRestoreFrom, platformv1.RestoreFromLatest)
			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseRestoring))
			Eventually(func() string { return fetch(tdb).Annotations[platformv1.AnnotationRestoreFrom] }, timeout, interval).Should(BeEmpty())

			var job *batchv1.Job
			Eventually(func() *batchv1.Job { job = activeJob(tdb, operationRestore)(); return job }, timeout, interval).ShouldNot(BeNil())
			Expect(job.Spec.Template.Spec.Containers[0].Args[0]).To(ContainSubstring("pg_restore"))

			finishJob(job, batchv1.JobComplete)
			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseReady))
		})

		It("sends a failed restore to Failed, which is terminal", func() {
			ns := newNamespace()
			tdb := provisionToReady(ns, "checkout")

			annotate(tdb, platformv1.AnnotationRestoreFrom, "20260101T000000Z")
			var job *batchv1.Job
			Eventually(func() *batchv1.Job { job = activeJob(tdb, operationRestore)(); return job }, timeout, interval).ShouldNot(BeNil())
			Expect(job.Spec.Template.Spec.Containers[0].Args[0]).To(ContainSubstring("20260101T000000Z"))
			finishJob(job, batchv1.JobFailed)

			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseFailed))
			Expect(conditionReason(tdb, platformv1.ConditionReady)()).To(Equal(platformv1.ReasonRestoreFailed))

			setStorage(tdb, 50)
			Consistently(pvcStorage(ns, dataPVCName(tdb)), 3*time.Second, interval).Should(Equal("20Gi"))
			Expect(phaseOf(tdb)()).To(Equal(platformv1.PhaseFailed))
		})
	})

	Context("concurrency", func() {
		It("defers a scale-up that arrives mid-backup and applies it only after the backup completes", func() {
			ns := newNamespace()
			tdb := provisionToReady(ns, "checkout")

			annotate(tdb, platformv1.AnnotationBackup, platformv1.AnnotationBackupNow)
			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseBackingUp))
			var job *batchv1.Job
			Eventually(func() *batchv1.Job { job = activeJob(tdb, operationBackup)(); return job }, timeout, interval).ShouldNot(BeNil())

			setStorage(tdb, 40)

			Eventually(conditionReason(tdb, platformv1.ConditionProgressing), timeout, interval).Should(Equal(platformv1.ReasonReconcileConflict))
			Consistently(pvcStorage(ns, dataPVCName(tdb)), 3*time.Second, interval).Should(Equal("20Gi"),
				"the PVC must not be resized while the backup Job is running")
			Expect(phaseOf(tdb)()).To(Equal(platformv1.PhaseBackingUp))
			Expect(activeJob(tdb, operationBackup)()).NotTo(BeNil(), "the backup Job must still be running")
			Eventually(eventsWithReason(ns, platformv1.ReasonReconcileConflict), timeout, interval).Should(BeNumerically(">=", 1))

			finishJob(job, batchv1.JobComplete)

			Eventually(phaseOf(tdb), timeout, interval).Should(Equal(platformv1.PhaseReady))
			Eventually(pvcStorage(ns, dataPVCName(tdb)), timeout, interval).Should(Equal("40Gi"))
			Eventually(func() int64 { return fetch(tdb).Status.ObservedGeneration }, timeout, interval).Should(Equal(fetch(tdb).Generation))
			Expect(fetch(tdb).Status.LastBackupTime).NotTo(BeNil())
		})
	})

	Context("deletion", func() {
		It("drains a running Job before the finalizer lets the object go", func() {
			ns := newNamespace()
			tdb := provisionToReady(ns, "checkout")

			annotate(tdb, platformv1.AnnotationBackup, platformv1.AnnotationBackupNow)
			var job *batchv1.Job
			Eventually(func() *batchv1.Job { job = activeJob(tdb, operationBackup)(); return job }, timeout, interval).ShouldNot(BeNil())

			Expect(k8sClient.Delete(ctx, fetch(tdb))).To(Succeed())

			Eventually(func() bool {
				return apierrors.IsNotFound(get(&batchv1.Job{}, ns, job.Name))
			}, timeout, interval).Should(BeTrue(), "the running Job is deleted by the finalizer")
			Eventually(func() bool {
				return apierrors.IsNotFound(get(&platformv1.TenantDatabase{}, ns, tdb.Name))
			}, timeout, interval).Should(BeTrue(), "the TenantDatabase is gone once nothing is running")
		})
	})
})
