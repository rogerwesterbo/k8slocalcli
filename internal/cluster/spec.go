// Package cluster defines the provider-agnostic description of a local
// Kubernetes cluster and the validation rules that apply to it.
package cluster

import (
	"fmt"
	"regexp"
)

// Provider identifies which backend creates and manages a cluster.
type Provider string

const (
	// ProviderKind runs Kubernetes nodes as Docker containers via kind.
	ProviderKind Provider = "kind"
	// ProviderTalos runs Talos Linux nodes as Docker containers via talosctl.
	ProviderTalos Provider = "talos"
)

// Providers is the ordered list of supported providers, used by the TUI and
// CLI flag validation.
var Providers = []Provider{ProviderKind, ProviderTalos}

// Valid reports whether p is a known provider.
func (p Provider) Valid() bool {
	for _, known := range Providers {
		if p == known {
			return true
		}
	}
	return false
}

// Default values applied when the user does not override them.
const (
	DefaultControlPlanes = 1
	DefaultWorkers       = 0
	DefaultHTTPPort      = 80
	DefaultHTTPSPort     = 443
)

// namePattern matches a valid cluster name: lowercase alphanumerics and dashes,
// starting and ending with an alphanumeric. This is the intersection of what
// kind and talos (and Docker container names) accept.
var namePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Spec is a provider-agnostic description of a cluster to create.
type Spec struct {
	Name          string
	Provider      Provider
	ControlPlanes int
	Workers       int

	// K8sVersion is the desired Kubernetes version. When empty the provider
	// picks its own sensible default.
	K8sVersion string

	// HTTPPort and HTTPSPort are the host ports mapped to the ingress
	// controller on the first control-plane node.
	HTTPPort  int
	HTTPSPort int
}

// DefaultSpec returns a Spec pre-populated with sensible defaults.
func DefaultSpec() Spec {
	return Spec{
		Provider:      ProviderKind,
		ControlPlanes: DefaultControlPlanes,
		Workers:       DefaultWorkers,
		HTTPPort:      DefaultHTTPPort,
		HTTPSPort:     DefaultHTTPSPort,
	}
}

// Validate checks that the Spec describes a creatable cluster, returning a
// human-readable error describing the first problem found.
func (s Spec) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("cluster name cannot be empty")
	}
	if len(s.Name) > 63 {
		return fmt.Errorf("cluster name %q is too long (max 63 characters)", s.Name)
	}
	if !namePattern.MatchString(s.Name) {
		return fmt.Errorf("cluster name %q is invalid: use lowercase letters, digits and dashes, starting and ending with a letter or digit", s.Name)
	}
	if !s.Provider.Valid() {
		return fmt.Errorf("unknown provider %q (supported: kind, talos)", s.Provider)
	}
	if s.ControlPlanes < 1 {
		return fmt.Errorf("at least one control plane is required")
	}
	if s.Workers < 0 {
		return fmt.Errorf("worker count cannot be negative")
	}
	if s.HTTPPort < 1 || s.HTTPPort > 65535 {
		return fmt.Errorf("http port %d is out of range", s.HTTPPort)
	}
	if s.HTTPSPort < 1 || s.HTTPSPort > 65535 {
		return fmt.Errorf("https port %d is out of range", s.HTTPSPort)
	}
	return nil
}
