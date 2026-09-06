package controller

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1 "github.com/trnahnh/kiln/chaos/api/v1"
	"github.com/trnahnh/kiln/slo"
)

const (
	timeout = 20 * time.Second
	tick    = 100 * time.Millisecond
)

var nsCounter int

func freshNamespace() string {
	nsCounter++
	name := fmt.Sprintf("chaos-%d", nsCounter)
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
	return name
}

var _ = Describe("ChaosExperiment", func() {
	It("rejects a target in another namespace", func() {
		ns := freshNamespace()
		cr := experiment(ns, "cross", "latency-injection", 50, 0.05)
		cr.Spec.Target.Namespace = "somewhere-else"
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())
		Eventually(func() string { return getCR(ns, "cross").Status.Reason }, timeout, tick).Should(Equal(platformv1.ReasonInvalidSpec))
		Consistently(func() []string { return injector.appliedPods(ns) }, time.Second, tick).Should(BeEmpty())
	})

	It("rejects a cap that floors to zero pods", func() {
		ns := freshNamespace()
		makePod(ns, "target-a", "target", false)
		makePod(ns, "target-b", "target", false)
		Expect(k8sClient.Create(ctx, experiment(ns, "toosmall", "latency-injection", 30, 0.05))).To(Succeed())
		Eventually(func() string { return getCR(ns, "toosmall").Status.Reason }, timeout, tick).Should(Equal(platformv1.ReasonInvalidSpec))
		Consistently(func() []string { return injector.appliedPods(ns) }, time.Second, tick).Should(BeEmpty())
	})

	It("runs a healthy latency experiment, scores it, and the agent reverts every fault", func() {
		ns := freshNamespace()
		source.script(ns, slo.Counters{Requests: 100}, false)
		for _, n := range []string{"target-a", "target-b", "target-c", "target-d"} {
			makePod(ns, n, "target", false)
		}
		Expect(k8sClient.Create(ctx, experiment(ns, "healthy", "latency-injection", 50, 0.05))).To(Succeed())

		Eventually(func() []string { return injector.appliedPods(ns) }, timeout, tick).Should(HaveLen(2), "50% of four pods is two")

		Eventually(func() platformv1.Phase { return getCR(ns, "healthy").Status.Phase }, timeout, tick).Should(Equal(platformv1.PhaseCompleted))
		cr := getCR(ns, "healthy")
		Expect(cr.Status.ResilienceScore).NotTo(BeNil())
		Expect(*cr.Status.ResilienceScore).To(BeNumerically(">", 90), "an untouched service scores high")

		for _, pod := range []string{"target-a", "target-b"} {
			Eventually(func() bool { return injector.wasReverted(ns, pod) }, timeout, tick).Should(BeTrue(), "fault on %s not reverted", pod)
		}
		Eventually(func() []string { return injector.appliedPods(ns) }, timeout, tick).Should(BeEmpty())
	})

	It("aborts on a forced SLO breach and the agent reverts within the lease", func() {
		ns := freshNamespace()
		source.script(ns, slo.Counters{Requests: 100}, false) // healthy first, so the fault is injected and held
		for _, n := range []string{"target-a", "target-b"} {
			makePod(ns, n, "target", false)
		}
		Expect(k8sClient.Create(ctx, experiment(ns, "breach", "latency-injection", 100, 0.05))).To(Succeed())

		Eventually(func() []string { return injector.appliedPods(ns) }, timeout, tick).Should(HaveLen(2))
		// Now the injected fault starts breaching the SLO.
		source.script(ns, slo.Counters{Requests: 100, Errors: 50}, false)

		Eventually(func() string { return getCR(ns, "breach").Status.AbortReason }, timeout, tick).Should(Equal(string(platformv1.ReasonSLOBreach)))
		cr := getCR(ns, "breach")
		Expect(cr.Status.Phase).To(Equal(platformv1.PhaseAborted))
		Expect(*cr.Status.ResilienceScore).To(Equal(0.0))

		for _, pod := range []string{"target-a", "target-b"} {
			Eventually(func() bool { return injector.wasReverted(ns, pod) }, timeout, tick).Should(BeTrue(), "%s still faulted after abort", pod)
		}
		Eventually(func() []string { return injector.appliedPods(ns) }, timeout, tick).Should(BeEmpty())
	})

	It("aborts when metrics are unavailable rather than injecting blind", func() {
		ns := freshNamespace()
		source.script(ns, slo.Counters{Requests: 100}, true) // Prometheus down from the start
		makePod(ns, "target-a", "target", false)
		makePod(ns, "target-b", "target", false)
		Expect(k8sClient.Create(ctx, experiment(ns, "blind", "latency-injection", 100, 0.05))).To(Succeed())

		Consistently(func() []string { return injector.appliedPods(ns) }, 2*time.Second, tick).Should(BeEmpty())
		Expect(getCR(ns, "blind").Status.Reason).To(Equal(platformv1.ReasonMetricsUnavailable))
	})

	It("never faults more pods than the cap allows", func() {
		ns := freshNamespace()
		source.script(ns, slo.Counters{Requests: 100}, false)
		for _, n := range []string{"target-a", "target-b", "target-c", "target-d"} {
			makePod(ns, n, "target", false)
		}
		Expect(k8sClient.Create(ctx, experiment(ns, "capped", "latency-injection", 30, 0.05))).To(Succeed())

		Eventually(func() []string { return injector.appliedPods(ns) }, timeout, tick).Should(HaveLen(1), "30% of four pods is one")
		Consistently(func() []string { return injector.appliedPods(ns) }, 800*time.Millisecond, tick).Should(HaveLen(1))
		Expect(injector.appliedPods(ns)).To(ConsistOf("target-a"), "the lowest-named pod, deterministically")
		Expect(getCR(ns, "capped").Status.Targets).To(HaveLen(1))
	})

	It("rejects resource-exhaustion against a container with no CPU limit", func() {
		ns := freshNamespace()
		source.script(ns, slo.Counters{Requests: 100}, false)
		makePod(ns, "target-a", "target", false)
		makePod(ns, "target-b", "target", false)
		Expect(k8sClient.Create(ctx, experiment(ns, "nolimit", "resource-exhaustion", 50, 0.05))).To(Succeed())
		Eventually(func() string { return getCR(ns, "nolimit").Status.Reason }, timeout, tick).Should(Equal(platformv1.ReasonInvalidSpec))
		Consistently(func() []string { return injector.appliedPods(ns) }, time.Second, tick).Should(BeEmpty())
	})

	It("resolves the app container for a resource-exhaustion burner", func() {
		ns := freshNamespace()
		source.script(ns, slo.Counters{Requests: 100}, false)
		makePod(ns, "target-a", "target", true)
		makePod(ns, "target-b", "target", true)
		Expect(k8sClient.Create(ctx, experiment(ns, "burn", "resource-exhaustion", 50, 0.05))).To(Succeed())
		Eventually(func() []string { return injector.appliedPods(ns) }, timeout, tick).Should(HaveLen(1))
		Expect(getCR(ns, "burn").Status.Targets[0].Container).To(Equal("app"))
	})

	It("kills pods for a pod-kill experiment and the agent injects nothing", func() {
		ns := freshNamespace()
		source.script(ns, slo.Counters{Requests: 100}, false)
		for _, n := range []string{"target-a", "target-b", "target-c", "target-d"} {
			makePod(ns, n, "target", false)
		}
		Expect(k8sClient.Create(ctx, experiment(ns, "kill", "pod-kill", 50, 0.05))).To(Succeed())

		Eventually(func() int32 { return getCR(ns, "kill").Status.Kills }, timeout, tick).Should(BeNumerically(">=", 1))
		Expect(injector.appliedPods(ns)).To(BeEmpty(), "pod-kill is the controller's job, not the agent's")
		Eventually(func() int {
			pods := &corev1.PodList{}
			_ = k8sClient.List(ctx, pods, client.InNamespace(ns))
			return len(pods.Items)
		}, timeout, tick).Should(BeNumerically("<", 4), "at least one pod was deleted")
	})

	It("holds the object with a finalizer until the lease has lapsed, then lets it go", func() {
		ns := freshNamespace()
		source.script(ns, slo.Counters{Requests: 100}, false)
		makePod(ns, "target-a", "target", false)
		makePod(ns, "target-b", "target", false)
		Expect(k8sClient.Create(ctx, experiment(ns, "deleteme", "latency-injection", 100, 0.05))).To(Succeed())
		Eventually(func() []string { return injector.appliedPods(ns) }, timeout, tick).Should(HaveLen(2))

		Expect(k8sClient.Delete(ctx, getCR(ns, "deleteme"))).To(Succeed())
		Expect(getCR(ns, "deleteme").Finalizers).To(ContainElement(platformv1.Finalizer))
		for _, pod := range []string{"target-a", "target-b"} {
			Eventually(func() bool { return injector.wasReverted(ns, pod) }, timeout, tick).Should(BeTrue())
		}
		Eventually(func() bool {
			err := k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: "deleteme"}, &platformv1.ChaosExperiment{})
			return err != nil
		}, timeout, tick).Should(BeTrue(), "the object outlived the finalizer")
	})
})
