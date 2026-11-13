// kube.go holds the small building blocks every installer uses to talk to
// the cluster: kubectl and apply wrappers, existence checks, Secrets,
// namespaces and cluster facts (default StorageClass, Talos).

package apps

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

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
	f, err := writeTemp("k8slocalcli-*.yaml", manifest)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f) }()
	// Quiet runner: the manifest may hold a private key, and kubectl's
	// "configured" lines are noise. Errors still carry kubectl's output tail.
	_, err = runner.New(nil).Capture(ctx, "kubectl", "--context", e.KubeContext,
		"apply", "--server-side", "--force-conflicts", "-f", f)
	return err
}

// applyYAMLRetry retries applyYAML while a just-installed admission webhook
// (cert-manager's) is still coming up and rejecting requests.
func (e *Env) applyYAMLRetry(ctx context.Context, manifest string, timeout time.Duration) error {
	return retry(ctx, timeout, 5*time.Second, func(ctx context.Context) error { return e.applyYAML(ctx, manifest) })
}

// applyYAMLDryRun validates manifest against the API server (admission
// webhooks included) without persisting it.
func (e *Env) applyYAMLDryRun(ctx context.Context, manifest string) error {
	f, err := writeTemp("k8slocalcli-*.yaml", manifest)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f) }()
	_, err = runner.New(nil).Capture(ctx, "kubectl", "--context", e.KubeContext,
		"apply", "--server-side", "--dry-run=server", "-f", f)
	return err
}

// retry runs fn until it succeeds, ctx ends, or timeout passes, waiting every
// between attempts. It returns fn's last error.
func retry(ctx context.Context, timeout, every time.Duration, fn func(ctx context.Context) error) error {
	deadline := time.Now().Add(timeout)
	for {
		err := fn(ctx)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

// writeTemp writes content to a private (0600) temp file and returns its path.
func writeTemp(pattern, content string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("writing temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("closing temp file: %w", err)
	}
	return f.Name(), nil
}

// waitAvailable waits for every deployment in ns to be Available.
func (e *Env) waitAvailable(ctx context.Context, ns string, timeout time.Duration) error {
	return e.kubectl(ctx, "wait", "--for=condition=Available", "deployment", "--all",
		"-n", ns, "--timeout="+timeout.String())
}

// namespaceYAML is a Namespace manifest document.
func namespaceYAML(name string) string {
	return fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", name)
}

// exists reports whether a "kind/name" resource exists (in ns, when given).
func (e *Env) exists(ctx context.Context, ns, ref string) bool {
	args := []string{"get", ref, "-o", "name"}
	if ns != "" {
		args = append(args, "-n", ns)
	}
	_, err := e.kubectlQuiet(ctx, args...)
	return err == nil
}

func (e *Env) crdExists(ctx context.Context, crd string) bool {
	return e.exists(ctx, "", "crd/"+crd)
}

// defaultStorageClass returns the name of the cluster's default StorageClass,
// or "".
func (e *Env) defaultStorageClass(ctx context.Context) string {
	out, err := e.kubectlQuiet(ctx, "get", "storageclass", "-o",
		`jsonpath={range .items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")]}{.metadata.name}{"\n"}{end}`)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
}

// helmManaged reports whether the app's first detected resource is owned by
// a Helm release: the meta.helm.sh/release-name annotation Helm checks
// before adopting it. Not the managed-by=Helm label: charts rendered by
// Argo CD carry that too, and Helm then refuses the install.
func (e *Env) helmManaged(ctx context.Context, app App) bool {
	for _, ref := range app.Detect {
		out, err := e.kubectlQuiet(ctx, "get", ref, "-n", app.Namespace,
			"-o", `jsonpath={.metadata.annotations.meta\.helm\.sh/release-name}`)
		if err == nil {
			return strings.TrimSpace(out) != ""
		}
	}
	return false
}

// remaining is the time left before ctx's deadline (at least a second), or
// def without one.
func remaining(ctx context.Context, def time.Duration) time.Duration {
	dl, ok := ctx.Deadline()
	if !ok {
		return def
	}
	left := time.Until(dl).Round(time.Second)
	if left < time.Second {
		return time.Second
	}
	return left
}

// ensurePrivilegedNamespace creates ns (if needed) labelled for the privileged
// Pod Security level. Talos enforces "baseline" cluster-wide (kube-system
// excepted), which rejects hostPath volumes, host networking and added
// capabilities — exactly what storage provisioners, MetalLB's speaker and
// node exporters need. Their upstream docs prescribe the same labels.
func (e *Env) ensurePrivilegedNamespace(ctx context.Context, ns string) error {
	if slices.Contains(protectedNamespaces, ns) {
		return nil
	}
	if err := e.applyYAML(ctx, namespaceYAML(ns)); err != nil {
		return err
	}
	// `kubectl label`, not apply: the labels are then owned by a separate
	// field manager. An upstream manifest that contains this Namespace
	// (local-path, MetalLB, ...) is applied server-side by the "kubectl"
	// manager, and server-side apply drops fields that manager owns but no
	// longer declares — labels applied the same way would vanish.
	_, err := e.kubectlQuiet(ctx, append([]string{"label", "--overwrite", "namespace", ns}, privilegedLabels...)...)
	if err != nil {
		return fmt.Errorf("labelling namespace %s for privileged pods: %w", ns, err)
	}
	return nil
}

// privilegedLabels are the Pod Security labels ensurePrivilegedNamespace sets.
var privilegedLabels = []string{
	"pod-security.kubernetes.io/enforce=privileged",
	"pod-security.kubernetes.io/audit=privileged",
	"pod-security.kubernetes.io/warn=privileged",
}

// namespacePrivileged reports whether ns already enforces the privileged
// Pod Security level.
func (e *Env) namespacePrivileged(ctx context.Context, ns string) bool {
	out, err := e.kubectlQuiet(ctx, "get", "namespace", ns,
		"-o", `jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)
	return err == nil && strings.TrimSpace(out) == "privileged"
}

// isTalos reports whether the cluster's nodes run Talos Linux (also for
// clusters reached by --context, where the provider is unknown).
func (e *Env) isTalos(ctx context.Context) bool {
	if e.Provider == cluster.ProviderTalos {
		return true
	}
	out, err := e.kubectlQuiet(ctx, "get", "nodes", "-o", "jsonpath={.items[0].status.nodeInfo.osImage}")
	return err == nil && strings.HasPrefix(strings.TrimSpace(out), "Talos")
}

// randomPassword returns a URL-safe random password (n bytes of entropy).
func randomPassword(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ensureSecret creates an Opaque Secret with data unless it already exists.
// Existing secrets are kept, so re-applying an app never rotates a password
// the app has already been initialised with.
func (e *Env) ensureSecret(ctx context.Context, ns, name string, data map[string]string) error {
	if e.exists(ctx, ns, "secret/"+name) {
		return nil
	}
	if err := e.applyYAML(ctx, namespaceYAML(ns)); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\n  labels:\n    app.kubernetes.io/managed-by: k8slocalcli\ntype: Opaque\nstringData:\n", name, ns)
	for k, v := range data {
		fmt.Fprintf(&b, "  %s: %q\n", k, v)
	}
	if err := e.applyYAML(ctx, b.String()); err != nil {
		return fmt.Errorf("creating secret %s/%s: %w", ns, name, err)
	}
	return nil
}

// ensurePasswordSecret creates a Secret holding username and a generated
// password, unless it exists.
func (e *Env) ensurePasswordSecret(ctx context.Context, ns, name, username string) error {
	if e.exists(ctx, ns, "secret/"+name) {
		return nil
	}
	pw, err := randomPassword(18)
	if err != nil {
		return err
	}
	return e.ensureSecret(ctx, ns, name, map[string]string{"username": username, "password": pw})
}

// secretValue returns one decoded key of a Secret, or "".
func (e *Env) secretValue(ctx context.Context, ns, name, key string) string {
	out, err := e.kubectlQuiet(ctx, "-n", ns, "get", "secret", name,
		"-o", "go-template={{ index .data \""+key+"\" }}")
	if err != nil {
		return ""
	}
	v, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		return ""
	}
	return string(v)
}
