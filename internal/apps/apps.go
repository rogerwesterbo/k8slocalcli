// Package apps installs things into a running cluster: the host-exposure stack
// (Gateway API CRDs, a gateway controller, TLS, and the shared Gateway) and the
// catalog applications (Argo CD, ...) that are routed through it.
//
// It sits after provider.Create in the flow and talks to the cluster only
// through kubectl and helm via the shared runner, so it works the same for
// every provider. Nothing here imports the provider package; callers describe
// the cluster with a Target.
package apps

import (
	"fmt"
	"io"
	"strings"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/progress"
)

// Target describes the cluster being installed into.
type Target struct {
	// Name is the cluster name, used in messages only.
	Name string
	// KubeContext is the kubectl/helm context of the cluster.
	KubeContext string
	// Exposed reports whether the cluster publishes the gateway host ports.
	Exposed bool
	// GatewayNode is the Kubernetes node that publishes the gateway host
	// ports (the first control plane). The gateway data plane must run there.
	GatewayNode string
	// HTTPPort and HTTPSPort are the host ports published to the gateway.
	HTTPPort  int
	HTTPSPort int
	// Provider is the k8slocalcli provider that created the cluster (kind,
	// talos), or "" for a cluster reached through an arbitrary kube context.
	Provider cluster.Provider
	// LoadBalancerRange is a free IPv4 range on the nodes' container network
	// (e.g. 172.18.255.200-172.18.255.250) for MetalLB, or "" when unknown.
	LoadBalancerRange string
	// CABundle is extra PEM CA certificates (a TLS-intercepting proxy's root)
	// that the nodes trust; components that fetch charts themselves are given
	// it too (see trustCABundle). Empty when there are none.
	CABundle string
}

// External reports whether the target is a cluster k8slocalcli did not
// create, reached only through its kube context.
func (t Target) External() bool { return t.Provider == "" }

// Env carries everything an installer needs.
type Env struct {
	Target
	Out io.Writer
	// Confirm asks the user a yes/no question. It returns an error when no
	// answer can be obtained (no terminal and no --yes); installers then skip
	// the optional step and print what to do instead.
	Confirm func(question string, def bool) (bool, error)
	// Progress renders long steps (spinner + live pod status on a terminal).
	// Nil means plain, streamed output.
	Progress *progress.Reporter
	// Profile sizes the apps (dev or near-production). Empty means dev.
	Profile cluster.Profile
	// Reinstall re-applies apps that are already installed instead of asking.
	Reinstall bool
	// Values are user chart values per app (canonical name), deep-merged over
	// k8slocalcli's own values for the helm, flux and argo methods. See
	// ParseValuesFlags.
	Values map[string]map[string]any

	// connected caches a successful connection check for this Env.
	connected bool
}

// production reports whether apps should be installed near-production.
func (e *Env) production() bool { return e.Profile.Production() }

// URL returns the host URL for hostname over scheme on this target, leaving
// the port out when it is the scheme's default.
func (t Target) URL(scheme, hostname string) string {
	port := t.HTTPPort
	def := 80
	if scheme == "https" {
		port, def = t.HTTPSPort, 443
	}
	if port == def || port == 0 {
		return scheme + "://" + hostname
	}
	return fmt.Sprintf("%s://%s:%d", scheme, hostname, port)
}

// MethodsText renders a method list for messages, e.g. "helm, manifest".
func MethodsText(ms []cluster.InstallMethod) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = string(m)
	}
	return strings.Join(parts, ", ")
}
