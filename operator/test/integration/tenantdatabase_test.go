//go:build integration

// Runs against the current kubeconfig context (a kind cluster) with the manager in-process.
// Ground truth is the cluster itself: PVC spec, Job completion, and row counts read through
// psql inside the database pod, never status.phase alone (CLAUDE.md, Verification).
package integration

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	platformv1 "github.com/trnahnh/kiln/operator/api/v1"
	"github.com/trnahnh/kiln/operator/internal/controller"
)

const (
	provisionTimeout = 5 * time.Minute
	jobTimeout       = 3 * time.Minute
	poll             = time.Second

	// Long enough that the scale request provably lands while pg_dump has not run yet.
	backupHold = 45 * time.Second
	rowCount   = 10000
)

// delayedBackups keeps the real pg_dump but holds the Job open first, so the collision
// window is deterministic instead of depending on how fast a tiny database dumps.
type delayedBackups struct {
	controller.Commands
	hold time.Duration
}

func (d delayedBackups) BackupScript(backupID string) string {
	return fmt.Sprintf("sleep %d\n%s", int(d.hold.Seconds()), d.Commands.BackupScript(backupID))
}

type harness struct {
	t         *testing.T
	g         *WithT
	ctx       context.Context
	cfg       *rest.Config
	k8s       client.Client
	clientset *kubernetes.Clientset
	ns        string
}

func TestTenantDatabaseLifecycle(t *testing.T) {
	g := NewWithT(t)
	logf.SetLogger(zap.New(zap.UseDevMode(true)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := ctrl.GetConfigOrDie()
	g.Expect(platformv1.AddToScheme(scheme.Scheme)).To(Succeed())
	_, err := envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{
		Paths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
	})
	g.Expect(err).NotTo(HaveOccurred())

	k8s, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	g.Expect(err).NotTo(HaveOccurred())
	clientset, err := kubernetes.NewForConfig(cfg)
	g.Expect(err).NotTo(HaveOccurred())

	h := &harness{t: t, g: g, ctx: ctx, cfg: cfg, k8s: k8s, clientset: clientset}
	h.allowVolumeExpansion("standard")
	h.startManager()

	h.ns = fmt.Sprintf("kiln-it-%d", time.Now().Unix())
	g.Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.ns}})).To(Succeed())
	defer func() {
		_ = k8s.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.ns}})
	}()

	tdb := &platformv1.TenantDatabase{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: h.ns},
		Spec: platformv1.TenantDatabaseSpec{
			Engine:    platformv1.EnginePostgres,
			Version:   "16",
			StorageGB: 1,
			Tier:      platformv1.TierStandard,
		},
	}
	g.Expect(k8s.Create(ctx, tdb)).To(Succeed())

	t.Log("provision: waiting for Ready and for Postgres to answer queries")
	g.Eventually(h.phase(tdb), provisionTimeout, poll).Should(Equal(platformv1.PhaseReady))
	g.Eventually(func() error { _, err := h.sql(tdb, "SELECT 1"); return err }, 2*time.Minute, poll).Should(Succeed())
	g.Expect(h.pvcSize(tdb)).To(Equal("1Gi"))

	_, err = h.sql(tdb, fmt.Sprintf("CREATE TABLE t (id int PRIMARY KEY); INSERT INTO t SELECT generate_series(1, %d)", rowCount))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(h.count(tdb)).To(Equal(rowCount))

	t.Log("backup: trigger, then scale while the Job is held open")
	h.annotate(tdb, platformv1.AnnotationBackup, platformv1.AnnotationBackupNow)
	g.Eventually(h.phase(tdb), time.Minute, poll).Should(Equal(platformv1.PhaseBackingUp))
	var job *batchv1.Job
	g.Eventually(func() *batchv1.Job { job = h.activeJob(tdb, "backup"); return job }, time.Minute, poll).ShouldNot(BeNil())

	h.setStorage(tdb, 2)
	g.Eventually(h.conditionReason(tdb, platformv1.ConditionProgressing), time.Minute, poll).Should(Equal(platformv1.ReasonReconcileConflict))
	g.Consistently(func() string { return h.pvcSize(tdb) }, 15*time.Second, poll).Should(Equal("1Gi"),
		"the data PVC must not change while the backup Job runs")
	g.Expect(h.phase(tdb)()).To(Equal(platformv1.PhaseBackingUp))
	g.Expect(h.jobFinished(job)).To(BeFalse(), "the backup Job must still be running when the scale is deferred")

	t.Log("backup completes, then the deferred scale applies")
	g.Eventually(func() bool { return h.jobFinished(job) }, jobTimeout, poll).Should(BeTrue())
	g.Expect(h.jobSucceeded(job)).To(BeTrue(), "backup Job must succeed")
	g.Eventually(h.phase(tdb), time.Minute, poll).Should(Equal(platformv1.PhaseReady))
	g.Eventually(func() string { return h.pvcSize(tdb) }, time.Minute, poll).Should(Equal("2Gi"))
	g.Eventually(func() bool {
		cur := h.fetch(tdb)
		return cur.Status.ObservedGeneration == cur.Generation && meta.IsStatusConditionTrue(cur.Status.Conditions, platformv1.ConditionReady)
	}, time.Minute, poll).Should(BeTrue())
	g.Expect(h.fetch(tdb).Status.LastBackupTime).NotTo(BeNil())
	g.Expect(h.count(tdb)).To(Equal(rowCount), "no rows lost across the scale/backup collision")
	g.Expect(h.eventCount(platformv1.ReasonReconcileConflict)).To(BeNumerically(">=", 1))

	t.Log("restore: damage the table, restore latest, count rows")
	_, err = h.sql(tdb, fmt.Sprintf("DELETE FROM t WHERE id > %d", rowCount/2))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(h.count(tdb)).To(Equal(rowCount / 2))

	h.annotate(tdb, platformv1.AnnotationRestoreFrom, platformv1.RestoreFromLatest)
	g.Eventually(h.phase(tdb), time.Minute, poll).Should(Equal(platformv1.PhaseRestoring))
	g.Eventually(h.phase(tdb), jobTimeout, poll).Should(Equal(platformv1.PhaseReady))
	g.Expect(h.count(tdb)).To(Equal(rowCount), "restore must bring back every row from the backup")

	t.Log("delete: every owned object disappears with the CR")
	g.Expect(k8s.Delete(ctx, h.fetch(tdb))).To(Succeed())
	g.Eventually(func() bool {
		return client.IgnoreNotFound(k8s.Get(ctx, client.ObjectKeyFromObject(tdb), &platformv1.TenantDatabase{})) == nil &&
			k8s.Get(ctx, client.ObjectKeyFromObject(tdb), &platformv1.TenantDatabase{}) != nil
	}, time.Minute, poll).Should(BeTrue())
	g.Eventually(func() int {
		pvcs := &corev1.PersistentVolumeClaimList{}
		g.Expect(k8s.List(ctx, pvcs, client.InNamespace(h.ns))).To(Succeed())
		return len(pvcs.Items)
	}, 2*time.Minute, poll).Should(Equal(0), "data and backup PVCs are garbage-collected through owner references")
}

func (h *harness) startManager() {
	mgr, err := ctrl.NewManager(h.cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	h.g.Expect(err).NotTo(HaveOccurred())
	r := &controller.TenantDatabaseReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("tenantdatabase"),
		Commands: delayedBackups{Commands: controller.PostgresCommands{}, hold: backupHold},
	}
	h.g.Expect(r.SetupWithManager(mgr)).To(Succeed())
	go func() {
		if err := mgr.Start(h.ctx); err != nil {
			h.t.Errorf("manager stopped: %v", err)
		}
	}()
}

// kind's local-path StorageClass ships without allowVolumeExpansion; the API server would
// otherwise reject the resize the scale path depends on.
func (h *harness) allowVolumeExpansion(name string) {
	sc := &storagev1.StorageClass{}
	h.g.Expect(h.k8s.Get(h.ctx, types.NamespacedName{Name: name}, sc)).To(Succeed())
	if sc.AllowVolumeExpansion != nil && *sc.AllowVolumeExpansion {
		return
	}
	base := sc.DeepCopy()
	sc.AllowVolumeExpansion = ptr.To(true)
	h.g.Expect(h.k8s.Patch(h.ctx, sc, client.MergeFrom(base))).To(Succeed())
}

func (h *harness) fetch(tdb *platformv1.TenantDatabase) *platformv1.TenantDatabase {
	out := &platformv1.TenantDatabase{}
	h.g.Expect(h.k8s.Get(h.ctx, client.ObjectKeyFromObject(tdb), out)).To(Succeed())
	return out
}

func (h *harness) phase(tdb *platformv1.TenantDatabase) func() platformv1.Phase {
	return func() platformv1.Phase { return h.fetch(tdb).Status.Phase }
}

func (h *harness) conditionReason(tdb *platformv1.TenantDatabase, condType string) func() string {
	return func() string {
		c := meta.FindStatusCondition(h.fetch(tdb).Status.Conditions, condType)
		if c == nil {
			return ""
		}
		return c.Reason
	}
}

func (h *harness) annotate(tdb *platformv1.TenantDatabase, key, value string) {
	h.g.Eventually(func() error {
		cur := h.fetch(tdb)
		if cur.Annotations == nil {
			cur.Annotations = map[string]string{}
		}
		cur.Annotations[key] = value
		return h.k8s.Update(h.ctx, cur)
	}, time.Minute, poll).Should(Succeed())
}

func (h *harness) setStorage(tdb *platformv1.TenantDatabase, gb int32) {
	h.g.Eventually(func() error {
		cur := h.fetch(tdb)
		cur.Spec.StorageGB = gb
		return h.k8s.Update(h.ctx, cur)
	}, time.Minute, poll).Should(Succeed())
}

func (h *harness) pvcSize(tdb *platformv1.TenantDatabase) string {
	pvc := &corev1.PersistentVolumeClaim{}
	h.g.Expect(h.k8s.Get(h.ctx, types.NamespacedName{Namespace: tdb.Namespace, Name: tdb.Name + "-data"}, pvc)).To(Succeed())
	q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	return q.String()
}

func (h *harness) activeJob(tdb *platformv1.TenantDatabase, operation string) *batchv1.Job {
	jobs := &batchv1.JobList{}
	h.g.Expect(h.k8s.List(h.ctx, jobs, client.InNamespace(tdb.Namespace),
		client.MatchingLabels{"platform.internal/tenantdatabase": tdb.Name, "platform.internal/operation": operation})).To(Succeed())
	for i := range jobs.Items {
		if !h.jobFinished(&jobs.Items[i]) {
			return &jobs.Items[i]
		}
	}
	return nil
}

func (h *harness) jobFinished(job *batchv1.Job) bool {
	cur := &batchv1.Job{}
	if err := h.k8s.Get(h.ctx, client.ObjectKeyFromObject(job), cur); err != nil {
		return false
	}
	for _, c := range cur.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (h *harness) jobSucceeded(job *batchv1.Job) bool {
	cur := &batchv1.Job{}
	h.g.Expect(h.k8s.Get(h.ctx, client.ObjectKeyFromObject(job), cur)).To(Succeed())
	for _, c := range cur.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (h *harness) eventCount(reason string) int {
	events := &corev1.EventList{}
	h.g.Expect(h.k8s.List(h.ctx, events, client.InNamespace(h.ns))).To(Succeed())
	n := 0
	for _, e := range events.Items {
		if e.Reason == reason {
			n++
		}
	}
	return n
}

func (h *harness) count(tdb *platformv1.TenantDatabase) int {
	out, err := h.sql(tdb, "SELECT count(*) FROM t")
	h.g.Expect(err).NotTo(HaveOccurred())
	n, err := strconv.Atoi(strings.TrimSpace(out))
	h.g.Expect(err).NotTo(HaveOccurred(), "psql output %q", out)
	return n
}

// sql runs psql inside the database pod so the check does not depend on network access
// from the test host.
func (h *harness) sql(tdb *platformv1.TenantDatabase, statement string) (string, error) {
	req := h.clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(tdb.Namespace).Name(tdb.Name + "-0").
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "postgres",
			Command:   []string{"psql", "-U", "postgres", "-tA", "-c", statement},
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(h.cfg, "POST", req.URL())
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	if err := exec.StreamWithContext(h.ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr}); err != nil {
		return "", fmt.Errorf("psql %q: %w: %s", statement, err, stderr.String())
	}
	return stdout.String(), nil
}
