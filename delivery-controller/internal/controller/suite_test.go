package controller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/trnahnh/kiln/audit"
	platformv1 "github.com/trnahnh/kiln/delivery-controller/api/v1"
	"github.com/trnahnh/kiln/delivery-controller/internal/mesh"
	"github.com/trnahnh/kiln/slo"
)

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
	source    = &fakeSource{}
	auditLog  = &audit.Recorder{}
)

// fakeSource plays Prometheus: every read advances the cumulative counters by the
// configured window, so each analysis tick sees one more window of the scripted traffic.
type fakeSource struct {
	mu      sync.Mutex
	window  slo.Counters
	current slo.Counters
	fail    bool
	reads   int
}

func (f *fakeSource) Counters(_ context.Context, _ slo.Target) (slo.Counters, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.fail {
		return slo.Counters{}, context.DeadlineExceeded
	}
	f.current.Requests += f.window.Requests
	f.current.Errors += f.window.Errors
	f.current.Slow += f.window.Slow
	return f.current, nil
}

func (f *fakeSource) script(window slo.Counters, fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.window = window
	f.fail = fail
}

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())

	Expect(platformv1.AddToScheme(scheme.Scheme)).To(Succeed())

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("..", "..", "test", "testdata"),
		},
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

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	Expect(err).NotTo(HaveOccurred())

	reconciler := &CanaryRolloutReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("canaryrollout"),
		Metrics:  source,
		Router:   &mesh.Istio{Client: mgr.GetClient()},
		Audit:    auditLog,
	}
	Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()
})

var _ = AfterSuite(func() {
	cancel()
	err := testEnv.Stop()
	if err != nil && runtime.GOOS == "windows" {
		// envtest stops its control plane with SIGTERM, which Windows lacks.
		err = exec.Command("taskkill", "/F", "/IM", "kube-apiserver.exe", "/IM", "etcd.exe").Run()
	}
	Expect(err).NotTo(HaveOccurred())
})

// Lets `go test` find the binaries without KUBEBUILDER_ASSETS after `make setup-envtest`.
func firstEnvtestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
