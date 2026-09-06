package controller

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/trnahnh/kiln/audit"
	platformv1 "github.com/trnahnh/kiln/chaos/api/v1"
	"github.com/trnahnh/kiln/chaos/internal/agent"
	"github.com/trnahnh/kiln/chaos/internal/fault"
	"github.com/trnahnh/kiln/slo"
)

const nodeName = "node-a"

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
	source    = &fakeSource{}
	injector  = &fakeInjector{}
	ledgerDir string
	auditLog  = &audit.Recorder{}
)

// fakeSource plays Prometheus, per namespace so concurrent experiments in the suite do not
// read each other's traffic: every read advances that namespace's cumulative counters by
// its scripted window.
type fakeSource struct {
	mu      sync.Mutex
	window  map[string]slo.Counters
	current map[string]slo.Counters
	failing map[string]bool
}

func (f *fakeSource) Counters(_ context.Context, t slo.Target) (slo.Counters, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing[t.Namespace] {
		return slo.Counters{}, context.DeadlineExceeded
	}
	w, ok := f.window[t.Namespace]
	if !ok {
		w = slo.Counters{Requests: 100}
	}
	c := f.current[t.Namespace]
	c.Requests += w.Requests
	c.Errors += w.Errors
	c.Slow += w.Slow
	if f.current == nil {
		f.current = map[string]slo.Counters{}
	}
	f.current[t.Namespace] = c
	return c, nil
}

func (f *fakeSource) script(ns string, window slo.Counters, fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.window == nil {
		f.window, f.current, f.failing = map[string]slo.Counters{}, map[string]slo.Counters{}, map[string]bool{}
	}
	f.window[ns], f.failing[ns] = window, fail
}

// fakeInjector is the agent's fault mechanism stood in: it records which pods it was asked
// to fault and revert, keyed by namespace and pod. It is the ground truth the tests read,
// never the CR's status.
type fakeInjector struct {
	mu       sync.Mutex
	applied  map[string]agent.Request
	reverted map[string]bool
	failNS   map[string]bool
}

func (f *fakeInjector) failIn(ns string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNS == nil {
		f.failNS = map[string]bool{}
	}
	f.failNS[ns] = true
}

func key(ns, pod string) string { return ns + "/" + pod }

func (f *fakeInjector) Apply(_ context.Context, r agent.Request) (fault.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNS[r.Namespace] {
		return fault.Entry{}, fmt.Errorf("synthetic injection failure")
	}
	if f.applied == nil {
		f.applied = map[string]agent.Request{}
	}
	f.applied[key(r.Namespace, r.Pod)] = r
	return fault.Entry{
		Namespace: r.Namespace, Experiment: r.Experiment, ExperimentUID: r.ExperimentUID,
		Pod: r.Pod, PodUID: r.PodUID, Kind: string(r.FaultType), Deadline: r.Deadline, AppliedAt: time.Now(),
	}, nil
}

func (f *fakeInjector) Revert(_ context.Context, e fault.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reverted == nil {
		f.reverted = map[string]bool{}
	}
	f.reverted[key(e.Namespace, e.Pod)] = true
	delete(f.applied, key(e.Namespace, e.Pod))
	return nil
}

func (f *fakeInjector) appliedPods(ns string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k, r := range f.applied {
		if r.Namespace == ns {
			out = append(out, k[len(ns)+1:])
		}
	}
	return out
}

func (f *fakeInjector) wasReverted(ns, pod string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reverted[key(ns, pod)]
}

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Chaos Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())
	ledgerDir = GinkgoT().TempDir()

	Expect(platformv1.AddToScheme(scheme.Scheme)).To(Succeed())

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if dir := firstEnvtestBinaryDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme.Scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	Expect(err).NotTo(HaveOccurred())

	Expect((&Reconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorderFor("chaos-controller"),
		Metrics:  source,
		LeaseTTL: 1500 * time.Millisecond,
		Audit:    auditLog,
	}).SetupWithManager(mgr)).To(Succeed())

	Expect((&agent.Reconciler{
		Client:   mgr.GetClient(),
		Injector: injector,
		Ledger:   fault.Ledger{Dir: ledgerDir},
		Recorder: mgr.GetEventRecorderFor("chaos-agent"),
		NodeName: nodeName,
		NodeIP:   "10.0.0.1",
	}).SetupWithManager(mgr)).To(Succeed())

	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()
})

var _ = AfterSuite(func() {
	cancel()
	err := testEnv.Stop()
	if err != nil && runtime.GOOS == "windows" {
		err = exec.Command("taskkill", "/F", "/IM", "kube-apiserver.exe", "/IM", "etcd.exe").Run()
	}
	Expect(err).NotTo(HaveOccurred())
})

func firstEnvtestBinaryDir() string {
	base := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(base, e.Name())
		}
	}
	return ""
}

// makePod creates a Ready pod on nodeName with a running container, so the controller and
// agent see a real, schedulable target; envtest runs no kubelet, so the test sets status.
func makePod(ns, name, app string, cpuLimit bool) *corev1.Pod {
	c := corev1.Container{Name: "app", Image: "busybox"}
	if cpuLimit {
		c.Resources = corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}}
	}
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"app": app}},
		Spec:       corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{c}},
	}
	Expect(k8sClient.Create(ctx, p)).To(Succeed())
	p.Status = corev1.PodStatus{
		Phase:      corev1.PodRunning,
		HostIP:     "10.0.0.1",
		Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		ContainerStatuses: []corev1.ContainerStatus{{
			Name: "app", Ready: true, ContainerID: "containerd://" + name,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}},
		}},
	}
	Expect(k8sClient.Status().Update(ctx, p)).To(Succeed())
	return p
}

func experiment(ns, name, faultType string, cap int32, errorRateMax float64) *platformv1.ChaosExperiment {
	return &platformv1.ChaosExperiment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: platformv1.ChaosExperimentSpec{
			Target:           platformv1.TargetSpec{LabelSelector: "app=target", MaxReplicaPercentage: cap},
			FaultType:        platformv1.FaultType(faultType),
			Duration:         metav1.Duration{Duration: 3 * time.Second},
			AbortOnSLOBreach: platformv1.SLO{ErrorRateMax: errorRateMax, LatencyP99MaxMs: 1000},
			Analysis: &platformv1.AnalysisSpec{
				Interval:        &metav1.Duration{Duration: 300 * time.Millisecond},
				MinSampleSize:   ptr.To(int32(10)),
				RecoveryWindows: ptr.To(int32(2)),
			},
		},
	}
}

func getCR(ns, name string) *platformv1.ChaosExperiment {
	cr := &platformv1.ChaosExperiment{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, cr)).To(Succeed())
	return cr
}
