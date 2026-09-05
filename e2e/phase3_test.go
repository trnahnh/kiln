//go:build e2e

// Phase 3 exit criterion: replay one deterministic workload trace through the default
// scheduler and through kiln-scheduler on the same nodes, then bill each run from the
// API server's own record of where pods ran and for how long (METRICS.md, node-occupancy
// instance-hours). The plugin's score is never consulted.
package e2e

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	labelWorkloadClass = "kiln.platform.internal/workload-class"
	labelCapacityType  = "kiln.platform.internal/capacity-type"
	labelHourlyCost    = "kiln.platform.internal/hourly-cost"

	kilnSchedulerName    = "kiln-scheduler"
	defaultSchedulerName = "default-scheduler"

	tracePods    = 60
	traceSeed    = 42
	traceSpacing = 2 * time.Second
	replayWait   = 12 * time.Minute
	sleepImage   = "registry.k8s.io/e2e-test-images/busybox:1.36.1-1"
)

type traceEntry struct {
	name     string
	class    string
	cpu      string
	memory   string
	sleepSec int
	arrival  time.Duration
}

// buildTrace is seeded so both replays submit byte-identical pod specs in the same order.
func buildTrace(seed int64) []traceEntry {
	r := rand.New(rand.NewSource(seed))
	classes := []string{"latency-sensitive", "standard", "standard", "batch", "batch"}
	sizes := [][2]string{{"100m", "64Mi"}, {"250m", "128Mi"}, {"500m", "256Mi"}}
	out := make([]traceEntry, tracePods)
	for i := range out {
		size := sizes[r.Intn(len(sizes))]
		out[i] = traceEntry{
			name:     fmt.Sprintf("trace-%02d", i),
			class:    classes[r.Intn(len(classes))],
			cpu:      size[0],
			memory:   size[1],
			sleepSec: 30 + r.Intn(31),
			arrival:  time.Duration(i) * traceSpacing,
		}
	}
	return out
}

type nodeFacts struct {
	spot       bool
	hourlyCost float64
}

type replayResult struct {
	billedHours    float64
	cost           float64
	violations     int
	nodeOccupancy  map[string]time.Duration
	podsPerCapType map[string]int
}

func TestPhase3SchedulerCostReduction(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{})
	g.Expect(err).NotTo(HaveOccurred())

	nodes := workerFacts(t, g, ctx, c)
	spot, onDemand := 0, 0
	for _, f := range nodes {
		if f.spot {
			spot++
		} else {
			onDemand++
		}
	}
	g.Expect(spot).To(BeNumerically(">=", 2), "need at least two spot workers")
	g.Expect(onDemand).To(BeNumerically(">=", 2), "need at least two on-demand workers")

	t.Log("waiting for kiln-scheduler to be running")
	g.Eventually(func() int32 {
		dep := &appsv1.Deployment{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: "kiln-scheduler-system", Name: "kiln-scheduler"}, dep); err != nil {
			return 0
		}
		return dep.Status.AvailableReplicas
	}, platformReady, poll).Should(BeNumerically(">=", 1))

	trace := buildTrace(traceSeed)
	runID := fmt.Sprintf("%d", time.Now().Unix())

	t.Log("replay 1/2: default-scheduler")
	baseline := replay(t, g, ctx, c, defaultSchedulerName, "replay-default-"+runID, trace, nodes)
	t.Log("replay 2/2: kiln-scheduler")
	kiln := replay(t, g, ctx, c, kilnSchedulerName, "replay-kiln-"+runID, trace, nodes)

	reduction := (baseline.cost - kiln.cost) / baseline.cost * 100
	t.Logf("default-scheduler: billed %.4f node-hours, cost %.5f, latency-class violations %d, pods by capacity %v",
		baseline.billedHours, baseline.cost, baseline.violations, baseline.podsPerCapType)
	t.Logf("kiln-scheduler:    billed %.4f node-hours, cost %.5f, latency-class violations %d, pods by capacity %v",
		kiln.billedHours, kiln.cost, kiln.violations, kiln.podsPerCapType)
	t.Logf("cost reduction: %.1f%%", reduction)
	for name, d := range kiln.nodeOccupancy {
		t.Logf("  kiln occupancy %s: %s (baseline %s)", name, d.Round(time.Second), baseline.nodeOccupancy[name].Round(time.Second))
	}

	g.Expect(kiln.violations).To(Equal(0), "no latency-sensitive pod may run on spot capacity under kiln-scheduler")
	g.Expect(kiln.cost).To(BeNumerically("<", baseline.cost), "kiln-scheduler must bill fewer instance-dollars than the default scheduler")
}

func workerFacts(t *testing.T, g *WithT, ctx context.Context, c client.Client) map[string]nodeFacts {
	list := &corev1.NodeList{}
	g.Expect(c.List(ctx, list)).To(Succeed())
	facts := map[string]nodeFacts{}
	for _, n := range list.Items {
		capacity, ok := n.Labels[labelCapacityType]
		if !ok {
			continue
		}
		cost, err := strconv.ParseFloat(n.Labels[labelHourlyCost], 64)
		g.Expect(err).NotTo(HaveOccurred(), "node %s hourly-cost label", n.Name)
		facts[n.Name] = nodeFacts{spot: capacity == "spot", hourlyCost: cost}
	}
	return facts
}

func replay(t *testing.T, g *WithT, ctx context.Context, c client.Client, scheduler, ns string, trace []traceEntry, nodes map[string]nodeFacts) replayResult {
	g.Expect(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
	defer func() { _ = c.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}) }()

	start := time.Now()
	for _, e := range trace {
		if wait := time.Until(start.Add(e.arrival)); wait > 0 {
			time.Sleep(wait)
		}
		g.Expect(c.Create(ctx, tracePod(ns, scheduler, e))).To(Succeed())
	}

	pods := &corev1.PodList{}
	g.Eventually(func() int {
		g.Expect(c.List(ctx, pods, client.InNamespace(ns))).To(Succeed())
		done := 0
		for _, p := range pods.Items {
			switch p.Status.Phase {
			case corev1.PodSucceeded:
				done++
			case corev1.PodFailed:
				t.Fatalf("%s: pod %s failed: %s", scheduler, p.Name, p.Status.Message)
			}
		}
		return done
	}, replayWait, 5*time.Second).Should(Equal(len(trace)), "every trace pod must run to completion under %s", scheduler)

	return bill(g, pods.Items, nodes)
}

func tracePod(ns, scheduler string, e traceEntry) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: e.name, Namespace: ns, Labels: map[string]string{labelWorkloadClass: e.class}},
		Spec: corev1.PodSpec{
			SchedulerName: scheduler,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "work",
				Image:   sleepImage,
				Command: []string{"sleep", strconv.Itoa(e.sleepSec)},
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(e.cpu),
					corev1.ResourceMemory: resource.MustParse(e.memory),
				}},
			}},
		},
	}
}

type interval struct{ start, end time.Time }

// bill reads each pod's actual container run window and node from the API server, merges
// the windows per node, and charges every occupied second at the node's hourly cost.
func bill(g *WithT, pods []corev1.Pod, nodes map[string]nodeFacts) replayResult {
	res := replayResult{nodeOccupancy: map[string]time.Duration{}, podsPerCapType: map[string]int{}}
	byNode := map[string][]interval{}
	for _, p := range pods {
		g.Expect(p.Spec.NodeName).NotTo(BeEmpty(), "pod %s has no node", p.Name)
		facts, ok := nodes[p.Spec.NodeName]
		g.Expect(ok).To(BeTrue(), "pod %s ran on unlabelled node %s", p.Name, p.Spec.NodeName)
		g.Expect(p.Status.ContainerStatuses).To(HaveLen(1))
		term := p.Status.ContainerStatuses[0].State.Terminated
		g.Expect(term).NotTo(BeNil(), "pod %s has no terminated container state", p.Name)
		byNode[p.Spec.NodeName] = append(byNode[p.Spec.NodeName], interval{term.StartedAt.Time, term.FinishedAt.Time})
		if facts.spot {
			res.podsPerCapType["spot"]++
		} else {
			res.podsPerCapType["on-demand"]++
		}
		if p.Labels[labelWorkloadClass] == "latency-sensitive" && facts.spot {
			res.violations++
		}
	}
	for node, ivs := range byNode {
		occupied := mergedDuration(ivs)
		res.nodeOccupancy[node] = occupied
		hours := occupied.Hours()
		res.billedHours += hours
		res.cost += hours * nodes[node].hourlyCost
	}
	return res
}

func mergedDuration(ivs []interval) time.Duration {
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].start.Before(ivs[j].start) })
	var total time.Duration
	cur := ivs[0]
	for _, iv := range ivs[1:] {
		if iv.start.After(cur.end) {
			total += cur.end.Sub(cur.start)
			cur = iv
			continue
		}
		if iv.end.After(cur.end) {
			cur.end = iv.end
		}
	}
	return total + cur.end.Sub(cur.start)
}
