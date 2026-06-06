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
}
