package apps

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// TestPodSecurityAudit renders every chart app (both profiles, Talos values)
// and asks a real API server whether its workloads pass Pod Security
// "baseline" — what Talos enforces — by dry-running them into a namespace
// labelled warn=baseline. An app whose workloads are rejected must be
// Privileged, or it never starts on Talos.
//
// It needs helm and a throwaway cluster, so it is opt-in:
//
//	kind create cluster --name psa
//	K8SLOCALCLI_PSA_AUDIT_CONTEXT=kind-psa go test ./internal/apps -run TestPodSecurityAudit -v
//	kind delete cluster --name psa
//
// Only rendered workloads are judged: pods an app creates at runtime
// (local-path's volume helpers, Trivy's node scans) are not, which is why
// those apps are Privileged without failing here. Run it after adding an app
// or bumping a chart.
func TestPodSecurityAudit(t *testing.T) {
	kubeContext := os.Getenv("K8SLOCALCLI_PSA_AUDIT_CONTEXT")
	if kubeContext == "" {
		t.Skip("set K8SLOCALCLI_PSA_AUDIT_CONTEXT to a throwaway cluster's context to run")
	}
	if !runner.LookPath("helm") || !runner.LookPath("kubectl") {
		t.Skip("needs helm and kubectl")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()

	for _, a := range Catalog() {
		if a.chart == nil {
			continue
		}
		for _, profile := range cluster.Profiles {
			env := &Env{Target: Target{Name: "audit", KubeContext: kubeContext, Provider: cluster.ProviderTalos}, Profile: profile}
			t.Run(a.Name+"/"+string(profile), func(t *testing.T) {
				violations := baselineViolations(ctx, t, env, a)
				if violations != "" && !a.Privileged {
					t.Errorf("%s is not Privileged, but Pod Security baseline rejects its workloads:\n%s", a.Name, violations)
				}
				if violations != "" {
					t.Logf("needs privileged (and is): %s", violations)
				}
			})
		}
	}
}

// baselineViolations renders the app's chart and returns the Pod Security
// baseline warnings the API server gives for it ("" when it passes).
func baselineViolations(ctx context.Context, t *testing.T, env *Env, a App) string {
	t.Helper()
	ns := "psa-audit-" + a.Name
	if _, err := env.kubectlQuiet(ctx, "create", "namespace", ns); err != nil && !env.exists(ctx, "", "namespace/"+ns) {
		t.Fatalf("creating %s: %v", ns, err)
	}
	t.Cleanup(func() { _, _ = env.kubectlQuiet(context.Background(), "delete", "namespace", ns, "--wait=false") })
	if _, err := env.kubectlQuiet(ctx, "label", "--overwrite", "namespace", ns, "pod-security.kubernetes.io/warn=baseline"); err != nil {
		t.Fatal(err)
	}

	rendered, err := renderChartYAML(ctx, env, a, ns)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := writeTemp("psa-audit-*.yaml", rendered)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(manifest) }()

	// Server-side dry run: admission (Pod Security included) judges every
	// object, nothing is stored. Errors for custom resources whose CRDs are
	// not installed are expected and irrelevant; only the warnings count.
	var out bytes.Buffer
	_ = runner.New(&out).Run(ctx, "kubectl", "--context", env.KubeContext,
		"apply", "--dry-run=server", "-n", ns, "-f", manifest)
	var warnings []string
	for _, line := range strings.Split(out.String(), "\n") {
		if _, rules, ok := strings.Cut(line, `would violate PodSecurity "baseline:latest": `); ok {
			warnings = append(warnings, rules)
		}
	}
	return strings.Join(warnings, "\n")
}

// renderChartYAML renders the app's chart with its values for env (as
// `helm template` into namespace ns), for the audits.
func renderChartYAML(ctx context.Context, env *Env, a App, ns string) (string, error) {
	c, err := a.renderChart(ctx, env, "audit.localtest.me")
	if err != nil {
		return "", err
	}
	values, err := c.valuesYAML()
	if err != nil {
		return "", err
	}
	valuesFile, err := writeTemp("audit-values-*.yaml", values)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(valuesFile) }()
	args := []string{"template", c.Release, c.Chart, "--namespace", ns, "--values", valuesFile}
	if c.Repo != "" {
		args = append(args, "--repo", c.Repo)
	}
	if c.Version != "" {
		args = append(args, "--version", c.Version)
	}
	out, err := runner.New(nil).Capture(ctx, "helm", args...)
	if err != nil {
		return "", fmt.Errorf("helm template %s: %w", c.Chart, err)
	}
	return out, nil
}
