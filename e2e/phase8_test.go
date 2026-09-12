//go:build e2e

// Phase 8 exit criterion (ROADMAP.md): with every controller SIGKILLed repeatedly during a
// burst of requests, each phase transition an informer the test owns observed has exactly
// one audit row, the chain verifies, and no publish failed. The transitions are read from
// the API server's watch stream and the rows with psql in the Postgres pod; neither the
// controllers' outboxes nor the service's endpoints are the evidence.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	outboxDatabases  = 6
	outboxKillEvery  = 40 * time.Second
	outboxSettleWait = 4 * time.Minute
)

var outboxKinds = []schema.GroupVersionResource{
	{Group: "platform.internal", Version: "v1", Resource: "tenantdatabases"},
	{Group: "platform.internal", Version: "v1", Resource: "canaryrollouts"},
	{Group: "platform.internal", Version: "v1", Resource: "chaosexperiments"},
}

var outboxControllers = []types.NamespacedName{
	{Namespace: "kiln-operator-system", Name: "kiln-operator-controller-manager"},
	{Namespace: "kiln-delivery-system", Name: "kiln-delivery-controller"},
	{Namespace: "kiln-chaos-system", Name: "kiln-chaos-controller"},
}

type transition struct {
	resource, action, outcome string
}

func (tr transition) String() string { return tr.action + "/" + tr.outcome + " " + tr.resource }

// transitionWatcher records every audited phase transition of the run's objects as the
// API server reports it, independently of what any controller wrote to its outbox.
type transitionWatcher struct {
	runID string
	mu    sync.Mutex
	seen  []transition
}

func startTransitionWatcher(t *testing.T, g *WithT, ctx context.Context, cfg *rest.Config, runID string) *transitionWatcher {
	w := &transitionWatcher{runID: runID}
	factory := dynamicinformer.NewDynamicSharedInformerFactory(dynamic.NewForConfigOrDie(cfg), 0)
	for _, gvr := range outboxKinds {
		_, err := factory.ForResource(gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{UpdateFunc: w.onUpdate})
		g.Expect(err).NotTo(HaveOccurred())
	}
	factory.Start(ctx.Done())
	for gvr, synced := range factory.WaitForCacheSync(ctx.Done()) {
		g.Expect(synced).To(BeTrue(), "informer for %s did not sync", gvr.Resource)
	}
	t.Log("watching phase transitions of the run's objects")
	return w
}

func (w *transitionWatcher) onUpdate(oldObj, newObj any) {
	o, ok1 := oldObj.(*unstructured.Unstructured)
	n, ok2 := newObj.(*unstructured.Unstructured)
	if !ok1 || !ok2 || !strings.HasSuffix(n.GetNamespace(), "-"+w.runID) {
		return
	}
	from, to := field(o, "status", "phase"), field(n, "status", "phase")
	if from == to {
		return
	}
	action, outcome, audited := auditedTransition(n.GetKind(), from, to, field(n, "status", "reason"))
	if !audited {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen = append(w.seen, transition{resource: n.GetKind() + "/" + n.GetNamespace() + "/" + n.GetName(), action: action, outcome: outcome})
}

// auditedTransition is the phase change each controller commits an event with; the pairs
// are the ones in each controller's reconcile, so the watch stream predicts the rows.
func auditedTransition(kind, from, to, reason string) (string, string, bool) {
	switch kind {
	case "TenantDatabase":
		switch {
		case to == "Ready" && (from == "" || from == "Provisioning"):
			return "PROVISION", "Ready", true
		case to == "Failed":
			return "PROVISION", "Failed", true
		case to == "Backing Up":
			return "BACKUP", "Started", true
		case from == "Backing Up" && to == "Ready":
			return "BACKUP", "Succeeded", true
		case to == "Restoring":
			return "RESTORE", "Started", true
		case from == "Restoring" && to == "Ready":
			return "RESTORE", "Succeeded", true
		}
	case "CanaryRollout":
		switch {
		case to == "Progressing":
			return "DEPLOY", "Started", true
		case to == "Draining" && reason == "Promoted":
			return "DEPLOY", "Promoted", true
		case to == "Draining":
			return "ROLLBACK", "RolledBack", true
		}
	case "ChaosExperiment":
		switch to {
		case "Running":
			return "CHAOS_EXPERIMENT", "Started", true
		case "Completed", "Aborted":
			return "CHAOS_EXPERIMENT", to, true
		}
	}
	return "", "", false
}

func (w *transitionWatcher) transitions() []transition {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]transition(nil), w.seen...)
}

// controllerKiller deletes every controller's pod on a cadence, alternating an immediate
// kill (grace 0, no time to flush anything) with an ordinary delete, until stopped.
type controllerKiller struct {
	t     *testing.T
	ctx   context.Context
	c     client.Client
	mu    sync.Mutex
	kills map[string]int
	hard  map[string]int
}

func (k *controllerKiller) run(stop <-chan struct{}) {
	round := 0
	for {
		for _, target := range outboxControllers {
			k.kill(target, round%2 == 0)
		}
		round++
		select {
		case <-stop:
			return
		case <-time.After(outboxKillEvery):
		}
	}
}

func (k *controllerKiller) kill(target types.NamespacedName, hard bool) {
	dep := &appsv1.Deployment{}
	if err := k.c.Get(k.ctx, target, dep); err != nil {
		k.t.Logf("killer: %s: %v", target.Name, err)
		return
	}
	pods := &corev1.PodList{}
	if err := k.c.List(k.ctx, pods, client.InNamespace(target.Namespace), client.MatchingLabels(dep.Spec.Selector.MatchLabels)); err != nil {
		k.t.Logf("killer: %s: %v", target.Name, err)
		return
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		var opts []client.DeleteOption
		if hard {
			opts = append(opts, client.GracePeriodSeconds(0))
		}
		if err := k.c.Delete(k.ctx, &pod, opts...); err != nil {
			k.t.Logf("killer: %s: %v", pod.Name, err)
			continue
		}
		k.mu.Lock()
		k.kills[target.Name]++
		if hard {
			k.hard[target.Name]++
		}
		k.mu.Unlock()
		k.t.Logf("killed %s/%s (grace 0: %v)", pod.Namespace, pod.Name, hard)
	}
}

func TestPhase8Outbox(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := ctrl.GetConfigOrDie()
	c, err := client.New(cfg, client.Options{})
	g.Expect(err).NotTo(HaveOccurred())
	cs, err := kubernetes.NewForConfig(cfg)
	g.Expect(err).NotTo(HaveOccurred())

	runID := fmt.Sprint(time.Now().Unix())
	h := &auditHarness{t: t, g: g, ctx: ctx, cfg: cfg, c: c, cs: cs, ns: "outbox-e2e-" + runID}
	h.loadSigningKey()
	h.waitForAudit()
	h.createNamespace()
	t.Cleanup(h.deleteNamespace)
	h.forward()
	t.Cleanup(h.stopForward)
	rowsBefore := len(h.readRows())

	watcher := startTransitionWatcher(t, g, ctx, cfg, runID)
	killer := &controllerKiller{t: t, ctx: ctx, c: c, kills: map[string]int{}, hard: map[string]int{}}
	stopKilling := make(chan struct{})
	killerDone := make(chan struct{})
	go func() {
		defer close(killerDone)
		killer.run(stopKilling)
	}()

	t.Run("burst under kills", func(t *testing.T) {
		t.Run("databases are provisioned and backed up", func(t *testing.T) {
			t.Parallel()
			h.outboxDatabases(t, runID)
		})
		t.Run("a broken rollout is rolled back", func(t *testing.T) {
			t.Parallel()
			ch := &canaryHarness{t: t, g: NewWithT(t), ctx: ctx, cfg: cfg, c: c, cs: cs, ns: "outbox-canary-" + runID, start: map[string]time.Time{}}
			ch.createMeshNamespace()
			t.Cleanup(ch.deleteNamespace)
			ch.deployTarget()
			ch.initialVersion(t)
			ch.brokenVersion(t)
		})
		t.Run("a pod-kill experiment runs to an end", func(t *testing.T) {
			t.Parallel()
			ch := &chaosHarness{t: t, g: NewWithT(t), ctx: ctx, cfg: cfg, c: c, cs: cs, ns: "outbox-chaos-" + runID}
			ch.createMeshNamespace()
			t.Cleanup(ch.deleteNamespace)
			ch.deployTarget()
			ch.applyExperiment(map[string]any{
				"target":           map[string]any{"labelSelector": "app=" + chaosTargetApp, "maxReplicaPercentage": int64(50)},
				"faultType":        "pod-kill",
				"duration":         "40s",
				"abortOnSLOBreach": map[string]any{"errorRateMax": 0.95, "latencyP99MaxMs": int64(5000)},
				"fault":            map[string]any{"interval": "10s"},
				"analysis":         map[string]any{"interval": "5s"},
			}, "podkill")
			// The controller is killed while the experiment runs; the lease dead-man switch
			// may end it Aborted rather than Completed (ADR-0015). Either end is audited.
			NewWithT(t).Eventually(func() string { return ch.phase("podkill") }, 5*time.Minute, poll).
				Should(BeElementOf("Completed", "Aborted"), ch.describe("podkill"))
			t.Logf("experiment ended %s: %s", ch.phase("podkill"), ch.describe("podkill"))
		})
	})
	close(stopKilling)
	<-killerDone
	for _, target := range outboxControllers {
		g.Expect(killer.hard[target.Name]).To(BeNumerically(">=", 3), "%s must have been killed without grace several times, got %d", target.Name, killer.hard[target.Name])
		g.Expect(killer.kills[target.Name]-killer.hard[target.Name]).To(BeNumerically(">=", 2), "%s must also have been deleted gracefully", target.Name)
	}
	t.Logf("kills per controller: %v (grace 0: %v)", killer.kills, killer.hard)

	t.Run("every controller comes back and every outbox drains", func(t *testing.T) {
		g := NewWithT(t)
		for _, target := range outboxControllers {
			g.Eventually(func() int32 {
				dep := &appsv1.Deployment{}
				if err := c.Get(ctx, target, dep); err != nil {
					return 0
				}
				return dep.Status.AvailableReplicas
			}, platformReady, poll).Should(BeNumerically(">=", 1), "%s did not come back", target.Name)
		}
		dyn := dynamic.NewForConfigOrDie(cfg)
		g.Eventually(func() []string {
			var pending []string
			for _, gvr := range outboxKinds {
				list, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
				if err != nil {
					return []string{err.Error()}
				}
				for _, obj := range list.Items {
					if !strings.HasSuffix(obj.GetNamespace(), "-"+runID) {
						continue
					}
					left, _, _ := unstructured.NestedSlice(obj.Object, "status", "audit", "pending")
					if len(left) > 0 {
						pending = append(pending, fmt.Sprintf("%s/%s/%s:%d", obj.GetKind(), obj.GetNamespace(), obj.GetName(), len(left)))
					}
				}
			}
			return pending
		}, outboxSettleWait, poll).Should(BeEmpty(), "events still committed and undelivered")
	})

	t.Run("every observed transition has exactly one row and there are no others", func(t *testing.T) {
		g := NewWithT(t)
		transitions := watcher.transitions()
		g.Expect(transitions).NotTo(BeEmpty(), "the informer saw no audited transition; the burst did nothing")
		var rows []auditRow
		g.Eventually(func() int {
			rows = nil
			for _, r := range h.readRows()[rowsBefore:] {
				if _, ns, _ := splitResource(r.resource); strings.HasSuffix(ns, "-"+runID) && isOutboxKind(r.resource) {
					rows = append(rows, r)
				}
			}
			return len(rows)
		}, auditSettle, poll).Should(BeNumerically(">=", len(transitions)), "fewer rows than transitions: %v", describeRows(rows))

		byKey := map[transition][]auditRow{}
		for _, r := range rows {
			byKey[transition{resource: r.resource, action: r.action, outcome: outcomeOf(r.details)}] = append(byKey[transition{resource: r.resource, action: r.action, outcome: outcomeOf(r.details)}], r)
		}
		var missing, duplicated []string
		matched := map[int64]bool{}
		for _, tr := range transitions {
			got := byKey[tr]
			switch len(got) {
			case 0:
				missing = append(missing, tr.String())
			case 1:
				matched[got[0].seq] = true
			default:
				duplicated = append(duplicated, fmt.Sprintf("%s x%d", tr, len(got)))
			}
		}
		var extra []string
		for _, r := range rows {
			if !matched[r.seq] {
				extra = append(extra, fmt.Sprintf("seq %d %s/%s %s", r.seq, r.action, outcomeOf(r.details), r.resource))
			}
		}
		t.Logf("%d transitions observed, %d rows for the run; kills %v", len(transitions), len(rows), killer.kills)
		g.Expect(missing).To(BeEmpty(), "transitions the API server showed but the trail lacks")
		g.Expect(duplicated).To(BeEmpty(), "transitions stored more than once")
		g.Expect(extra).To(BeEmpty(), "rows without an observed transition")
		for _, r := range rows {
			g.Expect(countRows(rows, r.eventID)).To(Equal(1), "eventId %s stored more than once", r.eventID)
		}
	})

	t.Run("the chain holds and nothing was dropped", func(t *testing.T) {
		g := NewWithT(t)
		all := h.readRows()
		g.Expect(verifyChain(all)).To(BeEmpty(), "the chain recomputed from the rows must hold")
		code, body := h.call("GET", "/v1/audit/verify", h.token("admin@kiln.e2e", "audit:admin"), nil)
		g.Expect(code).To(Equal(200))
		g.Expect(body["ok"]).To(BeTrue(), "verify: %v", body)

		pods := h.podsIn("monitoring", "app", "prometheus")
		g.Expect(pods).NotTo(BeEmpty(), "no prometheus pod")
		prom, stop := portForward(t, g, cfg, cs, "monitoring", pods[0].Name, 9090)
		defer close(stop)
		g.Eventually(func() float64 { return promScalarAt(prom, "sum(kiln_audit_outbox_pending) or vector(0)") }, 2*time.Minute, poll).
			Should(Equal(0.0), "the controllers must report empty outboxes")
		failures := promScalarAt(prom, "sum(kiln_audit_publish_failures_total) or vector(0)")
		g.Expect(failures).To(Equal(0.0), "no audit event may have been dropped (ADR-0022)")
	})
}

// outboxDatabases creates TenantDatabases directly and backs each up once the operator
// has provisioned it, while the operator is being killed underneath.
func (h *auditHarness) outboxDatabases(t *testing.T, runID string) {
	g := NewWithT(t)
	base := &harness{ctx: h.ctx, c: h.c, g: g, ns: h.ns}
	names := make([]string, 0, outboxDatabases)
	for i := 0; i < outboxDatabases; i++ {
		name := fmt.Sprintf("db%d", i)
		names = append(names, name)
		g.Expect(h.c.Create(h.ctx, base.tenantDatabase(name, 1))).To(Succeed())
	}
	for _, name := range names {
		g.Eventually(func() string { return h.tdbPhase(name) }, platformReady, poll).Should(Equal("Ready"), "%s must be provisioned", name)
	}
	for _, name := range names {
		h.annotate(name, "platform.internal/backup", "now")
	}
	for _, name := range names {
		g.Eventually(func() any {
			tdb := h.get(gvkTenant, name)
			if tdb == nil {
				return nil
			}
			v, _, _ := unstructured.NestedString(tdb.Object, "status", "lastBackupTime")
			if v == "" {
				return nil
			}
			return v
		}, 5*time.Minute, poll).ShouldNot(BeNil(), "%s must be backed up", name)
		g.Eventually(func() string { return h.tdbPhase(name) }, 2*time.Minute, poll).Should(Equal("Ready"))
	}
	t.Logf("%d databases provisioned and backed up", len(names))
}

func splitResource(resource string) (kind, ns, name string) {
	parts := strings.SplitN(resource, "/", 3)
	if len(parts) != 3 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}

func isOutboxKind(resource string) bool {
	kind, _, _ := splitResource(resource)
	return kind == "TenantDatabase" || kind == "CanaryRollout" || kind == "ChaosExperiment"
}

func outcomeOf(details string) string {
	var d map[string]any
	if err := json.Unmarshal([]byte(details), &d); err != nil {
		return ""
	}
	s, _ := d["outcome"].(string)
	return s
}

func describeRows(rows []auditRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("seq %d %s/%s %s", r.seq, r.action, outcomeOf(r.details), r.resource))
	}
	return out
}

func promScalarAt(base, expr string) float64 {
	resp, err := http.Get(base + "/api/v1/query?query=" + url.QueryEscape(expr))
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Result []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Data.Result) == 0 || len(out.Data.Result[0].Value) < 2 {
		return -1
	}
	v, _ := strconv.ParseFloat(out.Data.Result[0].Value[1].(string), 64)
	return v
}
