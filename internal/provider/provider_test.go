package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

func TestRegistry(t *testing.T) {
	for _, name := range cluster.Providers {
		p, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) returned error: %v", name, err)
		}
		if p.Name() != name {
			t.Errorf("provider name = %q, want %q", p.Name(), name)
		}
	}
	if _, err := Get(cluster.Provider("nope")); err == nil {
		t.Error("expected error for unknown provider")
	}
	if got := len(All()); got != len(cluster.Providers) {
		t.Errorf("All() returned %d providers, want %d", got, len(cluster.Providers))
	}
}

func TestContextNames(t *testing.T) {
	if got := NewKind().Context("foo"); got != "kind-foo" {
		t.Errorf("kind context = %q, want kind-foo", got)
	}
	if got := NewTalos().Context("foo"); got != "admin@foo" {
		t.Errorf("talos context = %q, want admin@foo", got)
	}
}

func TestKindImageFor(t *testing.T) {
	// default
	img, ver, ok := kindImageFor("")
	if !ok || !strings.HasPrefix(img, "kindest/node:") || ver == "" {
		t.Fatalf("default image lookup failed: img=%q ver=%q ok=%v", img, ver, ok)
	}
	// known with and without leading v
	if _, _, ok := kindImageFor("1.33.12"); !ok {
		t.Error("expected 1.33.12 to be known")
	}
	if _, _, ok := kindImageFor("v1.33.12"); !ok {
		t.Error("expected v1.33.12 to be known")
	}
	// unknown falls back but reports not ok
	if _, _, ok := kindImageFor("v9.99.99"); ok {
		t.Error("expected unknown version to report ok=false")
	}
}

func TestKindConfig(t *testing.T) {
	spec := cluster.Spec{
		Name:          "demo",
		Provider:      cluster.ProviderKind,
		ControlPlanes: 2,
		Workers:       3,
		HTTPPort:      8080,
		HTTPSPort:     8443,
	}
	cfg := kindConfig(spec, "kindest/node:test")

	if strings.Count(cfg, "role: control-plane") != 2 {
		t.Errorf("expected 2 control-plane entries:\n%s", cfg)
	}
	if strings.Count(cfg, "role: worker") != 3 {
		t.Errorf("expected 3 worker entries:\n%s", cfg)
	}
	// Only the first control plane gets the host port mappings.
	if strings.Count(cfg, "hostPort: 8080") != 1 || strings.Count(cfg, "hostPort: 8443") != 1 {
		t.Errorf("expected exactly one of each host port mapping:\n%s", cfg)
	}
	if !strings.Contains(cfg, "ingress-ready: \"true\"") {
		t.Errorf("expected ingress-ready label:\n%s", cfg)
	}
}

func TestKindConfigDefaultCNI(t *testing.T) {
	spec := cluster.Spec{Name: "x", ControlPlanes: 1, HTTPPort: 80, HTTPSPort: 443, CNI: cluster.CNIDefault}
	if cfg := kindConfig(spec, "img"); strings.Contains(cfg, "disableDefaultCNI") {
		t.Errorf("default CNI should not disable kindnet:\n%s", cfg)
	}
}

func TestKindConfigCustomCNI(t *testing.T) {
	spec := cluster.Spec{Name: "x", ControlPlanes: 1, HTTPPort: 80, HTTPSPort: 443, CNI: cluster.CNICilium}
	if cfg := kindConfig(spec, "img"); !strings.Contains(cfg, "disableDefaultCNI: true") {
		t.Errorf("custom CNI must disable kindnet:\n%s", cfg)
	}
}

func TestTalosCNIPatchLegacy(t *testing.T) {
	// Before 1.14: v1alpha1 cluster.network/cluster.proxy fields.
	// Cilium replaces kube-proxy, so the patch disables both CNI and proxy.
	cilium := talosCNIPatch(cluster.CNICilium, "1.13")
	if !strings.Contains(cilium, "name: none") || !strings.Contains(cilium, "disabled: true") {
		t.Errorf("cilium patch should disable cni and proxy:\n%s", cilium)
	}
	if strings.Contains(cilium, "kind:") {
		t.Errorf("pre-1.14 patch must not use multi-document kinds:\n%s", cilium)
	}
	// Calico keeps kube-proxy, so the patch only disables the CNI.
	calico := talosCNIPatch(cluster.CNICalico, "1.13")
	if !strings.Contains(calico, "name: none") {
		t.Errorf("calico patch should disable cni:\n%s", calico)
	}
	if strings.Contains(calico, "proxy") {
		t.Errorf("calico patch should not disable kube-proxy:\n%s", calico)
	}
}

func TestTalosCNIPatchMultiDoc(t *testing.T) {
	// 1.14+ (and unknown/newer versions): delete the Flannel document and, for
	// Cilium, disable kube-proxy via KubeProxyConfig. The legacy v1alpha1 fields
	// are rejected alongside these documents.
	for _, mm := range []string{"1.14", "1.15", "2.0", ""} {
		cilium := talosCNIPatch(cluster.CNICilium, mm)
		if !strings.Contains(cilium, "kind: KubeFlannelCNIConfig\n$patch: delete") {
			t.Errorf("[%s] cilium patch should delete KubeFlannelCNIConfig:\n%s", mm, cilium)
		}
		if !strings.Contains(cilium, "kind: KubeProxyConfig\nenabled: false") {
			t.Errorf("[%s] cilium patch should disable kube-proxy:\n%s", mm, cilium)
		}
		if strings.Contains(cilium, "cluster:") {
			t.Errorf("[%s] cilium patch must not set legacy v1alpha1 fields:\n%s", mm, cilium)
		}

		calico := talosCNIPatch(cluster.CNICalico, mm)
		if !strings.Contains(calico, "kind: KubeFlannelCNIConfig\n$patch: delete") {
			t.Errorf("[%s] calico patch should delete KubeFlannelCNIConfig:\n%s", mm, calico)
		}
		if strings.Contains(calico, "KubeProxyConfig") || strings.Contains(calico, "cluster:") {
			t.Errorf("[%s] calico patch should only remove flannel:\n%s", mm, calico)
		}
	}
}

func TestParseTalosContexts(t *testing.T) {
	out := "CURRENT   NAME     ENDPOINTS         NODES\n" +
		"          demo     127.0.0.1:50001   127.0.0.1\n" +
		"*         dev      127.0.0.1:50000   \n"
	got := parseTalosContexts(out)
	if len(got) != 2 || got[0] != "demo" || got[1] != "dev" {
		t.Errorf("parseTalosContexts = %v, want [demo dev]", got)
	}
	if got := parseTalosContexts("CURRENT   NAME   ENDPOINTS   NODES\n"); len(got) != 0 {
		t.Errorf("expected no contexts from header-only output, got %v", got)
	}
}

func TestTalosAtLeast(t *testing.T) {
	cases := []struct {
		mm   string
		want bool
	}{
		{"1.13", false},
		{"1.8", false},
		{"0.99", false},
		{"1.14", true},
		{"1.20", true},
		{"2.0", true},
		{"", true},
		{"garbage", true},
	}
	for _, c := range cases {
		if got := talosAtLeast(c.mm, 1, 14); got != c.want {
			t.Errorf("talosAtLeast(%q, 1, 14) = %v, want %v", c.mm, got, c.want)
		}
	}
}

func TestCNILabel(t *testing.T) {
	if got := cniLabel(""); got != "default" {
		t.Errorf("cniLabel(\"\") = %q, want default", got)
	}
	if got := cniLabel(cluster.CNICilium); got != "cilium" {
		t.Errorf("cniLabel(cilium) = %q, want cilium", got)
	}
}

func TestKindConfigNoWorkers(t *testing.T) {
	spec := cluster.Spec{Name: "x", ControlPlanes: 1, Workers: 0, HTTPPort: 80, HTTPSPort: 443}
	cfg := kindConfig(spec, "img")
	if strings.Contains(cfg, "role: worker") {
		t.Errorf("expected no worker entries:\n%s", cfg)
	}
}

func TestParseTalosMajorMinor(t *testing.T) {
	out := "Client:\n\tTag:         v1.13.3\n\tSHA:         undefined\n"
	if got := parseTalosMajorMinor(out); got != "1.13" {
		t.Errorf("parseTalosMajorMinor = %q, want 1.13", got)
	}
	if got := parseTalosMajorMinor("Client:\n\tTag:         v1.14.1\n"); got != "1.14" {
		t.Errorf("parseTalosMajorMinor = %q, want 1.14", got)
	}
	if got := parseTalosMajorMinor("no version here"); got != "" {
		t.Errorf("expected empty result, got %q", got)
	}
}

func TestKubernetesVersions(t *testing.T) {
	// kind: returns all pinned versions, newest first, with the latest at [0].
	kv := NewKind().KubernetesVersions(context.Background())
	if len(kv) == 0 {
		t.Fatal("expected kind to report versions")
	}
	if kv[0] != kindNodeImages[0].Version {
		t.Errorf("kind latest = %q, want %q", kv[0], kindNodeImages[0].Version)
	}
	// talos: non-empty (falls back to the latest known set when talosctl is
	// absent or unrecognised).
	if tv := NewTalos().KubernetesVersions(context.Background()); len(tv) == 0 {
		t.Error("expected talos to report versions")
	}
}

func TestParseHostPort(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:64659":             "64659",
		"[::]:64659":                "64659",
		"0.0.0.0:64659\n[::]:64659": "64659",
		"":                          "",
		"no port here":              "",
	}
	for in, want := range cases {
		if got := parseHostPort(in); got != want {
			t.Errorf("parseHostPort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTalosK8sVersionsFor(t *testing.T) {
	if v := talosK8sVersionsFor("1.14"); len(v) == 0 || v[0] != "1.37.0" {
		t.Errorf("talos 1.14 versions unexpected: %v", v)
	}
	if v := talosK8sVersionsFor("1.13"); len(v) == 0 || v[0] != "1.36.1" {
		t.Errorf("talos 1.13 versions unexpected: %v", v)
	}
	// Unknown version falls back to the latest set.
	if v := talosK8sVersionsFor("99.99"); len(v) == 0 || v[0] != talosK8sVersions["1.14"][0] {
		t.Errorf("expected newest fallback versions for unknown talos version, got %v", v)
	}
}

// Calico v3.32 stopped shipping the crd.projectcalico.org and operator.tigera.io
// CRDs inside the tigera-operator chart. The chart's templates still render
// Installation/APIServer/Goldmane/Whisker custom resources, so installing the
// operator chart without applying those CRDs first fails with
// "no matches for kind ... ensure CRDs are installed first".
func TestCalicoCRDTemplateArgs(t *testing.T) {
	args := calicoCRDTemplateArgs()
	joined := strings.Join(args, " ")
	if args[0] != "template" {
		t.Errorf("CRDs must be rendered with `helm template`, got %q", joined)
	}
	if !strings.Contains(joined, calicoCRDChart) {
		t.Errorf("CRD render must use the %s chart, got %q", calicoCRDChart, joined)
	}
}

// Some Calico CRDs exceed the client-side apply annotation size limit, so the
// manifest has to be applied server-side.
func TestCalicoCRDApplyArgs(t *testing.T) {
	args := calicoCRDApplyArgs("kind-test", "/tmp/crds.yaml")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--server-side") {
		t.Errorf("CRDs must be applied server-side, got %q", joined)
	}
	if !strings.Contains(joined, "--context kind-test") {
		t.Errorf("apply must target the cluster context, got %q", joined)
	}
	if !strings.Contains(joined, "-f /tmp/crds.yaml") {
		t.Errorf("apply must reference the rendered manifest, got %q", joined)
	}
}

// Kube-OVN runs its own IPAM and programs OVN load balancers from the configured
// CIDRs, so they must match what each provider actually built the cluster with:
// kind is dual-stack (kindConfig sets ipFamily: dual), the talosctl Docker
// backend is single-stack IPv4 with a /12 service subnet.
func TestKubeOVNValuesCIDRsMatchProvider(t *testing.T) {
	kindValues := strings.Join(kubeOVNValues(cluster.ProviderKind), " ")
	for _, want := range []string{
		"networking.NET_STACK=dual_stack",
		`dual_stack.POD_CIDR=10.244.0.0/16\,fd00:10:244::/56`,
		`dual_stack.SVC_CIDR=10.96.0.0/16\,fd00:10:96::/112`,
	} {
		if !strings.Contains(kindValues, want) {
			t.Errorf("kind kube-ovn values missing %q:\n%s", want, kindValues)
		}
	}

	talosValues := strings.Join(kubeOVNValues(cluster.ProviderTalos), " ")
	for _, want := range []string{
		"networking.NET_STACK=ipv4",
		"ipv4.POD_CIDR=10.244.0.0/16",
		"ipv4.SVC_CIDR=10.96.0.0/12",
	} {
		if !strings.Contains(talosValues, want) {
			t.Errorf("talos kube-ovn values missing %q:\n%s", want, talosValues)
		}
	}
	if strings.Contains(talosValues, "dual_stack") {
		t.Errorf("talos docker backend is single-stack; got:\n%s", talosValues)
	}
}

// Talos has a read-only rootfs, so the chart's default /etc/origin host paths
// cannot be created and the node cannot load kernel modules itself. These are
// the settings upstream documents for Talos in charts/kube-ovn/README.md.
func TestKubeOVNValuesTalosReadOnlyRootfs(t *testing.T) {
	values := strings.Join(kubeOVNValues(cluster.ProviderTalos), " ")
	for _, want := range []string{
		"OPENVSWITCH_DIR=/var/lib/openvswitch",
		"OVN_DIR=/var/lib/ovn",
		"OVN_IPSEC_KEY_DIR=/var/lib/ovs_ipsec_keys",
		"DISABLE_MODULES_MANAGEMENT=true",
		"cni_conf.MOUNT_LOCAL_BIN_DIR=false",
	} {
		if !strings.Contains(values, want) {
			t.Errorf("talos kube-ovn values missing %q:\n%s", want, values)
		}
	}

	// kind nodes have a writable rootfs and bind-mount /lib/modules, so they
	// must keep the chart defaults.
	kindValues := strings.Join(kubeOVNValues(cluster.ProviderKind), " ")
	if strings.Contains(kindValues, "DISABLE_MODULES_MANAGEMENT") {
		t.Errorf("kind must let kube-ovn manage modules:\n%s", kindValues)
	}
}

// The chart resolves ovn-central's members with a Helm `lookup` over nodes
// carrying kube-ovn/role=master and fails the render when none do, so the label
// has to be applied before `helm upgrade --install` runs.
func TestKubeOVNMasterLabelArgs(t *testing.T) {
	joined := strings.Join(kubeOVNMasterLabelArgs("kind-test"), " ")
	if !strings.Contains(joined, kubeOVNMasterRole) {
		t.Errorf("label must set %s, got %q", kubeOVNMasterRole, joined)
	}
	if !strings.Contains(joined, "-l node-role.kubernetes.io/control-plane") {
		t.Errorf("label must select control-plane nodes, got %q", joined)
	}
	if !strings.Contains(joined, "--overwrite") {
		t.Errorf("label must be idempotent across re-runs, got %q", joined)
	}
	if !strings.Contains(joined, "--context kind-test") {
		t.Errorf("label must target the cluster context, got %q", joined)
	}
}

// Kube-OVN does not replace kube-proxy, so the Talos patch must only remove
// Flannel — same as Calico, unlike Cilium.
func TestTalosCNIPatchKubeOVNKeepsKubeProxy(t *testing.T) {
	for _, mm := range []string{"1.13", "1.14", "1.15", ""} {
		patch := talosCNIPatch(cluster.CNIKubeOVN, mm)
		if strings.Contains(patch, "KubeProxyConfig") || strings.Contains(patch, "proxy") {
			t.Errorf("[%s] kube-ovn patch must not disable kube-proxy:\n%s", mm, patch)
		}
	}
}

// A second cluster must be creatable without flags, so a defaulted port that is
// taken moves to the recognisable fallback (80 -> 8080, 443 -> 8443).
func TestSpecPortRequestsCarryDefaultsAndFallbacks(t *testing.T) {
	reqs := specPortRequests(cluster.Spec{HTTPPort: 80, HTTPSPort: 443})
	if len(reqs) != 2 {
		t.Fatalf("both ingress ports must be resolved, got %d", len(reqs))
	}
	if reqs[0].Default != cluster.DefaultHTTPPort || reqs[0].Fallback != fallbackHTTPPort {
		t.Errorf("http request wrong: %+v", reqs[0])
	}
	if reqs[1].Default != cluster.DefaultHTTPSPort || reqs[1].Fallback != fallbackHTTPSPort {
		t.Errorf("https request wrong: %+v", reqs[1])
	}

	// Assign must write back to the right field.
	spec := cluster.Spec{}
	reqs[0].Assign(&spec, 8080)
	reqs[1].Assign(&spec, 8443)
	if spec.HTTPPort != 8080 || spec.HTTPSPort != 8443 {
		t.Errorf("Assign wrote the wrong fields: %+v", spec)
	}
}

// An explicitly chosen port is never silently moved: publishing on a different
// port than the user asked for is worse than failing.
func TestExplicitPortConflictErrorNamesOwnerAndFlag(t *testing.T) {
	err := explicitPortConflictError(
		portRequest{Port: 9090, Flag: "--http-port"}, "other-control-plane")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"9090", "--http-port", "other-control-plane"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message missing %q: %s", want, err)
		}
	}
}

// Every Talos docker cluster defaults to 10.5.0.0/24 and Docker refuses
// overlapping pools, so a second one has to move.
func TestFreeTalosSubnetSkipsTakenOnes(t *testing.T) {
	// The default being free means "pass nothing, let talosctl decide".
	if got := pickTalosSubnet(nil); got != "" {
		t.Errorf("free default should need no --subnet, got %q", got)
	}

	got := pickTalosSubnet([]string{talosDefaultSubnet, "172.18.0.0/16"})
	if got != "10.5.1.0/24" {
		t.Errorf("second cluster should get 10.5.1.0/24, got %q", got)
	}

	got = pickTalosSubnet([]string{talosDefaultSubnet, "10.5.1.0/24", "10.5.2.0/24"})
	if got != "10.5.3.0/24" {
		t.Errorf("fourth cluster should get 10.5.3.0/24, got %q", got)
	}
}
