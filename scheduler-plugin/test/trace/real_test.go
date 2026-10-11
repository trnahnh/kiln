//go:build trace

package trace

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/trnahnh/kiln/scheduler-plugin/internal/plugin"
	"github.com/trnahnh/kiln/scheduler-plugin/internal/pricing"
	"github.com/trnahnh/kiln/scheduler-plugin/internal/scoring"
)

// The same trace through the scheduler binary instead of the model: a real kube-apiserver
// (envtest), kube-scheduler in-process with the CostAware plugin compiled in, the default
// profile and the kiln profile of config/scheduler-config.yaml, one Node per machine and
// one Pod per task. Each arrival is created at its trace time and the driver waits for the
// scheduler's verdict, a binding or an Unschedulable condition, before the trace advances;
// a task the scheduler rejects is deleted and offered again a trace minute later, as the
// model does. Billing is the same ledger, fed by the node the scheduler chose.

const (
	realCreateWorkers = 48
	realWarmupTimeout = 2 * time.Minute
	realVerdictWait   = 5 * time.Minute
	realVerdictPerPod = 2 * time.Second
	// Trace time is driven one epoch at a time (microseconds): the departures due within
	// the epoch are applied, then its arrivals are offered together and every verdict is
	// awaited. One second blurs the order of events inside it; the model keeps the
	// microsecond order, which is the price of pipelining the binary.
	realEpoch = 1_000_000
)

// Both profiles in one scheduler, the kiln one as the production ConfigMap declares it
// with audit and tracing off. The client budget is raised from the defaults so the binary
// is measured, not its client-side throttle.
const realSchedulerConfig = `apiVersion: kubescheduler.config.k8s.io/v1
kind: KubeSchedulerConfiguration
clientConnection:
  kubeconfig: %s
  qps: 1000
  burst: 2000
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
      postBind:
        enabled: [{name: CostAware}]
    pluginConfig:
      - name: CostAware
        args:
          weights: {cost: 50, fragmentation: 30, preemption: 20}
`

type realResult struct {
	result
	// rejected counts Unschedulable verdicts, retries included; retried counts tasks that
	// were offered more than once.
	rejected, retried int
	wall              time.Duration
	// retryVerdicts and retryWait measure the scheduler on the tasks offered again: on a
	// full cluster every attempt walks all the nodes before it can say no.
	retryVerdicts int
	retryWait     time.Duration
	// truncatedAt is the trace time the driver stopped at when the real-time budget ran
	// out, zero when the whole window was replayed.
	truncatedAt int64
}

func TestGoogleTraceRealScheduler(t *testing.T) {
	dir := os.Getenv("KILN_TRACE_DIR")
	if dir == "" {
		t.Skip("KILN_TRACE_DIR unset; run hack/google-trace.sh to fetch the trace sample")
	}
	spotFrac := envFloat(t, "KILN_TRACE_SPOT_FRACTION", 0.5)
	seed := uint64(envFloat(t, "KILN_TRACE_SEED", 1))
	budget := time.Duration(envFloat(t, "KILN_TRACE_REAL_BUDGET_MINUTES", 30) * float64(time.Minute))
	scenarios := strings.Split(envOr("KILN_TRACE_REAL_SCENARIOS", "empty,warm"), ",")

	machines, windowEnd := readMachines(t, filepath.Join(dir, machineEventsFile))
	snapshot, arrivals, windowEnd := readTasks(t, filepath.Join(dir, taskEventsFile), windowEnd)
	arrivals, windowEnd = clampWindow(t, arrivals, windowEnd)
	t.Logf("trace: %d machines, %d tasks running at the start, %d placements in %s", len(machines), len(snapshot), len(arrivals), time.Duration(windowEnd)*time.Microsecond)
	t.Logf("economics (synthetic): spot fraction %.2f, seed %d; real-time budget %s per run", spotFrac, seed, budget)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs, kubeconfig, stop := startAPIServer(t)
	defer stop()

	economics := newCluster(machines, spotFrac, seed)
	byName := createTraceNodes(t, ctx, cs, economics)
	startScheduler(t, ctx, kubeconfig)
	for _, profile := range []string{"default-scheduler", "kiln-scheduler"} {
		waitForWarmupBinding(t, ctx, cs, profile)
	}

	var rows []string
	for _, scenario := range scenarios {
		var base []placement
		label := "empty cluster"
		switch scenario {
		case "empty":
		case "warm":
			base, label = snapshot, "cluster carrying the trace's running tasks"
		default:
			t.Fatalf("unknown scenario %q", scenario)
		}
		var baseline float64
		for _, profile := range []string{"default-scheduler", "kiln-scheduler"} {
			c := newCluster(machines, spotFrac, seed)
			ns := fmt.Sprintf("trace-%s-%s-%d", scenario, profile, time.Now().Unix())
			r := replayReal(t, ctx, cs, ns, profile, c, byName, base, arrivals, windowEnd, budget)
			if profile == "default-scheduler" {
				baseline = r.dollars
			}
			span := "whole window"
			if r.truncatedAt > 0 {
				span = fmt.Sprintf("stopped at %s", time.Duration(r.truncatedAt)*time.Microsecond)
			}
			retryRate := "n/a"
			if r.retryWait > 0 {
				retryRate = fmt.Sprintf("%.1f", float64(r.retryVerdicts)/r.retryWait.Seconds())
			}
			rows = append(rows, fmt.Sprintf("| %s | %s (binary) | %.2f | %+.1f%% | %.1f | %.1f | %d | %d | %d | %.1f%% | %.0f | %d | %d | %s | %s | %s |",
				label, profile, r.dollars, 100*(r.dollars-baseline)/baseline, r.hoursOnDemand, r.hoursSpot, r.machinesTouched, r.violations, r.unplaced, 100*r.meanUtil, r.perSecond, r.rejected, r.retried, retryRate, r.wall.Round(time.Second), span))
			t.Logf("%s / %s: %+v", label, profile, r)
			writeRealReport(t, rows)
		}
	}
	t.Log("\n" + realTable(rows))
}

func realTable(rows []string) string {
	return "| Scenario | Placement | Billed $ (node-occupancy) | vs default | On-demand node-h | Spot node-h | Machines touched | Latency-class on spot | Unplaced | Mean request util of occupied | Placements/s | Unschedulable verdicts | Tasks retried | Retry verdicts/s | Wall time | Span |\n|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n" + strings.Join(rows, "\n") + "\n"
}

// writeRealReport rewrites the report after every run, so the rows that finished survive
// a later run's failure.
func writeRealReport(t *testing.T, rows []string) {
	path := os.Getenv("KILN_TRACE_REAL_REPORT")
	if path == "" {
		return
	}
	if err := os.WriteFile(path, []byte(realTable(rows)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func startAPIServer(t *testing.T) (*kubernetes.Clientset, string, func()) {
	env := &envtest.Environment{}
	if dir := firstEnvtestBinaryDir(); dir != "" && os.Getenv("KUBEBUILDER_ASSETS") == "" {
		env.BinaryAssetsDirectory = dir
	}
	env.ControlPlane.Etcd = &envtest.Etcd{}
	env.ControlPlane.APIServer = &envtest.APIServer{}
	if dir := os.Getenv("KILN_TRACE_REAL_CONTROL_PLANE_LOGS"); dir != "" {
		env.ControlPlane.Etcd.Err = controlPlaneLog(t, dir, "etcd")
		env.ControlPlane.APIServer.Err = controlPlaneLog(t, dir, "kube-apiserver")
	}
	// The scheduler records an Event per binding; with the default hour of retention the
	// API server's periodic object count over 100k Events stalled etcd for ten seconds.
	env.ControlPlane.APIServer.Configure().Append("event-ttl", "1m")
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	cfg.QPS, cfg.Burst = 4000, 8000
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	kc.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest"}
	kc.CurrentContext = "envtest"
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, kubeconfig); err != nil {
		t.Fatal(err)
	}
	return cs, kubeconfig, func() { stopControlPlane(env) }
}

// stopControlPlane stops this test's own kube-apiserver and etcd. envtest signals them
// with SIGTERM, which Windows lacks, so there they are found by the temporary
// directories envtest gave them and killed by process id; a blanket kill by image name
// would take down another test's control plane running alongside.
func stopControlPlane(env *envtest.Environment) {
	// Stop resets the directories it allocated, so the markers are taken first; an empty
	// marker would match every process.
	var markers []string
	for _, dir := range []string{env.ControlPlane.APIServer.CertDir, env.ControlPlane.Etcd.DataDir} {
		if base := filepath.Base(dir); strings.HasPrefix(base, "k8s_test_framework_") {
			markers = append(markers, base)
		}
	}
	err := env.Stop()
	if err == nil || runtime.GOOS != "windows" || len(markers) == 0 {
		return
	}
	var clauses []string
	for _, m := range markers {
		clauses = append(clauses, fmt.Sprintf("$_.CommandLine -like '*%s*'", m))
	}
	script := fmt.Sprintf(`Get-CimInstance Win32_Process | Where-Object { ($_.Name -eq 'kube-apiserver.exe' -or $_.Name -eq 'etcd.exe') -and (%s) } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }`, strings.Join(clauses, " -or "))
	_ = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Run()
}

func controlPlaneLog(t *testing.T, dir, name string) io.Writer {
	f, err := os.Create(filepath.Join(dir, name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

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

func startScheduler(t *testing.T, ctx context.Context, kubeconfig string) {
	configPath := filepath.Join(t.TempDir(), "scheduler.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(realSchedulerConfig, kubeconfig)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := app.NewSchedulerCommand(app.WithPlugin(plugin.Name, plugin.New))
	cmd.SetArgs([]string{"--config", configPath, "--secure-port", "0", "--authentication-skip-lookup=true", "--v=1"})
	go func() {
		if err := cmd.ExecuteContext(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("scheduler exited: %v", err)
		}
	}()
}

func nodeName(id int64) string { return fmt.Sprintf("m%d", id) }

// createTraceNodes registers one Node per machine with the economics labels the plugin
// reads and the trace's capacity as allocatable, set through the status subresource
// because no kubelet will ever report it.
func createTraceNodes(t *testing.T, ctx context.Context, cs *kubernetes.Clientset, c *cluster) map[string]*machine {
	byName := make(map[string]*machine, len(c.machines))
	for _, m := range c.machines {
		byName[nodeName(m.id)] = m
	}
	wall := time.Now()
	parallel(t, ctx, len(c.machines), func(i int) error {
		m := c.machines[i]
		labels := map[string]string{
			pricing.LabelCapacityType: "on-demand",
			pricing.LabelHourlyCost:   fmt.Sprintf("%.6f", m.node.Economics.HourlyCost),
		}
		if m.node.Economics.Spot {
			labels[pricing.LabelCapacityType] = "spot"
			labels[pricing.LabelPreemptionRisk] = fmt.Sprintf("%.2f", m.node.Economics.PreemptionRisk)
		}
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName(m.id), Labels: labels}}
		var created *corev1.Node
		if err := retrying(ctx, func() error {
			var err error
			created, err = cs.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
			return err
		}); err != nil {
			return err
		}
		capacity := corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(m.node.Allocatable.MilliCPU, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(m.node.Allocatable.MemoryBytes, resource.BinarySI),
			corev1.ResourcePods:   resource.MustParse("100000"),
		}
		created.Status = corev1.NodeStatus{
			Capacity: capacity, Allocatable: capacity,
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastHeartbeatTime: metav1.Now(), LastTransitionTime: metav1.Now()}},
		}
		updated, err := cs.CoreV1().Nodes().UpdateStatus(ctx, created, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		// The API server stamps a not-ready NoSchedule taint on a new node; the lifecycle
		// controller that would remove it is not running, so clear it here.
		if len(updated.Spec.Taints) > 0 {
			updated.Spec.Taints = nil
			if _, err := cs.CoreV1().Nodes().Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
		return nil
	})
	t.Logf("registered %d nodes in %s", len(c.machines), time.Since(wall).Round(time.Second))
	return byName
}

// retrying repeats a call on the errors a busy control plane returns for a moment, a
// timeout or an unavailable etcd, so a stall does not end a run of many minutes.
func retrying(ctx context.Context, do func() error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		if err = do(); err == nil || ctx.Err() != nil {
			return err
		}
		msg := err.Error()
		if !strings.Contains(msg, "timed out") && !strings.Contains(msg, "Unavailable") && !strings.Contains(msg, "timeout") && !strings.Contains(msg, "too many requests") {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	return err
}

func parallel(t *testing.T, ctx context.Context, n int, do func(i int) error) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, realCreateWorkers)
	var once sync.Once
	var firstErr error
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := do(i); err != nil {
				once.Do(func() { firstErr = err })
			}
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatal(firstErr)
	}
}

// createNamespace also creates the default ServiceAccount: envtest runs no controller
// manager, and the ServiceAccount admission plugin rejects pods without it.
func createNamespace(t *testing.T, ctx context.Context, cs *kubernetes.Clientset, ns string) {
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns}}
	if _, err := cs.CoreV1().ServiceAccounts(ns).Create(ctx, sa, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func waitForWarmupBinding(t *testing.T, ctx context.Context, cs *kubernetes.Clientset, profile string) {
	ns := fmt.Sprintf("warmup-%s", profile)
	createNamespace(t, ctx, cs, ns)
	pod := tracePod("warm", profile, placement{class: scoring.ClassBatch, req: scoring.Resources{MilliCPU: 1000, MemoryBytes: 1 << 20}})
	if _, err := cs.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(realWarmupTimeout)
	for time.Now().Before(deadline) {
		got, err := cs.CoreV1().Pods(ns).Get(ctx, pod.Name, metav1.GetOptions{})
		if err == nil && got.Spec.NodeName != "" {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s never bound the warmup pod", profile)
}

func tracePod(name, profile string, p placement) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{plugin.LabelWorkloadClass: string(p.class)}},
		Spec: corev1.PodSpec{
			SchedulerName:                 profile,
			TerminationGracePeriodSeconds: ptr.To[int64](0),
			Containers: []corev1.Container{{
				Name: "c", Image: "registry.k8s.io/pause:3.10",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewMilliQuantity(p.req.MilliCPU, resource.DecimalSI),
					corev1.ResourceMemory: *resource.NewQuantity(p.req.MemoryBytes, resource.BinarySI),
				}},
			}},
		},
	}
}

// verdict is what the scheduler decided for one pod: the node it bound it to, or "" for an
// Unschedulable condition.
type verdict struct {
	done chan struct{}
	gone chan struct{}
	once sync.Once
	node string
}

func (v *verdict) resolve(node string) {
	v.once.Do(func() {
		v.node = node
		close(v.done)
	})
}

type verdicts struct {
	mu   sync.Mutex
	byID map[string]*verdict
}

func (vs *verdicts) expect(name string) *verdict {
	v := &verdict{done: make(chan struct{}), gone: make(chan struct{})}
	vs.mu.Lock()
	vs.byID[name] = v
	vs.mu.Unlock()
	return v
}

func (vs *verdicts) forget(name string) {
	vs.mu.Lock()
	delete(vs.byID, name)
	vs.mu.Unlock()
}

func (vs *verdicts) observe(pod *corev1.Pod) {
	vs.mu.Lock()
	v := vs.byID[pod.Name]
	vs.mu.Unlock()
	if v == nil {
		return
	}
	if pod.Spec.NodeName != "" {
		v.resolve(pod.Spec.NodeName)
		return
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			v.resolve("")
			return
		}
	}
}

func (vs *verdicts) deleted(pod *corev1.Pod) {
	vs.mu.Lock()
	v := vs.byID[pod.Name]
	delete(vs.byID, pod.Name)
	vs.mu.Unlock()
	if v != nil {
		close(v.gone)
	}
}

// watchVerdicts follows the namespace's pods through an informer, which survives a dropped
// watch, and feeds every binding, Unschedulable condition and deletion to the verdicts.
func watchVerdicts(t *testing.T, ctx context.Context, cs *kubernetes.Clientset, ns string, vs *verdicts) {
	factory := informers.NewSharedInformerFactoryWithOptions(cs, 0, informers.WithNamespace(ns))
	inf := factory.Core().V1().Pods().Informer()
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { vs.observe(obj.(*corev1.Pod)) },
		UpdateFunc: func(_, obj any) { vs.observe(obj.(*corev1.Pod)) },
		DeleteFunc: func(obj any) {
			if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = d.Obj
			}
			vs.deleted(obj.(*corev1.Pod))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), inf.HasSynced) {
		t.Fatal("pod informer never synced")
	}
}

func podNameOf(p placement, attempt int) string {
	return "t" + strings.ReplaceAll(p.key, "/", "-") + fmt.Sprintf("-r%d", attempt)
}

// replayReal drives the arrivals through the running scheduler. Trace time advances one
// epoch at a time: tasks due to leave are deleted and their deletions awaited, pending
// tasks are offered again when a trace minute has passed, then every task arriving in the
// epoch is created and the scheduler's verdict on each is awaited.
func replayReal(t *testing.T, ctx context.Context, cs *kubernetes.Clientset, ns, profile string, c *cluster, byName map[string]*machine, snapshot, arrivals []placement, windowEnd int64, budget time.Duration) realResult {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	createNamespace(t, ctx, cs, ns)
	vs := &verdicts{byID: map[string]*verdict{}}
	watchVerdicts(t, ctx, cs, ns, vs)
	pods := cs.CoreV1().Pods(ns)
	l := newLedger()
	names := map[string]string{}
	attempts := map[string]int{}
	var res realResult

	if len(snapshot) > 0 {
		wall := time.Now()
		placed := make([]bool, len(snapshot))
		parallel(t, ctx, len(snapshot), func(i int) error {
			p := snapshot[i]
			m := c.byID[p.borgMachine]
			if m == nil {
				return nil
			}
			pod := tracePod(podNameOf(p, 0), profile, p)
			pod.Spec.NodeName = nodeName(m.id)
			if err := retrying(ctx, func() error { _, err := pods.Create(ctx, pod, metav1.CreateOptions{}); return err }); err != nil {
				return err
			}
			placed[i] = true
			return nil
		})
		for i, p := range snapshot {
			if placed[i] {
				names[p.key] = podNameOf(p, 0)
				l.admit(0, c.byID[p.borgMachine], p)
			}
		}
		t.Logf("%s: %d running tasks created pre-bound in %s", ns, len(snapshot), time.Since(wall).Round(time.Second))
	}

	deleteAll := func(departed []departure) {
		if len(departed) == 0 {
			return
		}
		waits := make([]*verdict, len(departed))
		parallel(t, ctx, len(departed), func(i int) error {
			name := names[departed[i].p.key]
			waits[i] = vs.expect(name)
			return retrying(ctx, func() error {
				err := pods.Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)})
				if err != nil && strings.Contains(err.Error(), "not found") {
					return nil
				}
				return err
			})
		})
		for _, w := range waits {
			select {
			case <-w.gone:
			case <-time.After(realVerdictWait):
				t.Fatalf("%s: a departing pod was never seen deleted", ns)
			}
		}
	}

	// offer creates the batch and waits for every verdict; it returns the tasks rejected.
	// The whole batch shares one deadline that grows with its size, since the scheduler
	// takes the pods one at a time.
	offer := func(now int64, batch []placement, retry bool) []placement {
		waits := make([]*verdict, len(batch))
		parallel(t, ctx, len(batch), func(i int) error {
			p := batch[i]
			name := podNameOf(p, attempts[p.key])
			waits[i] = vs.expect(name)
			return retrying(ctx, func() error {
				_, err := pods.Create(ctx, tracePod(name, profile, p), metav1.CreateOptions{})
				if err != nil && strings.Contains(err.Error(), "already exists") {
					return nil
				}
				return err
			})
		})
		started := time.Now()
		deadline := time.After(realVerdictWait + time.Duration(len(batch))*realVerdictPerPod)
		var rejected []placement
		for i, w := range waits {
			select {
			case <-w.done:
			case <-deadline:
				t.Fatalf("%s: no verdict on %s, %d of %d in the batch still open after %s", ns, podNameOf(batch[i], attempts[batch[i].key]), len(batch)-i, len(batch), time.Since(started).Round(time.Second))
			}
			p := batch[i]
			name := podNameOf(p, attempts[p.key])
			if w.node == "" {
				res.rejected++
				vs.forget(name)
				_ = pods.Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)})
				attempts[p.key]++
				rejected = append(rejected, p)
				continue
			}
			m := byName[w.node]
			if m == nil {
				t.Fatalf("%s bound to unknown node %s", name, w.node)
			}
			names[p.key] = name
			l.record(now, m, p, windowEnd)
		}
		if retry {
			res.retryVerdicts += len(batch)
			res.retryWait += time.Since(started)
		}
		return rejected
	}

	var pending []placement
	nextRetry := int64(retryEvery)
	wall := time.Now()
	deadline := wall.Add(budget)
	nextLog := len(arrivals) / 10
	for i := 0; i < len(arrivals); {
		now := arrivals[i].start
		if time.Now().After(deadline) {
			res.truncatedAt = now
			t.Logf("%s: real-time budget spent at trace time %s with %d of %d arrivals offered", ns, time.Duration(now)*time.Microsecond, i, len(arrivals))
			windowEnd = now
			break
		}
		epochEnd := (now/realEpoch + 1) * realEpoch
		deleteAll(l.drain(epochEnd - 1))
		if now >= nextRetry && len(pending) > 0 {
			retry := pending
			pending = offer(now, retry, true)
			nextRetry = now + retryEvery
		}
		j := i
		for j < len(arrivals) && arrivals[j].start < epochEnd {
			j++
		}
		pending = append(pending, offer(now, arrivals[i:j], false)...)
		i = j
		if i >= nextLog {
			t.Logf("%s: %d of %d arrivals offered, %d pending, %s elapsed", ns, i, len(arrivals), len(pending), time.Since(wall).Round(time.Second))
			nextLog += len(arrivals) / 10
		}
	}
	res.wall = time.Since(wall)
	l.res.perSecond = float64(l.res.placed) / res.wall.Seconds()
	l.res.unplaced = len(pending)
	for _, n := range attempts {
		if n > 0 {
			res.retried++
		}
	}
	res.result = l.close(c, windowEnd)

	if err := pods.DeleteCollection(ctx, metav1.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)}, metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	for {
		list, err := pods.List(ctx, metav1.ListOptions{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Items) == 0 {
			break
		}
		time.Sleep(time.Second)
	}
	time.Sleep(3 * time.Second)
	return res
}
