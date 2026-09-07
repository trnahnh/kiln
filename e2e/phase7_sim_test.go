//go:build e2e

// Phase 7, second half: the Validation Plan in METRICS.md. A seeded team of developer
// identities submits a realistic mix of requests through the REST path over one simulated
// week, with a policy violation, a bad deploy and a forced SLO breach injected. Every
// number in the comparison table comes from the audit rows read inside the Postgres pod,
// cross-checked against what the cluster showed at the time (a pod turning Ready, a PVC
// growing, a VirtualService flipping, a fault gone from the node), never from a CR's
// status.
package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	simDefaultIdentities = 10
	simDefaultWindow     = 20 * time.Minute
	simDefaultSeed       = 7
	// Working days and hours of the simulated week; wall clock is the window.
	simDays  = 5
	simHours = 8.0
	simPoll  = time.Second
	// The operator polls its StatefulSet every 10 s (pollInterval), so the PROVISION row
	// can trail the pod's Ready condition by that much; every other transition is
	// published in the same reconcile that made it.
	provisionTolerance = 15 * time.Second
	actionTolerance    = 5 * time.Second
	simLoadQPS         = 25
)

type simConfig struct {
	identities int
	window     time.Duration
	seed       int64
	report     string
}

func simConfigFromEnv() simConfig {
	cfg := simConfig{identities: simDefaultIdentities, window: simDefaultWindow, seed: simDefaultSeed, report: "validation-results.md"}
	if v, err := strconv.Atoi(os.Getenv("KILN_SIM_SCALE")); err == nil && v >= 3 {
		cfg.identities = v
	}
	if v, err := time.ParseDuration(os.Getenv("KILN_SIM_WEEK")); err == nil && v > 0 {
		cfg.window = v
	}
	if v, err := strconv.ParseInt(os.Getenv("KILN_SIM_SEED"), 10, 64); err == nil {
		cfg.seed = v
	}
	if v := os.Getenv("KILN_SIM_REPORT"); v != "" {
		cfg.report = v
	}
	return cfg
}

type simIdentity struct {
	index   int
	name    string
	subject string
	ns      string
	canary  *canaryHarness
	chaos   *chaosHarness
	deploys int
	scales  int
	runs    int
}

// simAction is one request of the week: what was asked, when, what the cluster showed and
// when it showed it.
type simAction struct {
	identity *simIdentity
	kind     string
	queue    string
	day      int
	hour     float64

	scheduled time.Time
	started   time.Time
	done      time.Time
	observed  time.Time
	outcome   string
	err       error
	rtt       time.Duration
	traceID   string
}

type simulation struct {
	*traceHarness
	cfg        simConfig
	identities []*simIdentity
	actions    []*simAction
	start      time.Time
	mu         sync.Mutex
	prometheus string
}

func (h *traceHarness) testSimulatedWeek(t *testing.T) {
	g := NewWithT(t)
	h.g = g
	s := &simulation{traceHarness: h, cfg: simConfigFromEnv()}
	t.Logf("simulation: %d identities, one simulated week in %s, seed %d", s.cfg.identities, s.cfg.window, s.cfg.seed)
	s.forwardPrometheus()
	s.buildIdentities()
	t.Cleanup(s.deleteNamespaces)
	s.provisionServices()
	s.schedule()
	s.run()
	s.report()
}

// --- setup ---

func (s *simulation) forwardPrometheus() {
	pods := s.podsIn(monitoringNS, "app", "prometheus")
	s.g.Expect(pods).NotTo(BeEmpty())
	var stop chan struct{}
	s.prometheus, stop = s.portForward(monitoringNS, pods[0].Name, 9090)
	s.t.Cleanup(func() { close(stop) })
}

func (s *simulation) buildIdentities() {
	for i := 0; i < s.cfg.identities; i++ {
		name := fmt.Sprintf("dev%02d", i+1)
		id := &simIdentity{index: i, name: name, subject: name + "@kiln.sim", ns: fmt.Sprintf("sim-%d-%s", s.runID, name)}
		id.canary = &canaryHarness{t: s.t, g: s.g, ctx: s.ctx, cfg: s.auditHarness.cfg, c: s.c, cs: s.cs, ns: id.ns, start: map[string]time.Time{}, qps: simLoadQPS}
		id.canary.createRollout = func(cr *unstructured.Unstructured) {
			_, _, err := s.post(id.subject, cr.Object)
			s.g.Expect(err).NotTo(HaveOccurred())
		}
		id.chaos = &chaosHarness{t: s.t, g: s.g, ctx: s.ctx, c: s.c, cs: s.cs, ns: id.ns}
		id.canary.createMeshNamespace()
		s.identities = append(s.identities, id)
	}
}

func (s *simulation) deleteNamespaces() {
	for _, id := range s.identities {
		id.canary.deleteNamespace()
	}
}

// provisionServices gives every identity the service it already runs before the week
// starts: a fortio server behind canary delivery and a meshed client at simLoadQPS.
func (s *simulation) provisionServices() {
	var wg sync.WaitGroup
	for _, id := range s.identities {
		id.canary.deployTarget()
	}
	for _, id := range s.identities {
		wg.Add(1)
		go func(id *simIdentity) {
			defer wg.Done()
			deadline := time.Now().Add(canaryWait)
			for time.Now().Before(deadline) {
				p, c := id.canary.weights()
				if p == 100 && c == 0 && id.canary.available(targetApp+"-primary") == 2 && id.canary.replicas(targetApp) == 0 && id.canary.available(loadgenApp) == 1 {
					return
				}
				time.Sleep(simPoll)
			}
		}(id)
	}
	wg.Wait()
	for _, id := range s.identities {
		p, c := id.canary.weights()
		s.g.Expect([]int{p, c}).To(Equal([]int{100, 0}), "%s: primary must serve everything before the week starts", id.name)
	}
	// Istio counters need a baseline before any analysis reads them.
	time.Sleep(20 * time.Second)
}

// --- schedule ---

// schedule draws every action's simulated time from the seed. Per identity: one
// provision on the first morning, two scales, three deploys and two chaos runs across the
// week. The injections replace one identity's deploy, one's chaos run and add one
// identity's oversized claim, at fixed times.
func (s *simulation) schedule() {
	rng := rand.New(rand.NewSource(s.cfg.seed))
	n := len(s.identities)
	policyID, badDeployID, breachID := 3%n, 6%n, 8%n
	for _, id := range s.identities {
		s.add(id, "provision", "db", 0, rng.Float64()*1.5)
		for k := 0; k < 2; k++ {
			s.add(id, "scale", "db", 1+rng.Intn(simDays-1), rng.Float64()*simHours)
		}
		for k := 0; k < 3; k++ {
			day := rng.Intn(simDays)
			hour := rng.Float64() * simHours
			if day == 0 {
				hour = 2 + rng.Float64()*(simHours-2)
			}
			s.add(id, "deploy", "app", day, hour)
		}
		for k := 0; k < 2; k++ {
			s.add(id, "chaos", "app", 1+rng.Intn(simDays-1), rng.Float64()*simHours)
		}
	}
	s.add(s.identities[policyID], "policy-deny", "db", 1, 1)
	s.replace(s.identities[badDeployID], "deploy", "bad-deploy", 2, 5)
	s.replace(s.identities[breachID], "chaos", "breach", 3, 2)
	sort.SliceStable(s.actions, func(i, j int) bool { return s.simTime(s.actions[i]) < s.simTime(s.actions[j]) })
}

func (s *simulation) add(id *simIdentity, kind, queue string, day int, hour float64) {
	s.actions = append(s.actions, &simAction{identity: id, kind: kind, queue: queue, day: day, hour: hour})
}

// replace turns one of an identity's scheduled actions of a kind into the injected kind at
// a fixed time, keeping the request count per identity constant.
func (s *simulation) replace(id *simIdentity, kind, injected string, day int, hour float64) {
	for _, a := range s.actions {
		if a.identity == id && a.kind == kind {
			a.kind, a.day, a.hour = injected, day, hour
			return
		}
	}
}

func (s *simulation) simTime(a *simAction) float64 {
	return float64(a.day)*simHours + a.hour
}

func (s *simulation) wallTime(a *simAction) time.Time {
	fraction := s.simTime(a) / (simDays * simHours)
	return s.start.Add(time.Duration(fraction * float64(s.cfg.window)))
}

// --- run ---

// run executes every identity's two queues concurrently: an action waits for the previous
// one on the same resource, and starts at its scheduled time or as soon as the queue is
// free, whichever is later. Queueing is recorded apart from platform latency.
func (s *simulation) run() {
	s.start = time.Now()
	queues := map[string][]*simAction{}
	for _, a := range s.actions {
		a.scheduled = s.wallTime(a)
		key := a.identity.name + "/" + a.queue
		queues[key] = append(queues[key], a)
	}
	var wg sync.WaitGroup
	for _, q := range queues {
		wg.Add(1)
		go func(q []*simAction) {
			defer wg.Done()
			for _, a := range q {
				if wait := time.Until(a.scheduled); wait > 0 {
					time.Sleep(wait)
				}
				a.started = time.Now()
				s.execute(a)
				a.done = time.Now()
				s.t.Logf("[%s day %d %04.1fh] %-11s %-10s queued %s took %s -> %s%s", a.identity.name, a.day+1, a.hour, a.kind, a.identity.name,
					a.started.Sub(a.scheduled).Round(time.Second), a.done.Sub(a.started).Round(time.Second), a.outcome, errSuffix(a.err))
			}
		}(q)
	}
	wg.Wait()
	s.t.Logf("the simulated week took %s of wall clock for %d requests", time.Since(s.start).Round(time.Second), len(s.actions))
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return " ERROR " + err.Error()
}

func (s *simulation) execute(a *simAction) {
	id := a.identity
	switch a.kind {
	case "provision":
		a.outcome, a.observed, a.err = s.provision(id)
	case "scale":
		id.scales++
		a.outcome, a.observed, a.err = s.scale(id, id.scales)
	case "deploy", "bad-deploy":
		id.deploys++
		a.outcome, a.observed, a.err = s.deploy(id, id.deploys, a.kind == "bad-deploy")
	case "chaos":
		id.runs++
		a.outcome, a.observed, a.err = s.podKill(id, id.runs)
	case "breach":
		id.runs++
		a.outcome, a.observed, a.err = s.partitionBreach(id, id.runs)
	case "policy-deny":
		a.outcome, a.rtt, a.err = s.policyDeny(id)
	}
}

// --- the requests, each verified from the cluster ---

func (s *simulation) claim(id *simIdentity, name string, storageGB int64) map[string]any {
	return map[string]any{
		"apiVersion": "platform.internal/v1alpha1",
		"kind":       "DatabaseClaim",
		"metadata":   map[string]any{"name": name, "namespace": id.ns},
		"spec": map[string]any{
			"parameters": map[string]any{
				"tier":      "standard",
				"storageGB": storageGB,
				"tags":      map[string]any{"team": id.name, "costCenter": "eng-platform"},
			},
		},
	}
}

func (s *simulation) provision(id *simIdentity) (string, time.Time, error) {
	code, body, err := s.post(id.subject, s.claim(id, "db", 1))
	if err != nil {
		return "", time.Time{}, err
	}
	if code != 202 {
		return "", time.Time{}, fmt.Errorf("claim rejected: %d %v", code, body)
	}
	var pod corev1.Pod
	err = s.until(platformReady, func() bool {
		pods := s.listPods(id.ns, "platform.internal/tenantdatabase", "db")
		if len(pods) == 1 && !podReadyAt(pods[0]).IsZero() {
			pod = pods[0]
			return true
		}
		return false
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("database pod never became ready: %w", err)
	}
	if pod.Spec.SchedulerName != "kiln-scheduler" || pod.Spec.NodeName == "" {
		return "", time.Time{}, fmt.Errorf("database pod %s was not placed by kiln-scheduler", pod.Name)
	}
	return "Ready", podReadyAt(pod), nil
}

func (s *simulation) scale(id *simIdentity, k int) (string, time.Time, error) {
	size := int64(1 + k)
	code, body, err := s.post(id.subject, s.claim(id, "db", size))
	if err != nil {
		return "", time.Time{}, err
	}
	if code != 202 {
		return "", time.Time{}, fmt.Errorf("scale rejected: %d %v", code, body)
	}
	want := fmt.Sprintf("%dGi", size)
	var seen time.Time
	err = s.until(5*time.Minute, func() bool {
		pvc := &corev1.PersistentVolumeClaim{}
		if err := s.c.Get(s.ctx, types.NamespacedName{Namespace: id.ns, Name: "db-data"}, pvc); err != nil {
			return false
		}
		if pvc.Spec.Resources.Requests.Storage().String() == want {
			seen = time.Now()
			return true
		}
		return false
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("data volume never asked to grow to %s: %w", want, err)
	}
	return "Applied", seen, nil
}

// deploy changes the service's pod template and waits until the mesh has settled: traffic
// back on primary and the canary parked. Whether primary now runs the new template or the
// old one is the outcome.
func (s *simulation) deploy(id *simIdentity, k int, bad bool) (string, time.Time, error) {
	args := []string{"server", fmt.Sprintf("-echo-server-default-params=size=%d", 64*k)}
	if bad {
		args = []string{"server", "-echo-server-default-params=status=500:30"}
	}
	if err := s.setArgs(id.ns, args); err != nil {
		return "", time.Time{}, err
	}
	if err := s.until(canaryWait, func() bool { _, c := id.canary.weights(); return c > 0 }); err != nil {
		return "", time.Time{}, fmt.Errorf("the canary never received traffic: %w", err)
	}
	var flipped time.Time
	err := s.until(canaryWait, func() bool {
		p, c := id.canary.weights()
		if p == 100 && c == 0 && flipped.IsZero() {
			flipped = time.Now()
		}
		return p == 100 && c == 0 && id.canary.replicas(targetApp) == 0 && len(s.listPods(id.ns, labelRole, "canary")) == 0
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("the rollout never settled: %w", err)
	}
	primary := &appsv1.Deployment{}
	if err := s.c.Get(s.ctx, types.NamespacedName{Namespace: id.ns, Name: targetApp + "-primary"}, primary); err != nil {
		return "", time.Time{}, err
	}
	if equalArgs(primary.Spec.Template.Spec.Containers[0].Args, args) {
		return "Promoted", flipped, nil
	}
	return "RolledBack", flipped, nil
}

func equalArgs(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

func (s *simulation) setArgs(ns string, args []string) error {
	d := &appsv1.Deployment{}
	if err := s.c.Get(s.ctx, types.NamespacedName{Namespace: ns, Name: targetApp}, d); err != nil {
		return err
	}
	before := d.DeepCopy()
	d.Spec.Template.Spec.Containers[0].Args = args
	return s.c.Patch(s.ctx, d, client.MergeFrom(before))
}

func (s *simulation) experiment(id *simIdentity, name string, spec map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": "platform.internal/v1",
		"kind":       "ChaosExperiment",
		"metadata":   map[string]any{"name": name, "namespace": id.ns},
		"spec":       spec,
	}
}

// podKill runs the Phase 5 pod-kill experiment against the identity's service. Kills are
// proven by a pod UID disappearing; the end is when the experiment leaves Running.
func (s *simulation) podKill(id *simIdentity, k int) (string, time.Time, error) {
	name := fmt.Sprintf("chaos-%d", k)
	before := id.chaos.podUIDs(targetApp)
	code, body, err := s.post(id.subject, s.experiment(id, name, map[string]any{
		"target":           map[string]any{"labelSelector": "app=" + targetApp, "maxReplicaPercentage": int64(50)},
		"faultType":        "pod-kill",
		"duration":         "40s",
		"abortOnSLOBreach": map[string]any{"errorRateMax": 0.95, "latencyP99MaxMs": int64(2000)},
		"fault":            map[string]any{"interval": "10s"},
		"analysis":         map[string]any{"interval": "5s"},
	}))
	if err != nil {
		return "", time.Time{}, err
	}
	if code != 202 {
		return "", time.Time{}, fmt.Errorf("experiment rejected: %d %v", code, body)
	}
	defer id.chaos.deleteExperiment(name)
	killed := false
	var ended time.Time
	err = s.until(4*time.Minute, func() bool {
		if !killed {
			now := id.chaos.podUIDs(targetApp)
			for uid := range before {
				if !now[uid] {
					killed = true
				}
			}
		}
		phase := id.chaos.phase(name)
		if phase == "Completed" || phase == "Aborted" {
			ended = time.Now()
			return true
		}
		return false
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("the experiment never ended: %w", err)
	}
	if !killed {
		return "", time.Time{}, errors.New("no pod was actually killed")
	}
	if err := s.until(30*time.Second, func() bool { return len(id.chaos.pods(targetApp)) >= 2 }); err != nil {
		return "", time.Time{}, fmt.Errorf("the service did not recover its pods: %w", err)
	}
	return id.chaos.phase(name), ended, nil
}

// partitionBreach is the Phase 5 forced breach: a full partition of the service must trip
// the abort, and the abort is real only when the iptables rule is gone from every target.
func (s *simulation) partitionBreach(id *simIdentity, k int) (string, time.Time, error) {
	name := fmt.Sprintf("chaos-%d", k)
	code, body, err := s.post(id.subject, s.experiment(id, name, map[string]any{
		"target":           map[string]any{"labelSelector": "app=" + targetApp, "maxReplicaPercentage": int64(100)},
		"faultType":        "network-partition",
		"duration":         "120s",
		"abortOnSLOBreach": map[string]any{"errorRateMax": 0.2, "latencyP99MaxMs": int64(1000)},
		"analysis":         map[string]any{"interval": "5s"},
	}))
	if err != nil {
		return "", time.Time{}, err
	}
	if code != 202 {
		return "", time.Time{}, fmt.Errorf("experiment rejected: %d %v", code, body)
	}
	defer id.chaos.deleteExperiment(name)
	var targets []string
	if err := s.until(2*time.Minute, func() bool { targets = id.chaos.targetPods(name); return len(targets) >= 1 }); err != nil {
		return "", time.Time{}, fmt.Errorf("no targets selected: %w", err)
	}
	if err := s.until(90*time.Second, func() bool { return id.chaos.hasDropChain(targets[0]) }); err != nil {
		return "", time.Time{}, fmt.Errorf("the partition never landed on %s: %w", targets[0], err)
	}
	if err := s.until(3*time.Minute, func() bool { return id.chaos.abortReason(name) == "SLOBreach" }); err != nil {
		return "", time.Time{}, fmt.Errorf("a full partition did not trip the abort: %w", err)
	}
	var cleared time.Time
	for _, pod := range targets {
		if err := s.until(abortClearBound, func() bool { return !id.chaos.hasDropChain(pod) }); err != nil {
			return "", time.Time{}, fmt.Errorf("partition still on %s after the abort: %w", pod, err)
		}
		cleared = time.Now()
	}
	return "Aborted", cleared, nil
}

// policyDeny submits a claim over the storage ceiling; the guardrail is the 422 and, from
// the cluster, the absence of any object.
func (s *simulation) policyDeny(id *simIdentity) (string, time.Duration, error) {
	began := time.Now()
	code, body, err := s.post(id.subject, s.claim(id, "oversized", 500))
	rtt := time.Since(began)
	if err != nil {
		return "", rtt, err
	}
	if code != 422 || body["code"] != "POLICY_DENIED" {
		return "", rtt, fmt.Errorf("an oversized claim was not denied: %d %v", code, body)
	}
	if obj := (&harness{ctx: s.ctx, c: s.c}).get(gvkClaim, id.ns, "oversized"); obj != nil {
		return "", rtt, errors.New("a denied claim exists on the cluster")
	}
	return "Denied", rtt, nil
}

// --- goroutine-safe primitives ---

func (s *simulation) post(subject string, body any) (int, map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(s.ctx, "POST", s.base+"/v1/requests", strings.NewReader(string(raw)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.tokenFor(subject))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, nil
}

func (s *simulation) tokenFor(subject string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token(subject, "requests:submit", "audit:read")
}

func (s *simulation) until(timeout time.Duration, cond func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not within %s", timeout)
		}
		time.Sleep(simPoll)
	}
}

func (s *simulation) listPods(ns, label, value string) []corev1.Pod {
	list := &corev1.PodList{}
	if err := s.c.List(s.ctx, list, client.InNamespace(ns), client.MatchingLabels{label: value}); err != nil {
		return nil
	}
	return list.Items
}

// --- the report ---

type sample struct {
	action  *simAction
	latency time.Duration
	// audit is the terminal row's timestamp; observed is the cluster's own view of the same
	// moment, and the two must agree within the tolerance.
	audit time.Time
}

type reportRow struct {
	name      string
	baseline  string
	samples   []sample
	n         int
	errors    int
	guardrail string
	note      string
}

func (s *simulation) report() {
	g := s.g
	rows := s.rowsOfRun()
	s.t.Logf("%d audit rows belong to this run", len(rows))
	g.Expect(verifyChain(s.readRows())).To(BeEmpty(), "the chain recomputed from every row must hold after the week")
	code, verify := s.call("GET", "/v1/audit/verify", s.token("auditor@kiln.sim", "audit:admin"), nil)
	g.Expect(code).To(Equal(200))
	g.Expect(verify["ok"]).To(BeTrue())
	s.expectNoPublishFailures()

	table := []*reportRow{
		s.provisionRow(rows),
		s.scaleRow(rows),
		s.deployRow(rows, "deploy", "Healthy deploy (canary promoted)", "Promoted", ""),
		s.deployRow(rows, "bad-deploy", "Bad deploy (injected regression)", "RolledBack", "auto-rollback"),
		s.chaosRow(rows, "chaos", "Chaos test (pod-kill, completed)", "Completed", ""),
		s.chaosRow(rows, "breach", "Chaos test (injected SLO breach)", "Aborted", "auto-abort, fault removed on the node"),
		s.policyRow(rows),
		s.auditQueryRow(rows),
	}
	md := s.render(table)
	s.t.Log("\n" + md)
	g.Expect(os.WriteFile(s.cfg.report, []byte(md), 0o644)).To(Succeed())
	s.t.Logf("comparison table written to %s", s.cfg.report)

	var failures []string
	for _, a := range s.actions {
		if a.err != nil {
			failures = append(failures, fmt.Sprintf("%s %s: %v", a.identity.name, a.kind, a.err))
		}
	}
	g.Expect(failures).To(BeEmpty(), "every request must complete or be stopped by the guardrail it was meant to trip")
	for _, r := range table {
		g.Expect(r.n).To(BeNumerically(">", 0), "row %q has no samples", r.name)
		g.Expect(r.errors).To(BeZero(), "row %q has %d errors; a table with an unexplained failure is not the deliverable", r.name, r.errors)
	}
}

// rowsOfRun keeps the rows about this run's namespaces, in seq order.
func (s *simulation) rowsOfRun() []auditRow {
	prefix := fmt.Sprintf("/sim-%d-", s.runID)
	var out []auditRow
	for _, r := range s.readRows() {
		if strings.Contains(r.resource, prefix) {
			out = append(out, r)
		}
	}
	return out
}

func (s *simulation) actionsOf(kind string) []*simAction {
	var out []*simAction
	for _, a := range s.actions {
		if a.kind == kind {
			out = append(out, a)
		}
	}
	return out
}

func rowsFor(rows []auditRow, action, outcome, resource string) []auditRow {
	var out []auditRow
	for _, r := range rows {
		if r.action == action && r.resource == resource && strings.Contains(r.details, `"outcome": "`+outcome+`"`) {
			out = append(out, r)
		}
	}
	return out
}

func (s *simulation) provisionRow(rows []auditRow) *reportRow {
	r := &reportRow{name: "Provisioning (standard Postgres)", baseline: baselineProvisioning}
	for _, a := range s.actionsOf("provision") {
		r.n++
		if a.err != nil {
			r.errors++
			continue
		}
		claimRef := "DatabaseClaim/" + a.identity.ns + "/db"
		tdbRef := "TenantDatabase/" + a.identity.ns + "/db"
		requests := rowsFor(rows, "PROVISION_REQUEST", "Received", claimRef)
		ready := rowsFor(rows, "PROVISION", "Ready", tdbRef)
		if len(requests) == 0 || len(ready) == 0 {
			r.errors++
			a.err = errors.New("audit trail is missing the request or the Ready row")
			continue
		}
		from, to := rowTime(s.g, requests[0].occurredAt), rowTime(s.g, ready[0].occurredAt)
		r.samples = append(r.samples, sample{action: a, latency: to.Sub(from), audit: to})
		s.crossCheck(a, to, provisionTolerance)
	}
	r.note = "submitted = PROVISION_REQUEST row, done = PROVISION Ready row; cross-checked against the pod's Ready condition"
	return r
}

func (s *simulation) scaleRow(rows []auditRow) *reportRow {
	r := &reportRow{name: "Scaling (storage growth)", baseline: baselineProvisioning}
	perIdentity := map[*simIdentity]int{}
	for _, a := range s.actionsOf("scale") {
		r.n++
		if a.err != nil {
			r.errors++
			continue
		}
		claimRef := "DatabaseClaim/" + a.identity.ns + "/db"
		tdbRef := "TenantDatabase/" + a.identity.ns + "/db"
		k := perIdentity[a.identity]
		perIdentity[a.identity]++
		requests := rowsFor(rows, "PROVISION_REQUEST", "Received", claimRef)
		applied := rowsFor(rows, "SCALE", "Applied", tdbRef)
		if len(requests) < k+2 || len(applied) < k+1 {
			r.errors++
			a.err = errors.New("audit trail is missing the re-submission or the SCALE row")
			continue
		}
		from, to := rowTime(s.g, requests[k+1].occurredAt), rowTime(s.g, applied[k].occurredAt)
		r.samples = append(r.samples, sample{action: a, latency: to.Sub(from), audit: to})
		s.crossCheck(a, to, actionTolerance)
	}
	r.note = "submitted = the claim's re-submission row, done = SCALE Applied row; on kind the volume is asked to grow, the provisioner does not resize it"
	return r
}

func (s *simulation) deployRow(rows []auditRow, kind, name, terminal, guardrail string) *reportRow {
	r := &reportRow{name: name, baseline: baselineDelivery}
	fired := 0
	perIdentity := map[*simIdentity]int{}
	for _, a := range s.actionsOf(kind) {
		r.n++
		if a.err != nil {
			r.errors++
			continue
		}
		if a.outcome != terminal {
			r.errors++
			a.err = fmt.Errorf("the mesh showed %s, expected %s", a.outcome, terminal)
			continue
		}
		ref := "CanaryRollout/" + a.identity.ns + "/" + targetApp
		started := s.rolloutsOf(rows, ref)
		// This identity's k-th deploy of the week is its k-th rollout after the week began.
		k := s.deployIndex(a, perIdentity)
		if k >= len(started) {
			r.errors++
			a.err = errors.New("audit trail is missing the DEPLOY Started row")
			continue
		}
		from := rowTime(s.g, started[k].occurredAt)
		end := terminalRow(rows, ref, started[k], terminal)
		if end == nil {
			r.errors++
			a.err = fmt.Errorf("audit trail is missing the %s row for template %s", terminal, hashOf(started[k]))
			continue
		}
		to := rowTime(s.g, end.occurredAt)
		r.samples = append(r.samples, sample{action: a, latency: to.Sub(from), audit: to})
		s.crossCheck(a, to, actionTolerance)
		if guardrail != "" {
			fired++
		}
	}
	if guardrail != "" {
		r.guardrail = fmt.Sprintf("%s fired %d/%d", guardrail, fired, r.n)
	}
	r.note = "submitted = DEPLOY Started row, done = DEPLOY Promoted or ROLLBACK row; cross-checked against the VirtualService flipping back to primary"
	return r
}

// rolloutsOf lists the DEPLOY Started rows of a rollout that began during the week, in
// order; the adoption of the initial version happened during setup.
func (s *simulation) rolloutsOf(rows []auditRow, ref string) []auditRow {
	var out []auditRow
	for _, r := range rowsFor(rows, "DEPLOY", "Started", ref) {
		if rowTime(s.g, r.occurredAt).After(s.start) {
			out = append(out, r)
		}
	}
	return out
}

func (s *simulation) deployIndex(a *simAction, perIdentity map[*simIdentity]int) int {
	// Deploys and bad deploys share the identity's rollout sequence; count in schedule order.
	k := 0
	for _, b := range s.actions {
		if b.identity == a.identity && (b.kind == "deploy" || b.kind == "bad-deploy") {
			if b == a {
				break
			}
			k++
		}
	}
	perIdentity[a.identity] = k + 1
	return k
}

func hashOf(r auditRow) string {
	var d map[string]any
	_ = json.Unmarshal([]byte(r.details), &d)
	h, _ := d["templateHash"].(string)
	return h
}

func terminalRow(rows []auditRow, ref string, started auditRow, terminal string) *auditRow {
	hash := hashOf(started)
	action := "DEPLOY"
	if terminal == "RolledBack" {
		action = "ROLLBACK"
	}
	for _, r := range rowsFor(rows, action, terminal, ref) {
		if hashOf(r) == hash && r.seq > started.seq {
			return &r
		}
	}
	return nil
}

func (s *simulation) chaosRow(rows []auditRow, kind, name, terminal, guardrail string) *reportRow {
	r := &reportRow{name: name, baseline: baselineChaos}
	fired := 0
	perIdentity := map[*simIdentity]int{}
	for _, a := range s.actionsOf(kind) {
		r.n++
		if a.err != nil {
			r.errors++
			continue
		}
		if a.outcome != terminal {
			r.errors++
			a.err = fmt.Errorf("the experiment ended %s, expected %s", a.outcome, terminal)
			continue
		}
		perIdentity[a.identity]++
		k := s.chaosIndex(a)
		ref := fmt.Sprintf("ChaosExperiment/%s/chaos-%d", a.identity.ns, k)
		started := rowsFor(rows, "CHAOS_EXPERIMENT", "Started", ref)
		ended := rowsFor(rows, "CHAOS_EXPERIMENT", terminal, ref)
		if len(started) == 0 || len(ended) == 0 {
			r.errors++
			a.err = fmt.Errorf("audit trail is missing the Started or %s row for %s", terminal, ref)
			continue
		}
		from, to := rowTime(s.g, started[0].occurredAt), rowTime(s.g, ended[0].occurredAt)
		r.samples = append(r.samples, sample{action: a, latency: to.Sub(from), audit: to})
		if kind == "breach" {
			// The abort row is written when the breach is seen; the node clears the fault
			// within the bound after that.
			lag := a.observed.Sub(to)
			s.g.Expect(lag).To(BeNumerically(">=", -actionTolerance), "the fault cleared before the abort was recorded")
			s.g.Expect(lag).To(BeNumerically("<=", abortClearBound), "the fault outlived the abort bound")
			s.t.Logf("%s breach: fault gone from the node %s after the Aborted row", a.identity.name, lag.Round(time.Second))
		} else {
			s.crossCheck(a, to, actionTolerance)
		}
		if guardrail != "" {
			fired++
		}
	}
	if guardrail != "" {
		r.guardrail = fmt.Sprintf("%s fired %d/%d", guardrail, fired, r.n)
	}
	r.note = "submitted = CHAOS_EXPERIMENT Started row, done = Completed or Aborted row; the breach is cross-checked against the iptables rule clearing on the node"
	return r
}

func (s *simulation) chaosIndex(a *simAction) int {
	k := 0
	for _, b := range s.actions {
		if b.identity == a.identity && (b.kind == "chaos" || b.kind == "breach") {
			k++
			if b == a {
				return k
			}
		}
	}
	return k
}

// policyRow measures the denial from the HTTP request span in Jaeger, found through the
// POLICY_DENY record's traceparent header on the broker.
func (s *simulation) policyRow(rows []auditRow) *reportRow {
	r := &reportRow{name: "Policy violation (storage over the ceiling)", baseline: baselineProvisioning}
	records := s.readTopicHeaders()
	fired := 0
	for _, a := range s.actionsOf("policy-deny") {
		r.n++
		if a.err != nil {
			r.errors++
			continue
		}
		ref := "DatabaseClaim/" + a.identity.ns + "/oversized"
		if len(rowsFor(rows, "POLICY_DENY", "Denied", ref)) == 0 {
			r.errors++
			a.err = errors.New("audit trail is missing the POLICY_DENY row")
			continue
		}
		rec := findRecord(records, expectedEvent{"POLICY_DENY", "Denied", ref, a.identity.subject})
		latency := a.rtt
		note := "client round trip"
		if rec != nil && traceParentRe.MatchString(rec.headers["traceparent"]) {
			a.traceID = traceParentRe.FindStringSubmatch(rec.headers["traceparent"])[1]
			var spans []span
			_ = s.until(traceSettle, func() bool { spans = s.trace(a.traceID); return root(spans).id != "" })
			if rs := root(spans); rs.id != "" {
				latency = rs.end.Sub(rs.start)
				note = "HTTP request span in Jaeger, trace " + a.traceID
			}
		}
		s.t.Logf("%s policy denial: %s (%s), client saw %s", a.identity.name, latency.Round(time.Millisecond), note, a.rtt.Round(time.Millisecond))
		r.samples = append(r.samples, sample{action: a, latency: latency})
		fired++
	}
	r.guardrail = fmt.Sprintf("policy denial fired %d/%d", fired, r.n)
	r.note = "latency = the HTTP request span of the denied POST, found from the POLICY_DENY record's traceparent header; no object exists on the cluster afterwards"
	return r
}

// auditQueryRow is the Audit/RBAC target: an actor query over the week's rows, timed by
// the client and cross-checked against the row count read with psql.
func (s *simulation) auditQueryRow(rows []auditRow) *reportRow {
	r := &reportRow{name: "Audit query (actor, time range)", baseline: baselineAudit}
	// The window is the week itself; the rollouts every identity submitted during setup
	// fall before it, and the psql count must apply the same bound.
	fromT := s.start.UTC().Add(-time.Minute)
	from := fromT.Format(time.RFC3339)
	to := time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	for _, id := range s.identities {
		r.n++
		expected := 0
		for _, row := range rows {
			if row.actor == id.subject && !rowTime(s.g, row.occurredAt).Before(fromT) {
				expected++
			}
		}
		q := fmt.Sprintf("/v1/audit?actor=%s&from=%s&to=%s&limit=1000", url.QueryEscape(id.subject), url.QueryEscape(from), url.QueryEscape(to))
		began := time.Now()
		code, body := s.call("GET", q, s.token("auditor@kiln.sim", "audit:read"), nil)
		latency := time.Since(began)
		entries, _ := body["entries"].([]any)
		if code != 200 || len(entries) != expected {
			r.errors++
			s.t.Logf("%s audit query: %d, %d entries, expected %d from psql", id.name, code, len(entries), expected)
			continue
		}
		r.samples = append(r.samples, sample{latency: latency})
	}
	r.note = "client round trip of GET /v1/audit per identity; the entry count must equal the rows psql returns for that actor"
	return r
}

// crossCheck requires the audit row's time and the cluster's own observation of the same
// transition to agree within the tolerance; the observation lags by at most one poll.
func (s *simulation) crossCheck(a *simAction, audit time.Time, tolerance time.Duration) {
	if a.observed.IsZero() {
		return
	}
	diff := a.observed.Sub(audit)
	s.g.Expect(diff).To(BeNumerically("~", 0, tolerance+simPoll), "%s %s: the audit row (%s) and the cluster (%s) disagree", a.identity.name, a.kind, audit.Format(time.RFC3339Nano), a.observed.Format(time.RFC3339Nano))
}

func (s *simulation) expectNoPublishFailures() {
	query := func(expr string) float64 {
		resp, err := http.Get(s.prometheus + "/api/v1/query?query=" + url.QueryEscape(expr))
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
	published := query("sum(kiln_audit_events_published_total)")
	failures := query("sum(kiln_audit_publish_failures_total) or vector(0)")
	s.t.Logf("prometheus: %v audit events acknowledged by kafka, %v publish failures", published, failures)
	s.g.Expect(published).To(BeNumerically(">", 0), "the controllers' publish counters must be scraped")
	s.g.Expect(failures).To(Equal(0.0), "no audit event may have been dropped during the week (ADR-0017)")
}

// --- rendering ---

const (
	baselineProvisioning = "Manual Terraform PR with human review: hours to a day before merge and apply."
	baselineDelivery     = "Manual on-call detection of a bad deploy (minutes to tens of minutes), manual rollback."
	baselineChaos        = "Zero pre-production resilience coverage; failure modes discovered during real incidents."
	baselineAudit        = "Manual compliance log reconstruction across disparate systems, commonly a multi-day effort at audit time."
)

func percentile(samples []sample, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]time.Duration, 0, len(samples))
	for _, s := range samples {
		sorted = append(sorted, s.latency)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(float64(len(sorted))*p+0.5) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func (s *simulation) render(table []*reportRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run %d: %d identities, one simulated week (%d working days) in %s of wall clock, seed %d, %d requests.\n\n",
		s.runID, len(s.identities), simDays, s.cfg.window, s.cfg.seed, len(s.actions))
	b.WriteString("| Request type | Baseline (status quo) | Platform p50 | Platform p95 | n | Error rate | Guardrail |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, r := range table {
		guard := r.guardrail
		if guard == "" {
			guard = "-"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %s | %s |\n", r.name, r.baseline,
			formatDuration(percentile(r.samples, 0.5)), formatDuration(percentile(r.samples, 0.95)), r.n,
			formatRate(r.errors, r.n), guard)
	}
	b.WriteString("\nHow each row is measured:\n\n")
	for _, r := range table {
		fmt.Fprintf(&b, "- **%s**: %s.\n", r.name, r.note)
	}
	return b.String()
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return d.Round(time.Second).String()
}

func formatRate(errors, n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%% (%d/%d)", 100*float64(errors)/float64(n), errors, n)
}
