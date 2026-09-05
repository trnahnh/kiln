//go:build e2e

// Phase 4 exit criterion: a deliberately broken version is rolled back and a noisy but
// healthy one is promoted, on a real mesh. Every assertion reads the VirtualService Istio
// enforces, the pods that exist and what real requests through the mesh return; the
// CanaryRollout's own status is logged, never trusted.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	fortioImage   = "fortio/fortio:1.75.3"
	targetApp     = "fortio"
	loadgenApp    = "loadgen"
	labelRole     = "platform.internal/canary-role"
	canaryWait    = 8 * time.Minute
	trafficWindow = "10s"
)

var (
	gvkCanary = schema.GroupVersionKind{Group: "platform.internal", Version: "v1", Kind: "CanaryRollout"}
	gvkVS     = schema.GroupVersionKind{Group: "networking.istio.io", Version: "v1", Kind: "VirtualService"}
)

type canaryHarness struct {
	t     *testing.T
	g     *WithT
	ctx   context.Context
	cfg   *rest.Config
	c     client.Client
	cs    *kubernetes.Clientset
	ns    string
	seen  []int
	start map[string]time.Time
}

func TestPhase4ProgressiveDelivery(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := ctrl.GetConfigOrDie()
	c, err := client.New(cfg, client.Options{})
	g.Expect(err).NotTo(HaveOccurred())
	cs, err := kubernetes.NewForConfig(cfg)
	g.Expect(err).NotTo(HaveOccurred())

	h := &canaryHarness{t: t, g: g, ctx: ctx, cfg: cfg, c: c, cs: cs, ns: fmt.Sprintf("canary-%d", time.Now().Unix()), start: map[string]time.Time{}}
	h.waitForDelivery()
	h.createMeshNamespace()
	defer h.deleteNamespace()
	h.deployTarget()

	t.Run("initial version serves from primary", h.initialVersion)
	t.Run("a broken version is rolled back", h.brokenVersion)
	t.Run("a noisy but healthy version is promoted", h.noisyVersion)
}

func (h *canaryHarness) waitForDelivery() {
	h.t.Log("waiting for prometheus, istiod and the delivery controller")
	for _, d := range []types.NamespacedName{{Namespace: "monitoring", Name: "prometheus"}, {Namespace: "istio-system", Name: "istiod"}, {Namespace: "kiln-delivery-system", Name: "kiln-delivery-controller"}} {
		h.g.Eventually(func() int32 {
			dep := &appsv1.Deployment{}
			if err := h.c.Get(h.ctx, d, dep); err != nil {
				return 0
			}
			return dep.Status.AvailableReplicas
		}, platformReady, poll).Should(BeNumerically(">=", 1), "%s is not available", d.Name)
	}
	h.g.Eventually(func() string { return h.appSyncStatus("kiln-delivery") }, platformReady, poll).Should(Equal("Synced"))
}

func (h *canaryHarness) createMeshNamespace() {
	h.g.Expect(h.c.Create(h.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   h.ns,
		Labels: map[string]string{"istio-injection": "enabled"},
	}})).To(Succeed())
}

func (h *canaryHarness) deleteNamespace() {
	_ = h.c.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.ns}})
}

func (h *canaryHarness) appSyncStatus(name string) string {
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(gvkApp)
	if err := h.c.Get(h.ctx, types.NamespacedName{Namespace: argocdNS, Name: name}, app); err != nil {
		return ""
	}
	v, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
	return v
}

func fortioDeployment(ns, name string, replicas int32, args ...string) *appsv1.Deployment {
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
				}}},
			},
		},
	}
}

func (h *canaryHarness) deployTarget() {
	h.g.Expect(h.c.Create(h.ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: targetApp, Namespace: h.ns},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": targetApp},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080)}},
		},
	})).To(Succeed())
	h.g.Expect(h.c.Create(h.ctx, fortioDeployment(h.ns, targetApp, 2, "server"))).To(Succeed())
	// A meshed client: VirtualService weights are applied by the caller's sidecar.
	h.g.Expect(h.c.Create(h.ctx, fortioDeployment(h.ns, loadgenApp, 1,
		"load", "-qps", "100", "-c", "8", "-t", "0", "-allow-initial-errors", "http://"+targetApp+":8080/"))).To(Succeed())

	rollout := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "platform.internal/v1",
		"kind":       "CanaryRollout",
		"metadata":   map[string]any{"name": targetApp, "namespace": h.ns},
		"spec": map[string]any{
			"targetDeployment": targetApp,
			"metricProvider":   "prometheus",
			"successCriteria":  map[string]any{"errorRateMax": 0.01, "latencyP99MaxMs": int64(300), "minSampleSize": int64(200)},
			"stepPercentages":  []any{int64(20), int64(50), int64(100)},
			"analysis":         map[string]any{"interval": "15s", "maxStepDuration": "10m"},
		},
	}}
	h.g.Expect(h.c.Create(h.ctx, rollout)).To(Succeed())
}

func (h *canaryHarness) initialVersion(t *testing.T) {
	h.g = NewWithT(t)
	h.g.Eventually(func() bool {
		p, c := h.weights()
		return p == 100 && c == 0 && h.available(targetApp+"-primary") == 2 && h.replicas(targetApp) == 0
	}, canaryWait, poll).Should(BeTrue(), "primary serves everything and the target is parked")
	h.g.Expect(h.pods(labelRole, "canary")).To(BeEmpty())
	h.g.Eventually(func() int32 { return h.available(loadgenApp) }, canaryWait, poll).Should(Equal(int32(1)))

	codes := h.fortioLoad()
	t.Logf("traffic through the mesh before any rollout: %v", codes)
	h.g.Expect(errorRate(codes)).To(BeNumerically("<", 0.01))
	h.logRollout()
}

func (h *canaryHarness) brokenVersion(t *testing.T) {
	h.g = NewWithT(t)
	h.setServerArgs("server", "-echo-server-default-params=status=500:30")
	h.start["broken"] = time.Now()

	h.g.Eventually(func() int { _, c := h.weights(); return c }, canaryWait, poll).Should(Equal(20), h.describe("the canary receives its first checkpoint of real traffic"))
	shifted := time.Now()
	h.g.Expect(h.pods(labelRole, "canary")).NotTo(BeEmpty(), "canary pods exist while traffic is split")
	t.Logf("canary at 20%% after %s; %v", shifted.Sub(h.start["broken"]).Round(time.Second), h.rolloutSummary())

	h.g.Eventually(func() bool {
		p, c := h.weights()
		return p == 100 && c == 0 && h.replicas(targetApp) == 0 && len(h.pods(labelRole, "canary")) == 0
	}, canaryWait, poll).Should(BeTrue(), h.describe("traffic returned to primary and the canary pods are gone"))
	t.Logf("rolled back on the mesh %s after the split began; %v", time.Since(shifted).Round(time.Second), h.rolloutSummary())

	for _, pod := range h.pods(labelRole, "primary") {
		h.g.Expect(pod.Spec.Containers[0].Args).To(Equal([]string{"server"}), "primary pod %s must still run the original version", pod.Name)
		h.g.Expect(podReady(pod)).To(BeTrue(), "primary pod %s must be ready", pod.Name)
	}
	codes := h.fortioLoad()
	t.Logf("traffic through the mesh after rollback: %v", codes)
	h.g.Expect(errorRate(codes)).To(BeNumerically("<", 0.01), "real requests after rollback must be healthy again")
	h.g.Expect(h.rollout().reason).To(Equal("RegressionDetected"))
}

func (h *canaryHarness) noisyVersion(t *testing.T) {
	h.g = NewWithT(t)
	h.setServerArgs("server", "-echo-server-default-params=status=500:0.5")
	h.start["noisy"] = time.Now()
	h.seen = nil

	h.g.Eventually(func() bool {
		_, c := h.weights()
		if c > 0 && (len(h.seen) == 0 || h.seen[len(h.seen)-1] != c) {
			h.seen = append(h.seen, c)
		}
		return c == 100
	}, canaryWait, poll).Should(BeTrue(), h.describe("the canary earns all traffic"))
	t.Logf("canary weights observed on the mesh: %v after %s", h.seen, time.Since(h.start["noisy"]).Round(time.Second))
	h.g.Expect(h.seen).To(ContainElements(20, 50, 100), "every checkpoint was visited")
	h.g.Expect(len(h.seen)).To(BeNumerically(">", 3), "confidence-sized sub-steps moved traffic between checkpoints")

	h.g.Eventually(func() bool {
		p, c := h.weights()
		primary := h.pods(labelRole, "primary")
		if p != 100 || c != 0 || h.replicas(targetApp) != 0 || len(h.pods(labelRole, "canary")) != 0 || len(primary) != 2 {
			return false
		}
		for _, pod := range primary {
			if !podReady(pod) || !strings.Contains(strings.Join(pod.Spec.Containers[0].Args, " "), "status=500:0.5") {
				return false
			}
		}
		return true
	}, canaryWait, poll).Should(BeTrue(), h.describe("primary runs the new version and serves everything"))
	t.Logf("promoted %s after the rollout began; %v", time.Since(h.start["noisy"]).Round(time.Second), h.rolloutSummary())

	codes := h.fortioLoad()
	t.Logf("traffic through the mesh after promotion: %v", codes)
	h.g.Expect(errorRate(codes)).To(BeNumerically("<", 0.02), "the promoted version serves with its expected noise")
	h.g.Expect(h.rollout().phase).To(Equal("Succeeded"))
}

func (h *canaryHarness) setServerArgs(args ...string) {
	d := &appsv1.Deployment{}
	h.g.Expect(h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: targetApp}, d)).To(Succeed())
	before := d.DeepCopy()
	d.Spec.Template.Spec.Containers[0].Args = args
	h.g.Expect(h.c.Patch(h.ctx, d, client.MergeFrom(before))).To(Succeed())
}

func (h *canaryHarness) weights() (int, int) {
	vs := &unstructured.Unstructured{}
	vs.SetGroupVersionKind(gvkVS)
	if err := h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: targetApp}, vs); err != nil {
		return -1, -1
	}
	http, _, _ := unstructured.NestedSlice(vs.Object, "spec", "http")
	if len(http) == 0 {
		return -1, -1
	}
	routes, _, _ := unstructured.NestedSlice(http[0].(map[string]any), "route")
	out := map[string]int{}
	for _, r := range routes {
		m := r.(map[string]any)
		host, _, _ := unstructured.NestedString(m, "destination", "host")
		w, _, _ := unstructured.NestedInt64(m, "weight")
		out[host] = int(w)
	}
	return out[targetApp+"-primary"], out[targetApp+"-canary"]
}

func (h *canaryHarness) available(name string) int32 {
	d := &appsv1.Deployment{}
	if err := h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: name}, d); err != nil {
		return -1
	}
	return d.Status.AvailableReplicas
}

func (h *canaryHarness) replicas(name string) int32 {
	d := &appsv1.Deployment{}
	if err := h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: name}, d); err != nil {
		return -1
	}
	return *d.Spec.Replicas
}

// pods lists non-terminated pods carrying the label; terminating pods are excluded because
// a rolled-back canary is proven gone by its pods leaving, not by a scale value.
func (h *canaryHarness) pods(label, value string) []corev1.Pod {
	list := &corev1.PodList{}
	h.g.Expect(h.c.List(h.ctx, list, client.InNamespace(h.ns), client.MatchingLabels{label: value})).To(Succeed())
	var out []corev1.Pod
	for _, p := range list.Items {
		if p.DeletionTimestamp == nil && p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			out = append(out, p)
		}
	}
	return out
}

func podReady(p corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// fortioLoad runs a bounded load from inside the meshed client pod and returns the
// response-code histogram, so what the mesh actually served is measured from the caller's
// side rather than inferred from any controller state.
func (h *canaryHarness) fortioLoad() map[string]int64 {
	pods := h.pods("app", loadgenApp)
	h.g.Expect(pods).NotTo(BeEmpty())
	req := h.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(h.ns).Name(pods[0].Name).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "fortio",
			Command:   []string{"fortio", "load", "-quiet", "-qps", "50", "-c", "4", "-t", trafficWindow, "-json", "-", "http://" + targetApp + ":8080/"},
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(h.cfg, "POST", req.URL())
	h.g.Expect(err).NotTo(HaveOccurred())
	var stdout, stderr bytes.Buffer
	h.g.Expect(exec.StreamWithContext(h.ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr})).To(Succeed(), stderr.String())
	raw := stdout.String()
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	h.g.Expect(start).To(BeNumerically(">=", 0), "no JSON in fortio output: %s %s", raw, stderr.String())
	var result struct {
		RetCodes map[string]int64 `json:"RetCodes"`
	}
	h.g.Expect(json.Unmarshal([]byte(raw[start:end+1]), &result)).To(Succeed())
	h.g.Expect(result.RetCodes).NotTo(BeEmpty())
	return result.RetCodes
}

func errorRate(codes map[string]int64) float64 {
	var total, failed int64
	for code, n := range codes {
		total += n
		if !strings.HasPrefix(code, "2") {
			failed += n
		}
	}
	if total == 0 {
		return 1
	}
	return float64(failed) / float64(total)
}

type rolloutView struct {
	phase, reason string
	weight, step  int64
	samples       int64
	ready         string
}

func (h *canaryHarness) rollout() rolloutView {
	cr := &unstructured.Unstructured{}
	cr.SetGroupVersionKind(gvkCanary)
	if err := h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: targetApp}, cr); err != nil {
		return rolloutView{}
	}
	v := rolloutView{}
	v.phase, _, _ = unstructured.NestedString(cr.Object, "status", "phase")
	v.reason, _, _ = unstructured.NestedString(cr.Object, "status", "reason")
	v.weight, _, _ = unstructured.NestedInt64(cr.Object, "status", "canaryWeight")
	v.step, _, _ = unstructured.NestedInt64(cr.Object, "status", "currentStep")
	v.samples, _, _ = unstructured.NestedInt64(cr.Object, "status", "analysis", "totalSamples")
	conds, _, _ := unstructured.NestedSlice(cr.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == "Ready" {
			v.ready = fmt.Sprintf("%v/%v: %v", m["status"], m["reason"], m["message"])
		}
	}
	return v
}

// describe renders a timeout with the controller's own account of the rollout, so a CI
// failure says why the mesh never moved instead of only that it did not.
func (h *canaryHarness) describe(what string) func() string {
	return func() string {
		p, c := h.weights()
		return fmt.Sprintf("%s; VirtualService primary=%d canary=%d; target replicas=%d canary pods=%d; %s; Ready %s",
			what, p, c, h.replicas(targetApp), len(h.pods(labelRole, "canary")), h.rolloutSummary(), h.rollout().ready)
	}
}

func (h *canaryHarness) rolloutSummary() string {
	v := h.rollout()
	return fmt.Sprintf("CanaryRollout reports phase=%s reason=%s weight=%d step=%d samples=%d (informational)", v.phase, v.reason, v.weight, v.step, v.samples)
}

func (h *canaryHarness) logRollout() { h.t.Log(h.rolloutSummary()) }
