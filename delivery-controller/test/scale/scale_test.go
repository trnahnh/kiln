//go:build scale

// Controller scalability under envtest: a real kube-apiserver, the reconciler as main.go
// wires it with the production client budget and worker count unless overridden, a
// scripted healthy metric source, and a stand-in for the Deployment controller that
// reports every Deployment rolled out. For each size, N rollouts are created and
// initialised, then every target is updated and the wave of analyses is measured end to
// end: reconcile latency from the reconciler's own clock, how late each 15 s analysis
// tick fires, queue depth, the API server's request rate by verb and resource, and the
// live heap of the process.
package scale

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"go.uber.org/zap/zapcore"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/trnahnh/kiln/audit"
	"github.com/trnahnh/kiln/audit/outbox"
	platformv1 "github.com/trnahnh/kiln/delivery-controller/api/v1"
	"github.com/trnahnh/kiln/delivery-controller/internal/controller"
	"github.com/trnahnh/kiln/delivery-controller/internal/mesh"
	"github.com/trnahnh/kiln/slo"
)

const (
	scaleInterval   = 15 * time.Second
	scaleDrainGrace = 2 * time.Second
	harnessWorkers  = 32
	// The window every metric read advances by: enough to clear minSampleSize at each
	// tick, with errors at a tenth of the limit.
	windowRequests = 3000
	windowErrors   = 3
)

type fakeSource struct {
	mu      sync.Mutex
	current map[slo.Target]slo.Counters
	reads   atomic.Int64
}

func (f *fakeSource) Counters(_ context.Context, target slo.Target) (slo.Counters, error) {
	f.reads.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.current[target]
	c.Requests += windowRequests
	c.Errors += windowErrors
	c.Slow += windowErrors
	f.current[target] = c
	return c, nil
}

// rolloutFaker plays the Deployment controller: whenever a Deployment's spec moves ahead
// of its status, it reports the generation observed and every replica available.
type rolloutFaker struct {
	cs     *kubernetes.Clientset
	writes atomic.Int64
	work   chan string
}

func rolledOut(d *appsv1.Deployment) bool {
	if d.Spec.Replicas == nil || d.Status.ObservedGeneration < d.Generation {
		return false
	}
	n := *d.Spec.Replicas
	return d.Status.Replicas == n && d.Status.UpdatedReplicas == n && d.Status.AvailableReplicas == n
}

func (f *rolloutFaker) start(t *testing.T, ctx context.Context, factory informers.SharedInformerFactory) {
	f.work = make(chan string, 100000)
	inf := factory.Apps().V1().Deployments().Informer()
	enqueue := func(obj any) {
		if d, ok := obj.(*appsv1.Deployment); ok && !rolledOut(d) {
			f.work <- d.Namespace + "/" + d.Name
		}
	}
	if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{AddFunc: enqueue, UpdateFunc: func(_, obj any) { enqueue(obj) }}); err != nil {
		t.Fatal(err)
	}
	for range harnessWorkers {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case key := <-f.work:
					ns, name, _ := strings.Cut(key, "/")
					f.report(ctx, ns, name)
				}
			}
		}()
	}
}

func (f *rolloutFaker) report(ctx context.Context, ns, name string) {
	for attempt := 0; attempt < 5; attempt++ {
		d, err := f.cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil || rolledOut(d) {
			return
		}
		n := *d.Spec.Replicas
		d.Status.ObservedGeneration = d.Generation
		d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas = n, n, n, n
		f.writes.Add(1)
		if _, err := f.cs.AppsV1().Deployments(ns).UpdateStatus(ctx, d, metav1.UpdateOptions{}); err == nil {
			return
		}
	}
}

// rolloutTracker follows every CanaryRollout's phase and analysis clock through an
// informer, so the wave's completion and each tick's lateness come from the API server's
// own stream rather than from the controller.
type rolloutTracker struct {
	mu       sync.Mutex
	phase    map[string]platformv1.Phase
	lastTick map[string]time.Time
	rolling  map[string]bool
	done     map[string]bool
	lateness []time.Duration
	armed    bool
}

func newTracker() *rolloutTracker {
	return &rolloutTracker{phase: map[string]platformv1.Phase{}, lastTick: map[string]time.Time{}, rolling: map[string]bool{}, done: map[string]bool{}}
}

func (tr *rolloutTracker) start(t *testing.T, ctx context.Context, cfg *rest.Config) {
	w, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	list := &platformv1.CanaryRolloutList{}
	if err := w.List(ctx, list); err != nil {
		t.Fatal(err)
	}
	for i := range list.Items {
		tr.observe(nil, &list.Items[i])
	}
	go func() {
		rv := list.ResourceVersion
		for ctx.Err() == nil {
			wi, err := w.Watch(ctx, &platformv1.CanaryRolloutList{}, &client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: rv, AllowWatchBookmarks: true}})
			if err != nil {
				time.Sleep(time.Second)
				continue
			}
			for ev := range wi.ResultChan() {
				cr, ok := ev.Object.(*platformv1.CanaryRollout)
				if !ok {
					continue
				}
				rv = cr.ResourceVersion
				if ev.Type == "BOOKMARK" {
					continue
				}
				tr.observe(nil, cr)
			}
		}
	}()
}

func (tr *rolloutTracker) observe(_ any, cr *platformv1.CanaryRollout) {
	key := cr.Namespace + "/" + cr.Name
	tr.mu.Lock()
	defer tr.mu.Unlock()
	prev := tr.phase[key]
	tr.phase[key] = cr.Status.Phase
	if tr.armed {
		switch {
		case cr.Status.Phase != platformv1.PhaseSucceeded && cr.Status.Phase != "":
			tr.rolling[key] = true
		case tr.rolling[key] && cr.Status.Phase == platformv1.PhaseSucceeded:
			tr.done[key] = true
		}
		if prev == platformv1.PhaseAnalyzing && cr.Status.Phase == platformv1.PhaseAnalyzing && cr.Status.Analysis != nil && cr.Status.Analysis.LastTickAt != nil {
			if last, ok := tr.lastTick[key]; ok && !cr.Status.Analysis.LastTickAt.Time.Equal(last) {
				tr.lateness = append(tr.lateness, cr.Status.Analysis.LastTickAt.Time.Sub(last)-scaleInterval)
			}
		}
	}
	if cr.Status.Analysis != nil && cr.Status.Analysis.LastTickAt != nil {
		tr.lastTick[key] = cr.Status.Analysis.LastTickAt.Time
	}
}

func (tr *rolloutTracker) count(phase platformv1.Phase, prefix string) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := 0
	for key, p := range tr.phase {
		if p == phase && strings.HasPrefix(key, prefix) {
			n++
		}
	}
	return n
}

func (tr *rolloutTracker) arm() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.armed = true
	tr.rolling = map[string]bool{}
	tr.done = map[string]bool{}
	tr.lateness = nil
}

func (tr *rolloutTracker) finished(prefix string) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := 0
	for key := range tr.done {
		if strings.HasPrefix(key, prefix) {
			n++
		}
	}
	return n
}

type reconcileLog struct {
	mu    sync.Mutex
	took  []time.Duration
	count atomic.Int64
}

func (l *reconcileLog) observe(_ ctrl.Request, took time.Duration) {
	l.count.Add(1)
	l.mu.Lock()
	l.took = append(l.took, took)
	l.mu.Unlock()
}

func (l *reconcileLog) reset() []time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.took
	l.took = nil
	return out
}

func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := slices.Clone(d)
	slices.Sort(s)
	i := int(float64(len(s)-1) * p)
	return s[i]
}

// apiCounts reads apiserver_request_total from the API server's own metrics endpoint,
// keyed by verb, resource and subresource.
func apiCounts(t *testing.T, ctx context.Context, cs *kubernetes.Clientset) map[string]float64 {
	raw, err := cs.Discovery().RESTClient().Get().AbsPath("/metrics").DoRaw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	fam := families["apiserver_request_total"]
	if fam == nil {
		return out
	}
	for _, m := range fam.Metric {
		labels := map[string]string{}
		for _, l := range m.Label {
			labels[l.GetName()] = l.GetValue()
		}
		key := labels["verb"] + " " + labels["resource"]
		if labels["subresource"] != "" {
			key += "/" + labels["subresource"]
		}
		out[key] += m.Counter.GetValue()
	}
	return out
}

func queueDepth() float64 {
	families, err := ctrlmetrics.Registry.Gather()
	if err != nil {
		return 0
	}
	for _, f := range families {
		if f.GetName() != "workqueue_depth" {
			continue
		}
		for _, m := range f.Metric {
			for _, l := range m.Label {
				if l.GetName() == "name" && l.GetValue() == "canaryrollout" {
					return gaugeValue(m)
				}
			}
		}
	}
	return 0
}

func gaugeValue(m *dto.Metric) float64 {
	if m.Gauge != nil {
		return m.Gauge.GetValue()
	}
	return 0
}

type waveResult struct {
	n, workers           int
	qps, burst           float64
	perNamespace         int
	initWave, rollout    time.Duration
	finished             int
	reconciles           int
	reconcileP50, p99    time.Duration
	lateP50, lateP99     time.Duration
	lateMax              time.Duration
	ticks                int
	queueMax, queueMean  float64
	controllerWrites     float64
	harnessWrites        float64
	reads                float64
	liveHeapMB           float64
	apiserverRSSMB       float64
	apiByKey             map[string]float64
	controllerWritesKeys []string
}

func TestCanaryRolloutScale(t *testing.T) {
	sizes := parseInts(t, envOr("KILN_SCALE_ROLLOUTS", "500,1000,2000"))
	perNamespace := int(envFloat(t, "KILN_SCALE_PER_NAMESPACE", 20))
	workers := int(envFloat(t, "KILN_SCALE_WORKERS", 1))
	qps := float32(envFloat(t, "KILN_SCALE_QPS", 20))
	burst := int(envFloat(t, "KILN_SCALE_BURST", 30))
	stagger := time.Duration(envFloat(t, "KILN_SCALE_STAGGER_SECONDS", 30) * float64(time.Second))
	waveTimeout := time.Duration(envFloat(t, "KILN_SCALE_WAVE_TIMEOUT_MINUTES", 15) * float64(time.Minute))

	logf.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.Level(zapcore.ErrorLevel)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := platformv1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatal(err)
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases"), filepath.Join("..", "..", "test", "testdata")},
		ErrorIfCRDPathMissing: true,
	}
	if dir := firstEnvtestBinaryDir(); dir != "" && os.Getenv("KUBEBUILDER_ASSETS") == "" {
		env.BinaryAssetsDirectory = dir
	}
	// Three ClusterIP Services per rollout; envtest's default /24 holds 254 addresses.
	env.ControlPlane.APIServer = &envtest.APIServer{}
	env.ControlPlane.APIServer.Configure().Append("service-cluster-ip-range", "10.0.0.0/12")
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	defer stopControlPlane(env)

	// The controller gets the budget main.go would: controller-runtime's 20 qps and 30
	// burst unless flags say otherwise. The harness gets its own, generous one.
	ctrlCfg := rest.CopyConfig(cfg)
	ctrlCfg.QPS, ctrlCfg.Burst = qps, burst
	harnessCfg := rest.CopyConfig(cfg)
	harnessCfg.QPS, harnessCfg.Burst = 2000, 4000
	cs := kubernetes.NewForConfigOrDie(harnessCfg)
	hc, err := client.New(harnessCfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatal(err)
	}

	source := &fakeSource{current: map[slo.Target]slo.Counters{}}
	rlog := &reconcileLog{}
	startManager(t, ctx, ctrlCfg, source, rlog, workers)
	faker := &rolloutFaker{cs: cs}
	factory := informers.NewSharedInformerFactory(cs, 0)
	faker.start(t, ctx, factory)
	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())
	tracker := newTracker()
	tracker.start(t, ctx, harnessCfg)

	var rows []waveResult
	for _, n := range sizes {
		r := runWave(t, ctx, cs, hc, tracker, faker, rlog, n, perNamespace, stagger, waveTimeout)
		r.workers, r.qps, r.burst = workers, float64(qps), float64(burst)
		rows = append(rows, r)
		t.Logf("%d rollouts: %+v", n, r)
	}
	table := renderTable(rows)
	t.Log("\n" + table)
	if path := os.Getenv("KILN_SCALE_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(table), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func startManager(t *testing.T, ctx context.Context, cfg *rest.Config, source slo.Source, rlog *reconcileLog, workers int) ctrl.Manager {
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme.Scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		t.Fatal(err)
	}
	drainer, err := audit.NewDrainer(audit.DrainerOptions{
		Deliverer: audit.Discard{},
		Store: outbox.Store[platformv1.CanaryRollout, *platformv1.CanaryRollout]{
			Client: mgr.GetClient(),
			List:   func(obj *platformv1.CanaryRollout) *[]audit.Pending { return &obj.Status.Audit.Pending },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Add(drainer); err != nil {
		t.Fatal(err)
	}
	r := &controller.CanaryRolloutReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("canaryrollout"),
		Metrics:  source,
		Router:   &mesh.Istio{Client: mgr.GetClient()},
		Outbox:   drainer,
		Observe:  rlog.observe,
		Workers:  workers,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := mgr.Start(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("manager exited: %v", err)
		}
	}()
	return mgr
}

func namespaceOf(n, i, perNamespace int) string {
	return fmt.Sprintf("scale-%d-%03d", n, i/perNamespace)
}

func runWave(t *testing.T, ctx context.Context, cs *kubernetes.Clientset, hc client.Client, tracker *rolloutTracker, faker *rolloutFaker, rlog *reconcileLog, n, perNamespace int, stagger, waveTimeout time.Duration) waveResult {
	res := waveResult{n: n, perNamespace: perNamespace}
	prefix := fmt.Sprintf("scale-%d-", n)
	namespaces := (n + perNamespace - 1) / perNamespace
	parallel(t, ctx, namespaces, func(i int) error {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("scale-%d-%03d", n, i)}}
		_, err := cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		return err
	})

	wall := time.Now()
	parallel(t, ctx, n, func(i int) error {
		ns, name := namespaceOf(n, i, perNamespace), fmt.Sprintf("app-%05d", i)
		labels := map[string]string{"app": name}
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Name: "http", Port: 8080}}}}
		if _, err := cs.CoreV1().Services(ns).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
			return err
		}
		if _, err := cs.AppsV1().Deployments(ns).Create(ctx, deployment(ns, name, "v1"), metav1.CreateOptions{}); err != nil {
			return err
		}
		cr := &platformv1.CanaryRollout{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: platformv1.CanaryRolloutSpec{
				TargetDeployment: name,
				SuccessCriteria:  platformv1.SuccessCriteria{ErrorRateMax: 0.01, LatencyP99MaxMs: 300, MinSampleSize: 500},
				StepPercentages:  []int32{5, 20, 50, 100},
				Analysis: &platformv1.AnalysisSpec{
					Interval:   metav1.Duration{Duration: scaleInterval},
					DrainGrace: &metav1.Duration{Duration: scaleDrainGrace},
				},
			},
		}
		return hc.Create(ctx, cr)
	})
	created := time.Since(wall)

	waitFor(t, func() int { return tracker.count(platformv1.PhaseSucceeded, prefix) }, n, waveTimeout, "initialised")
	res.initWave = time.Since(wall)
	t.Logf("%d rollouts created in %s and initialised in %s", n, created.Round(time.Second), res.initWave.Round(time.Second))
	rlog.reset()

	before := apiCounts(t, ctx, cs)
	tracker.arm()
	var depths []float64
	sampling, stopSampling := context.WithCancel(ctx)
	var samplingDone sync.WaitGroup
	samplingDone.Add(1)
	go func() {
		defer samplingDone.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-sampling.Done():
				return
			case <-tick.C:
				depths = append(depths, queueDepth())
			}
		}
	}()

	wall = time.Now()
	pause := stagger / time.Duration(max(1, n))
	for i := 0; i < n; i++ {
		ns, name := namespaceOf(n, i, perNamespace), fmt.Sprintf("app-%05d", i)
		d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		patch := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"server","image":"fortio/fortio:v2"}]}}}}`)
		if err := hc.Patch(ctx, d, client.RawPatch(types.StrategicMergePatchType, patch)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(pause)
	}
	res.finished = waitFor(t, func() int { return tracker.finished(prefix) }, n, waveTimeout, "promoted")
	res.rollout = time.Since(wall)
	stopSampling()
	samplingDone.Wait()

	after := apiCounts(t, ctx, cs)
	res.apiByKey = map[string]float64{}
	for k, v := range after {
		if d := v - before[k]; d > 0 {
			res.apiByKey[k] = d / res.rollout.Seconds()
		}
	}
	for k, v := range res.apiByKey {
		verb := strings.ToLower(strings.Fields(k)[0])
		switch {
		case strings.HasSuffix(k, "deployments/status") && verb == "put":
			res.harnessWrites += v
		case verb == "get" || verb == "list" || verb == "watch":
			res.reads += v
		case strings.Contains(k, "canaryrollouts") || strings.Contains(k, "deployments") || strings.Contains(k, "services") || strings.Contains(k, "virtualservices") || strings.Contains(k, "events"):
			res.controllerWrites += v
			res.controllerWritesKeys = append(res.controllerWritesKeys, k)
		}
	}
	took := rlog.reset()
	res.reconciles = len(took)
	res.reconcileP50, res.p99 = percentile(took, 0.5), percentile(took, 0.99)
	tracker.mu.Lock()
	late := slices.Clone(tracker.lateness)
	tracker.mu.Unlock()
	res.ticks = len(late)
	res.lateP50, res.lateP99, res.lateMax = percentile(late, 0.5), percentile(late, 0.99), percentile(late, 1)
	for _, d := range depths {
		res.queueMax = max(res.queueMax, d)
		res.queueMean += d
	}
	if len(depths) > 0 {
		res.queueMean /= float64(len(depths))
	}
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	res.liveHeapMB = float64(ms.HeapInuse) / (1 << 20)
	res.apiserverRSSMB = processRSSMB("kube-apiserver")

	cleanup(t, ctx, hc, n, perNamespace)
	return res
}

func cleanup(t *testing.T, ctx context.Context, hc client.Client, n, perNamespace int) {
	namespaces := (n + perNamespace - 1) / perNamespace
	for i := 0; i < namespaces; i++ {
		ns := fmt.Sprintf("scale-%d-%03d", n, i)
		vs := &unstructured.Unstructured{}
		vs.SetGroupVersionKind(mesh.VirtualServiceGVK)
		for _, obj := range []client.Object{&platformv1.CanaryRollout{}, vs, &appsv1.Deployment{}, &corev1.Service{}} {
			if err := hc.DeleteAllOf(ctx, obj, client.InNamespace(ns)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func waitFor(t *testing.T, count func() int, want int, timeout time.Duration, what string) int {
	deadline := time.Now().Add(timeout)
	lastLog := time.Now()
	for {
		n := count()
		if n >= want {
			return n
		}
		if time.Now().After(deadline) {
			t.Logf("timeout: %d of %d %s after %s", n, want, what, timeout)
			return n
		}
		if time.Since(lastLog) > 30*time.Second {
			t.Logf("%d of %d %s", n, want, what)
			lastLog = time.Now()
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func deployment(ns, name, version string) *appsv1.Deployment {
	labels := map[string]string{"app": name}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(2)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "server",
					Image: "fortio/fortio:" + version,
					Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
				}}},
			},
		},
	}
}

func parallel(t *testing.T, ctx context.Context, n int, do func(i int) error) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, harnessWorkers)
	var once sync.Once
	var firstErr error
	for i := 0; i < n; i++ {
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

func processRSSMB(name string) float64 {
	if runtime.GOOS != "windows" {
		return 0
	}
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+name+".exe", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return 0
	}
	m := regexp.MustCompile(`"([0-9,.]+) K"`).FindStringSubmatch(string(out))
	if m == nil {
		return 0
	}
	kb, err := strconv.ParseFloat(strings.NewReplacer(",", "", ".", "").Replace(m[1]), 64)
	if err != nil {
		return 0
	}
	return kb / 1024
}

func renderTable(rows []waveResult) string {
	var b strings.Builder
	b.WriteString("| Rollouts | Workers | Client qps/burst | Init wave | Rollout wave | Finished | Reconciles | Reconcile p50 / p99 | Tick lateness p50 / p99 / max (of 15 s) | Ticks | Queue depth max / mean | Controller writes/s | Harness writes/s | Reads/s | Live heap MB | kube-apiserver RSS MB |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %d | %d | %.0f/%.0f | %s | %s | %d | %d | %s / %s | %s / %s / %s | %d | %.0f / %.1f | %.1f | %.1f | %.1f | %.0f | %.0f |\n",
			r.n, r.workers, r.qps, r.burst, r.initWave.Round(time.Second), r.rollout.Round(time.Second), r.finished, r.reconciles,
			r.reconcileP50.Round(time.Millisecond), r.p99.Round(time.Millisecond), r.lateP50.Round(time.Second), r.lateP99.Round(time.Second), r.lateMax.Round(time.Second), r.ticks,
			r.queueMax, r.queueMean, r.controllerWrites, r.harnessWrites, r.reads, r.liveHeapMB, r.apiserverRSSMB)
	}
	b.WriteString("\nAPI server requests per second during the rollout wave, by verb and resource:\n\n")
	for _, r := range rows {
		keys := make([]string, 0, len(r.apiByKey))
		for k := range r.apiByKey {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, c string) int {
			switch {
			case r.apiByKey[a] > r.apiByKey[c]:
				return -1
			case r.apiByKey[a] < r.apiByKey[c]:
				return 1
			}
			return strings.Compare(a, c)
		})
		fmt.Fprintf(&b, "- %d rollouts:", r.n)
		for i, k := range keys {
			if i == 12 {
				break
			}
			fmt.Fprintf(&b, " %s %.1f;", k, r.apiByKey[k])
		}
		b.WriteString("\n")
	}
	return b.String()
}

func parseInts(t *testing.T, s string) []int {
	var out []int
	for _, f := range strings.Split(s, ",") {
		v, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			t.Fatalf("KILN_SCALE_ROLLOUTS: %v", err)
		}
		out = append(out, v)
	}
	return out
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envFloat(t *testing.T, name string, def float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return f
}

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
