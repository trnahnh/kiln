//go:build e2e

// Phase 6 exit criterion against a live cluster: every action from Phases 1 to 5 is visible
// in the audit log, a tampered entry is detected on verification, and a duplicate Kafka
// delivery does not duplicate an entry. Visibility is read from the Kafka topic inside the
// broker pod and from the rows inside the Postgres pod; the chain is recomputed here from
// those rows. The service's own query endpoint is never the evidence.
package e2e

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	auditNS         = "kiln-audit"
	kafkaNS         = "kiln-kafka"
	auditTopic      = "kiln.audit"
	auditSubject    = "phase6@kiln.e2e"
	annotationActor = "platform.internal/requested-by"
	genesisHash     = "0000000000000000000000000000000000000000000000000000000000000000"
	auditSettle     = 2 * time.Minute
)

type auditHarness struct {
	t     *testing.T
	g     *WithT
	ctx   context.Context
	cfg   *rest.Config
	c     client.Client
	cs    *kubernetes.Clientset
	ns    string
	key   *rsa.PrivateKey
	runID int64

	expected []expectedEvent
	base     string
	stop     chan struct{}
}

type expectedEvent struct {
	action, outcome, resource, actor string
}

type auditRow struct {
	seq        int64
	eventID    string
	actor      string
	action     string
	resource   string
	occurredAt string
	details    string
	prevHash   string
	hash       string
}

type topicRecord struct {
	key   string
	value string
	event map[string]any
}

func TestPhase6Audit(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := ctrl.GetConfigOrDie()
	c, err := client.New(cfg, client.Options{})
	g.Expect(err).NotTo(HaveOccurred())
	cs, err := kubernetes.NewForConfig(cfg)
	g.Expect(err).NotTo(HaveOccurred())

	runID := time.Now().Unix()
	h := &auditHarness{t: t, g: g, ctx: ctx, cfg: cfg, c: c, cs: cs, ns: fmt.Sprintf("audit-e2e-%d", runID), runID: runID}
	h.loadSigningKey()
	h.waitForAudit()
	h.allowVolumeExpansion()
	h.createNamespace()
	t.Cleanup(h.deleteNamespace)
	h.forward()
	t.Cleanup(h.stopForward)

	t.Run("who may submit is enforced by the service", h.testSubmitRBAC)
	t.Run("a claim submitted through the API is provisioned, backed up, restored and scaled", h.testOperatorActions)
	t.Run("a violating claim is denied and the denial is audited", h.testPolicyDeny)
	t.Run("a pod placed by kiln-scheduler is audited", h.testSchedule)
	t.Run("a rollout and its rollback are audited", h.testDelivery)
	t.Run("a chaos experiment is audited", h.testChaos)
	t.Run("every action is on the topic and in the table", h.testVisibility)
	t.Run("the chain recomputed from the rows holds", h.testChainHolds)
	t.Run("a tampered row is detected on verification", h.testTamper)
	t.Run("a redelivered record does not duplicate a row", h.testRedelivery)
	t.Run("no other subsystem can read the writer credential", h.testWriteBoundary)
}

func (h *auditHarness) loadSigningKey() {
	path := os.Getenv("KILN_AUDIT_JWT_PRIVATE_KEY")
	if path == "" {
		path = filepath.Join("..", "hack", "keys", "audit-jwt-private.pem")
	}
	raw, err := os.ReadFile(path)
	h.g.Expect(err).NotTo(HaveOccurred(), "the JWT private key from hack/audit-secrets.sh is needed to mint tokens")
	block, _ := pem.Decode(raw)
	h.g.Expect(block).NotTo(BeNil(), "%s is not PEM", path)
	var key any
	if key, err = x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	h.g.Expect(err).NotTo(HaveOccurred())
	h.key = key.(*rsa.PrivateKey)
}

func (h *auditHarness) waitForAudit() {
	h.t.Log("waiting for kafka, the audit postgres, the audit service and every publishing controller")
	for _, s := range []types.NamespacedName{{Namespace: kafkaNS, Name: "kafka"}, {Namespace: auditNS, Name: "audit-postgres"}} {
		h.g.Eventually(func() int32 {
			sts := &appsv1.StatefulSet{}
			if err := h.c.Get(h.ctx, s, sts); err != nil {
				return 0
			}
			return sts.Status.ReadyReplicas
		}, platformReady, poll).Should(BeNumerically(">=", 1), "%s has no ready replica", s.Name)
	}
	for _, d := range []types.NamespacedName{
		{Namespace: auditNS, Name: "kiln-audit-service"},
		{Namespace: "kiln-operator-system", Name: "kiln-operator-controller-manager"},
		{Namespace: "kiln-delivery-system", Name: "kiln-delivery-controller"},
		{Namespace: "kiln-chaos-system", Name: "kiln-chaos-controller"},
		{Namespace: "kiln-scheduler-system", Name: "kiln-scheduler"},
		{Namespace: "monitoring", Name: "prometheus"},
		{Namespace: "istio-system", Name: "istiod"},
	} {
		h.g.Eventually(func() int32 {
			dep := &appsv1.Deployment{}
			if err := h.c.Get(h.ctx, d, dep); err != nil {
				return 0
			}
			return dep.Status.AvailableReplicas
		}, platformReady, poll).Should(BeNumerically(">=", 1), "%s is not available", d.Name)
	}
	for _, app := range []string{"kafka", "kiln-audit", "kiln-operator", "kiln-delivery", "kiln-chaos", "kiln-scheduler", "kiln-platform"} {
		h.g.Eventually(func() string { return (&harness{ctx: h.ctx, c: h.c}).appSyncStatus(app) }, platformReady, poll).Should(Equal("Synced"), "%s is not Synced", app)
	}
}

// allowVolumeExpansion makes the SCALE path apply rather than be rejected: kind's default
// StorageClass refuses growth until it is patched (README, Getting started).
func (h *auditHarness) allowVolumeExpansion() {
	sc := &storagev1.StorageClass{}
	h.g.Expect(h.c.Get(h.ctx, types.NamespacedName{Name: "standard"}, sc)).To(Succeed())
	if sc.AllowVolumeExpansion != nil && *sc.AllowVolumeExpansion {
		return
	}
	before := sc.DeepCopy()
	allow := true
	sc.AllowVolumeExpansion = &allow
	h.g.Expect(h.c.Patch(h.ctx, sc, client.MergeFrom(before))).To(Succeed())
}

func (h *auditHarness) createNamespace() {
	_ = h.c.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.ns}})
	h.g.Eventually(func() bool {
		return h.c.Get(h.ctx, types.NamespacedName{Name: h.ns}, &corev1.Namespace{}) != nil
	}, 2*time.Minute, poll).Should(BeTrue(), "a stale namespace from a previous run must clear first")
	h.g.Expect(h.c.Create(h.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.ns}})).To(Succeed())
}

func (h *auditHarness) deleteNamespace() {
	_ = h.c.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.ns}})
}

// forward opens a port-forward to the audit service pod; the REST endpoints are exercised
// through it. The port is chosen by the kernel.
func (h *auditHarness) forward() {
	pods := h.podsIn(auditNS, "app.kubernetes.io/name", "kiln-audit-service")
	h.g.Expect(pods).NotTo(BeEmpty(), "no audit service pod")
	req := h.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(auditNS).Name(pods[0].Name).SubResource("portforward")
	transport, upgrader, err := spdy.RoundTripperFor(h.cfg)
	h.g.Expect(err).NotTo(HaveOccurred())
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())
	h.stop = make(chan struct{})
	ready := make(chan struct{})
	fw, err := portforward.New(dialer, []string{"0:8080"}, h.stop, ready, io.Discard, os.Stderr)
	h.g.Expect(err).NotTo(HaveOccurred())
	go func() {
		if err := fw.ForwardPorts(); err != nil {
			h.t.Logf("port-forward ended: %v", err)
		}
	}()
	<-ready
	ports, err := fw.GetPorts()
	h.g.Expect(err).NotTo(HaveOccurred())
	h.base = fmt.Sprintf("http://127.0.0.1:%d", ports[0].Local)
	h.g.Eventually(func() int {
		resp, err := http.Get(h.base + "/healthz")
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}, time.Minute, poll).Should(Equal(200))
}

func (h *auditHarness) stopForward() {
	if h.stop != nil {
		close(h.stop)
	}
}

// token mints an RS256 JWT with the private key whose public half the service trusts.
func (h *auditHarness) token(subject string, roles ...string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss":   "kiln-e2e",
		"sub":   subject,
		"roles": roles,
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, h.key, crypto.SHA256, digest[:])
	h.g.Expect(err).NotTo(HaveOccurred())
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (h *auditHarness) call(method, path, token string, body any) (int, map[string]any) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		h.g.Expect(err).NotTo(HaveOccurred())
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(h.ctx, method, h.base+path, reader)
	h.g.Expect(err).NotTo(HaveOccurred())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	h.g.Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (h *auditHarness) claimBody(name string, storageGB int64) map[string]any {
	return map[string]any{
		"apiVersion": "platform.internal/v1alpha1",
		"kind":       "DatabaseClaim",
		"metadata":   map[string]any{"name": name, "namespace": h.ns},
		"spec": map[string]any{
			"parameters": map[string]any{
				"tier":      "standard",
				"storageGB": storageGB,
				"tags":      map[string]any{"team": "phase6", "costCenter": "eng-platform"},
			},
		},
	}
}

func (h *auditHarness) expect(action, outcome, resource, actor string) {
	h.expected = append(h.expected, expectedEvent{action: action, outcome: outcome, resource: resource, actor: actor})
}

func (h *auditHarness) testSubmitRBAC(t *testing.T) {
	g := NewWithT(t)
	code, _ := h.call("POST", "/v1/requests", "", h.claimBody("anon", 1))
	g.Expect(code).To(Equal(401), "no token must be rejected")
	code, _ = h.call("POST", "/v1/requests", h.token("reader@kiln.e2e", "audit:read"), h.claimBody("reader", 1))
	g.Expect(code).To(Equal(403), "a token without requests:submit must be rejected")
	code, _ = h.call("GET", "/v1/audit/verify", h.token("reader@kiln.e2e", "audit:read"), nil)
	g.Expect(code).To(Equal(403), "verify needs audit:admin")
	g.Expect(h.list(gvkClaim)).To(BeEmpty(), "a rejected submission must create nothing")
}

func (h *auditHarness) testOperatorActions(t *testing.T) {
	g := NewWithT(t)
	name := "phase6-db"
	code, body := h.call("POST", "/v1/requests", h.token(auditSubject, "requests:submit"), h.claimBody(name, 1))
	g.Expect(code).To(Equal(202), "submission failed: %v", body)
	claimRef := "DatabaseClaim/" + h.ns + "/" + name
	h.expect("PROVISION_REQUEST", "Received", claimRef, auditSubject)

	tdbRef := "TenantDatabase/" + h.ns + "/" + name
	g.Eventually(func() string {
		tdb := h.get(gvkTenant, name)
		if tdb == nil {
			return ""
		}
		return tdb.GetAnnotations()[annotationActor]
	}, syncTimeout, poll).Should(Equal(auditSubject), "the composed TenantDatabase must carry the caller as requested-by")
	g.Eventually(func() string { return h.tdbPhase(name) }, platformReady, poll).Should(Equal("Ready"), "the in-cluster operator must provision the database")
	h.expect("PROVISION", "Ready", tdbRef, auditSubject)

	h.annotate(name, "platform.internal/backup", "now")
	g.Eventually(func() string { return h.tdbPhase(name) }, 2*time.Minute, poll).Should(Equal("Backing Up"))
	g.Eventually(func() string { return h.tdbPhase(name) }, 5*time.Minute, poll).Should(Equal("Ready"))
	g.Expect(h.get(gvkTenant, name).Object["status"].(map[string]any)["lastBackupTime"]).NotTo(BeNil(), "the backup must have completed")
	h.expect("BACKUP", "Started", tdbRef, auditSubject)
	h.expect("BACKUP", "Succeeded", tdbRef, auditSubject)

	h.annotate(name, "platform.internal/restore-from", "latest")
	g.Eventually(func() string { return h.tdbPhase(name) }, 2*time.Minute, poll).Should(Equal("Restoring"))
	g.Eventually(func() string { return h.tdbPhase(name) }, 5*time.Minute, poll).Should(Equal("Ready"))
	h.expect("RESTORE", "Started", tdbRef, auditSubject)
	h.expect("RESTORE", "Succeeded", tdbRef, auditSubject)

	claim := h.get(gvkClaim, name)
	g.Expect(claim).NotTo(BeNil())
	g.Expect(unstructured.SetNestedField(claim.Object, int64(2), "spec", "parameters", "storageGB")).To(Succeed())
	g.Expect(h.c.Update(h.ctx, claim)).To(Succeed())
	g.Eventually(func() string {
		pvc := &corev1.PersistentVolumeClaim{}
		if err := h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: name + "-data"}, pvc); err != nil {
			return ""
		}
		return pvc.Spec.Resources.Requests.Storage().String()
	}, 5*time.Minute, poll).Should(Equal("2Gi"), "the data volume must actually be asked to grow")
	h.expect("SCALE", "Applied", tdbRef, auditSubject)
}

func (h *auditHarness) testPolicyDeny(t *testing.T) {
	g := NewWithT(t)
	code, body := h.call("POST", "/v1/requests", h.token(auditSubject, "requests:submit"), h.claimBody("too-big", 500))
	g.Expect(code).To(Equal(422), "a claim over the storage ceiling must be denied at admission: %v", body)
	g.Expect(body["code"]).To(Equal("POLICY_DENIED"))
	g.Expect(h.get(gvkClaim, "too-big")).To(BeNil(), "a denied claim must not exist")
	h.expect("POLICY_DENY", "Denied", "DatabaseClaim/"+h.ns+"/too-big", auditSubject)
}

func (h *auditHarness) testSchedule(t *testing.T) {
	g := NewWithT(t)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "phase6-batch", Namespace: h.ns, Labels: map[string]string{labelWorkloadClass: "batch"}},
		Spec: corev1.PodSpec{
			SchedulerName: "kiln-scheduler",
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: "work", Image: sleepImage, Command: []string{"sleep", "30"}}},
		},
	}
	g.Expect(h.c.Create(h.ctx, pod)).To(Succeed())
	g.Eventually(func() string {
		p := &corev1.Pod{}
		if err := h.c.Get(h.ctx, client.ObjectKeyFromObject(pod), p); err != nil {
			return ""
		}
		return p.Spec.NodeName
	}, 2*time.Minute, poll).ShouldNot(BeEmpty(), "kiln-scheduler must bind the pod")
	h.expect("SCHEDULE", "Bound", "Pod/"+h.ns+"/"+pod.Name, "system:kiln-scheduler")
}

// testDelivery reuses the Phase 4 harness in its own mesh namespace: the broken version's
// rollback is proven on the mesh there, and here it must also be in the audit log.
func (h *auditHarness) testDelivery(t *testing.T) {
	ch := &canaryHarness{t: t, g: NewWithT(t), ctx: h.ctx, cfg: h.cfg, c: h.c, cs: h.cs, ns: fmt.Sprintf("audit-canary-%d", h.runID), start: map[string]time.Time{}}
	ch.createMeshNamespace()
	t.Cleanup(ch.deleteNamespace)
	ch.deployTarget()
	ch.initialVersion(t)
	ch.brokenVersion(t)
	ref := "CanaryRollout/" + ch.ns + "/" + targetApp
	h.expect("DEPLOY", "Started", ref, "system:canaryrollout")
	h.expect("ROLLBACK", "RolledBack", ref, "system:canaryrollout")
}

// testChaos reuses the Phase 5 harness for one pod-kill experiment.
func (h *auditHarness) testChaos(t *testing.T) {
	ch := &chaosHarness{t: t, g: NewWithT(t), ctx: h.ctx, c: h.c, cs: h.cs, ns: fmt.Sprintf("audit-chaos-%d", h.runID)}
	ch.createMeshNamespace()
	t.Cleanup(ch.deleteNamespace)
	ch.deployTarget()
	ch.testPodKillScored(t)
	ref := "ChaosExperiment/" + ch.ns + "/podkill"
	h.expect("CHAOS_EXPERIMENT", "Started", ref, "system:chaos-controller")
	h.expect("CHAOS_EXPERIMENT", "Completed", ref, "system:chaos-controller")
}

func (h *auditHarness) testVisibility(t *testing.T) {
	g := NewWithT(t)
	g.Expect(h.expected).NotTo(BeEmpty())

	var records []topicRecord
	g.Eventually(func() []string {
		records = h.readTopic()
		return h.missing(func(e expectedEvent) bool { return findRecord(records, e) != nil })
	}, auditSettle, 5*time.Second).Should(BeEmpty(), "actions missing from the Kafka topic")
	t.Logf("%d records on %s", len(records), auditTopic)

	var rows []auditRow
	g.Eventually(func() []string {
		rows = h.readRows()
		return h.missing(func(e expectedEvent) bool { return findRow(rows, e) != nil })
	}, auditSettle, 5*time.Second).Should(BeEmpty(), "actions missing from audit_entry")
	t.Logf("%d rows in audit_entry", len(rows))

	for _, e := range h.expected {
		rec := findRecord(records, e)
		row := findRow(rows, e)
		g.Expect(row.eventID).To(Equal(rec.event["eventId"]), "%v: the row must be the record", e)
		g.Expect(rec.key).To(Equal(e.resource), "records are keyed by resource")
		if e.actor != "" {
			g.Expect(row.actor).To(Equal(e.actor), "%s %s on %s attributed to the wrong actor", e.action, e.outcome, e.resource)
		}
		seen := 0
		for _, r := range rows {
			if r.eventID == row.eventID {
				seen++
			}
		}
		g.Expect(seen).To(Equal(1), "%s stored more than once", row.eventID)
	}
}

func (h *auditHarness) testChainHolds(t *testing.T) {
	g := NewWithT(t)
	rows := h.readRows()
	g.Expect(rows).NotTo(BeEmpty())
	g.Expect(verifyChain(rows)).To(BeEmpty(), "the chain recomputed from the rows must hold")
	code, body := h.call("GET", "/v1/audit/verify", h.token("auditor@kiln.e2e", "audit:admin"), nil)
	g.Expect(code).To(Equal(200))
	g.Expect(body["ok"]).To(BeTrue(), "the service must agree with the independent recomputation: %v", body)
}

func (h *auditHarness) testTamper(t *testing.T) {
	g := NewWithT(t)
	rows := h.readRows()
	victim := findRow(rows, h.expected[0])
	g.Expect(victim).NotTo(BeNil())
	h.psql(fmt.Sprintf("UPDATE audit_entry SET actor = 'mallory@kiln.e2e' WHERE seq = %d", victim.seq))
	defer h.psql(fmt.Sprintf("UPDATE audit_entry SET actor = '%s' WHERE seq = %d", victim.actor, victim.seq))

	g.Expect(verifyChain(h.readRows())).To(ContainElement(victim.seq), "the recomputation must see the corruption")
	code, body := h.call("GET", "/v1/audit/verify", h.token("auditor@kiln.e2e", "audit:admin"), nil)
	g.Expect(code).To(Equal(200))
	g.Expect(body["ok"]).To(BeFalse(), "verification must fail on a tampered row: %v", body)
	g.Expect(body["code"]).To(Equal("AUDIT_CHAIN_BROKEN"))
	var seqs []int64
	links, _ := body["brokenLinks"].([]any)
	for _, l := range links {
		m, _ := l.(map[string]any)
		if s, ok := m["seq"].(float64); ok {
			seqs = append(seqs, int64(s))
		}
	}
	g.Expect(seqs).To(ContainElement(victim.seq), "the broken link must name the tampered row: %v", body)
	t.Logf("tampering row %d reported as %v", victim.seq, links)
}

func (h *auditHarness) testRedelivery(t *testing.T) {
	g := NewWithT(t)
	records := h.readTopic()
	rec := findRecord(records, h.expected[0])
	g.Expect(rec).NotTo(BeNil())
	rowsBefore := h.readRows()
	dupID, _ := rec.event["eventId"].(string)
	g.Expect(countRows(rowsBefore, dupID)).To(Equal(1))
	onTopic := func() int {
		n := 0
		for _, r := range h.readTopic() {
			if r.event["eventId"] == dupID {
				n++
			}
		}
		return n
	}
	before := onTopic()

	h.produce(rec.key, rec.value)
	g.Eventually(onTopic, time.Minute, 5*time.Second).Should(Equal(before+1), "the redelivery must actually be on the topic")

	g.Consistently(func() int { return countRows(h.readRows(), dupID) }, 30*time.Second, 10*time.Second).Should(Equal(1), "a redelivered record must not add a row")
	rowsAfter := h.readRows()
	g.Expect(len(rowsAfter)).To(BeNumerically(">=", len(rowsBefore)))
	g.Expect(verifyChain(rowsAfter)).To(BeEmpty(), "the chain must still hold after the redelivery")
	code, body := h.call("GET", "/v1/audit/verify", h.token("auditor@kiln.e2e", "audit:admin"), nil)
	g.Expect(code).To(Equal(200))
	g.Expect(body["ok"]).To(BeTrue())
	t.Logf("row for %s still stored once after redelivery; %d rows in total", dupID, len(rowsAfter))
}

// testWriteBoundary proves the API-level boundary from the authoriser and the pod specs:
// every publishing controller's ServiceAccount is a real identity (it may list its own
// kind), none may read the writer credential, the operator's Secret grant is create only
// (ADR-0019), and no publisher's pod references the Secret. The NetworkPolicy is a separate,
// network-level boundary and is only checked to be delivered.
func (h *auditHarness) testWriteBoundary(t *testing.T) {
	g := NewWithT(t)
	subjects := []struct {
		ns, name, ownGroup, ownResource string
	}{
		{"kiln-operator-system", "kiln-operator-controller-manager", "platform.internal", "tenantdatabases"},
		{"kiln-delivery-system", "kiln-delivery-controller", "platform.internal", "canaryrollouts"},
		{"kiln-chaos-system", "kiln-chaos-controller", "platform.internal", "chaosexperiments"},
		{"kiln-scheduler-system", "kiln-scheduler", "", "pods"},
	}
	for _, s := range subjects {
		sa := "system:serviceaccount:" + s.ns + ":" + s.name
		g.Expect(h.allowed(sa, s.ownGroup, s.ownResource, "", "list")).To(BeTrue(), "%s should be able to list %s; is the ServiceAccount name right?", sa, s.ownResource)
		for _, verb := range []string{"get", "list", "watch"} {
			g.Expect(h.allowed(sa, "", "secrets", "audit-postgres", verb)).To(BeFalse(), "%s must not %s the audit writer credential", sa, verb)
		}
		dep := &appsv1.Deployment{}
		g.Expect(h.c.Get(h.ctx, types.NamespacedName{Namespace: s.ns, Name: s.name}, dep)).To(Succeed())
		g.Expect(referencesSecret(dep.Spec.Template.Spec, "audit-postgres")).To(BeFalse(), "%s must not mount or read the writer credential", s.name)
	}
	operatorSA := "system:serviceaccount:kiln-operator-system:kiln-operator-controller-manager"
	g.Expect(h.allowed(operatorSA, "", "secrets", "", "create")).To(BeTrue(), "the operator still creates tenant credentials")
	role := &rbacv1.ClusterRole{}
	g.Expect(h.c.Get(h.ctx, types.NamespacedName{Name: "kiln-operator-manager-role"}, role)).To(Succeed())
	var secretVerbs []string
	for _, rule := range role.Rules {
		for _, res := range rule.Resources {
			if res == "secrets" {
				secretVerbs = append(secretVerbs, rule.Verbs...)
			}
		}
	}
	g.Expect(secretVerbs).To(Equal([]string{"create"}), "the operator's Secret grant must be create only")
	np := &unstructured.Unstructured{}
	np.SetGroupVersionKind(schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"})
	g.Expect(h.c.Get(h.ctx, types.NamespacedName{Namespace: auditNS, Name: "audit-postgres-writer-only"}, np)).To(Succeed(), "the network boundary must be delivered even where the CNI ignores it")
}

func referencesSecret(spec corev1.PodSpec, name string) bool {
	for _, v := range spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == name {
			return true
		}
	}
	for _, c := range append(spec.InitContainers, spec.Containers...) {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == name {
				return true
			}
		}
		for _, ef := range c.EnvFrom {
			if ef.SecretRef != nil && ef.SecretRef.Name == name {
				return true
			}
		}
	}
	return false
}

func (h *auditHarness) allowed(user, group, resource, name, verb string) bool {
	sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User: user,
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Namespace: auditNS, Group: group, Resource: resource, Name: name, Verb: verb,
		},
	}}
	if name == "" {
		sar.Spec.ResourceAttributes.Namespace = ""
	}
	out, err := h.cs.AuthorizationV1().SubjectAccessReviews().Create(h.ctx, sar, metav1.CreateOptions{})
	h.g.Expect(err).NotTo(HaveOccurred())
	return out.Status.Allowed
}

func (h *auditHarness) missing(present func(expectedEvent) bool) []string {
	var out []string
	for _, e := range h.expected {
		if !present(e) {
			out = append(out, fmt.Sprintf("%s/%s on %s", e.action, e.outcome, e.resource))
		}
	}
	return out
}

func findRecord(records []topicRecord, e expectedEvent) *topicRecord {
	for i := range records {
		r := &records[i]
		details, _ := r.event["details"].(map[string]any)
		if r.event["action"] == e.action && r.event["resource"] == e.resource && details["outcome"] == e.outcome {
			return r
		}
	}
	return nil
}

func findRow(rows []auditRow, e expectedEvent) *auditRow {
	for i := range rows {
		r := &rows[i]
		if r.action != e.action || r.resource != e.resource {
			continue
		}
		var details map[string]any
		_ = json.Unmarshal([]byte(r.details), &details)
		if details["outcome"] == e.outcome {
			return r
		}
	}
	return nil
}

func countRows(rows []auditRow, eventID string) int {
	n := 0
	for _, r := range rows {
		if r.eventID == eventID {
			n++
		}
	}
	return n
}

// readTopic consumes the whole topic from the broker pod itself, so what is asserted is
// what Kafka holds rather than what any client claims to have sent.
func (h *auditHarness) readTopic() []topicRecord {
	out, errOut, err := h.exec(kafkaNS, "kafka-0", nil, "/opt/kafka/bin/kafka-console-consumer.sh",
		"--bootstrap-server", "localhost:9092", "--topic", auditTopic, "--from-beginning", "--timeout-ms", "10000",
		"--property", "print.key=true", "--property", "key.separator=\t")
	if err != nil && strings.TrimSpace(out) == "" {
		h.t.Logf("consumer: %v: %s", err, errOut)
		return nil
	}
	var records []topicRecord
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(value), &event); err != nil {
			continue
		}
		records = append(records, topicRecord{key: key, value: value, event: event})
	}
	return records
}

func (h *auditHarness) produce(key, value string) {
	stdin := strings.NewReader(key + "\t" + value + "\n")
	_, errOut, err := h.exec(kafkaNS, "kafka-0", stdin, "/opt/kafka/bin/kafka-console-producer.sh",
		"--bootstrap-server", "localhost:9092", "--topic", auditTopic,
		"--property", "parse.key=true", "--property", "key.separator=\t")
	h.g.Expect(err).NotTo(HaveOccurred(), "producer: %s", errOut)
}

const rowSeparator = "\x1f"

// readRows reads the table inside the Postgres pod as the superuser over the local socket;
// the service's query endpoint is not involved.
func (h *auditHarness) readRows() []auditRow {
	query := `SELECT seq, event_id, actor, action, resource, ` +
		`to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), details::text, prev_hash, hash ` +
		`FROM audit_entry ORDER BY seq`
	out := h.psql(query)
	var rows []auditRow
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, rowSeparator)
		if len(f) != 9 {
			continue
		}
		seq, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		rows = append(rows, auditRow{seq: seq, eventID: f[1], actor: f[2], action: f[3], resource: f[4], occurredAt: f[5], details: f[6], prevHash: f[7], hash: f[8]})
	}
	return rows
}

func (h *auditHarness) psql(sql string) string {
	out, errOut, err := h.exec(auditNS, "audit-postgres-0", nil, "psql", "-U", "postgres", "-d", "audit", "-At", "-F", rowSeparator, "-c", sql)
	h.g.Expect(err).NotTo(HaveOccurred(), "psql: %s", errOut)
	return out
}

func (h *auditHarness) exec(ns, pod string, stdin io.Reader, cmd ...string) (string, string, error) {
	req := h.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Command: cmd, Stdin: stdin != nil, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	ex, err := remotecommand.NewSPDYExecutor(h.cfg, "POST", req.URL())
	if err != nil {
		return "", "", err
	}
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Minute)
	defer cancel()
	err = ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: &stdout, Stderr: &stderr})
	return stdout.String(), stderr.String(), err
}

// verifyChain recomputes every hash per DATA_MODEL.md and returns the seqs that do not
// hold, from the rows alone.
func verifyChain(rows []auditRow) []int64 {
	var broken []int64
	prev := genesisHash
	for _, r := range rows {
		want := entryHash(r.prevHash, r.eventID, r.actor, r.action, r.resource, r.occurredAt, canonicalJSON(r.details))
		if r.prevHash != prev || r.hash != want {
			broken = append(broken, r.seq)
		}
		prev = r.hash
	}
	return broken
}

func entryHash(prevHash, eventID, actor, action, resource, occurredAt, details string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{prevHash, eventID, actor, action, resource, occurredAt, details}, "\n")))
	return hex.EncodeToString(sum[:])
}

// canonicalJSON: keys sorted at every level, no whitespace, numbers as stored.
func canonicalJSON(raw string) string {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	var buf bytes.Buffer
	writeCanonical(&buf, v)
	return buf.String()
}

func writeCanonical(buf *bytes.Buffer, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, k)
			buf.WriteByte(':')
			writeCanonical(buf, x[k])
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeCanonical(buf, e)
		}
		buf.WriteByte(']')
	case json.Number:
		buf.WriteString(x.String())
	case string:
		writeString(buf, x)
	case bool:
		buf.WriteString(strconv.FormatBool(x))
	case nil:
		buf.WriteString("null")
	}
}

func writeString(buf *bytes.Buffer, s string) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	buf.Truncate(buf.Len() - 1)
}

func (h *auditHarness) get(gvk schema.GroupVersionKind, name string) *unstructured.Unstructured {
	return (&harness{ctx: h.ctx, c: h.c}).get(gvk, h.ns, name)
}

func (h *auditHarness) list(gvk schema.GroupVersionKind) []unstructured.Unstructured {
	return (&harness{ctx: h.ctx, c: h.c, g: h.g}).list(gvk, h.ns)
}

func (h *auditHarness) tdbPhase(name string) string {
	tdb := h.get(gvkTenant, name)
	if tdb == nil {
		return ""
	}
	return field(tdb, "status", "phase")
}

func (h *auditHarness) annotate(name, key, value string) {
	tdb := h.get(gvkTenant, name)
	h.g.Expect(tdb).NotTo(BeNil())
	before := tdb.DeepCopy()
	ann := tdb.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[key] = value
	tdb.SetAnnotations(ann)
	h.g.Expect(h.c.Patch(h.ctx, tdb, client.MergeFrom(before))).To(Succeed())
}

func (h *auditHarness) podsIn(ns, label, value string) []corev1.Pod {
	list := &corev1.PodList{}
	h.g.Expect(h.c.List(h.ctx, list, client.InNamespace(ns), client.MatchingLabels{label: value})).To(Succeed())
	return list.Items
}
