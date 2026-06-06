package provider

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
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
	for _, tool := range []string{"kind", "docker"} {
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

// kindConfig renders a kind cluster config. The first control-plane node gets
// host port mappings for ingress; additional nodes are plain.
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
		if i == 0 {
			fmt.Fprintf(&b, "  - role: control-plane\n    image: %s\n", image)
			b.WriteString("    labels:\n")
			b.WriteString("      ingress-ready: \"true\"\n")
			b.WriteString("    extraPortMappings:\n")
			fmt.Fprintf(&b, "      - containerPort: 80\n        hostPort: %d\n        protocol: TCP\n", spec.HTTPPort)
			fmt.Fprintf(&b, "      - containerPort: 443\n        hostPort: %d\n        protocol: TCP\n", spec.HTTPSPort)
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
