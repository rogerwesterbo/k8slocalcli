// Package provider implements the local Kubernetes cluster backends. Each
// provider (kind, talos) satisfies the Provider interface so the CLI and TUI
// can treat them uniformly.
package provider

import (
	"context"
	"fmt"
	"io"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// Provider creates and manages local Kubernetes clusters for a single backend.
type Provider interface {
	// Name returns the provider identifier (e.g. "kind").
	Name() cluster.Provider

	// CheckPrerequisites verifies the required CLI tools are installed,
	// returning an actionable error when something is missing.
	CheckPrerequisites() error

	// Create provisions a cluster from spec, streaming progress to out.
	Create(ctx context.Context, spec cluster.Spec, out io.Writer) error

	// Delete tears down the named cluster, streaming progress to out.
	Delete(ctx context.Context, name string, out io.Writer) error

	// List returns the names of clusters managed by this provider.
	List(ctx context.Context) ([]string, error)

	// Exists reports whether a cluster with the given name already exists.
	Exists(ctx context.Context, name string) (bool, error)

	// Kubeconfig returns the kubectl context name used for the cluster.
	Context(name string) string

	// KubernetesVersions returns the Kubernetes versions this provider can
	// create, newest first (index 0 is the default/latest). The list may depend
	// on the installed tooling, hence the context.
	KubernetesVersions(ctx context.Context) []string
}

// registry holds the available providers keyed by name.
var registry = map[cluster.Provider]Provider{}

func register(p Provider) {
	registry[p.Name()] = p
}

func init() {
	register(NewKind())
	register(NewTalos())
}

// Get returns the provider for the given name.
func Get(name cluster.Provider) (Provider, error) {
	p, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	return p, nil
}

// All returns every registered provider in the canonical order.
func All() []Provider {
	out := make([]Provider, 0, len(cluster.Providers))
	for _, name := range cluster.Providers {
		if p, ok := registry[name]; ok {
			out = append(out, p)
		}
	}
	return out
}
