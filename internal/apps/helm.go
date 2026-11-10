package apps

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// helmChart describes a chart. The same description drives every
// chart-based install method: `helm upgrade --install`, a Flux HelmRelease and
// an Argo CD Application, which is why values are a map rather than --set
// strings.
type helmChart struct {
	Release string
	Chart   string // chart name, or a full oci:// reference
	Repo    string // classic repo URL; empty for oci:// charts
	// Version pins the chart. Empty means the newest release, resolved once
	// at install time (see resolveVersion) so every method installs the same
	// version.
	Version   string
	Namespace string
	Values    map[string]any
}

// helm runs helm against the target's context, streaming its output.
func (e *Env) helm(ctx context.Context, args ...string) error {
	return e.runner().Run(ctx, "helm", append(args, "--kube-context", e.KubeContext)...)
}

// helmQuiet runs helm against the target's context without streaming,
// returning stdout.
func (e *Env) helmQuiet(ctx context.Context, args ...string) (string, error) {
	return runner.New(nil).Capture(ctx, "helm", append(args, "--kube-context", e.KubeContext)...)
}

// renderChart returns the app's chart with its values for env: the app's own,
// with the user's --values for it merged on top.
func (a App) renderChart(ctx context.Context, env *Env, host string) (helmChart, error) {
	if a.chart == nil {
		return helmChart{}, fmt.Errorf("%s has no Helm chart", a.Name)
	}
	c := *a.chart
	if a.values != nil {
		v, err := a.values(ctx, env, host)
		if err != nil {
			return c, fmt.Errorf("%s values: %w", a.Name, err)
		}
		c.Values = v
	}
	if user := env.Values[a.Name]; len(user) > 0 {
		c.Values = mergeValues(c.Values, user)
	}
	return c, nil
}

// oci reports whether the chart is pulled from an OCI registry.
func (c helmChart) oci() bool { return strings.HasPrefix(c.Chart, "oci://") }

// ociParts splits an oci:// chart reference into the repository part and the
// chart name, e.g. ("ghcr.io/k8sdockside/helm", "k8sdockside").
func (c helmChart) ociParts() (repo, name string) {
	ref := strings.TrimPrefix(c.Chart, "oci://")
	i := strings.LastIndex(ref, "/")
	if i < 0 {
		return ref, ref
	}
	return ref[:i], ref[i+1:]
}

// valuesYAML renders Values as a YAML document.
func (c helmChart) valuesYAML() (string, error) {
	if len(c.Values) == 0 {
		return "", nil
	}
	b, err := yaml.Marshal(c.Values)
	if err != nil {
		return "", fmt.Errorf("rendering %s values: %w", c.Release, err)
	}
	return string(b), nil
}

// helmInstall installs or upgrades a chart and waits (up to timeout) for its
// resources to be ready. Values go through a private temp file rather than
// --set, so nested values, lists and keys with dots need no escaping.
func (e *Env) helmInstall(ctx context.Context, c helmChart, timeout time.Duration) error {
	if !runner.LookPath("helm") {
		return fmt.Errorf("helm is required to install %s via Helm; install it (e.g. brew install helm) or choose another method", c.Release)
	}
	args := []string{"upgrade", "--install", c.Release, c.Chart,
		"--namespace", c.Namespace, "--create-namespace",
		"--wait", "--timeout", timeout.String(),
	}
	if c.Version != "" {
		args = append(args, "--version", c.Version)
	}
	if helmMajor() >= 4 {
		// Helm 4 upgrades with server-side apply, and fields that controllers
		// write themselves (e.g. a webhook's caBundle, injected by OpenBao's
		// agent injector) then conflict on every re-apply. Same choice as
		// `kubectl apply --server-side --force-conflicts` elsewhere.
		args = append(args, "--force-conflicts")
	}
	if c.Repo != "" {
		args = append(args, "--repo", c.Repo)
	}
	values, err := c.valuesYAML()
	if err != nil {
		return err
	}
	if values != "" {
		f, err := writeTemp("k8slocalcli-values-*.yaml", values)
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(f) }()
		args = append(args, "--values", f)
	}
	return e.helm(ctx, args...)
}

// resolveVersion pins an unpinned chart to its newest release by asking helm.
// GitOps controllers each have their own idea of "latest" (Flux semver ranges,
// Argo CD's limited OCI support), so the version is resolved here once. When
// helm is not available the chart stays unpinned and the caller falls back to
// the controller's own "newest" syntax.
func resolveVersion(ctx context.Context, c helmChart) helmChart {
	if c.Version != "" || !runner.LookPath("helm") {
		return c
	}
	args := []string{"show", "chart", c.Chart}
	if c.Repo != "" {
		args = append(args, "--repo", c.Repo)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := runner.New(nil).Capture(ctx, "helm", args...)
	if err != nil {
		return c
	}
	if v := chartVersion(out); v != "" {
		c.Version = v
	}
	return c
}

// chartVersion extracts the version field from `helm show chart` output.
func chartVersion(chartYAML string) string {
	var meta struct {
		Version string `json:"version"`
	}
	if err := yaml.Unmarshal([]byte(chartYAML), &meta); err != nil {
		return ""
	}
	return meta.Version
}

var (
	helmMajorOnce sync.Once
	helmMajorVer  int
)

// helmMajor returns the installed helm's major version (0 when unknown).
// It is asked once per process, with its own deadline rather than a caller's
// context, so one cancelled caller cannot leave it wrong for every later one.
func helmMajor() int {
	helmMajorOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := runner.New(nil).Capture(ctx, "helm", "version", "--short")
		if err == nil {
			helmMajorVer = parseHelmMajor(out)
		}
	})
	return helmMajorVer
}

// parseHelmMajor reads the major version from `helm version --short`, e.g.
// "v4.3.1+gc0d08d0" → 4.
func parseHelmMajor(s string) int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	major, _, _ := strings.Cut(s, ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0
	}
	return n
}
