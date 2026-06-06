package provider

import (
	"strings"
	"testing"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

func TestRegistry(t *testing.T) {
	for _, name := range cluster.Providers {
		p, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) returned error: %v", name, err)
		}
		if p.Name() != name {
			t.Errorf("provider name = %q, want %q", p.Name(), name)
		}
	}
	if _, err := Get(cluster.Provider("nope")); err == nil {
		t.Error("expected error for unknown provider")
	}
	if got := len(All()); got != len(cluster.Providers) {
		t.Errorf("All() returned %d providers, want %d", got, len(cluster.Providers))
	}
}

func TestContextNames(t *testing.T) {
	if got := NewKind().Context("foo"); got != "kind-foo" {
		t.Errorf("kind context = %q, want kind-foo", got)
	}
	if got := NewTalos().Context("foo"); got != "admin@foo" {
		t.Errorf("talos context = %q, want admin@foo", got)
	}
}

func TestKindImageFor(t *testing.T) {
	// default
	img, ver, ok := kindImageFor("")
	if !ok || !strings.HasPrefix(img, "kindest/node:") || ver == "" {
		t.Fatalf("default image lookup failed: img=%q ver=%q ok=%v", img, ver, ok)
	}
	// known with and without leading v
	if _, _, ok := kindImageFor("1.33.12"); !ok {
		t.Error("expected 1.33.12 to be known")
	}
	if _, _, ok := kindImageFor("v1.33.12"); !ok {
		t.Error("expected v1.33.12 to be known")
	}
	// unknown falls back but reports not ok
	if _, _, ok := kindImageFor("v9.99.99"); ok {
		t.Error("expected unknown version to report ok=false")
	}
}

func TestKindConfig(t *testing.T) {
	spec := cluster.Spec{
		Name:          "demo",
		Provider:      cluster.ProviderKind,
		ControlPlanes: 2,
		Workers:       3,
		HTTPPort:      8080,
		HTTPSPort:     8443,
	}
	cfg := kindConfig(spec, "kindest/node:test")

	if strings.Count(cfg, "role: control-plane") != 2 {
		t.Errorf("expected 2 control-plane entries:\n%s", cfg)
	}
	if strings.Count(cfg, "role: worker") != 3 {
		t.Errorf("expected 3 worker entries:\n%s", cfg)
	}
	// Only the first control plane gets the host port mappings.
	if strings.Count(cfg, "hostPort: 8080") != 1 || strings.Count(cfg, "hostPort: 8443") != 1 {
		t.Errorf("expected exactly one of each host port mapping:\n%s", cfg)
	}
	if !strings.Contains(cfg, "ingress-ready: \"true\"") {
		t.Errorf("expected ingress-ready label:\n%s", cfg)
	}
}

func TestKindConfigNoWorkers(t *testing.T) {
	spec := cluster.Spec{Name: "x", ControlPlanes: 1, Workers: 0, HTTPPort: 80, HTTPSPort: 443}
	cfg := kindConfig(spec, "img")
	if strings.Contains(cfg, "role: worker") {
		t.Errorf("expected no worker entries:\n%s", cfg)
	}
}

func TestParseTalosMajorMinor(t *testing.T) {
	out := "Client:\n\tTag:         v1.13.3\n\tSHA:         undefined\n"
	if got := parseTalosMajorMinor(out); got != "1.13" {
		t.Errorf("parseTalosMajorMinor = %q, want 1.13", got)
	}
	if got := parseTalosMajorMinor("no version here"); got != "" {
		t.Errorf("expected empty result, got %q", got)
	}
}

func TestParseHostPort(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:64659":             "64659",
		"[::]:64659":                "64659",
		"0.0.0.0:64659\n[::]:64659": "64659",
		"":                          "",
		"no port here":              "",
	}
	for in, want := range cases {
		if got := parseHostPort(in); got != want {
			t.Errorf("parseHostPort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTalosK8sVersionsFor(t *testing.T) {
	if v := talosK8sVersionsFor("1.13"); len(v) == 0 || v[0] != "1.36.1" {
		t.Errorf("talos 1.13 versions unexpected: %v", v)
	}
	// Unknown version falls back to the latest set.
	if v := talosK8sVersionsFor("99.99"); len(v) == 0 {
		t.Error("expected fallback versions for unknown talos version")
	}
}
