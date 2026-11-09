package provider

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/engine"
	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// Kind implements Provider using kind (Kubernetes in Docker).
type Kind struct{}

// NewKind returns a kind provider.
func NewKind() *Kind { return &Kind{} }

// Name implements Provider.
func (k *Kind) Name() cluster.Provider { return cluster.ProviderKind }

// Context implements Provider. kind prefixes contexts with "kind-".
func (k *Kind) Context(name string) string { return "kind-" + name }

// GatewayNode implements Provider: kind names the first control plane
// "<name>-control-plane".
func (k *Kind) GatewayNode(name string) string { return name + "-control-plane" }

// KubernetesVersions implements Provider, returning the pinned kind node-image
// versions newest first.
func (k *Kind) KubernetesVersions(_ context.Context) []string {
	out := make([]string, 0, len(kindNodeImages))
	for _, e := range kindNodeImages {
		out = append(out, e.Version)
	}
	return out
}

// CheckPrerequisites implements Provider.
func (k *Kind) CheckPrerequisites() error {
	var missing []string
	for _, tool := range []string{"kind", engine.Current().Binary} {
		if !runner.LookPath(tool) {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing prerequisites for kind provider: %s\ninstall kind: https://kind.sigs.k8s.io/docs/user/quick-start/", strings.Join(missing, ", "))
	}
	return nil
}

// Create implements Provider.
func (k *Kind) Create(ctx context.Context, spec cluster.Spec, out io.Writer) error {
	r := runner.New(out)

	image, version, known := kindImageFor(spec.K8sVersion)
	if !known && spec.K8sVersion != "" {
		fmt.Fprintf(out, "⚠️  Kubernetes version %q is not pinned; falling back to %s\n", spec.K8sVersion, version)
	}

	bundle, sources, err := caBundle(spec.CACerts)
	if err != nil {
		return err
	}

	configYAML := kindConfig(spec, image)

	cfgFile, err := os.CreateTemp("", "kind-config-*.yaml")
	if err != nil {
		return fmt.Errorf("creating kind config: %w", err)
	}
	defer func() { _ = os.Remove(cfgFile.Name()) }()
	if _, err := cfgFile.WriteString(configYAML); err != nil {
		_ = cfgFile.Close()
		return fmt.Errorf("writing kind config: %w", err)
	}
	if err := cfgFile.Close(); err != nil {
		return fmt.Errorf("closing kind config: %w", err)
	}

	fmt.Fprintf(out, "\n⏰ Creating kind cluster %q (%s, CNI %s, %d control plane(s), %d worker(s))\n",
		spec.Name, version, cniLabel(spec.CNI), spec.ControlPlanes, spec.Workers)

	// Multiple control planes are heavy locally: kind adds a load balancer and
	// each one is an etcd member that must reach quorum during kubeadm join.
	if spec.ControlPlanes%2 == 0 {
		fmt.Fprintf(out, "ℹ️  An odd number of control planes (1, 3, 5) is recommended for etcd quorum.\n")
	}
	if spec.ControlPlanes > 3 {
		fmt.Fprintf(out, "⚠️  %d control planes is heavy for a single Docker host; kind may time out joining them all. 1 or 3 is recommended.\n", spec.ControlPlanes)
	}

	if err := r.Run(ctx, "kind", "create", "cluster", "--name", spec.Name, "--config", cfgFile.Name()); err != nil {
		return fmt.Errorf("could not create kind cluster: %w", err)
	}

	// Before anything else pulls images (CNI, apps): behind a TLS-intercepting
	// proxy every pull from an intercepted registry fails with x509 otherwise.
	if bundle != "" {
		fmt.Fprintf(out, "\n🔐 Trusting extra CA certificates from: %s\n", strings.Join(sources, ", "))
		if err := kindTrustCAs(ctx, spec.Name, bundle, out); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "\n🔄 Switching kubectl context to %s\n", k.Context(spec.Name))
	if err := r.Run(ctx, "kubectl", "config", "use-context", k.Context(spec.Name)); err != nil {
		return fmt.Errorf("could not switch kubectl context: %w", err)
	}

	// With a custom CNI the default was disabled, so nodes stay NotReady until
	// we install the chosen CNI here.
	if spec.CNI.Custom() {
		if err := installCNI(ctx, k.Name(), spec.CNI, k.Context(spec.Name), out); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "\n⏰ Waiting for nodes to become Ready\n")
	if err := r.Run(ctx, "kubectl", "wait", "--for=condition=Ready", "nodes", "--all", "--timeout=180s"); err != nil {
		return fmt.Errorf("cluster nodes not ready in time: %w", err)
	}

	if spec.Workers == 0 {
		makeControlPlaneSchedulable(ctx, k.Context(spec.Name), out)
	}

	fmt.Fprintf(out, "\n✅ kind cluster %q is ready\n", spec.Name)
	return nil
}

// Delete implements Provider.
func (k *Kind) Delete(ctx context.Context, name string, out io.Writer) error {
	r := runner.New(out)
	fmt.Fprintf(out, "\n⏰ Deleting kind cluster %q\n", name)
	if err := r.Run(ctx, "kind", "delete", "cluster", "--name", name); err != nil {
		return fmt.Errorf("could not delete kind cluster %q: %w", name, err)
	}
	fmt.Fprintf(out, "\n✅ kind cluster %q deleted\n", name)
	return nil
}

// Kubeconfig implements Provider. kind already records the host-mapped API
// server address, so its output is usable as is.
func (k *Kind) Kubeconfig(ctx context.Context, name string) ([]byte, error) {
	cfg, err := runner.New(nil).Capture(ctx, "kind", "get", "kubeconfig", "--name", name)
	if err != nil {
		return nil, fmt.Errorf("could not get kubeconfig for kind cluster %q: %w", name, err)
	}
	return []byte(cfg + "\n"), nil
}

// MergeKubeconfig implements Provider. `kind export kubeconfig` merges and
// switches the current context in one step.
func (k *Kind) MergeKubeconfig(ctx context.Context, name string, out io.Writer) error {
	if err := runner.New(out).Run(ctx, "kind", "export", "kubeconfig", "--name", name); err != nil {
		return fmt.Errorf("could not export kubeconfig for kind cluster %q: %w", name, err)
	}
	return nil
}

// List implements Provider.
func (k *Kind) List(ctx context.Context) ([]string, error) {
	r := runner.New(nil)
	out, err := r.Capture(ctx, "kind", "get", "clusters")
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}

// Exists implements Provider.
func (k *Kind) Exists(ctx context.Context, name string) (bool, error) {
	clusters, err := k.List(ctx)
	if err != nil {
		return false, err
	}
	for _, c := range clusters {
		if c == name {
			return true, nil
		}
	}
	return false, nil
}

// kindCAPath is where the extra CA bundle goes on each kind node; the node
// image is Debian, so update-ca-certificates folds it into the system roots.
const kindCAPath = "/usr/local/share/ca-certificates/k8slocalcli.crt"

// kindTrustCAs adds bundle to every node's system trust store and restarts
// containerd, whose Go TLS client only reads the system roots at startup.
// Running containers survive the restart (they live in containerd-shim).
// Kubernetes' own images are preloaded in the node image, so the cluster comes
// up without this; it is image pulls afterwards that need it.
func kindTrustCAs(ctx context.Context, name, bundle string, out io.Writer) error {
	nodesOut, err := runner.New(nil).Capture(ctx, "kind", "get", "nodes", "--name", name)
	if err != nil {
		return fmt.Errorf("listing kind nodes: %w", err)
	}

	f, err := os.CreateTemp("", "k8slocalcli-ca-*.crt")
	if err != nil {
		return fmt.Errorf("writing CA bundle: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(bundle); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing CA bundle: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing CA bundle: %w", err)
	}

	eng := engine.Current().Binary
	r := runner.New(out)
	for _, node := range nonEmptyLines(nodesOut) {
		for _, args := range [][]string{
			{"cp", f.Name(), node + ":" + kindCAPath},
			{"exec", node, "update-ca-certificates"},
			{"exec", node, "systemctl", "restart", "containerd"},
		} {
			if err := r.Run(ctx, eng, args...); err != nil {
				return fmt.Errorf("trusting CA certificates on %s: %w", node, err)
			}
		}
	}
	return nil
}

// kindConfig renders a kind cluster config. With host exposure on, the first
// control-plane node publishes the gateway host ports to the fixed gateway
// NodePorts; additional nodes are plain. Port mappings cannot be added to a kind
// node after creation, which is why exposure is decided up front.
func kindConfig(spec cluster.Spec, image string) string {
	var b strings.Builder
	b.WriteString("kind: Cluster\n")
	b.WriteString("apiVersion: kind.x-k8s.io/v1alpha4\n")
	b.WriteString("networking:\n")
	b.WriteString("  ipFamily: dual\n")
	if spec.CNI.Custom() {
		// Disable kindnet so the chosen CNI can be installed afterwards.
		b.WriteString("  disableDefaultCNI: true\n")
	}
	b.WriteString("nodes:\n")

	for i := 0; i < spec.ControlPlanes; i++ {
		if i == 0 && spec.Exposure.Enabled {
			fmt.Fprintf(&b, "  - role: control-plane\n    image: %s\n", image)
			b.WriteString("    labels:\n")
			b.WriteString("      ingress-ready: \"true\"\n")
			b.WriteString("    extraPortMappings:\n")
			fmt.Fprintf(&b, "      - containerPort: %d\n        hostPort: %d\n        protocol: TCP\n", cluster.GatewayHTTPNodePort, spec.HTTPPort)
			fmt.Fprintf(&b, "      - containerPort: %d\n        hostPort: %d\n        protocol: TCP\n", cluster.GatewayHTTPSNodePort, spec.HTTPSPort)
		} else {
			fmt.Fprintf(&b, "  - role: control-plane\n    image: %s\n", image)
		}
	}

	for i := 0; i < spec.Workers; i++ {
		fmt.Fprintf(&b, "  - role: worker\n    image: %s\n", image)
	}

	return b.String()
}

// nonEmptyLines splits s on newlines and drops blank lines.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
