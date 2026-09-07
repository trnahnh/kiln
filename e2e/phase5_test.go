//go:build e2e

// Phase 5 exit criterion against a live cluster: an experiment against a test service
// produces a resilience score, and a forced SLO breach auto-aborts with the fault actually
// removed. Every safety assertion is read from the node itself, the tc qdisc and the
// iptables rules inside the target pod's own network namespace, never from the
// ChaosExperiment's status.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	corev1res "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	chaosTargetApp = "chaos-target"
	chaosLoadApp   = "chaos-load"
	// The abort bound: every injected fault must be gone this soon after the breach is
	// visible. Prometheus scrapes every 15 s and the controller polls every 5 s, so the
	// end-to-end observed clearance runs to roughly 45 s; the assertion allows margin.
	abortClearBound = 60 * time.Second
)

var gvkChaos = schema.GroupVersionKind{Group: "platform.internal", Version: "v1", Kind: "ChaosExperiment"}

type chaosHarness struct {
	t   *testing.T
	g   *WithT
	ctx context.Context
	cfg *rest.Config
	c   client.Client
	cs  *kubernetes.Clientset
	ns  string
	// createExperiment replaces the direct create; Phase 7 submits through the REST path.
	createExperiment func(*unstructured.Unstructured)
	// prometheus is the port-forwarded query base; set, every experiment waits for the
	// target's traffic to be clean before it starts (see waitForCleanWindow).
	prometheus string
}

func TestPhase5Chaos(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := ctrl.GetConfigOrDie()
	c, err := client.New(cfg, client.Options{})
	g.Expect(err).NotTo(HaveOccurred())
	cs, err := kubernetes.NewForConfig(cfg)
	g.Expect(err).NotTo(HaveOccurred())

	h := &chaosHarness{t: t, g: g, ctx: ctx, cfg: cfg, c: c, cs: cs, ns: "chaos-e2e"}
	h.waitForChaos()
	h.forwardPrometheus()
	h.createMeshNamespace()
	t.Cleanup(h.deleteNamespace)
	h.deployTarget()

	t.Run("network partition breach auto-aborts and the rule is cleared on the node", h.testPartitionBreachAborts)
	t.Run("latency injection aborts and leaves no tc rule on the node", h.testLatencyAborts)
	t.Run("network partition stays within the blast radius", h.testPartitionContained)
	t.Run("pod-kill produces a score and stops on completion", h.testPodKillScored)
	t.Run("resource-exhaustion produces a score", h.testExhaustionScored)
}

func (h *chaosHarness) waitForChaos() {
	h.t.Log("waiting for prometheus, istiod, the chaos controller and the agent daemonset")
	for _, d := range []types.NamespacedName{
		{Namespace: "monitoring", Name: "prometheus"},
		{Namespace: "istio-system", Name: "istiod"},
		{Namespace: "kiln-chaos-system", Name: "kiln-chaos-controller"},
	} {
		h.g.Eventually(func() int32 {
			dep := &appsv1.Deployment{}
			if err := h.c.Get(h.ctx, d, dep); err != nil {
				return 0
			}
			return dep.Status.AvailableReplicas
		}, platformReady, poll).Should(BeNumerically(">=", 1), "%s is not available", d.Name)
	}
	h.g.Eventually(func() int32 {
		ds := &appsv1.DaemonSet{}
		if err := h.c.Get(h.ctx, types.NamespacedName{Namespace: "kiln-chaos-system", Name: "kiln-chaos-agent"}, ds); err != nil {
			return 0
		}
		return ds.Status.NumberReady
	}, platformReady, poll).Should(BeNumerically(">=", 1), "the chaos agent daemonset has no ready pods")
	h.g.Eventually(func() string { return h.appSyncStatus("kiln-chaos") }, platformReady, poll).Should(Equal("Synced"))
}

func (h *chaosHarness) appSyncStatus(name string) string {
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(gvkApp)
	if err := h.c.Get(h.ctx, types.NamespacedName{Namespace: argocdNS, Name: name}, app); err != nil {
		return ""
	}
	v, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
	return v
}

func (h *chaosHarness) createMeshNamespace() {
	_ = h.c.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.ns}})
	h.g.Eventually(func() bool {
		err := h.c.Get(h.ctx, types.NamespacedName{Name: h.ns}, &corev1.Namespace{})
		return err != nil
	}, 2*time.Minute, poll).Should(BeTrue(), "a stale namespace from a previous run must clear first")
	h.g.Expect(h.c.Create(h.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   h.ns,
		Labels: map[string]string{"istio-injection": "enabled"},
	}})).To(Succeed())
}

func (h *chaosHarness) deleteNamespace() {
	_ = h.c.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.ns}})
}

func (h *chaosHarness) deployTarget() {
	h.g.Expect(h.c.Create(h.ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: chaosTargetApp, Namespace: h.ns},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": chaosTargetApp},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080)}},
		},
	})).To(Succeed())
	// Four replicas so a 50% cap selects two pods and a 30% cap selects one, and each pod
	// carries a CPU limit so resource-exhaustion has a cgroup to confine a burner to.
	dep := chaosFortio(h.ns, chaosTargetApp, 4)
	h.g.Expect(h.c.Create(h.ctx, dep)).To(Succeed())
	h.g.Expect(h.c.Create(h.ctx, chaosFortio(h.ns, chaosLoadApp, 1,
		"load", "-qps", "80", "-c", "8", "-t", "0", "-allow-initial-errors", "http://"+chaosTargetApp+":8080/"))).To(Succeed())

	h.g.Eventually(func() int32 { return h.readyReplicas(chaosTargetApp) }, platformReady, poll).Should(Equal(int32(4)), "target not ready")
	h.g.Eventually(func() int32 { return h.readyReplicas(chaosLoadApp) }, platformReady, poll).Should(BeNumerically(">=", 1))
	// Let Istio counters accumulate a baseline before any experiment reads them.
	time.Sleep(20 * time.Second)
}

func chaosFortio(ns, name string, replicas int32, args ...string) *appsv1.Deployment {
	if len(args) == 0 {
		args = []string{"server"}
	}
	labels := map[string]string{"app": name}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "fortio",
					Image: fortioImage,
					Args:  args,
					Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: corev1res.MustParse("50m")},
						Limits:   corev1.ResourceList{corev1.ResourceCPU: corev1res.MustParse("200m")},
					},
				}}},
			},
		},
	}
}

// testPartitionBreachAborts is the headline forced-breach proof and works on any kernel:
// partitioning the whole service breaks its SLO, and the abort is real only when the
// iptables rule is gone from the node.
func (h *chaosHarness) testPartitionBreachAborts(t *testing.T) {
	g := NewWithT(t)
	name := "partition-breach"
	h.applyExperiment(map[string]any{
		"target":           map[string]any{"labelSelector": "app=" + chaosTargetApp, "maxReplicaPercentage": int64(100)},
		"faultType":        "network-partition",
		"duration":         "120s",
		"abortOnSLOBreach": map[string]any{"errorRateMax": 0.2, "latencyP99MaxMs": int64(1000)},
		"analysis":         map[string]any{"interval": "5s"},
	}, name)
	defer h.deleteExperiment(name)

	var targets []string
	g.Eventually(func() []string { targets = h.targetPods(name); return targets }, 2*time.Minute, poll).Should(HaveLen(4), "100% of four pods is four")

	// Ground truth: the DROP chain is actually present on a targeted pod during the fault.
	g.Eventually(func() bool { return h.hasDropChain(targets[0]) }, 90*time.Second, 2*time.Second).Should(BeTrue(), "partition never applied to %s", targets[0])
	t.Logf("partition observed on %v", targets)

	// The forced breach aborts the experiment.
	g.Eventually(func() string { return h.abortReason(name) }, 3*time.Minute, poll).Should(Equal("SLOBreach"), "a full partition did not trip the abort")
	breachSeen := time.Now()

	// The abort is real only if the fault is gone from every targeted pod, read on the node.
	for _, pod := range targets {
		g.Eventually(func() bool { return h.hasDropChain(pod) }, abortClearBound, 2*time.Second).Should(BeFalse(), "partition still on %s after abort", pod)
	}
	t.Logf("all partition rules cleared %s after the breach was visible", time.Since(breachSeen).Round(time.Second))
	g.Expect(h.phase(name)).To(Equal("Aborted"))
	g.Expect(h.score(name)).To(Equal(0.0))
}

// testLatencyAborts exercises the latency fault. Where the kernel has sch_netem the 2s delay
// trips the SLO breach; where it does not the injection fails and the controller aborts
// rather than scoring a run that never happened. Either way the experiment aborts and leaves
// no tc rule on the node.
func (h *chaosHarness) testLatencyAborts(t *testing.T) {
	g := NewWithT(t)
	name := "latency"
	h.applyExperiment(map[string]any{
		"target":           map[string]any{"labelSelector": "app=" + chaosTargetApp, "maxReplicaPercentage": int64(50)},
		"faultType":        "latency-injection",
		"duration":         "90s",
		"abortOnSLOBreach": map[string]any{"errorRateMax": 0.5, "latencyP99MaxMs": int64(1000)},
		"fault":            map[string]any{"latencyMs": int64(2000), "jitterMs": int64(50)},
		"analysis":         map[string]any{"interval": "5s"},
	}, name)
	defer h.deleteExperiment(name)

	var targets []string
	g.Eventually(func() []string { targets = h.targetPods(name); return targets }, 2*time.Minute, poll).Should(HaveLen(2))

	g.Eventually(func() string { return h.abortReason(name) }, 3*time.Minute, poll).Should(Or(Equal("SLOBreach"), Equal("InjectionFailed")), "latency neither breached nor reported an injection failure")
	t.Logf("latency aborted with reason %q", h.abortReason(name))

	for _, pod := range targets {
		g.Eventually(func() bool { return h.hasNetem(pod) }, abortClearBound, 2*time.Second).Should(BeFalse(), "netem still on %s after abort", pod)
	}
	g.Expect(h.phase(name)).To(Equal("Aborted"))
	g.Expect(h.score(name)).To(Equal(0.0))
}

func (h *chaosHarness) testPartitionContained(t *testing.T) {
	g := NewWithT(t)
	name := "partition"
	h.applyExperiment(map[string]any{
		"target":    map[string]any{"labelSelector": "app=" + chaosTargetApp, "maxReplicaPercentage": int64(50)},
		"faultType": "network-partition",
		"duration":  "30s",
		// Lenient so the experiment runs its course rather than aborting; the point here is
		// containment, not the score.
		"abortOnSLOBreach": map[string]any{"errorRateMax": 0.95, "latencyP99MaxMs": int64(1000)},
		"analysis":         map[string]any{"interval": "5s"},
	}, name)
	defer h.deleteExperiment(name)

	var targets []string
	g.Eventually(func() []string { targets = h.targetPods(name); return targets }, 2*time.Minute, poll).Should(HaveLen(2))
	targeted := map[string]bool{targets[0]: true, targets[1]: true}

	// The DROP chain must appear on the targeted pods only, never on a pod outside the cap.
	g.Eventually(func() bool { return h.hasDropChain(targets[0]) && h.hasDropChain(targets[1]) }, 60*time.Second, 2*time.Second).Should(BeTrue(), "partition never applied to the targeted pods")
	for _, p := range h.pods(chaosTargetApp) {
		if targeted[p.Name] {
			continue
		}
		g.Consistently(func() bool { return h.hasDropChain(p.Name) }, 6*time.Second, 2*time.Second).Should(BeFalse(), "a pod outside the cap was partitioned: %s", p.Name)
	}
	t.Logf("partition confined to %v, %d untargeted pods untouched", targets, len(h.pods(chaosTargetApp))-2)

	// After it ends, the chain is cleared from the node.
	g.Eventually(func() string { return h.phase(name) }, 3*time.Minute, poll).Should(Or(Equal("Completed"), Equal("Aborted")))
	for _, pod := range targets {
		g.Eventually(func() bool { return h.hasDropChain(pod) }, abortClearBound, 2*time.Second).Should(BeFalse(), "partition rule left on %s", pod)
	}
}

func (h *chaosHarness) testPodKillScored(t *testing.T) {
	g := NewWithT(t)
	name := "podkill"
	h.applyExperiment(map[string]any{
		"target":    map[string]any{"labelSelector": "app=" + chaosTargetApp, "maxReplicaPercentage": int64(50)},
		"faultType": "pod-kill",
		"duration":  "40s",
		// A kill strands the requests in flight to that pod for the 2 to 3 s its connections
		// take to fail, and a 2000 ms p99 bound judges more than 1% of a window slower than 2 s
		// as a breach: a healthy run consumed half of that margin and CI contention took the
		// rest. This subtest proves kills happen, a score is produced and kills stop; the
		// breach subtests keep the tight bounds.
		"abortOnSLOBreach": map[string]any{"errorRateMax": 0.95, "latencyP99MaxMs": int64(5000)},
		"fault":            map[string]any{"interval": "10s"},
		"analysis":         map[string]any{"interval": "5s"},
	}, name)
	defer h.deleteExperiment(name)
	defer func() { t.Log(h.describe(name)) }()

	// Pods are actually deleted: at least one UID from the initial set disappears.
	before := h.podUIDs(chaosTargetApp)
	g.Eventually(func() bool {
		now := h.podUIDs(chaosTargetApp)
		for uid := range before {
			if !now[uid] {
				return true
			}
		}
		return false
	}, 90*time.Second, poll).Should(BeTrue(), "no original pod was killed")

	g.Eventually(func() string { return h.phase(name) }, 3*time.Minute, poll).Should(Equal("Completed"))
	g.Expect(h.score(name)).To(BeNumerically(">=", 0), "a completed experiment carries a score")

	// Prove the kills stopped: the pod UID set is stable across two kill intervals.
	stable := h.podUIDs(chaosTargetApp)
	g.Consistently(func() int {
		diff := 0
		now := h.podUIDs(chaosTargetApp)
		for uid := range stable {
			if !now[uid] {
				diff++
			}
		}
		return diff
	}, 25*time.Second, 5*time.Second).Should(Equal(0), "pods are still being killed after the experiment ended")
}

func (h *chaosHarness) testExhaustionScored(t *testing.T) {
	g := NewWithT(t)
	name := "exhaustion"
	h.applyExperiment(map[string]any{
		"target":           map[string]any{"labelSelector": "app=" + chaosTargetApp, "maxReplicaPercentage": int64(50)},
		"faultType":        "resource-exhaustion",
		"duration":         "30s",
		"abortOnSLOBreach": map[string]any{"errorRateMax": 0.95, "latencyP99MaxMs": int64(3000)},
		"fault":            map[string]any{"cpuPercent": int64(100), "memoryMiB": int64(0)},
		"analysis":         map[string]any{"interval": "5s"},
	}, name)
	defer h.deleteExperiment(name)

	g.Eventually(func() string { return h.phase(name) }, 3*time.Minute, poll).Should(Or(Equal("Completed"), Equal("Aborted")))
	g.Expect(h.hasScore(name)).To(BeTrue(), "resource-exhaustion produced no score")
	g.Expect(h.score(name)).To(BeNumerically(">=", 0))
	t.Logf("resource-exhaustion scored %.1f (%s)", h.score(name), h.phase(name))
}

// --- node ground truth ---

func (h *chaosHarness) netnsPID(podName string) (string, int, bool) {
	p := &corev1.Pod{}
	if err := h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: podName}, p); err != nil {
		return "", 0, false
	}
	if p.Spec.NodeName == "" || len(p.Status.ContainerStatuses) == 0 {
		return "", 0, false
	}
	id := strings.TrimPrefix(p.Status.ContainerStatuses[0].ContainerID, "containerd://")
	if id == "" {
		return "", 0, false
	}
	out, err := nodeExec(h.ctx, p.Spec.NodeName, "crictl", "inspect", "--output", "go-template", "--template", "{{.info.pid}}", id)
	if err != nil {
		return "", 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return "", 0, false
	}
	return p.Spec.NodeName, pid, true
}

func (h *chaosHarness) hasNetem(podName string) bool {
	node, pid, ok := h.netnsPID(podName)
	if !ok {
		return false
	}
	out, err := nodeExec(h.ctx, node, "nsenter", "-t", strconv.Itoa(pid), "-n", "tc", "qdisc", "show", "dev", "eth0")
	return err == nil && strings.Contains(out, "netem")
}

func (h *chaosHarness) hasDropChain(podName string) bool {
	node, pid, ok := h.netnsPID(podName)
	if !ok {
		return false
	}
	out, err := nodeExec(h.ctx, node, "nsenter", "-t", strconv.Itoa(pid), "-n", "iptables", "-S")
	return err == nil && strings.Contains(out, "KILN-CHAOS")
}

func nodeExec(ctx context.Context, node string, args ...string) (string, error) {
	full := append([]string{"exec", node}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %w: %s", strings.Join(full, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// --- CR helpers ---

func (h *chaosHarness) applyExperiment(spec map[string]any, name string) {
	if h.prometheus != "" {
		bound, _, _ := unstructured.NestedInt64(spec, "abortOnSLOBreach", "latencyP99MaxMs")
		h.waitForCleanWindow(name, float64(bound))
	}
	cr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "platform.internal/v1",
		"kind":       "ChaosExperiment",
		"metadata":   map[string]any{"name": name, "namespace": h.ns},
		"spec":       spec,
	}}
	cr.SetGroupVersionKind(gvkChaos)
	if h.createExperiment != nil {
		h.createExperiment(cr)
		return
	}
	h.g.Expect(h.c.Create(h.ctx, cr)).To(Succeed())
}

func (h *chaosHarness) forwardPrometheus() {
	pods := &corev1.PodList{}
	h.g.Expect(h.c.List(h.ctx, pods, client.InNamespace("monitoring"), client.MatchingLabels{"app": "prometheus"})).To(Succeed())
	h.g.Expect(pods.Items).NotTo(BeEmpty(), "no prometheus pod")
	var stop chan struct{}
	h.prometheus, stop = portForward(h.t, h.g, h.cfg, h.cs, "monitoring", pods.Items[0].Name, 9090)
	h.t.Cleanup(func() { close(stop) })
}

// waitForCleanWindow holds the next experiment until the target's source-reported
// traffic shows a scrape-to-scrape window with requests, no errors and nothing slower than
// the bound the experiment will judge against. The controller's baseline is Prometheus'
// latest scrape, up to 15 s old, so an experiment started seconds after the previous fault
// on the same pods would otherwise judge that fault's tail as its own first window.
func (h *chaosHarness) waitForCleanWindow(name string, latencyMaxMs float64) {
	deadline := time.Now().Add(60 * time.Second)
	prev := h.sourceCounters(latencyMaxMs)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		cur := h.sourceCounters(latencyMaxMs)
		if cur.requests <= prev.requests {
			continue
		}
		errors, slow := cur.errors-prev.errors, cur.slow-prev.slow
		if errors <= 0 && slow <= 0 {
			h.t.Logf("%s: target traffic clean before start (%.0f requests, 0 errors, 0 slower than %.0f ms)", name, cur.requests-prev.requests, latencyMaxMs)
			return
		}
		h.t.Logf("%s: waiting for clean traffic, last window had %.0f errors and %.0f requests slower than %.0f ms", name, errors, slow, latencyMaxMs)
		prev = cur
	}
	h.g.Expect(false).To(BeTrue(), "%s: the target's traffic did not come clean within 60 s of the previous fault", name)
}

type sourceCounters struct{ requests, errors, slow float64 }

// sourceCounters reads what the controller reads: the meshed callers' view of the target
// workload, and the count above the latency bound interpolated within Istio's buckets.
func (h *chaosHarness) sourceCounters(latencyMaxMs float64) sourceCounters {
	sel := fmt.Sprintf(`reporter="source",destination_workload=%q,destination_workload_namespace=%q`, chaosTargetApp, h.ns)
	requests := h.promScalar(fmt.Sprintf(`sum(istio_request_duration_milliseconds_count{%s})`, sel))
	errors := h.promScalar(fmt.Sprintf(`sum(istio_requests_total{%s,response_code=~"5..|0"})`, sel))
	atOrBelow := h.promAtOrBelow(fmt.Sprintf(`sum by (le) (istio_request_duration_milliseconds_bucket{%s})`, sel), latencyMaxMs)
	return sourceCounters{requests: requests, errors: errors, slow: math.Max(0, requests-atOrBelow)}
}

func (h *chaosHarness) promQuery(expr string) []struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value"`
} {
	resp, err := http.Get(h.prometheus + "/api/v1/query?query=" + url.QueryEscape(expr))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil
	}
	return out.Data.Result
}

func (h *chaosHarness) promScalar(expr string) float64 {
	for _, r := range h.promQuery(expr) {
		if len(r.Value) == 2 {
			v, _ := strconv.ParseFloat(fmt.Sprint(r.Value[1]), 64)
			return v
		}
	}
	return 0
}

func (h *chaosHarness) promAtOrBelow(expr string, threshold float64) float64 {
	type bucket struct{ le, count float64 }
	var buckets []bucket
	for _, r := range h.promQuery(expr) {
		if len(r.Value) != 2 {
			continue
		}
		le, err := strconv.ParseFloat(r.Metric["le"], 64)
		if err != nil {
			continue
		}
		count, _ := strconv.ParseFloat(fmt.Sprint(r.Value[1]), 64)
		buckets = append(buckets, bucket{le, count})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].le < buckets[j].le })
	loLe, loCount := 0.0, 0.0
	for _, b := range buckets {
		switch {
		case b.le == threshold:
			return b.count
		case b.le < threshold:
			loLe, loCount = b.le, b.count
		case math.IsInf(b.le, 1):
			return loCount
		default:
			return loCount + (b.count-loCount)*(threshold-loLe)/(b.le-loLe)
		}
	}
	return loCount
}

// describe reads the experiment's outcome and analysis for the log, so an abort in CI is
// explained without a reproduction; never used as evidence.
func (h *chaosHarness) describe(name string) string {
	cr := h.getExperiment(name)
	if cr == nil {
		return name + ": gone"
	}
	phase, _, _ := unstructured.NestedString(cr.Object, "status", "phase")
	reason, _, _ := unstructured.NestedString(cr.Object, "status", "reason")
	abort, _, _ := unstructured.NestedString(cr.Object, "status", "abortReason")
	windows, _, _ := unstructured.NestedInt64(cr.Object, "status", "analysis", "faultWindows")
	worstErr, _, _ := unstructured.NestedFieldNoCopy(cr.Object, "status", "analysis", "worstErrorRate")
	worstSlow, _, _ := unstructured.NestedFieldNoCopy(cr.Object, "status", "analysis", "worstSlowFraction")
	return fmt.Sprintf("%s: phase=%s reason=%s abortReason=%q score=%.1f faultWindows=%d worstErrorRate=%v worstSlowFraction=%v (informational)",
		name, phase, reason, abort, h.score(name), windows, worstErr, worstSlow)
}

func (h *chaosHarness) deleteExperiment(name string) {
	cr := &unstructured.Unstructured{}
	cr.SetGroupVersionKind(gvkChaos)
	cr.SetNamespace(h.ns)
	cr.SetName(name)
	_ = h.c.Delete(context.Background(), cr)
}

func (h *chaosHarness) getExperiment(name string) *unstructured.Unstructured {
	cr := &unstructured.Unstructured{}
	cr.SetGroupVersionKind(gvkChaos)
	if err := h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: name}, cr); err != nil {
		return nil
	}
	return cr
}

func (h *chaosHarness) targetPods(name string) []string {
	cr := h.getExperiment(name)
	if cr == nil {
		return nil
	}
	targets, _, _ := unstructured.NestedSlice(cr.Object, "status", "targets")
	var out []string
	for _, t := range targets {
		if m, ok := t.(map[string]any); ok {
			if pod, ok := m["pod"].(string); ok {
				out = append(out, pod)
			}
		}
	}
	return out
}

func (h *chaosHarness) phase(name string) string {
	cr := h.getExperiment(name)
	if cr == nil {
		return ""
	}
	v, _, _ := unstructured.NestedString(cr.Object, "status", "phase")
	return v
}

func (h *chaosHarness) abortReason(name string) string {
	cr := h.getExperiment(name)
	if cr == nil {
		return ""
	}
	v, _, _ := unstructured.NestedString(cr.Object, "status", "abortReason")
	return v
}

// score returns -1 when the field is absent. A whole-number score can come back as int64
// from the API server, which NestedFloat64 rejects, so read the raw value and coerce.
func (h *chaosHarness) score(name string) float64 {
	cr := h.getExperiment(name)
	if cr == nil {
		return -1
	}
	raw, found, _ := unstructured.NestedFieldNoCopy(cr.Object, "status", "resilienceScore")
	if !found || raw == nil {
		return -1
	}
	switch v := raw.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	default:
		return -1
	}
}

func (h *chaosHarness) hasScore(name string) bool {
	raw, found, _ := unstructured.NestedFieldNoCopy(h.getExperiment(name).Object, "status", "resilienceScore")
	return found && raw != nil
}

func (h *chaosHarness) pods(app string) []corev1.Pod {
	list := &corev1.PodList{}
	if err := h.c.List(h.ctx, list, client.InNamespace(h.ns), client.MatchingLabels{"app": app}); err != nil {
		return nil
	}
	return list.Items
}

func (h *chaosHarness) podUIDs(app string) map[string]bool {
	out := map[string]bool{}
	for _, p := range h.pods(app) {
		out[string(p.UID)] = true
	}
	return out
}

func (h *chaosHarness) readyReplicas(name string) int32 {
	dep := &appsv1.Deployment{}
	if err := h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: name}, dep); err != nil {
		return 0
	}
	return dep.Status.ReadyReplicas
}
