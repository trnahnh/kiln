//go:build e2e

// Phase 7, first half: a request submitted through the REST path is one OpenTelemetry
// trace across every subsystem it touches (ADR-0021). Each traced request is proven from
// the CR's annotation and the pod on the cluster, the record headers read from the broker,
// and the spans Jaeger holds, never from a subsystem's own status.
package e2e

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	monitoringNS    = "monitoring"
	traceSubject    = "phase7@kiln.e2e"
	annotationTrace = "platform.internal/traceparent"
	// The batching exporters flush within seconds; Jaeger indexes on arrival.
	traceSettle = 2 * time.Minute
	// Audit timestamps, cluster timestamps and span timestamps come from the same host
	// clock and are set at the moment of each transition; publishing is asynchronous but
	// the timestamp is fixed before the queue.
	crossCheckTolerance = 5 * time.Second
)

var traceParentRe = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-0[01]$`)

type traceHarness struct {
	*auditHarness
	jaeger     string
	stopJaeger chan struct{}
}

type span struct {
	service, name, id, parent string
	start, end                time.Time
}

func TestPhase7Validation(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := ctrl.GetConfigOrDie()
	c, err := client.New(cfg, client.Options{})
	g.Expect(err).NotTo(HaveOccurred())
	cs, err := kubernetes.NewForConfig(cfg)
	g.Expect(err).NotTo(HaveOccurred())

	runID := time.Now().Unix()
	a := &auditHarness{t: t, g: g, ctx: ctx, cfg: cfg, c: c, cs: cs, ns: fmt.Sprintf("phase7-%d", runID), runID: runID}
	a.loadSigningKey()
	a.waitForAudit()
	a.allowVolumeExpansion()
	a.createNamespace()
	t.Cleanup(a.deleteNamespace)
	a.forward()
	t.Cleanup(a.stopForward)
	h := &traceHarness{auditHarness: a}
	h.forwardJaeger()
	t.Cleanup(func() { close(h.stopJaeger) })

	t.Run("a database request is one trace from the REST call to the scheduled pod", h.testClaimTrace)
	t.Run("a rollout request is one trace from the REST call to the rollback", h.testRolloutTrace)
	t.Run("a chaos request is one trace from the REST call to its result", h.testExperimentTrace)
	t.Run("a simulated week of ten developers produces the comparison table", h.testSimulatedWeek)
}

func (h *traceHarness) testClaimTrace(t *testing.T) {
	g := NewWithT(t)
	h.g = g
	name := "traced-db"
	code, body := h.call("POST", "/v1/requests", h.token(traceSubject, "requests:submit"), h.claimBody(name, 1))
	g.Expect(code).To(Equal(202), "submission failed: %v", body)

	claim := h.get(gvkClaim, name)
	g.Expect(claim).NotTo(BeNil())
	traceID := h.traceIDOf(claim.GetAnnotations())
	t.Logf("request trace %s", traceID)

	g.Eventually(func() string { return h.tdbPhase(name) }, platformReady, poll).Should(Equal("Ready"), "the operator must provision the database")
	tdb := h.get(gvkTenant, name)
	g.Expect(h.traceIDOf(tdb.GetAnnotations())).To(Equal(traceID), "the composition carries the trace to the TenantDatabase")

	pods := h.podsIn(h.ns, "platform.internal/tenantdatabase", name)
	g.Expect(pods).To(HaveLen(1))
	pod := pods[0]
	g.Expect(pod.Spec.SchedulerName).To(Equal("kiln-scheduler"), "database pods are placed by kiln-scheduler")
	g.Expect(pod.Spec.NodeName).NotTo(BeEmpty())
	g.Expect(h.traceIDOf(pod.Annotations)).To(Equal(traceID), "the pod carries the trace its placement belongs to")

	claimRef := "DatabaseClaim/" + h.ns + "/" + name
	tdbRef := "TenantDatabase/" + h.ns + "/" + name
	podRef := "Pod/" + h.ns + "/" + pod.Name
	h.expectHeaders(traceID,
		expectedEvent{"PROVISION_REQUEST", "Received", claimRef, traceSubject},
		expectedEvent{"PROVISION", "Ready", tdbRef, traceSubject},
		expectedEvent{"SCHEDULE", "Bound", podRef, traceSubject},
	)

	spans := h.expectTrace(traceID,
		[]string{"audit-service", "kiln-operator", "kiln-scheduler"},
		[]string{"admission", "PROVISION", "SCHEDULE"})

	rows := h.readRows()
	request := findRow(rows, expectedEvent{"PROVISION_REQUEST", "Received", claimRef, traceSubject})
	ready := findRow(rows, expectedEvent{"PROVISION", "Ready", tdbRef, traceSubject})
	g.Expect(request).NotTo(BeNil())
	g.Expect(ready).NotTo(BeNil())
	fromAudit := rowTime(g, ready.occurredAt).Sub(rowTime(g, request.occurredAt))
	fromCluster := podReadyAt(pod).Sub(claim.GetCreationTimestamp().Time)
	provision := spanNamed(spans, "PROVISION")
	fromTrace := provision.end.Sub(root(spans).start)
	t.Logf("provisioning latency: audit trail %s, cluster state %s, trace %s", fromAudit.Round(time.Millisecond), fromCluster.Round(time.Second), fromTrace.Round(time.Millisecond))
	g.Expect(fromAudit-fromCluster).To(BeNumerically("~", 0, provisionTolerance), "audit rows and cluster state disagree on provisioning latency")
	g.Expect(fromTrace-fromAudit).To(BeNumerically("~", 0, crossCheckTolerance), "the trace and the audit rows disagree on provisioning latency")
}

func (h *traceHarness) testRolloutTrace(t *testing.T) {
	g := NewWithT(t)
	h.g = g
	ch := &canaryHarness{t: t, g: g, ctx: h.ctx, cfg: h.cfg, c: h.c, cs: h.cs, ns: fmt.Sprintf("phase7-canary-%d", h.runID), start: map[string]time.Time{}}
	ch.createRollout = h.submitCR
	ch.createMeshNamespace()
	t.Cleanup(ch.deleteNamespace)
	ch.deployTarget()
	ch.initialVersion(t)
	ch.brokenVersion(t)

	cr := (&harness{ctx: h.ctx, c: h.c}).get(gvkCanary, ch.ns, targetApp)
	g.Expect(cr).NotTo(BeNil())
	traceID := h.traceIDOf(cr.GetAnnotations())
	t.Logf("request trace %s", traceID)
	ref := "CanaryRollout/" + ch.ns + "/" + targetApp
	h.expectHeaders(traceID,
		expectedEvent{"PROVISION_REQUEST", "Received", ref, traceSubject},
		expectedEvent{"DEPLOY", "Started", ref, traceSubject},
		expectedEvent{"ROLLBACK", "RolledBack", ref, traceSubject},
	)
	h.expectTrace(traceID, []string{"audit-service", "kiln-delivery-controller"}, []string{"admission", "DEPLOY", "ROLLBACK"})
}

func (h *traceHarness) testExperimentTrace(t *testing.T) {
	g := NewWithT(t)
	h.g = g
	ch := &chaosHarness{t: t, g: g, ctx: h.ctx, c: h.c, cs: h.cs, ns: fmt.Sprintf("phase7-chaos-%d", h.runID)}
	var traceID string
	ch.createExperiment = func(cr *unstructured.Unstructured) {
		h.submitCR(cr)
		// The Phase 5 subtest deletes the experiment when it is done; the trace is read now.
		traceID = h.traceIDOf(cr.GetAnnotations())
	}
	ch.createMeshNamespace()
	t.Cleanup(ch.deleteNamespace)
	ch.deployTarget()
	ch.testPodKillScored(t)

	t.Logf("request trace %s", traceID)
	ref := "ChaosExperiment/" + ch.ns + "/podkill"
	h.expectHeaders(traceID,
		expectedEvent{"PROVISION_REQUEST", "Received", ref, traceSubject},
		expectedEvent{"CHAOS_EXPERIMENT", "Started", ref, traceSubject},
		expectedEvent{"CHAOS_EXPERIMENT", "Completed", ref, traceSubject},
	)
	h.expectTrace(traceID, []string{"audit-service", "kiln-chaos-controller"}, []string{"admission", "CHAOS_EXPERIMENT"})
}

// submitCR sends a CR through POST /v1/requests and reads back what the service applied, so
// the caller sees the annotations the REST path stamped.
func (h *traceHarness) submitCR(cr *unstructured.Unstructured) {
	code, body := h.call("POST", "/v1/requests", h.token(traceSubject, "requests:submit"), cr.Object)
	h.g.Expect(code).To(Equal(202), "submission failed: %v", body)
	applied := (&harness{ctx: h.ctx, c: h.c}).get(cr.GroupVersionKind(), cr.GetNamespace(), cr.GetName())
	h.g.Expect(applied).NotTo(BeNil(), "%s/%s was not applied", cr.GetKind(), cr.GetName())
	cr.Object = applied.Object
}

// traceIDOf asserts the annotation the REST path stamps is a W3C traceparent and returns
// its trace id.
func (h *traceHarness) traceIDOf(annotations map[string]string) string {
	tp := annotations[annotationTrace]
	h.g.Expect(tp).To(MatchRegexp(traceParentRe.String()), "no valid traceparent among %v", annotations)
	return traceParentRe.FindStringSubmatch(tp)[1]
}

// expectHeaders reads the topic from the broker pod and requires each event's record to
// carry the trace as a W3C traceparent header.
func (h *traceHarness) expectHeaders(traceID string, events ...expectedEvent) {
	h.g.Eventually(func() []string {
		records := h.readTopicHeaders()
		var missing []string
		for _, e := range events {
			rec := findRecord(records, e)
			switch {
			case rec == nil:
				missing = append(missing, fmt.Sprintf("%s/%s on %s: not on the topic", e.action, e.outcome, e.resource))
			case !strings.HasPrefix(rec.headers["traceparent"], "00-"+traceID+"-"):
				missing = append(missing, fmt.Sprintf("%s/%s on %s: headers %v do not carry trace %s", e.action, e.outcome, e.resource, rec.headers, traceID))
			}
		}
		return missing
	}, auditSettle, poll).Should(BeEmpty())
}

// expectTrace fetches the trace from Jaeger and requires spans from every named service
// and with every named span name, each of the named spans a direct child of the HTTP
// request span; it returns the spans for further checks.
func (h *traceHarness) expectTrace(traceID string, services, names []string) []span {
	var spans []span
	h.g.Eventually(func() []string {
		spans = h.trace(traceID)
		var missing []string
		for _, s := range services {
			if !hasService(spans, s) {
				missing = append(missing, "service "+s)
			}
		}
		for _, n := range names {
			if spanNamed(spans, n).id == "" {
				missing = append(missing, "span "+n)
			}
		}
		return missing
	}, traceSettle, poll).Should(BeEmpty(), "trace %s is incomplete", traceID)
	h.t.Log(renderTrace(spans))

	rootSpan := root(spans)
	h.g.Expect(rootSpan.service).To(Equal("audit-service"), "the trace starts at the REST path")
	h.g.Expect(strings.ToLower(rootSpan.name)).To(ContainSubstring("/v1/requests"), "the root span is the HTTP request")
	for _, n := range names {
		// Spring Security wraps the handler in its own "secured request" span, so the
		// controllers' spans descend from the HTTP request through it.
		h.g.Expect(descendsFrom(spans, spanNamed(spans, n), rootSpan.id)).To(BeTrue(), "span %s must descend from the request span", n)
	}
	h.g.Expect(consumerSpans(spans)).NotTo(BeEmpty(), "the audit service's consume-and-store side must join the trace through the record headers")
	return spans
}

func descendsFrom(spans []span, s span, ancestor string) bool {
	byID := map[string]span{}
	for _, x := range spans {
		byID[x.id] = x
	}
	for p := s.parent; p != ""; {
		if p == ancestor {
			return true
		}
		next, ok := byID[p]
		if !ok {
			return false
		}
		p = next.parent
	}
	return false
}

func (h *traceHarness) forwardJaeger() {
	pods := h.podsIn(monitoringNS, "app", "jaeger")
	h.g.Expect(pods).NotTo(BeEmpty(), "no jaeger pod: apply gitops/observability/")
	h.jaeger, h.stopJaeger = h.portForward(monitoringNS, pods[0].Name, 16686)
	h.g.Eventually(func() int {
		resp, err := http.Get(h.jaeger + "/api/v3/services")
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}, time.Minute, poll).Should(Equal(200))
}

func (h *auditHarness) portForward(ns, pod string, port int) (string, chan struct{}) {
	return portForward(h.t, h.g, h.cfg, h.cs, ns, pod, port)
}

// portForward opens a port-forward to one pod and returns the local base URL; the port is
// chosen by the kernel.
func portForward(t *testing.T, g *WithT, cfg *rest.Config, cs *kubernetes.Clientset, ns, pod string, port int) (string, chan struct{}) {
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("portforward")
	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	g.Expect(err).NotTo(HaveOccurred())
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())
	stop := make(chan struct{})
	ready := make(chan struct{})
	fw, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", port)}, stop, ready, io.Discard, os.Stderr)
	g.Expect(err).NotTo(HaveOccurred())
	go func() {
		if err := fw.ForwardPorts(); err != nil {
			t.Logf("port-forward to %s/%s ended: %v", ns, pod, err)
		}
	}()
	<-ready
	ports, err := fw.GetPorts()
	g.Expect(err).NotTo(HaveOccurred())
	return fmt.Sprintf("http://127.0.0.1:%d", ports[0].Local), stop
}

// trace reads one trace from Jaeger's query API as OTLP JSON.
func (h *traceHarness) trace(traceID string) []span {
	resp, err := http.Get(h.jaeger + "/api/v3/traces/" + traceID)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil
	}
	var doc struct {
		Result        otlpTrace `json:"result"`
		ResourceSpans []otlpResourceSpans
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		h.t.Logf("jaeger response for %s: %v: %.300s", traceID, err, raw)
		return nil
	}
	resources := doc.Result.ResourceSpans
	if len(resources) == 0 {
		resources = doc.ResourceSpans
	}
	var out []span
	for _, rs := range resources {
		service := attr(rs.Resource.Attributes, "service.name")
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				out = append(out, span{
					service: service, name: s.Name, id: idHex(s.SpanID), parent: idHex(s.ParentSpanID),
					start: unixNano(s.StartTimeUnixNano), end: unixNano(s.EndTimeUnixNano),
				})
			}
		}
	}
	return out
}

type otlpTrace struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource struct {
		Attributes []otlpAttr `json:"attributes"`
	} `json:"resource"`
	ScopeSpans []struct {
		Spans []struct {
			SpanID            string     `json:"spanId"`
			ParentSpanID      string     `json:"parentSpanId"`
			Name              string     `json:"name"`
			StartTimeUnixNano string     `json:"startTimeUnixNano"`
			EndTimeUnixNano   string     `json:"endTimeUnixNano"`
			Attributes        []otlpAttr `json:"attributes"`
		} `json:"spans"`
	} `json:"scopeSpans"`
}

type otlpAttr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

func attr(attrs []otlpAttr, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value.StringValue
		}
	}
	return ""
}

// idHex accepts the hex form OTLP/JSON specifies and the base64 form protojson emits.
func idHex(id string) string {
	if id == "" {
		return ""
	}
	if _, err := hex.DecodeString(id); err == nil && len(id)%2 == 0 {
		return strings.ToLower(id)
	}
	if b, err := base64.StdEncoding.DecodeString(id); err == nil {
		return hex.EncodeToString(b)
	}
	return id
}

func unixNano(s string) time.Time {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, n)
}

func hasService(spans []span, service string) bool {
	for _, s := range spans {
		if s.service == service {
			return true
		}
	}
	return false
}

func spanNamed(spans []span, name string) span {
	for _, s := range spans {
		if s.name == name {
			return s
		}
	}
	return span{}
}

func root(spans []span) span {
	for _, s := range spans {
		if s.parent == "" {
			return s
		}
	}
	return span{}
}

// consumerSpans are the audit service's receive-side spans: Spring Kafka names the
// listener's span after the topic with the process verb.
func consumerSpans(spans []span) []span {
	var out []span
	for _, s := range spans {
		if s.service == "audit-service" && strings.Contains(s.name, auditTopic) && strings.Contains(strings.ToLower(s.name), "process") {
			out = append(out, s)
		}
	}
	return out
}

func renderTrace(spans []span) string {
	sorted := append([]span(nil), spans...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start.Before(sorted[j].start) })
	var b strings.Builder
	b.WriteString("trace:\n")
	for _, s := range sorted {
		fmt.Fprintf(&b, "  %-28s %-40s parent=%-16s %s\n", s.service, s.name, s.parent, s.end.Sub(s.start).Round(time.Millisecond))
	}
	return b.String()
}

// readTopicHeaders is readTopic with the record headers, which the console consumer prints
// before the key as comma-separated key:value pairs, or NO_HEADERS.
func (h *auditHarness) readTopicHeaders() []topicRecord {
	out, errOut, err := h.exec(kafkaNS, "kafka-0", nil, "/opt/kafka/bin/kafka-console-consumer.sh",
		"--bootstrap-server", "localhost:9092", "--topic", auditTopic, "--from-beginning", "--timeout-ms", "10000",
		"--property", "print.headers=true", "--property", "print.key=true", "--property", "key.separator=\t", "--property", "headers.separator=,")
	if err != nil && strings.TrimSpace(out) == "" {
		h.t.Logf("consumer: %v: %s", err, errOut)
		return nil
	}
	var records []topicRecord
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(parts[2]), &event); err != nil {
			continue
		}
		records = append(records, topicRecord{key: parts[1], value: parts[2], event: event, headers: parseHeaders(parts[0])})
	}
	return records
}

func parseHeaders(raw string) map[string]string {
	out := map[string]string{}
	if raw == "NO_HEADERS" {
		return out
	}
	for _, kv := range strings.Split(raw, ",") {
		if k, v, ok := strings.Cut(kv, ":"); ok {
			out[k] = v
		}
	}
	return out
}

func rowTime(g *WithT, occurredAt string) time.Time {
	t, err := time.Parse("2006-01-02T15:04:05.000000Z", occurredAt)
	g.Expect(err).NotTo(HaveOccurred(), "row timestamp %q", occurredAt)
	return t
}

func podReadyAt(pod corev1.Pod) time.Time {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return c.LastTransitionTime.Time
		}
	}
	return time.Time{}
}
