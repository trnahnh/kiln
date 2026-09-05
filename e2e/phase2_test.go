//go:build e2e

// Phase 2 exit criterion against a live cluster bootstrapped from gitops/argocd: a
// policy-violating request never yields a TenantDatabase (checked in the cluster, not a
// log), a valid one flows Git -> ArgoCD -> Crossplane -> TenantDatabase, and drift is
// handled per resource class. The Git leg pushes to a throwaway branch of the real repo.
package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	repoURL  = "https://github.com/trnahnh/kiln.git"
	argocdNS = "argocd"

	poll          = 2 * time.Second
	platformReady = 10 * time.Minute
	syncTimeout   = 5 * time.Minute
)

var (
	gvkClaim   = schema.GroupVersionKind{Group: "platform.internal", Version: "v1alpha1", Kind: "DatabaseClaim"}
	gvkTenant  = schema.GroupVersionKind{Group: "platform.internal", Version: "v1", Kind: "TenantDatabase"}
	gvkApp     = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}
	gvkPolicy  = schema.GroupVersionKind{Group: "policies.kyverno.io", Version: "v1", Kind: "ValidatingPolicy"}
	gvkWebhook = schema.GroupVersionKind{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingWebhookConfiguration"}
	gvkXRD     = schema.GroupVersionKind{Group: "apiextensions.crossplane.io", Version: "v2", Kind: "CompositeResourceDefinition"}
)

type harness struct {
	t     *testing.T
	g     *WithT
	ctx   context.Context
	c     client.Client
	runID string
	ns    string
}

func TestPhase2PolicyAndGitOps(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{})
	g.Expect(err).NotTo(HaveOccurred())

	h := &harness{t: t, g: g, ctx: ctx, c: c, runID: fmt.Sprintf("%d", time.Now().Unix())}
	h.ns = "e2e-" + h.runID

	h.waitForPlatform()
	h.createNamespace()
	defer h.deleteNamespace()

	t.Run("policy webhook fails closed", h.webhookFailsClosed)
	t.Run("violating requests never become a TenantDatabase", h.directViolationsRejected)
	t.Run("claims flow through Git", h.gitFlow)
}

// waitForPlatform blocks until the pieces Git installs are actually serving: the XRD is
// established, the platform Application is Synced, and Kyverno has registered a webhook
// rule for DatabaseClaims.
func (h *harness) waitForPlatform() {
	h.t.Log("waiting for Crossplane XRD, kiln-platform Application and Kyverno webhook rules")
	h.g.Eventually(func() bool {
		xrd := h.get(gvkXRD, "", "databaseclaims.platform.internal")
		return xrd != nil && conditionTrue(xrd, "Established")
	}, platformReady, poll).Should(BeTrue(), "XRD databaseclaims.platform.internal is not Established")
	h.g.Eventually(func() string { return h.appSyncStatus("kiln-platform") }, platformReady, poll).Should(Equal("Synced"))
	h.g.Eventually(func() bool { return h.kyvernoWebhookFor("databaseclaims") != nil }, platformReady, poll).Should(BeTrue())
	h.g.Eventually(func() bool { return h.kyvernoWebhookFor("tenantdatabases") != nil }, platformReady, poll).Should(BeTrue())
}

func (h *harness) webhookFailsClosed(t *testing.T) {
	for _, resource := range []string{"databaseclaims", "tenantdatabases"} {
		wh := h.kyvernoWebhookFor(resource)
		h.g.Expect(wh).NotTo(BeNil())
		policy, _, _ := unstructured.NestedString(wh, "failurePolicy")
		h.g.Expect(policy).To(Equal("Fail"), "Kyverno webhook covering %s must fail closed", resource)
	}
}

func (h *harness) directViolationsRejected(t *testing.T) {
	cases := []struct {
		name string
		obj  *unstructured.Unstructured
		rule string
	}{
		{"missing tags", h.claim("no-tags", 20, nil, ""), "mandatory-tags"},
		{"over ceiling", h.claim("too-big", 500, goodTags(), ""), "storage-ceiling"},
		{"custom tier", h.claim("custom-tier", 20, goodTags(), "custom"), "standard-tier-only"},
		{"direct TenantDatabase over ceiling", h.tenantDatabase("direct-big", 500), "storage-ceiling"},
	}
	for _, c := range cases {
		err := h.c.Create(h.ctx, c.obj)
		h.g.Expect(err).To(HaveOccurred(), "%s must be rejected at admission", c.name)
		h.g.Expect(apierrors.IsForbidden(err) || apierrors.IsInvalid(err)).To(BeTrue(), "%s: %v", c.name, err)
		h.g.Expect(err.Error()).To(ContainSubstring("POLICY_DENIED rule=" + c.rule))
	}

	h.g.Expect(h.list(gvkClaim, h.ns)).To(BeEmpty(), "no DatabaseClaim may exist after rejected requests")
	h.g.Consistently(func() []unstructured.Unstructured { return h.list(gvkTenant, h.ns) }, 10*time.Second, poll).
		Should(BeEmpty(), "no TenantDatabase may exist after rejected requests")
}

func (h *harness) gitFlow(t *testing.T) {
	branch := "e2e/" + h.runID
	validClaim := h.claim("checkout-db", 20, goodTags(), "")
	invalidClaim := h.claim("big-db", 500, goodTags(), "")

	t.Logf("pushing branch %s", branch)
	h.pushBranch(branch, map[string]*unstructured.Unstructured{
		"tenants/valid/checkout-db.yaml": validClaim,
		"tenants/invalid/big-db.yaml":    invalidClaim,
	})
	defer h.deleteBranch(branch)

	validApp := h.createApp("e2e-valid-"+h.runID, branch, "tenants/valid")
	invalidApp := h.createApp("e2e-invalid-"+h.runID, branch, "tenants/invalid")
	defer h.deleteApp(validApp)
	defer h.deleteApp(invalidApp)

	t.Log("valid claim: Git -> ArgoCD -> Crossplane -> TenantDatabase")
	h.g.Eventually(func() string { return h.appSyncStatus(validApp) }, syncTimeout, poll).Should(Equal("Synced"))
	var tenant *unstructured.Unstructured
	h.g.Eventually(func() *unstructured.Unstructured {
		tenant = h.get(gvkTenant, h.ns, "checkout-db")
		return tenant
	}, syncTimeout, poll).ShouldNot(BeNil(), "Crossplane must compose a TenantDatabase from the synced claim")
	h.g.Expect(field(tenant, "spec", "engine")).To(Equal("postgres"))
	h.g.Expect(field(tenant, "spec", "version")).To(Equal("16"))
	h.g.Expect(field(tenant, "spec", "tier")).To(Equal("standard"))
	h.g.Expect(field(tenant, "spec", "backupSchedule")).To(Equal("0 3 * * *"))
	h.g.Expect(intField(tenant, "spec", "storageGB")).To(Equal(int64(20)))
	h.g.Expect(tenant.GetLabels()).To(HaveKeyWithValue("platform.internal/team", "checkout"))
	h.g.Expect(tenant.GetLabels()).To(HaveKeyWithValue("platform.internal/cost-center", "eng-platform"))
	h.g.Expect(tenant.GetOwnerReferences()).NotTo(BeEmpty(), "the composed TenantDatabase is owned by its claim")

	t.Log("invalid claim in Git: sync fails at admission, nothing is provisioned")
	h.g.Eventually(func() string { return h.appOperationPhase(invalidApp) }, syncTimeout, poll).Should(Or(Equal("Failed"), Equal("Error")))
	h.g.Expect(h.appOperationMessage(invalidApp)).To(ContainSubstring("POLICY_DENIED rule=storage-ceiling"))
	h.g.Expect(h.get(gvkClaim, h.ns, "big-db")).To(BeNil(), "rejected claim must not exist")
	h.g.Consistently(func() *unstructured.Unstructured { return h.get(gvkTenant, h.ns, "big-db") }, 10*time.Second, poll).
		Should(BeNil(), "rejected claim must never produce a TenantDatabase")

	t.Log("tenant drift is flagged, not reverted")
	h.patchClaimStorage("checkout-db", 30)
	h.g.Eventually(func() string { return h.appSyncStatus(validApp) }, syncTimeout, poll).Should(Equal("OutOfSync"))
	h.g.Consistently(func() int64 { return intField(h.get(gvkClaim, h.ns, "checkout-db"), "spec", "parameters", "storageGB") },
		30*time.Second, poll).Should(Equal(int64(30)), "stateful drift must not be auto-reverted")
	h.g.Eventually(func() int64 { return intField(h.get(gvkTenant, h.ns, "checkout-db"), "spec", "storageGB") },
		syncTimeout, poll).Should(Equal(int64(30)), "Crossplane keeps the composed resource in step with the live claim")

	t.Log("platform drift is auto-healed")
	h.labelPolicy("tenantdatabase-storage-ceiling", "e2e-drift", h.runID)
	h.g.Eventually(func() string {
		p := h.get(gvkPolicy, "", "tenantdatabase-storage-ceiling")
		if p == nil {
			return "missing"
		}
		return p.GetLabels()["e2e-drift"]
	}, syncTimeout, poll).Should(BeEmpty(), "stateless drift must be reverted by kiln-platform selfHeal")
}

func goodTags() map[string]interface{} {
	return map[string]interface{}{"team": "checkout", "costCenter": "eng-platform"}
}

func (h *harness) claim(name string, storageGB int64, tags map[string]interface{}, tier string) *unstructured.Unstructured {
	params := map[string]interface{}{"storageGB": storageGB}
	if tags != nil {
		params["tags"] = tags
	}
	if tier != "" {
		params["tier"] = tier
	}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "platform.internal/v1alpha1",
		"kind":       "DatabaseClaim",
		"metadata":   map[string]interface{}{"name": name, "namespace": h.ns},
		"spec":       map[string]interface{}{"parameters": params},
	}}
	return obj
}

func (h *harness) tenantDatabase(name string, storageGB int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "platform.internal/v1",
		"kind":       "TenantDatabase",
		"metadata":   map[string]interface{}{"name": name, "namespace": h.ns},
		"spec":       map[string]interface{}{"engine": "postgres", "version": "16", "storageGB": storageGB},
	}}
}

func (h *harness) createApp(name, revision, path string) string {
	app := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]interface{}{
			"name":       name,
			"namespace":  argocdNS,
			"finalizers": []interface{}{"resources-finalizer.argocd.argoproj.io"},
		},
		"spec": map[string]interface{}{
			"project":     "default",
			"source":      map[string]interface{}{"repoURL": repoURL, "targetRevision": revision, "path": path},
			"destination": map[string]interface{}{"server": "https://kubernetes.default.svc"},
			"syncPolicy": map[string]interface{}{
				"automated": map[string]interface{}{"prune": false, "selfHeal": false},
				"retry":     map[string]interface{}{"limit": int64(2), "backoff": map[string]interface{}{"duration": "5s", "factor": int64(2), "maxDuration": "30s"}},
			},
		},
	}}
	h.g.Expect(h.c.Create(h.ctx, app)).To(Succeed())
	return name
}

func (h *harness) deleteApp(name string) {
	app := h.get(gvkApp, argocdNS, name)
	if app == nil {
		return
	}
	_ = h.c.Delete(context.Background(), app)
	h.g.Eventually(func() *unstructured.Unstructured { return h.get(gvkApp, argocdNS, name) }, syncTimeout, poll).Should(BeNil())
}

func (h *harness) appSyncStatus(name string) string {
	return field(h.get(gvkApp, argocdNS, name), "status", "sync", "status")
}

func (h *harness) appOperationPhase(name string) string {
	return field(h.get(gvkApp, argocdNS, name), "status", "operationState", "phase")
}

func (h *harness) appOperationMessage(name string) string {
	app := h.get(gvkApp, argocdNS, name)
	msg := field(app, "status", "operationState", "message")
	results, _, _ := unstructured.NestedSlice(app.Object, "status", "operationState", "syncResult", "resources")
	for _, r := range results {
		if m, ok := r.(map[string]interface{}); ok {
			msg += " " + fmt.Sprint(m["message"])
		}
	}
	return msg
}

func (h *harness) patchClaimStorage(name string, storageGB int64) {
	h.g.Eventually(func() error {
		obj := h.get(gvkClaim, h.ns, name)
		if obj == nil {
			return fmt.Errorf("claim %s not found", name)
		}
		if err := unstructured.SetNestedField(obj.Object, storageGB, "spec", "parameters", "storageGB"); err != nil {
			return err
		}
		return h.c.Update(h.ctx, obj)
	}, time.Minute, poll).Should(Succeed())
}

func (h *harness) labelPolicy(name, key, value string) {
	h.g.Eventually(func() error {
		obj := h.get(gvkPolicy, "", name)
		if obj == nil {
			return fmt.Errorf("policy %s not found", name)
		}
		labels := obj.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[key] = value
		obj.SetLabels(labels)
		return h.c.Update(h.ctx, obj)
	}, time.Minute, poll).Should(Succeed())
}

// kyvernoWebhookFor returns the Kyverno webhook entry whose rules cover the given
// platform.internal resource, or nil while Kyverno has not registered one yet.
func (h *harness) kyvernoWebhookFor(resource string) map[string]interface{} {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvkWebhook)
	if err := h.c.List(h.ctx, list); err != nil {
		return nil
	}
	for _, cfg := range list.Items {
		if !strings.Contains(cfg.GetName(), "kyverno") {
			continue
		}
		webhooks, _, _ := unstructured.NestedSlice(cfg.Object, "webhooks")
		for _, w := range webhooks {
			wh, ok := w.(map[string]interface{})
			if !ok {
				continue
			}
			rules, _, _ := unstructured.NestedSlice(wh, "rules")
			for _, r := range rules {
				rule, _ := r.(map[string]interface{})
				groups, _, _ := unstructured.NestedStringSlice(rule, "apiGroups")
				resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
				if contains(groups, "platform.internal") && contains(resources, resource) {
					return wh
				}
			}
		}
	}
	return nil
}

func (h *harness) pushBranch(branch string, files map[string]*unstructured.Unstructured) {
	blobs := map[string]string{}
	for path, obj := range files {
		yamlBytes, err := yamlOf(obj)
		h.g.Expect(err).NotTo(HaveOccurred())
		blobs[path] = h.git(strings.NewReader(yamlBytes), "hash-object", "-w", "--stdin")
	}
	root := h.mktree(blobs, "")
	commit := h.git(nil, "-c", "user.name=kiln-e2e", "-c", "user.email=e2e@kiln.invalid",
		"commit-tree", root, "-m", "e2e "+h.runID)
	h.git(nil, "push", "origin", commit+":refs/heads/"+branch)
}

// mktree builds nested git trees from path -> blob without touching the working tree.
func (h *harness) mktree(blobs map[string]string, prefix string) string {
	var entries []string
	subdirs := map[string]map[string]string{}
	for path, blob := range blobs {
		rel := strings.TrimPrefix(path, prefix)
		if i := strings.Index(rel, "/"); i >= 0 {
			dir := rel[:i]
			if subdirs[dir] == nil {
				subdirs[dir] = map[string]string{}
			}
			subdirs[dir][path] = blob
			continue
		}
		entries = append(entries, fmt.Sprintf("100644 blob %s\t%s", blob, rel))
	}
	for dir, sub := range subdirs {
		entries = append(entries, fmt.Sprintf("040000 tree %s\t%s", h.mktree(sub, prefix+dir+"/"), dir))
	}
	return h.git(strings.NewReader(strings.Join(entries, "\n")+"\n"), "mktree")
}

func (h *harness) deleteBranch(branch string) {
	cmd := exec.Command("git", "push", "origin", "--delete", branch)
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		h.t.Logf("branch %s not deleted: %v: %s", branch, err, out)
	}
}

func (h *harness) git(stdin *strings.Reader, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot()
	if stdin != nil {
		cmd.Stdin = stdin
	}
	out, err := cmd.CombinedOutput()
	h.g.Expect(err).NotTo(HaveOccurred(), "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func repoRoot() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "."
	}
	return strings.TrimSpace(string(out))
}

func (h *harness) createNamespace() {
	ns := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]interface{}{"name": h.ns},
	}}
	h.g.Expect(h.c.Create(h.ctx, ns)).To(Succeed())
}

func (h *harness) deleteNamespace() {
	ns := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]interface{}{"name": h.ns},
	}}
	_ = h.c.Delete(context.Background(), ns)
}

func (h *harness) get(gvk schema.GroupVersionKind, ns, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := h.c.Get(h.ctx, types.NamespacedName{Namespace: ns, Name: name}, obj); err != nil {
		return nil
	}
	return obj
}

func (h *harness) list(gvk schema.GroupVersionKind, ns string) []unstructured.Unstructured {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	h.g.Expect(h.c.List(h.ctx, list, client.InNamespace(ns))).To(Succeed())
	return list.Items
}

func conditionTrue(obj *unstructured.Unstructured, condType string) bool {
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]interface{})
		if m["type"] == condType && m["status"] == "True" {
			return true
		}
	}
	return false
}

func field(obj *unstructured.Unstructured, path ...string) string {
	if obj == nil {
		return ""
	}
	v, _, _ := unstructured.NestedString(obj.Object, path...)
	return v
}

func intField(obj *unstructured.Unstructured, path ...string) int64 {
	if obj == nil {
		return -1
	}
	v, _, _ := unstructured.NestedInt64(obj.Object, path...)
	return v
}

func yamlOf(obj *unstructured.Unstructured) (string, error) {
	out, err := yaml.Marshal(obj.Object)
	return string(out), err
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want || s == "*" {
			return true
		}
	}
	return false
}
