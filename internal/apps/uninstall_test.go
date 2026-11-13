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
	patch, err := localPathConfigPatch(localPathTalosDir)
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
	if len(cfg.NodePathMap) != 1 || cfg.NodePathMap[0].Paths[0] != localPathTalosDir {
		t.Errorf("config = %+v", cfg)
	}
}

func TestTrivyManifestPatches(t *testing.T) {
	for _, talos := range []bool{false, true} {
		patches, err := trivyManifestPatches(talos)
		if err != nil {
			t.Fatal(err)
		}
		data := func(configMap string) map[string]string {
			var outer struct {
				Data map[string]string `json:"data"`
			}
			if err := json.Unmarshal([]byte(patches[configMap]), &outer); err != nil {
				t.Fatalf("%s patch is not JSON: %v\n%s", configMap, err, patches[configMap])
			}
			return outer.Data
		}

		var cfg struct {
			Cache struct {
				Backend string `json:"backend"`
			} `json:"cache"`
		}
		if err := json.Unmarshal([]byte(data("trivy-operator-trivy-config")["trivy.configFile"]), &cfg); err != nil || cfg.Cache.Backend != "memory" {
			t.Errorf("talos=%v: trivy.configFile cache = %+v (%v), want memory", talos, cfg, err)
		}

		operator := data("trivy-operator")
		if !strings.Contains(operator["nodeCollector.tolerations"], "node-role.kubernetes.io/control-plane") {
			t.Errorf("talos=%v: tolerations = %q", talos, operator["nodeCollector.tolerations"])
		}
		_, hasVolumes := operator["nodeCollector.volumes"]
		if hasVolumes != talos {
			t.Errorf("talos=%v: nodeCollector.volumes set = %v", talos, hasVolumes)
		}
		if !talos {
			continue
		}
		var volumes []struct {
			Name     string `json:"name"`
			HostPath struct {
				Path string `json:"path"`
			} `json:"hostPath"`
		}
		if err := json.Unmarshal([]byte(operator["nodeCollector.volumes"]), &volumes); err != nil {
			t.Fatalf("nodeCollector.volumes is not JSON: %v", err)
		}
		var mounts []struct {
			Name      string `json:"name"`
			MountPath string `json:"mountPath"`
			ReadOnly  bool   `json:"readOnly"`
		}
		if err := json.Unmarshal([]byte(operator["nodeCollector.volumeMounts"]), &mounts); err != nil {
			t.Fatalf("nodeCollector.volumeMounts is not JSON: %v", err)
		}
		if len(volumes) == 0 || len(volumes) != len(mounts) {
			t.Fatalf("%d volumes, %d mounts", len(volumes), len(mounts))
		}
		for i, v := range volumes {
			if v.HostPath.Path == "/etc/systemd" {
				t.Errorf("volume %q mounts /etc/systemd, which Talos cannot create", v.Name)
			}
			if m := mounts[i]; m.Name != v.Name || m.MountPath != v.HostPath.Path || !m.ReadOnly {
				t.Errorf("mount %+v does not match volume %+v", m, v)
			}
		}
	}
}
