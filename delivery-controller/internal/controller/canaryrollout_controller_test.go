package controller

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1 "github.com/trnahnh/kiln/delivery-controller/api/v1"
	"github.com/trnahnh/kiln/delivery-controller/internal/mesh"
	"github.com/trnahnh/kiln/slo"
)

const (
	ns      = "shop"
	appName = "checkout"
	timeout = 30 * time.Second
	tick    = 200 * time.Millisecond
)

var _ = Describe("CanaryRollout", Ordered, func() {
	var router *mesh.Istio

	BeforeAll(func() {
		router = &mesh.Istio{Client: k8sClient}
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: ns},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": appName},
				Ports:    []corev1.ServicePort{{Name: "http", Port: 8080}},
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, deployment("v1"))).To(Succeed())
		Expect(k8sClient.Create(ctx, &platformv1.CanaryRollout{
			ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: ns},
			Spec: platformv1.CanaryRolloutSpec{
				TargetDeployment: appName,
				SuccessCriteria:  platformv1.SuccessCriteria{ErrorRateMax: 0.01, LatencyP99MaxMs: 300, MinSampleSize: 500},
				StepPercentages:  []int32{5, 20, 50, 100},
				Analysis: &platformv1.AnalysisSpec{
					Interval:        metav1.Duration{Duration: time.Second},
					MaxStepDuration: metav1.Duration{Duration: 20 * time.Second},
					DrainGrace:      &metav1.Duration{Duration: 3 * time.Second},
				},
			},
		})).To(Succeed())
	})

	weights := func() (int, int) {
		p, c, err := router.Weights(ctx, mesh.Route{Namespace: ns, Host: appName, PrimaryService: appName + "-primary", CanaryService: appName + "-canary"})
		if err != nil {
			return -1, -1
		}
		return p, c
	}

	It("clones a primary, creates role services and parks the target once primary serves", func() {
		Eventually(func() bool {
			return hasRole(get[*appsv1.Deployment](appName).Spec.Template.Labels, appName, platformv1.RoleCanary)
		}, timeout, tick).Should(BeTrue(), "target pods are labelled canary")

		var primary *appsv1.Deployment
		Eventually(func() error {
			var err error
			primary, err = tryGet[*appsv1.Deployment](appName + "-primary")
			return err
		}, timeout, tick).Should(Succeed())
		Expect(primary.Spec.Selector.MatchLabels).To(HaveKeyWithValue(platformv1.LabelRole, platformv1.RolePrimary))
		Expect(primary.Spec.Template.Labels).To(HaveKeyWithValue(platformv1.LabelRole, platformv1.RolePrimary))
		Expect(primary.Spec.Template.Labels).To(HaveKeyWithValue("app", appName))
		Expect(*primary.Spec.Replicas).To(Equal(int32(2)))
		Expect(primary.Spec.Template.Spec.Containers[0].Image).To(Equal("fortio/fortio:v1"))
		expectOwnedByRollout(primary)

		for name, role := range map[string]string{appName + "-primary": platformv1.RolePrimary, appName + "-canary": platformv1.RoleCanary} {
			svc := get[*corev1.Service](name)
			Expect(svc.Spec.Selector).To(Equal(map[string]string{"app": appName, platformv1.LabelRole: role, platformv1.LabelRolloutRef: appName}))
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8080)))
			expectOwnedByRollout(svc)
		}

		Expect(get[*platformv1.CanaryRollout](appName).Status.Phase).To(Equal(platformv1.PhaseInitializing))
		Expect(*get[*appsv1.Deployment](appName).Spec.Replicas).To(Equal(int32(2)), "target keeps serving until primary is up")

		Eventually(func() bool {
			markRolledOut(appName + "-primary")
			p, c := weights()
			return p == 100 && c == 0 && *get[*appsv1.Deployment](appName).Spec.Replicas == 0
		}, timeout, tick).Should(BeTrue(), "all traffic on primary and the target parked at zero")

		cr := get[*platformv1.CanaryRollout](appName)
		Expect(cr.Status.Phase).To(Equal(platformv1.PhaseSucceeded))
		Expect(cr.Status.PromotedTemplateHash).NotTo(BeEmpty())
		Expect(*cr.Status.TargetReplicas).To(Equal(int32(2)))
		Expect(meta.IsStatusConditionTrue(cr.Status.Conditions, platformv1.ConditionReady)).To(BeTrue())
	})

	It("rolls a regressed version back on the mesh and parks the canary", func() {
		source.script(slo.Counters{Requests: 100}, false)
		setImage("v2")

		Eventually(func() bool {
			markRolledOut(appName)
			_, c := weights()
			return c == 5
		}, timeout, tick).Should(BeTrue(), "the first checkpoint is entered outright")
		Expect(*get[*appsv1.Deployment](appName).Spec.Replicas).To(Equal(int32(2)), "the canary runs at the target's replica count")
		Expect(get[*platformv1.CanaryRollout](appName).Status.Phase).To(Equal(platformv1.PhaseAnalyzing))
		Consistently(func() int { _, c := weights(); return c }, 2*time.Second, tick).Should(Equal(5), "nothing moves under the sample-size gate")

		source.script(slo.Counters{Requests: 600, Errors: 200}, false)

		Eventually(func() bool {
			markRolledOut(appName)
			p, c := weights()
			return p == 100 && c == 0
		}, timeout, tick).Should(BeTrue(), "traffic returned to primary")
		flipped := time.Now()
		cr := get[*platformv1.CanaryRollout](appName)
		Expect(cr.Status.Phase).To(Equal(platformv1.PhaseDraining))
		Expect(cr.Status.Reason).To(Equal(platformv1.ReasonRegressionDetected))
		Expect(*get[*appsv1.Deployment](appName).Spec.Replicas).To(Equal(int32(2)), "the canary keeps running while clients catch up")

		Eventually(func() int32 {
			markRolledOut(appName)
			return *get[*appsv1.Deployment](appName).Spec.Replicas
		}, timeout, tick).Should(Equal(int32(0)), "the canary is parked after the grace")
		Expect(time.Since(flipped)).To(BeNumerically(">=", 2*time.Second), "parking waited for the drain grace")

		Eventually(func() platformv1.Phase { return get[*platformv1.CanaryRollout](appName).Status.Phase }, timeout, tick).Should(Equal(platformv1.PhaseRolledBack))
		cr = get[*platformv1.CanaryRollout](appName)
		Expect(cr.Status.Reason).To(Equal(platformv1.ReasonRegressionDetected))
		Expect(cr.Status.LastAnalysisResult).To(Equal(platformv1.AnalysisFail))
		Expect(cr.Status.TrafficFlippedAt).To(BeNil())
		Expect(cr.Status.Analysis.TotalSamples).To(BeNumerically(">=", 1800), "three capped windows were needed")
		Expect(get[*appsv1.Deployment](appName + "-primary").Spec.Template.Spec.Containers[0].Image).To(Equal("fortio/fortio:v1"), "primary never saw the bad version")
		Expect(eventReasons()).To(ContainElement("RolledBack"))
	})

	It("does not restart a rollout for the version it just rolled back", func() {
		Consistently(func() platformv1.Phase {
			return get[*platformv1.CanaryRollout](appName).Status.Phase
		}, 3*time.Second, tick).Should(Equal(platformv1.PhaseRolledBack))
		Expect(*get[*appsv1.Deployment](appName).Spec.Replicas).To(Equal(int32(0)))
	})

	It("promotes a healthy version through every checkpoint onto primary", func() {
		source.script(slo.Counters{Requests: 600}, false)
		setImage("v3")

		Eventually(func() bool {
			markRolledOut(appName)
			return get[*appsv1.Deployment](appName + "-primary").Spec.Template.Spec.Containers[0].Image == "fortio/fortio:v3"
		}, 2*timeout, tick).Should(BeTrue(), "primary receives the accepted template")
		Expect(get[*platformv1.CanaryRollout](appName).Status.Phase).To(Equal(platformv1.PhasePromoting))
		_, c := weights()
		Expect(c).To(Equal(100), "the canary carried all traffic while primary catches up")

		Eventually(func() bool {
			markRolledOut(appName + "-primary")
			markRolledOut(appName)
			p, c := weights()
			return p == 100 && c == 0
		}, timeout, tick).Should(BeTrue(), "traffic handed back to the updated primary")
		flipped := time.Now()
		Expect(get[*platformv1.CanaryRollout](appName).Status.Phase).To(Equal(platformv1.PhaseDraining))
		Expect(*get[*appsv1.Deployment](appName).Spec.Replicas).To(Equal(int32(2)), "the canary keeps running while clients catch up")

		Eventually(func() bool {
			markRolledOut(appName)
			return *get[*appsv1.Deployment](appName).Spec.Replicas == 0 && get[*platformv1.CanaryRollout](appName).Status.Phase == platformv1.PhaseSucceeded
		}, timeout, tick).Should(BeTrue(), "the canary is parked after the grace")
		Expect(time.Since(flipped)).To(BeNumerically(">=", 2*time.Second), "parking waited for the drain grace")

		cr := get[*platformv1.CanaryRollout](appName)
		Expect(cr.Status.LastAnalysisResult).To(Equal(platformv1.AnalysisPass))
		Expect(cr.Status.PromotedTemplateHash).To(Equal(templateHash(get[*appsv1.Deployment](appName).Spec.Template)))
		Expect(meta.IsStatusConditionTrue(cr.Status.Conditions, platformv1.ConditionReady)).To(BeTrue())
		Expect(*get[*appsv1.Deployment](appName + "-primary").Spec.Replicas).To(Equal(int32(2)))
		for _, pct := range []int{5, 20, 50, 100} {
			Expect(eventMessages()).To(ContainElement(ContainSubstring(fmt.Sprintf("canary receives %d%% of traffic", pct))))
		}
	})

	It("restarts from zero when the template changes mid-rollout", func() {
		source.script(slo.Counters{Requests: 100}, false)
		setImage("v4")
		Eventually(func() bool {
			markRolledOut(appName)
			_, c := weights()
			return c == 5
		}, timeout, tick).Should(BeTrue())
		hashBefore := get[*platformv1.CanaryRollout](appName).Status.ObservedTemplateHash

		setImage("v5")
		Eventually(func() bool {
			cr := get[*platformv1.CanaryRollout](appName)
			p, c := weights()
			return cr.Status.ObservedTemplateHash != hashBefore && p == 100 && c == 0 && cr.Status.Analysis.TotalSamples == 0
		}, timeout, tick).Should(BeTrue(), "the mesh went back to primary and the evidence was discarded")

		source.script(slo.Counters{Requests: 600, Errors: 200}, false)
		Eventually(func() platformv1.Phase {
			markRolledOut(appName)
			return get[*platformv1.CanaryRollout](appName).Status.Phase
		}, timeout, tick).Should(Equal(platformv1.PhaseRolledBack))
	})

	It("rolls back when metrics stay unavailable past maxStepDuration", func() {
		cr := get[*platformv1.CanaryRollout](appName)
		cr.Spec.Analysis.MaxStepDuration = metav1.Duration{Duration: 3 * time.Second}
		Expect(k8sClient.Update(ctx, cr)).To(Succeed())

		source.script(slo.Counters{}, true)
		setImage("v6")
		Eventually(func() platformv1.Phase {
			markRolledOut(appName)
			return get[*platformv1.CanaryRollout](appName).Status.Phase
		}, timeout, tick).Should(Equal(platformv1.PhaseProgressing))

		Eventually(func() bool {
			markRolledOut(appName)
			cr := get[*platformv1.CanaryRollout](appName)
			p, c := weights()
			return cr.Status.Phase == platformv1.PhaseRolledBack && p == 100 && c == 0 && *get[*appsv1.Deployment](appName).Spec.Replicas == 0
		}, timeout, tick).Should(BeTrue())
		Expect(get[*platformv1.CanaryRollout](appName).Status.Reason).To(Equal(platformv1.ReasonMetricsUnavailable))
	})

	It("reports an invalid step schedule instead of acting on it", func() {
		Expect(k8sClient.Create(ctx, &platformv1.CanaryRollout{
			ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: ns},
			Spec: platformv1.CanaryRolloutSpec{
				TargetDeployment: "missing",
				SuccessCriteria:  platformv1.SuccessCriteria{ErrorRateMax: 0.01, LatencyP99MaxMs: 300, MinSampleSize: 500},
				StepPercentages:  []int32{5, 50},
			},
		})).To(Succeed())
		Eventually(func() string {
			c := meta.FindStatusCondition(get[*platformv1.CanaryRollout]("bad").Status.Conditions, platformv1.ConditionReady)
			if c == nil {
				return ""
			}
			return c.Reason
		}, timeout, tick).Should(Equal(platformv1.ReasonInvalidSpec))
	})
})

func deployment(version string) *appsv1.Deployment {
	labels := map[string]string{"app": appName}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: ns, Labels: labels},
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

func setImage(version string) {
	d := get[*appsv1.Deployment](appName)
	before := d.DeepCopy()
	d.Spec.Template.Spec.Containers[0].Image = "fortio/fortio:" + version
	Expect(k8sClient.Patch(ctx, d, client.MergeFrom(before))).To(Succeed())
}

// envtest runs no Deployment controller, so the pods a Deployment would create are
// represented by writing the status the real controller would report.
func markRolledOut(name string) {
	d, err := tryGet[*appsv1.Deployment](name)
	if err != nil {
		return
	}
	n := *d.Spec.Replicas
	if d.Status.ObservedGeneration == d.Generation && d.Status.Replicas == n && d.Status.UpdatedReplicas == n && d.Status.AvailableReplicas == n {
		return
	}
	d.Status.ObservedGeneration = d.Generation
	d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas = n, n, n, n
	_ = k8sClient.Status().Update(ctx, d)
}

func tryGet[T client.Object](name string) (T, error) {
	var obj T
	switch any(obj).(type) {
	case *appsv1.Deployment:
		obj = any(&appsv1.Deployment{}).(T)
	case *corev1.Service:
		obj = any(&corev1.Service{}).(T)
	case *platformv1.CanaryRollout:
		obj = any(&platformv1.CanaryRollout{}).(T)
	}
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, obj)
	return obj, err
}

func get[T client.Object](name string) T {
	obj, err := tryGet[T](name)
	Expect(err).NotTo(HaveOccurred())
	return obj
}

func expectOwnedByRollout(obj client.Object) {
	refs := obj.GetOwnerReferences()
	Expect(refs).To(HaveLen(1))
	Expect(refs[0].Kind).To(Equal("CanaryRollout"))
	Expect(refs[0].Name).To(Equal(appName))
	Expect(*refs[0].Controller).To(BeTrue())
}

func events() []corev1.Event {
	list := &corev1.EventList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(ns))).To(Succeed())
	return list.Items
}

func eventReasons() []string {
	var out []string
	for _, e := range events() {
		out = append(out, e.Reason)
	}
	return out
}

func eventMessages() []string {
	var out []string
	for _, e := range events() {
		out = append(out, e.Message)
	}
	return out
}
