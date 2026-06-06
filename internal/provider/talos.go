package provider

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// Talos implements Provider using talosctl with the Docker backend, which runs
// Talos Linux nodes as Docker containers.
type Talos struct{}

// NewTalos returns a talos provider.
func NewTalos() *Talos { return &Talos{} }

// Name implements Provider.
func (t *Talos) Name() cluster.Provider { return cluster.ProviderTalos }

// Context implements Provider. talosctl names the admin context "admin@<name>".
func (t *Talos) Context(name string) string { return "admin@" + name }

// CheckPrerequisites implements Provider.
func (t *Talos) CheckPrerequisites() error {
	var missing []string
	for _, tool := range []string{"talosctl", "docker"} {
		if !runner.LookPath(tool) {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing prerequisites for talos provider: %s\ninstall talosctl: brew install siderolabs/tap/talosctl", strings.Join(missing, ", "))
	}
	return nil
}

// stateDir returns the directory talosctl uses to store cluster state.
func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".k8slocalcli", "clusters")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	return dir, nil
}

// Create implements Provider.
func (t *Talos) Create(ctx context.Context, spec cluster.Spec, out io.Writer) error {
	r := runner.New(out)

	state, err := stateDir()
	if err != nil {
		return fmt.Errorf("preparing talos state directory: %w", err)
	}

	version := spec.K8sVersion
	if version == "" {
		mm := t.majorMinor(ctx)
		version = talosK8sVersionsFor(mm)[0]
	}
	version = strings.TrimPrefix(version, "v")

	// The Docker backend always provisions a single control plane.
	if spec.ControlPlanes > 1 {
		fmt.Fprintf(out, "⚠️  talosctl Docker backend supports only one control plane; creating 1 (requested %d)\n", spec.ControlPlanes)
		fmt.Fprintf(out, "   💡 Use the QEMU backend for multiple control planes.\n")
	}

	fmt.Fprintf(out, "\n⏰ Creating Talos cluster %q (k8s %s, 1 control plane, %d worker(s))\n",
		spec.Name, version, spec.Workers)

	args := []string{
		"cluster", "create", "docker",
		"--name", spec.Name,
		"--state", state,
		"--workers", fmt.Sprintf("%d", spec.Workers),
		"--kubernetes-version", version,
		"--memory-controlplanes", "2048MB",
		"--memory-workers", "2048MB",
		"--exposed-ports", fmt.Sprintf("%d:80/tcp,%d:443/tcp", spec.HTTPPort, spec.HTTPSPort),
	}
	if err := r.Run(ctx, "talosctl", args...); err != nil {
		return fmt.Errorf("could not create talos cluster: %w", err)
	}

	// Merge the kubeconfig, then rewrite its server URL. talosctl records the
	// control-plane's internal Docker IP (e.g. https://10.5.0.2:6443), which is
	// not routable from the host on macOS/Windows. The Docker backend forwards
	// the API server (6443) to a localhost port, so we point kubeconfig there.
	fmt.Fprintf(out, "\n⏰ Merging kubeconfig\n")
	if err := r.Run(ctx, "talosctl", "kubeconfig", "--cluster", spec.Name, "--nodes", "127.0.0.1", "--force"); err != nil {
		fmt.Fprintf(out, "⚠️  Could not merge kubeconfig automatically: %v\n", err)
	} else if err := fixKubeconfigServer(ctx, r, spec.Name, out); err != nil {
		fmt.Fprintf(out, "⚠️  Could not point kubeconfig at the host port (kubectl may time out): %v\n", err)
	}

	fmt.Fprintf(out, "\n✅ Talos cluster %q is ready\n", spec.Name)
	return nil
}

// fixKubeconfigServer rewrites the cluster's API server URL in the active
// kubeconfig to the host-mapped port. talosctl records the control-plane's
// internal Docker IP, which the host cannot reach; the Docker backend forwards
// the API server (6443) to a localhost port instead.
func fixKubeconfigServer(ctx context.Context, r *runner.Runner, name string, out io.Writer) error {
	port, err := dockerHostPort(ctx, name+"-controlplane-1", "6443")
	if err != nil {
		return err
	}
	server := fmt.Sprintf("https://127.0.0.1:%s", port)
	fmt.Fprintf(out, "🔧 Pointing kubeconfig cluster %q at %s\n", name, server)
	return r.Run(ctx, "kubectl", "config", "set-cluster", name, "--server="+server)
}

// dockerHostPort returns the host port mapped to containerPort/tcp on the named
// container (e.g. "0.0.0.0:64659" -> "64659").
func dockerHostPort(ctx context.Context, container, containerPort string) (string, error) {
	r := runner.New(nil)
	out, err := r.Capture(ctx, "docker", "port", container, containerPort+"/tcp")
	if err != nil {
		return "", err
	}
	port := parseHostPort(out)
	if port == "" {
		return "", fmt.Errorf("no host port mapped to %s/tcp on %s", containerPort, container)
	}
	return port, nil
}

var hostPortRe = regexp.MustCompile(`:(\d+)\s*$`)

// parseHostPort extracts the port from `docker port` output, handling both the
// "0.0.0.0:64659" and "[::]:64659" forms (taking the first mapping).
func parseHostPort(dockerPortOutput string) string {
	for _, line := range strings.Split(dockerPortOutput, "\n") {
		line = strings.TrimSpace(line)
		if m := hostPortRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// Delete implements Provider.
func (t *Talos) Delete(ctx context.Context, name string, out io.Writer) error {
	r := runner.New(out)

	state, err := stateDir()
	if err != nil {
		return fmt.Errorf("locating talos state directory: %w", err)
	}

	fmt.Fprintf(out, "\n⏰ Deleting Talos cluster %q\n", name)
	if err := r.Run(ctx, "talosctl", "cluster", "destroy", "--name", name, "--state", state); err != nil {
		fmt.Fprintf(out, "⚠️  talosctl destroy failed, attempting manual cleanup: %v\n", err)
	}

	// Best-effort cleanup of any leftover containers/network for this cluster.
	if names, derr := dockerNames(ctx, name+"-"); derr == nil {
		for _, c := range names {
			_ = r.Run(ctx, "docker", "rm", "-f", c)
		}
	}
	_ = r.Run(ctx, "docker", "network", "rm", name)

	// Remove the cluster state directory.
	_ = os.RemoveAll(filepath.Join(state, name))

	fmt.Fprintf(out, "\n✅ Talos cluster %q deleted\n", name)
	return nil
}

// List implements Provider. Talos Docker clusters are discovered by their
// "<name>-controlplane-1" container.
func (t *Talos) List(ctx context.Context) ([]string, error) {
	if !runner.LookPath("docker") {
		return nil, nil
	}
	names, err := dockerNames(ctx, "controlplane-1")
	if err != nil {
		return nil, err
	}
	var clusters []string
	for _, n := range names {
		if strings.HasSuffix(n, "-controlplane-1") {
			clusters = append(clusters, strings.TrimSuffix(n, "-controlplane-1"))
		}
	}
	return clusters, nil
}

// Exists implements Provider.
func (t *Talos) Exists(ctx context.Context, name string) (bool, error) {
	clusters, err := t.List(ctx)
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

// majorMinor returns the installed talosctl version as "major.minor" (e.g.
// "1.13"), or "" if it cannot be determined.
func (t *Talos) majorMinor(ctx context.Context) string {
	r := runner.New(nil)
	out, err := r.Capture(ctx, "talosctl", "version", "--client")
	if err != nil {
		return ""
	}
	return parseTalosMajorMinor(out)
}

var talosTagRe = regexp.MustCompile(`Tag:\s*v?(\d+)\.(\d+)`)

func parseTalosMajorMinor(versionOutput string) string {
	m := talosTagRe.FindStringSubmatch(versionOutput)
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2]
}

// dockerNames returns the names of docker containers whose name matches the
// given substring filter.
func dockerNames(ctx context.Context, nameFilter string) ([]string, error) {
	r := runner.New(nil)
	out, err := r.Capture(ctx, "docker", "ps", "-a", "--filter", "name="+nameFilter, "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}
