package apps

import (
	"encoding/json"
	"strings"
	"testing"
)

// Dependants go first; dependencies are not added, only reordered.
func TestRemovalOrder(t *testing.T) {
	got, err := RemovalOrder([]string{"cnpg", "nyancat", "keycloak"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	pos := map[string]int{}
	for i, a := range got {
		names = append(names, a.Name)
		pos[a.Name] = i
	}
	if len(got) != 3 || pos["keycloak"] > pos["cnpg"] {
		t.Errorf("order = %v, want keycloak before cnpg and nothing added", names)
	}
	if _, err := RemovalOrder([]string{"nope"}); err == nil {
		t.Error("unknown app should fail")
	}
}

// Manifest removal never deletes CRDs or Namespaces.
func TestWithoutKinds(t *testing.T) {
	manifest := "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: a\n---\n" +
		"apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: x.y\n---\n" +
		"# comment only\n---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d\n"
	got, err := withoutKinds(manifest, "CustomResourceDefinition", "Namespace")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "Namespace") || strings.Contains(got, "CustomResourceDefinition") || !strings.Contains(got, "kind: Deployment") {
		t.Errorf("got:\n%s", got)
	}
}

// The manifest-method patch must leave config.json valid JSON naming the dir.
func TestLocalPathConfigPatch(t *testing.T) {
	patch, err := localPathConfigPatch("/var/local-path-provisioner")
	if err != nil {
		t.Fatal(err)
	}
	var outer struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(patch), &outer); err != nil {
		t.Fatalf("patch is not JSON: %v\n%s", err, patch)
	}
	var cfg struct {
		NodePathMap []struct {
			Paths []string `json:"paths"`
		} `json:"nodePathMap"`
	}
	if err := json.Unmarshal([]byte(outer.Data["config.json"]), &cfg); err != nil {
		t.Fatalf("config.json is not JSON: %v", err)
	}
	if len(cfg.NodePathMap) != 1 || cfg.NodePathMap[0].Paths[0] != "/var/local-path-provisioner" {
		t.Errorf("config = %+v", cfg)
	}
}
