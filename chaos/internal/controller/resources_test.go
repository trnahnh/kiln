package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestWorkloadNameIsWhatIstioReports(t *testing.T) {
	owned := func(kind, name, hash string) corev1.Pod {
		labels := map[string]string{"app": "fortio"}
		if hash != "" {
			labels["pod-template-hash"] = hash
		}
		return corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{{Kind: kind, Name: name, Controller: ptr.To(true)}},
		}}
	}
	cases := map[string]struct {
		pods []corev1.Pod
		want string
	}{
		"a canary-managed service's primary pods belong to the primary Deployment": {
			[]corev1.Pod{owned("ReplicaSet", "fortio-primary-856b7f9b89", "856b7f9b89")}, "fortio-primary"},
		"a plain Deployment named after its app label": {
			[]corev1.Pod{owned("ReplicaSet", "fortio-64c5f89b97", "64c5f89b97")}, "fortio"},
		"a StatefulSet pod is its StatefulSet": {
			[]corev1.Pod{owned("StatefulSet", "db", "")}, "db"},
		"an unowned pod falls back to the app label": {
			[]corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "fortio"}}}}, "fortio"},
		"no pods": {nil, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := workloadName(nil, tc.pods); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
