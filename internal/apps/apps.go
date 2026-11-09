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
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/progress"
	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
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
}

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
}

func (e *Env) runner() *runner.Runner { return runner.New(e.Out) }

// kubectl runs kubectl against the target's context.
func (e *Env) kubectl(ctx context.Context, args ...string) error {
	return e.runner().Run(ctx, "kubectl", append([]string{"--context", e.KubeContext}, args...)...)
}

// kubectlQuiet runs kubectl without streaming output, returning stdout.
func (e *Env) kubectlQuiet(ctx context.Context, args ...string) (string, error) {
	return runner.New(nil).Capture(ctx, "kubectl", append([]string{"--context", e.KubeContext}, args...)...)
}

// applyURL applies a remote manifest. Server-side apply is used throughout:
// the Gateway API and Argo CD CRDs exceed the client-side
// last-applied-configuration annotation limit.
func (e *Env) applyURL(ctx context.Context, url string) error {
	return e.kubectl(ctx, "apply", "--server-side", "--force-conflicts", "-f", url)
}

// applyYAML applies an in-memory manifest via a private temp file (it may
// contain TLS keys; os.CreateTemp creates it 0600).
func (e *Env) applyYAML(ctx context.Context, manifest string) error {
	f, err := os.CreateTemp("", "k8slocalcli-*.yaml")
	if err != nil {
		return fmt.Errorf("creating manifest file: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(manifest); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing manifest file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing manifest file: %w", err)
	}
	// Quiet runner: the manifest may hold a private key, and kubectl's
	// "configured" lines are noise. Errors still carry kubectl's output tail.
	_, err = runner.New(nil).Capture(ctx, "kubectl", "--context", e.KubeContext,
		"apply", "--server-side", "--force-conflicts", "-f", f.Name())
	return err
}

// applyYAMLRetry retries applyYAML while a just-installed admission webhook
// (cert-manager's) is still coming up and rejecting requests.
func (e *Env) applyYAMLRetry(ctx context.Context, manifest string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := e.applyYAML(ctx, manifest)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// waitAvailable waits for every deployment in ns to be Available.
func (e *Env) waitAvailable(ctx context.Context, ns string, timeout time.Duration) error {
	return e.kubectl(ctx, "wait", "--for=condition=Available", "deployment", "--all",
		"-n", ns, "--timeout="+timeout.String())
}

// helmChart describes a chart to install with `helm upgrade --install`.
type helmChart struct {
	Release   string
	Chart     string // chart name, or a full oci:// reference
	Repo      string // classic repo URL; empty for oci:// charts
	Version   string
	Namespace string
	Set       []string // --set key=value
}

// helmInstall installs or upgrades a chart and waits for it to be ready.
func (e *Env) helmInstall(ctx context.Context, c helmChart) error {
	if !runner.LookPath("helm") {
		return fmt.Errorf("helm is required to install %s via Helm; install it (e.g. brew install helm) or choose the manifest method", c.Release)
	}
	args := []string{"upgrade", "--install", c.Release, c.Chart,
		"--kube-context", e.KubeContext,
		"--namespace", c.Namespace, "--create-namespace",
		"--version", c.Version,
		"--wait", "--timeout", "10m",
	}
	if c.Repo != "" {
		args = append(args, "--repo", c.Repo)
	}
	for _, s := range c.Set {
		args = append(args, "--set", s)
	}
	return e.runner().Run(ctx, "helm", args...)
}

// namespaceYAML is a Namespace manifest document.
func namespaceYAML(name string) string {
	return fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", name)
}

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

// methodsText renders a method list for messages, e.g. "helm, manifest".
func methodsText(ms []cluster.InstallMethod) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = string(m)
	}
	return strings.Join(parts, ", ")
}
