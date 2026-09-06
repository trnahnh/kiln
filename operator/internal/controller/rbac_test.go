package controller

import (
	"os"
	"path/filepath"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// The generated ClusterRole is what the cluster enforces; this pins the one rule ADR-0019
// depends on so a marker edit cannot widen it unnoticed.
func TestGeneratedRoleGrantsOnlyCreateOnSecrets(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(raw, &role); err != nil {
		t.Fatal(err)
	}
	var verbs []string
	for _, rule := range role.Rules {
		for _, res := range rule.Resources {
			if res == "secrets" {
				verbs = append(verbs, rule.Verbs...)
			}
		}
	}
	if len(verbs) != 1 || verbs[0] != "create" {
		t.Fatalf("secrets verbs must be exactly [create], got %v", verbs)
	}
}
