//go:build perf

// Placement-latency benchmark in the spirit of upstream scheduler_perf, which is not
// importable: a real kube-apiserver (envtest), the real scheduler in-process with both a
// default profile and the kiln profile, synthetic nodes, and a burst of pods per profile.
// Latency is measured from pod creation to the bind observed through a watch.
package perf

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/trnahnh/kiln/scheduler-plugin/internal/plugin"
)

const (
	nodeCount = 200
	podCount  = 500
)

const schedulerConfig = `apiVersion: kubescheduler.config.k8s.io/v1
kind: KubeSchedulerConfiguration
clientConnection:
  kubeconfig: %s
  qps: 500
  burst: 1000
leaderElection:
  leaderElect: false
profiles:
  - schedulerName: default-scheduler
  - schedulerName: kiln-scheduler
    plugins:
      filter:
        enabled: [{name: CostAware}]
      preScore:
        enabled: [{name: CostAware}]
      score:
        enabled: [{name: CostAware, weight: 5}]
        disabled: [{name: NodeResourcesFit}, {name: NodeResourcesBalancedAllocation}]
    pluginConfig:
      - name: CostAware
        args:
          weights: {cost: 50, fragmentation: 30, preemption: 20}
`

func BenchmarkPlacementLatency(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		b.Fatalf("start envtest: %v", err)
	}
	defer func() { _ = env.Stop() }()

	kubeconfig := writeKubeconfig(b, cfg.Host, cfg.CAData, cfg.CertData, cfg.KeyData)
	configPath := filepath.Join(b.TempDir(), "scheduler.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(schedulerConfig, kubeconfig)), 0o600); err != nil {
		b.Fatal(err)
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		b.Fatal(err)
	}
	createNodes(b, ctx, cs, nodeCount)

	cmd := app.NewSchedulerCommand(app.WithPlugin(plugin.Name, plugin.New))
	cmd.SetArgs([]string{"--config", configPath, "--secure-port", "0", "--authentication-skip-lookup=true", "--v=1"})
	go func() {
		if err := cmd.ExecuteContext(ctx); err != nil && ctx.Err() == nil {
			b.Errorf("scheduler exited: %v", err)
		}
	}()
	waitForBinding(b, ctx, cs, "default-scheduler", "warmup")

	for _, scheduler := range []string{"default-scheduler", "kiln-scheduler"} {
		b.Run(scheduler, func(b *testing.B) {
			for b.Loop() {
				ns := fmt.Sprintf("perf-%s-%d", scheduler, time.Now().UnixNano())
				createNamespace(b, ctx, cs, ns)
				latencies := placeBurst(b, ctx, cs, ns, scheduler, podCount)
				sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
				total := latencies[len(latencies)-1]
				b.ReportMetric(float64(latencies[len(latencies)/2].Milliseconds()), "p50_ms")
				b.ReportMetric(float64(latencies[len(latencies)*99/100].Milliseconds()), "p99_ms")
				b.ReportMetric(float64(podCount)/total.Seconds(), "pods/s")
			}
		})
	}
}

// placeBurst creates pods as fast as the API allows and returns creation-to-bind latency
// for each, measured from the pod's creationTimestamp to the watch event carrying nodeName.
func placeBurst(b *testing.B, ctx context.Context, cs *kubernetes.Clientset, ns, scheduler string, n int) []time.Duration {
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	w, err := cs.CoreV1().Pods(ns).Watch(watchCtx, metav1.ListOptions{})
	if err != nil {
		b.Fatal(err)
	}
	created := make(map[string]time.Time, n)
	for i := 0; i < n; i++ {
		class := []string{"latency-sensitive", "standard", "batch"}[i%3]
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p-%04d", i), Namespace: ns, Labels: map[string]string{plugin.LabelWorkloadClass: class}},
			Spec: corev1.PodSpec{SchedulerName: scheduler, Containers: []corev1.Container{{
				Name: "c", Image: "registry.k8s.io/pause:3.10",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi"),
				}},
			}}},
		}
		if _, err := cs.CoreV1().Pods(ns).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			b.Fatal(err)
		}
		created[p.Name] = time.Now()
	}
	latencies := make([]time.Duration, 0, n)
	bound := map[string]bool{}
	deadline := time.After(5 * time.Minute)
	for len(bound) < n {
		select {
		case ev, ok := <-w.ResultChan():
			if !ok {
				b.Fatal("watch closed early")
			}
			p, isPod := ev.Object.(*corev1.Pod)
			if !isPod || p.Spec.NodeName == "" || bound[p.Name] {
				continue
			}
			bound[p.Name] = true
			latencies = append(latencies, time.Since(created[p.Name]))
		case <-deadline:
			b.Fatalf("%s: only %d/%d pods bound within 5 minutes", scheduler, len(bound), n)
		}
	}
	return latencies
}

func waitForBinding(b *testing.B, ctx context.Context, cs *kubernetes.Clientset, scheduler, ns string) {
	createNamespace(b, ctx, cs, ns)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if warmupBound(b, ctx, cs, ns, scheduler) {
			return
		}
		time.Sleep(time.Second)
	}
	b.Fatal("scheduler never bound the warmup pod")
}

// createNamespace also creates the default ServiceAccount: envtest runs no controller
// manager, and the ServiceAccount admission plugin rejects pods without it.
func createNamespace(b *testing.B, ctx context.Context, cs *kubernetes.Clientset, ns string) {
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
		b.Fatal(err)
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns}}
	if _, err := cs.CoreV1().ServiceAccounts(ns).Create(ctx, sa, metav1.CreateOptions{}); err != nil {
		b.Fatal(err)
	}
}

func warmupBound(b *testing.B, ctx context.Context, cs *kubernetes.Clientset, ns, scheduler string) bool {
	name := fmt.Sprintf("warm-%d", time.Now().UnixNano())
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{plugin.LabelWorkloadClass: "batch"}},
		Spec:       corev1.PodSpec{SchedulerName: scheduler, Containers: []corev1.Container{{Name: "c", Image: "registry.k8s.io/pause:3.10"}}},
	}
	if _, err := cs.CoreV1().Pods(ns).Create(ctx, p, metav1.CreateOptions{}); err != nil {
		b.Logf("warmup pod create: %v", err)
		return false
	}
	for i := 0; i < 20; i++ {
		got, err := cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil && got.Spec.NodeName != "" {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// createNodes registers synthetic nodes, alternating spot and on-demand, with allocatable
// set through the status subresource because no kubelet will ever report it.
func createNodes(b *testing.B, ctx context.Context, cs *kubernetes.Clientset, n int) {
	for i := 0; i < n; i++ {
		labels := map[string]string{"kiln.platform.internal/capacity-type": "on-demand", "kiln.platform.internal/hourly-cost": "0.10"}
		if i%2 == 0 {
			labels = map[string]string{"kiln.platform.internal/capacity-type": "spot", "kiln.platform.internal/hourly-cost": "0.03", "kiln.platform.internal/preemption-risk": "0.2"}
		}
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%03d", i), Labels: labels}}
		created, err := cs.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
		if err != nil {
			b.Fatal(err)
		}
		created.Status = corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("32"), corev1.ResourceMemory: resource.MustParse("128Gi"), corev1.ResourcePods: resource.MustParse("250"),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("32"), corev1.ResourceMemory: resource.MustParse("128Gi"), corev1.ResourcePods: resource.MustParse("250"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastHeartbeatTime: metav1.Now(), LastTransitionTime: metav1.Now()}},
		}
		updated, err := cs.CoreV1().Nodes().UpdateStatus(ctx, created, metav1.UpdateOptions{})
		if err != nil {
			b.Fatal(err)
		}
		// The API server stamps a not-ready NoSchedule taint on a new node; the lifecycle
		// controller that would remove it is not running, so clear it here.
		if len(updated.Spec.Taints) > 0 {
			updated.Spec.Taints = nil
			if _, err := cs.CoreV1().Nodes().Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func writeKubeconfig(b *testing.B, host string, ca, cert, key []byte) string {
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: host, CertificateAuthorityData: ca}
	kc.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: cert, ClientKeyData: key}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest"}
	kc.CurrentContext = "envtest"
	path := filepath.Join(b.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		b.Fatal(err)
	}
	return path
}
