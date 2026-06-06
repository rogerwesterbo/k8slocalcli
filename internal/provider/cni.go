package provider

import (
	"context"
	"fmt"
	"io"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// installCNI installs the requested CNI into a freshly created cluster whose
// default CNI has been disabled. It is a no-op for the default CNI.
//
// The Helm commands and per-provider values are ported from the createlocalk8s
// bash project (scripts/installers/helm.sh), which is the proven reference for
// these clusters.
func installCNI(ctx context.Context, prov cluster.Provider, cni cluster.CNI, kubeContext string, out io.Writer) error {
	if !cni.Custom() {
		return nil
	}
	if !runner.LookPath("helm") {
		return fmt.Errorf("helm is required to install the %s CNI; install it (e.g. brew install helm) or choose the default CNI", cni)
	}

	r := runner.New(out)
	switch cni {
	case cluster.CNICilium:
		return installCilium(ctx, r, prov, kubeContext, out)
	case cluster.CNICalico:
		return installCalico(ctx, r, prov, kubeContext, out)
	default:
		return fmt.Errorf("unsupported CNI %q", cni)
	}
}

func installCilium(ctx context.Context, r *runner.Runner, prov cluster.Provider, kubeContext string, out io.Writer) error {
	fmt.Fprintf(out, "\n📦 Installing Cilium CNI via Helm\n")

	if prov == cluster.ProviderTalos {
		// Talos enforces PodSecurity; Cilium runs privileged in kube-system.
		labelNamespacePrivileged(ctx, kubeContext, "kube-system", out)
	}

	if err := r.Run(ctx, "helm", "repo", "add", "cilium", "https://helm.cilium.io/", "--force-update"); err != nil {
		return fmt.Errorf("adding cilium helm repo: %w", err)
	}

	args := []string{
		"upgrade", "--install", "cilium", "cilium/cilium",
		"--kube-context", kubeContext,
		"--namespace", "kube-system",
		"--set", "operator.replicas=1",
		"--set", "ipam.mode=kubernetes",
		"--set", "kubeProxyReplacement=true",
		"--set", "tolerations[0].operator=Exists",
		"--set", "operator.tolerations[0].operator=Exists",
		"--set", "image.pullPolicy=IfNotPresent",
	}
	if prov == cluster.ProviderTalos {
		// Talos needs explicit capabilities and KubePrism (localhost:7445) as the
		// API endpoint since kube-proxy is disabled.
		args = append(args,
			"--set", "securityContext.capabilities.ciliumAgent={CHOWN,KILL,NET_ADMIN,NET_RAW,IPC_LOCK,SYS_ADMIN,SYS_RESOURCE,DAC_OVERRIDE,FOWNER,SETGID,SETUID}",
			"--set", "securityContext.capabilities.cleanCiliumState={NET_ADMIN,SYS_ADMIN,SYS_RESOURCE}",
			"--set", "cgroup.autoMount.enabled=false",
			"--set", "cgroup.hostRoot=/sys/fs/cgroup",
			"--set", "bpf.hostLegacyRouting=true",
			"--set", "k8sServiceHost=localhost",
			"--set", "k8sServicePort=7445",
		)
	}
	if err := r.Run(ctx, "helm", args...); err != nil {
		return fmt.Errorf("installing cilium: %w", err)
	}

	// Best-effort: surface readiness without failing the whole create if the
	// rollout is merely slow (the caller's node-readiness wait is the real gate).
	fmt.Fprintf(out, "\n⏰ Waiting for Cilium to roll out\n")
	if err := r.Run(ctx, "kubectl", "--context", kubeContext, "rollout", "status",
		"daemonset/cilium", "-n", "kube-system", "--timeout=180s"); err != nil {
		fmt.Fprintf(out, "⚠️  Cilium daemonset not ready yet; it may still be coming up\n")
	}
	return nil
}

func installCalico(ctx context.Context, r *runner.Runner, prov cluster.Provider, kubeContext string, out io.Writer) error {
	fmt.Fprintf(out, "\n📦 Installing Calico CNI via Helm (Tigera operator)\n")

	if prov == cluster.ProviderTalos {
		labelNamespacePrivileged(ctx, kubeContext, "tigera-operator", out)
	}

	if err := r.Run(ctx, "helm", "repo", "add", "projectcalico", "https://docs.tigera.io/calico/charts", "--force-update"); err != nil {
		return fmt.Errorf("adding calico helm repo: %w", err)
	}

	args := []string{
		"upgrade", "--install", "calico", "projectcalico/tigera-operator",
		"--kube-context", kubeContext,
		"--namespace", "tigera-operator",
		"--create-namespace",
		"--set", "installation.kubernetesProvider=",
	}
	if prov == cluster.ProviderTalos {
		args = append(args, "--set", "installation.cni.type=Calico")
	}
	if err := r.Run(ctx, "helm", args...); err != nil {
		return fmt.Errorf("installing calico: %w", err)
	}

	fmt.Fprintf(out, "\n⏰ Waiting for Calico to roll out\n")
	if err := r.Run(ctx, "kubectl", "--context", kubeContext, "rollout", "status",
		"daemonset/calico-node", "-n", "calico-system", "--timeout=240s"); err != nil {
		fmt.Fprintf(out, "⚠️  Calico node daemonset not ready yet; it may still be coming up\n")
	}
	return nil
}

// labelNamespacePrivileged ensures a namespace exists and carries the
// privileged PodSecurity label, required on Talos for CNI components. It is
// best-effort and never blocks cluster creation.
func labelNamespacePrivileged(ctx context.Context, kubeContext, ns string, out io.Writer) {
	// Create the namespace quietly (ignore "already exists").
	_, _ = runner.New(nil).Capture(ctx, "kubectl", "--context", kubeContext, "create", "namespace", ns)
	if err := runner.New(out).Run(ctx, "kubectl", "--context", kubeContext, "label", "ns", ns,
		"pod-security.kubernetes.io/enforce=privileged", "--overwrite"); err != nil {
		fmt.Fprintf(out, "⚠️  Could not label namespace %s privileged: %v\n", ns, err)
	}
}

// cniLabel returns a display label for a CNI, treating empty as "default".
func cniLabel(c cluster.CNI) string {
	if c == "" {
		return string(cluster.CNIDefault)
	}
	return string(c)
}

// talosCNIPatch returns the Talos machine-config patch that disables the default
// CNI (and, for Cilium, kube-proxy, which Cilium replaces). Matches the
// createlocalk8s talos provider behaviour.
func talosCNIPatch(cni cluster.CNI) string {
	patch := "cluster:\n  network:\n    cni:\n      name: none\n"
	if cni == cluster.CNICilium {
		patch += "  proxy:\n    disabled: true\n"
	}
	return patch
}
