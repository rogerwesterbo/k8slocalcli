package apps

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// smokeContainerDisk is a tiny VM disk (CirrOS) packaged as a container
// image, booted by kubevirt's check and imported by cdi's. Its tag follows
// KubeVirt's releases; amd64 + arm64 builds (checked with `docker manifest
// inspect`).
const smokeContainerDisk = "quay.io/kubevirt/cirros-container-disk-demo:" + kubevirtVersion

// KubeVirt pins. Upstream publishes no Helm chart, only release manifests:
// the operator (kubevirt-operator.yaml) and a KubeVirt custom resource that
// tells it to deploy virt-api, virt-controller, virt-handler and the rest.
// The operator picks those images (and virt-launcher's) at runtime from its
// own tag, so TestImageArchAudit sees only virt-operator; all of them have
// amd64 + arm64 builds for v1.9.1 (checked with `docker manifest inspect`).
const (
	kubevirtVersion   = "v1.9.1"
	kubevirtNamespace = "kubevirt"
)

var kubevirtApp = App{
	Name:        "kubevirt",
	Description: "KubeVirt " + kubevirtVersion + " (virtual machines · amd64, or arm64 with KVM)",
	Hint: "runs VMs as Kubernetes resources (VirtualMachine), managed with virtctl · needs KVM, or amd64 nodes " +
		"(emulated, slow) · not on Apple Silicon Macs: Docker Desktop has no KVM and KubeVirt cannot emulate arm64",
	Category:  CategoryVirtualization,
	Namespace: kubevirtNamespace,
	Detect:    []string{"deployment/virt-operator"},
	// No CRD: uninstall removes every KubeVirt CRD (see operatorCR.remove).
	//
	// Deploying the CR pulls virt-api/controller/handler before Available.
	Timeout: 15 * time.Minute,
	// virt-handler is a privileged DaemonSet with hostPath mounts; upstream's
	// own Namespace carries enforce=privileged.
	Privileged:    true,
	manifestURL:   "https://github.com/kubevirt/kubevirt/releases/download/" + kubevirtVersion + "/kubevirt-operator.yaml",
	unsupported:   kubevirtUnsupported,
	afterManifest: kubevirtDeploy,
	check:         kubevirtSmokeTest,
	notes:         kubevirtNotes,
	beforeRemove:  kubevirtCR.remove,
}

var kubevirtCR = operatorCR{
	ref:       "kubevirt.kubevirt.io/kubevirt",
	crd:       "kubevirts.kubevirt.io",
	namespace: kubevirtNamespace,
	deploysTo: kubevirtNamespace,
	managedBy: "virt-operator",
}

// kubevirtUnsupported keeps KubeVirt off clusters where no VM could ever
// start, instead of failing ten minutes later on the test VM. Docker Desktop
// (its nodes run a "linuxkit" kernel) never exposes /dev/kvm, and without
// KVM only emulation is left — which KubeVirt cannot do on arm64: its webhook
// allows no CPU model but host-passthrough there, and QEMU cannot emulate
// that ("CPU mode 'host-passthrough' for aarch64 qemu domain ... is not
// supported by hypervisor"). Emulation is on only for k8slocalcli clusters.
func kubevirtUnsupported(nodes []Node, external bool) string {
	if slices.ContainsFunc(nodes, func(n Node) bool { return kubevirtCanRunVMs(n, !external) }) {
		return ""
	}
	n := nodes[0]
	why := "KubeVirt cannot emulate VMs on " + n.Arch + " (it allows only the host-passthrough CPU there, which needs KVM)"
	if n.Arch == "amd64" {
		why = "k8slocalcli turns KubeVirt's emulation on only for clusters it created"
	}
	return fmt.Sprintf("no node can run VMs: Docker Desktop (kernel %s) has no /dev/kvm, and %s. "+
		"Use an amd64 machine, or a cluster with KVM (--context)", n.Kernel, why)
}

// kubevirtCanRunVMs reports whether KubeVirt can start VMs on the node:
// always with KVM, which only Docker Desktop's linuxkit kernel is known to
// lack; without it only on amd64 with emulation allowed.
func kubevirtCanRunVMs(n Node, emulation bool) bool {
	if !strings.Contains(n.Kernel, "linuxkit") {
		return true
	}
	return emulation && n.Arch == "amd64"
}

// kubevirtDeploy creates the KubeVirt resource (upstream's kubevirt-cr.yaml
// plus two settings) and waits until the operator reports it Available.
//
//   - useEmulation on k8slocalcli clusters: Docker Desktop's VM has no
//     /dev/kvm, so virt-launcher would refuse every VM. The flag only
//     *allows* QEMU emulation — a node with KVM (kind on a Linux host) still
//     uses it (virt-launcher's KVM domain configurator). Other clusters
//     (--context) keep upstream's default.
//   - uninstallStrategy BlockUninstallIfWorkloadsExist: the default,
//     RemoveWorkloads, lets deleting the resource delete every running VM.
func kubevirtDeploy(ctx context.Context, env *Env) error {
	cr := fmt.Sprintf(`apiVersion: kubevirt.io/v1
kind: KubeVirt
metadata:
  name: kubevirt
  namespace: %s
spec:
  certificateRotateStrategy: {}
  configuration:
    developerConfiguration:
      featureGates: []
      useEmulation: %t
  customizeComponents: {}
  imagePullPolicy: IfNotPresent
  workloadUpdateStrategy: {}
  uninstallStrategy: BlockUninstallIfWorkloadsExist
`, kubevirtNamespace, !env.External())
	return kubevirtCR.deploy(ctx, env, cr)
}

// kubevirtSmokeTest boots a tiny VM and waits until it runs: proves
// virt-handler, virt-launcher and — on Docker nodes — emulation work. The
// first run also pulls virt-launcher, so later VMs start faster.
func kubevirtSmokeTest(ctx context.Context, env *Env) error {
	const name = "k8slocalcli-smoke"
	return env.smokeTest(ctx, smokeObject{
		kind: "vmi", namespace: kubevirtNamespace, name: name,
		waitFor: "condition=Ready", what: "the test VM did not start",
		manifest: fmt.Sprintf(`apiVersion: kubevirt.io/v1
kind: VirtualMachineInstance
metadata:
  name: %s
  namespace: %s
spec:
  terminationGracePeriodSeconds: 0
  domain:
    devices:
      disks:
        - name: containerdisk
          disk:
            bus: virtio
    resources:
      requests:
        memory: 128Mi
  volumes:
    - name: containerdisk
      containerDisk:
        image: %s
`, name, kubevirtNamespace, smokeContainerDisk),
	})
}

func kubevirtNotes(ctx context.Context, env *Env, _ string) {
	emulation, _ := env.kubectlQuiet(ctx, kubevirtCR.args("get",
		"-o", "jsonpath={.spec.configuration.developerConfiguration.useEmulation}")...)
	if strings.TrimSpace(emulation) == "true" {
		fmt.Fprintf(env.Out, "   Emulation: on — VMs run on QEMU without /dev/kvm (slow, fine for testing); nodes with KVM still use it\n")
	}
	fmt.Fprintf(env.Out, "   virtctl:  kubectl krew install virt, or https://github.com/kubevirt/kubevirt/releases/tag/%s\n", kubevirtVersion)
	// kubectl rejects flags before a plugin's name, so --context goes last.
	fmt.Fprintf(env.Out, "   Try:      kubectl --context %[1]s apply -f https://kubevirt.io/labs/manifests/vm.yaml && kubectl virt start testvm --context %[1]s\n", env.KubeContext)
	fmt.Fprintf(env.Out, "   VMs:      kubectl --context %s get vms,vmis -A\n", env.KubeContext)
}

// CDI pins (Containerized Data Importer: imports disk images into PVCs as
// DataVolumes, for KubeVirt VMs). Like KubeVirt: an operator manifest plus a
// CDI resource. The operator manifest names every image (cdi-operator's
// image: field, the rest as *_IMAGE env values), so TestImageArchAudit sees
// them all.
const (
	cdiVersion   = "v1.66.1"
	cdiNamespace = "cdi"
)

var cdiApp = App{
	Name:        "cdi",
	Aliases:     []string{"containerized-data-importer"},
	Description: "CDI " + cdiVersion + " (disk images → PVCs for KubeVirt)",
	Hint:        "imports VM disk images (URL, registry, upload) into PVCs as DataVolumes · needs a default StorageClass (local-path)",
	Category:    CategoryVirtualization,
	Namespace:   cdiNamespace,
	Detect:      []string{"deployment/cdi-operator"},
	// No CRD: uninstall removes every CDI CRD (see operatorCR.remove).
	Timeout:     15 * time.Minute,
	manifestURL: "https://github.com/kubevirt/containerized-data-importer/releases/download/" + cdiVersion + "/cdi-operator.yaml",
	// Every DataVolume becomes a PVC with no StorageClass named, and imports
	// use scratch space from the default one.
	preflight:     requireDefaultStorageClass,
	afterManifest: cdiDeploy,
	configure:     cdiLocalPathProfiles,
	check:         cdiSmokeTest,
	notes: func(_ context.Context, env *Env, _ string) {
		fmt.Fprintf(env.Out, "   Import:   a DataVolume with source http (qcow2/raw/iso URL) or registry (containerdisk); see https://github.com/kubevirt/containerized-data-importer/blob/%s/doc/datavolumes.md\n", cdiVersion)
		fmt.Fprintf(env.Out, "   Status:   kubectl --context %s get datavolumes -A\n", env.KubeContext)
	},
	beforeRemove: cdiCR.remove,
}

var cdiCR = operatorCR{
	ref:       "cdi.cdi.kubevirt.io/cdi",
	crd:       "cdis.cdi.kubevirt.io",
	deploysTo: cdiNamespace,
	managedBy: "cdi-operator",
}

// cdiDeploy creates the CDI resource (upstream's cdi-cr.yaml plus
// uninstallStrategy: the default lets deleting it delete every DataVolume)
// and waits until it is Available.
func cdiDeploy(ctx context.Context, env *Env) error {
	const cr = `apiVersion: cdi.kubevirt.io/v1beta1
kind: CDI
metadata:
  name: cdi
spec:
  config:
    featureGates:
      - HonorWaitForFirstConsumer
  imagePullPolicy: IfNotPresent
  infra:
    nodeSelector:
      kubernetes.io/os: linux
  workload:
    nodeSelector:
      kubernetes.io/os: linux
  uninstallStrategy: BlockUninstallIfWorkloadsExist
`
	return cdiCR.deploy(ctx, env, cr)
}

// localPathProvisioners are local-path's provisioner names: kind's and the
// upstream manifest's, and the local-path chart's (cluster.local/<release>).
var localPathProvisioners = []string{"rancher.io/local-path", "cluster.local/" + localPathApp.chart.Release}

// cdiLocalPathProfiles tells CDI which access and volume mode local-path
// volumes have. CDI knows them only for provisioners in its own table, which
// local-path is not in; without this every DataVolume that leaves them to
// CDI (spec.storage, the recommended form) is rejected with "Spec is missing
// accessMode and volumeMode, cannot get access mode from StorageProfile".
// cdi-controller creates a StorageProfile per StorageClass shortly after it
// starts, hence the retry.
func cdiLocalPathProfiles(ctx context.Context, env *Env) error {
	out, err := env.kubectlQuiet(ctx, "get", "storageclass", "-o",
		`jsonpath={range .items[*]}{.metadata.name} {.provisioner}{"\n"}{end}`)
	if err != nil {
		return err
	}
	const patch = `{"spec":{"claimPropertySets":[{"accessModes":["ReadWriteOnce"],"volumeMode":"Filesystem"}]}}`
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		sc, provisioner, _ := strings.Cut(l, " ")
		if !slices.Contains(localPathProvisioners, provisioner) {
			continue
		}
		if err := retry(ctx, remaining(ctx, time.Minute), 3*time.Second, func(ctx context.Context) error {
			_, err := env.kubectlQuiet(ctx, "patch", "storageprofile", sc, "--type", "merge", "-p", patch)
			return err
		}); err != nil {
			return fmt.Errorf("setting the access mode of StorageProfile %s: %w", sc, err)
		}
	}
	return nil
}

// cdiSmokeTest imports a containerdisk into a DataVolume and waits for it to
// succeed: proves the importer runs and writes to the default StorageClass.
// pullMethod node lets the node's container runtime pull the image, which
// trusts the corporate CA k8slocalcli gives the nodes; the importer pod
// itself would not.
func cdiSmokeTest(ctx context.Context, env *Env) error {
	const name = "k8slocalcli-smoke"
	return env.smokeTest(ctx, smokeObject{
		kind: "datavolume", namespace: cdiNamespace, name: name,
		waitFor: "jsonpath={.status.phase}=Succeeded", what: "the test import did not succeed",
		manifest: fmt.Sprintf(`apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: %s
  namespace: %s
  annotations:
    # Import now, although local-path binds a volume only for its first pod.
    cdi.kubevirt.io/storage.bind.immediate.requested: "true"
spec:
  source:
    registry:
      url: docker://%s
      pullMethod: node
  storage:
    resources:
      requests:
        storage: 256Mi
`, name, cdiNamespace, smokeContainerDisk),
	})
}
