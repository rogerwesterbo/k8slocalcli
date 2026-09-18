package provider

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

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
	case cluster.CNIKubeOVN:
		return installKubeOVN(ctx, r, prov, kubeContext, out)
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

	// Calico v3.32 removed the CRDs from the tigera-operator chart (it has no
	// crds/ directory), but its templates still render operator.tigera.io
	// custom resources. They must exist before the operator chart is installed.
	if err := installCalicoCRDs(ctx, r, kubeContext, out); err != nil {
		return err
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

// calicoCRDChart ships the crd.projectcalico.org and operator.tigera.io CRDs.
// As of Calico v3.32 these are no longer bundled in the tigera-operator chart
// and must be installed (and upgraded) separately.
const calicoCRDChart = "projectcalico/crd.projectcalico.org.v1"

// calicoCRDTemplateArgs renders the CRD chart locally. Upstream renders rather
// than installs it because Helm neither upgrades nor deletes CRDs that live in
// a chart's crds/ directory.
func calicoCRDTemplateArgs() []string {
	return []string{"template", "calico-crds", calicoCRDChart}
}

// calicoCRDApplyArgs applies the rendered manifest. Server-side apply is
// required: some Calico CRDs exceed the size limit for client-side apply.
func calicoCRDApplyArgs(kubeContext, path string) []string {
	return []string{"--context", kubeContext, "apply", "--server-side", "-f", path}
}

// installCalicoCRDs renders the Calico CRD chart and applies it to the cluster.
func installCalicoCRDs(ctx context.Context, r *runner.Runner, kubeContext string, out io.Writer) error {
	fmt.Fprintf(out, "\n📦 Installing Calico CRDs (%s)\n", calicoCRDChart)

	// Rendered quietly: the manifest is several thousand lines of CRD YAML.
	manifest, err := runner.New(nil).Capture(ctx, "helm", calicoCRDTemplateArgs()...)
	if err != nil {
		return fmt.Errorf("rendering calico CRDs: %w", err)
	}

	f, err := os.CreateTemp("", "calico-crds-*.yaml")
	if err != nil {
		return fmt.Errorf("creating calico CRD manifest: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(manifest); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing calico CRD manifest: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing calico CRD manifest: %w", err)
	}

	if err := r.Run(ctx, "kubectl", calicoCRDApplyArgs(kubeContext, f.Name())...); err != nil {
		return fmt.Errorf("applying calico CRDs: %w", err)
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
// CNI (and, for Cilium, kube-proxy, which Cilium replaces). talosMajorMinor is
// the installed talosctl version: Talos 1.14 moved these settings out of the
// v1alpha1 document into the KubeFlannelCNIConfig/KubeProxyConfig documents and
// rejects the legacy cluster.network/cluster.proxy fields alongside them.
func talosCNIPatch(cni cluster.CNI, talosMajorMinor string) string {
	if talosAtLeast(talosMajorMinor, 1, 14) {
		patch := "apiVersion: v1alpha1\nkind: KubeFlannelCNIConfig\n$patch: delete\n"
		if cni == cluster.CNICilium {
			patch += "---\napiVersion: v1alpha1\nkind: KubeProxyConfig\nenabled: false\n"
		}
		return patch
	}

	patch := "cluster:\n  network:\n    cni:\n      name: none\n"
	if cni == cluster.CNICilium {
		patch += "  proxy:\n    disabled: true\n"
	}
	return patch
}

// Kube-OVN's Helm chart discovers the ovn-central members with a `lookup` over
// nodes carrying MASTER_NODES_LABEL, and hard-fails ("No nodes found with
// label ...") when none do. So the control planes must be labelled before the
// install, not by it.
const (
	kubeOVNRepo       = "https://kubeovn.github.io/kube-ovn/"
	kubeOVNMasterRole = "kube-ovn/role=master"

	// ovs-ovn's startup runs `ovs-ctl load-kmod`, which modprobes openvswitch
	// and exits when that fails — a crash loop that leaves the control plane
	// looking perfectly healthy while no node has a dataplane.
	//
	// Both providers need it off, for different reasons. Talos has a read-only
	// rootfs and manages modules declaratively. kind is the surprising one: on
	// Docker Desktop the nodes share a LinuxKit kernel that has openvswitch
	// (and vport-vxlan/geneve/gre) compiled *in* — they are listed in
	// modules.builtin with no .ko to load — so the modprobe both fails and is
	// unnecessary. With this set the chart stubs modprobe out, and OVS comes up
	// on the kernel datapath as it should.
	disableModulesManagement = "DISABLE_MODULES_MANAGEMENT=true"
)

// kubeOVNValues returns the provider-specific `--set` arguments, flattened into
// helm's key=value form.
//
// The CIDRs must match what the cluster was actually built with: Kube-OVN runs
// its own IPAM out of POD_CIDR and programs OVN load balancers for SVC_CIDR, so
// a mismatch with the API server's --service-cluster-ip-range silently breaks
// service routing.
//
//   - kind creates dual-stack clusters (kindConfig sets ipFamily: dual) and
//     defaults to 10.244.0.0/16,fd00:10:244::/56 for pods and
//     10.96.0.0/16,fd00:10:96::/112 for services.
//   - the talosctl Docker backend has no CIDR flags; Talos defaults to a
//     single-stack 10.244.0.0/16 pod subnet and 10.96.0.0/12 service subnet.
//
// Commas are escaped because helm's --set parser treats them as list separators.
func kubeOVNValues(prov cluster.Provider) []string {
	if prov == cluster.ProviderTalos {
		return []string{
			"networking.NET_STACK=ipv4",
			"ipv4.POD_CIDR=10.244.0.0/16",
			"ipv4.POD_GATEWAY=10.244.0.1",
			"ipv4.SVC_CIDR=10.96.0.0/12",
			// Talos has a read-only rootfs, so the chart's /etc/origin host
			// paths cannot be created. These are the settings upstream
			// documents for Talos (charts/kube-ovn/README.md).
			"cni_conf.MOUNT_LOCAL_BIN_DIR=false",
			"OPENVSWITCH_DIR=/var/lib/openvswitch",
			"OVN_DIR=/var/lib/ovn",
			"OVN_IPSEC_KEY_DIR=/var/lib/ovs_ipsec_keys",
			disableModulesManagement,
		}
	}
	return []string{
		"networking.NET_STACK=dual_stack",
		disableModulesManagement,
		`dual_stack.POD_CIDR=10.244.0.0/16\,fd00:10:244::/56`,
		`dual_stack.POD_GATEWAY=10.244.0.1\,fd00:10:244::1`,
		`dual_stack.SVC_CIDR=10.96.0.0/16\,fd00:10:96::/112`,
	}
}

func installKubeOVN(ctx context.Context, r *runner.Runner, prov cluster.Provider, kubeContext string, out io.Writer) error {
	fmt.Fprintf(out, "\n📦 Installing Kube-OVN CNI via Helm\n")

	if prov == cluster.ProviderTalos {
		// Talos enforces PodSecurity; ovs-ovn and kube-ovn-cni are privileged,
		// host-network pods in kube-system.
		labelNamespacePrivileged(ctx, kubeContext, "kube-system", out)
		fmt.Fprintf(out, "ℹ️  Kube-OVN needs the openvswitch kernel module. Talos nodes in Docker share the\n")
		fmt.Fprintf(out, "   Docker host's kernel and cannot load it themselves (DISABLE_MODULES_MANAGEMENT=true),\n")
		fmt.Fprintf(out, "   so ovs-ovn will crash-loop unless the module is already loaded on the Docker host.\n")
	}

	if err := r.Run(ctx, "helm", "repo", "add", "kubeovn", kubeOVNRepo, "--force-update"); err != nil {
		return fmt.Errorf("adding kube-ovn helm repo: %w", err)
	}

	if err := labelKubeOVNMasters(ctx, r, kubeContext, out); err != nil {
		return err
	}

	args := []string{
		"upgrade", "--install", "kube-ovn", "kubeovn/kube-ovn",
		"--kube-context", kubeContext,
		"--namespace", "kube-system",
		"--set", "image.pullPolicy=IfNotPresent",
	}
	for _, v := range kubeOVNValues(prov) {
		args = append(args, "--set", v)
	}
	if err := r.Run(ctx, "helm", args...); err != nil {
		return fmt.Errorf("installing kube-ovn: %w", err)
	}

	// Best-effort: surface readiness without failing the whole create if the
	// rollout is merely slow (the caller's node-readiness wait is the real gate).
	fmt.Fprintf(out, "\n⏰ Waiting for Kube-OVN to roll out\n")
	if err := r.Run(ctx, "kubectl", "--context", kubeContext, "rollout", "status",
		"daemonset/kube-ovn-cni", "-n", "kube-system", "--timeout=300s"); err != nil {
		fmt.Fprintf(out, "⚠️  Kube-OVN daemonset not ready yet; it may still be coming up\n")
	}
	return nil
}

// kubeOVNMasterLabelArgs labels every control-plane node as an ovn-central
// member. --overwrite keeps re-runs idempotent.
func kubeOVNMasterLabelArgs(kubeContext string) []string {
	return []string{
		"--context", kubeContext, "label", "node",
		"-l", "node-role.kubernetes.io/control-plane",
		kubeOVNMasterRole, "--overwrite",
	}
}

// labelKubeOVNMasters waits for the control-plane nodes to register, then
// labels them. The wait matters on Talos: runTalosCreate returns as soon as the
// API server answers, which can be before the node object exists.
func labelKubeOVNMasters(ctx context.Context, r *runner.Runner, kubeContext string, out io.Writer) error {
	fmt.Fprintf(out, "\n🏷️  Labelling control-plane nodes %s\n", kubeOVNMasterRole)

	// The nodes are NotReady (no CNI yet), so we can only wait for them to exist.
	quiet := runner.New(nil)
	var lastErr error
	for i := 0; i < 30; i++ {
		names, err := quiet.Capture(ctx, "kubectl", "--context", kubeContext, "get", "nodes",
			"-l", "node-role.kubernetes.io/control-plane", "-o", "name")
		if err == nil && len(nonEmptyLines(names)) > 0 {
			lastErr = nil
			break
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if lastErr != nil {
		return fmt.Errorf("waiting for control-plane nodes to register: %w", lastErr)
	}

	if err := r.Run(ctx, "kubectl", kubeOVNMasterLabelArgs(kubeContext)...); err != nil {
		return fmt.Errorf("labelling control-plane nodes for kube-ovn: %w", err)
	}
	return nil
}
