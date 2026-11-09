package cluster

import "testing"

func TestSpecValidate(t *testing.T) {
	base := func() Spec {
		s := DefaultSpec()
		s.Name = "mycluster"
		return s
	}

	tests := []struct {
		name    string
		mutate  func(*Spec)
		wantErr bool
	}{
		{"valid default", func(*Spec) {}, false},
		{"valid with dashes and digits", func(s *Spec) { s.Name = "dev-1-cluster" }, false},
		{"empty name", func(s *Spec) { s.Name = "" }, true},
		{"uppercase name", func(s *Spec) { s.Name = "MyCluster" }, true},
		{"leading dash", func(s *Spec) { s.Name = "-bad" }, true},
		{"trailing dash", func(s *Spec) { s.Name = "bad-" }, true},
		{"space in name", func(s *Spec) { s.Name = "bad name" }, true},
		{"unknown provider", func(s *Spec) { s.Provider = Provider("k3d") }, true},
		{"talos provider", func(s *Spec) { s.Provider = ProviderTalos }, false},
		{"unknown cni", func(s *Spec) { s.CNI = CNI("weave") }, true},
		{"empty cni ok", func(s *Spec) { s.CNI = "" }, false},
		{"cilium cni", func(s *Spec) { s.CNI = CNICilium }, false},
		{"kube-ovn cni", func(s *Spec) { s.CNI = CNIKubeOVN }, false},
		{"zero control planes", func(s *Spec) { s.ControlPlanes = 0 }, true},
		{"negative workers", func(s *Spec) { s.Workers = -1 }, true},
		{"zero workers ok", func(s *Spec) { s.Workers = 0 }, false},
		{"bad http port", func(s *Spec) { s.HTTPPort = 0 }, true},
		{"bad https port", func(s *Spec) { s.HTTPSPort = 70000 }, true},
		{"name too long", func(s *Spec) { s.Name = "a" + string(make([]byte, 63)) }, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := base()
			tc.mutate(&s)
			err := s.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestProviderValid(t *testing.T) {
	if !ProviderKind.Valid() || !ProviderTalos.Valid() {
		t.Fatal("expected kind and talos to be valid")
	}
	if Provider("nope").Valid() {
		t.Fatal("expected unknown provider to be invalid")
	}
}

func TestCNIValid(t *testing.T) {
	for _, c := range CNIs {
		if !c.Valid() {
			t.Errorf("expected %q to be a valid CNI", c)
		}
	}
	if CNI("weave").Valid() {
		t.Error("expected unknown CNI to be invalid")
	}
	if !CNICilium.Custom() || !CNICalico.Custom() || !CNIKubeOVN.Custom() {
		t.Error("expected cilium, calico and kube-ovn to be custom CNIs")
	}
	if CNIDefault.Custom() || CNI("").Custom() {
		t.Error("expected default/empty CNI to not be custom")
	}
}

func TestDefaultSpec(t *testing.T) {
	s := DefaultSpec()
	if s.Provider != ProviderKind {
		t.Errorf("default provider = %q, want kind", s.Provider)
	}
	if s.ControlPlanes != 1 {
		t.Errorf("default control planes = %d, want 1", s.ControlPlanes)
	}
	if s.Workers != 0 {
		t.Errorf("default workers = %d, want 0", s.Workers)
	}
	if s.CNI != CNIDefault {
		t.Errorf("default CNI = %q, want default", s.CNI)
	}
}

func TestExposureValidation(t *testing.T) {
	base := func() Spec {
		s := DefaultSpec()
		s.Name = "c"
		return s
	}
	tests := []struct {
		name    string
		mutate  func(*Spec)
		wantErr bool
	}{
		{"defaults", func(*Spec) {}, false},
		{"experimental channel", func(s *Spec) { s.Exposure.Channel = ChannelExperimental }, false},
		{"unknown channel", func(s *Spec) { s.Exposure.Channel = "beta" }, true},
		{"unknown controller", func(s *Spec) { s.Exposure.Controller = "traefik" }, true},
		{"unknown tls", func(s *Spec) { s.Exposure.TLS = "acme" }, true},
		{"verified needs valid cert-manager method", func(s *Spec) {
			s.Exposure.TLS = TLSVerified
			s.Exposure.CertManagerMethod = "kustomize"
		}, true},
		{"same http and https port", func(s *Spec) { s.HTTPSPort = s.HTTPPort }, true},
		// With exposure off nothing is published, so ports and gateway
		// settings are irrelevant.
		{"exposure off ignores ports", func(s *Spec) {
			s.Exposure = Exposure{}
			s.HTTPPort = 0
		}, false},
		{"bad app host", func(s *Spec) { s.Apps = []AppRequest{{Name: "argocd", Host: "Not Valid"}} }, true},
		{"duplicate app", func(s *Spec) { s.Apps = []AppRequest{{Name: "argocd"}, {Name: "argocd"}} }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := base()
			tc.mutate(&s)
			if err := s.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestWarnings(t *testing.T) {
	s := DefaultSpec()
	if w := s.Warnings(); len(w) != 0 {
		t.Errorf("defaults should not warn: %v", w)
	}
	s.Exposure.Enabled = false
	s.Apps = []AppRequest{{Name: "argocd", Host: "argocd.localtest.me"}}
	if w := s.Warnings(); len(w) != 2 {
		t.Errorf("expose=no with a hostname should give 2 warnings, got %v", w)
	}
	s = DefaultSpec()
	s.Apps = []AppRequest{{Name: "argocd", Host: "argocd.example.com"}}
	if w := s.Warnings(); len(w) != 1 {
		t.Errorf("a host outside localtest.me should warn, got %v", w)
	}
}

func TestIsLocalDomain(t *testing.T) {
	for host, want := range map[string]bool{
		"argocd.localtest.me":     true,
		"a.b.localtest.me":        false,
		"localtest.me":            false,
		"argocd.example.com":      false,
		"evil-localtest.me":       false,
		"argocd.localtest.me.com": false,
	} {
		if got := IsLocalDomain(host); got != want {
			t.Errorf("IsLocalDomain(%q) = %v, want %v", host, got, want)
		}
	}
}
