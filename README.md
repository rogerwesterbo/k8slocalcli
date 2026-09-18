# k8slocalcli

Create local Kubernetes clusters whose nodes run as **Docker containers**, using
either **kind** or **Talos Linux** as the provider — driven by an interactive
terminal UI (built with [`termui/v3`](https://github.com/gizak/termui)).

The TUI asks for the three things you care about: the cluster **name**, how many
**control planes**, and how many **workers**. You can also skip the TUI entirely
and drive everything from flags.

```
  _    ___      _                 _      _ _
 | |__( _ )___ | |___  __ __ _ | | __| (_)
 | / /| _ (_-< | / _ \/ _/ _` || |/ _| | |
 |_\_\\___/__/ |_\___/\__\__,_||_|\__|_|_|
        local kubernetes clusters · kind + talos
```

## Why

This is a Go port of the bash tooling in the sibling
[`createlocalk8s`](../createlocalk8s) project. The provider model, kind config
generation, and the Talos support matrix are carried over from there; this
version adds a typed, testable codebase and an interactive TUI.

## Requirements

- [Docker](https://www.docker.com/) (running) — the nodes are containers.
- [`kubectl`](https://kubernetes.io/docs/tasks/tools/) — to talk to the cluster.
- For the **kind** provider: [`kind`](https://kind.sigs.k8s.io/).
- For the **talos** provider: [`talosctl`](https://www.talos.dev/) (`brew install siderolabs/tap/talosctl`).
- For the **Cilium**/**Calico**/**Kube-OVN** CNIs: [`helm`](https://helm.sh/) (not needed for the default CNI).

Each provider checks its own prerequisites before doing anything and prints
install hints if a tool is missing.

## Install / build

```sh
make build-cli          # builds ./bin/k8slocalcli
# or
make install            # go install into your GOBIN
```

## Usage

### Interactive (TUI)

```sh
k8slocalcli create
```

You'll get a form:

| Field              | Keys                                                  |
|--------------------|------------------------------------------------------|
| Provider           | `←` / `→` to toggle **kind** ↔ **talos**             |
| Cluster name       | type (lowercase letters, digits, `-`)                |
| Control planes     | `←` / `→` to adjust, or type a number                |
| Workers            | `←` / `→` to adjust, or type a number                |
| Kubernetes version | `←` / `→` to cycle versions (defaults to latest)     |
| CNI                | `←` / `→` to choose **default** / **cilium** / **calico** / **kube-ovn** |

`↑`/`↓` or `Tab` move between fields. `Enter` advances to the next field; on the
**Create** button it builds the cluster. There are explicit **Create** and
**Cancel** buttons at the bottom — `←`/`→` switches between them. `Esc` cancels
at any time.

The version list is provider-specific; switching provider resets it to that
provider's latest version. Cilium/Calico/Kube-OVN are installed via Helm, so
`helm` must be on your PATH when choosing them.

### Non-interactive (flags)

Passing `--name` skips the TUI:

```sh
# kind cluster with 1 control plane and 2 workers
k8slocalcli create --name dev --provider kind --workers 2

# talos cluster
k8slocalcli create --name talosdev --provider talos --workers 1

# pin a Kubernetes version and custom ingress host ports
k8slocalcli create --name dev --k8s-version v1.33.12 --http-port 8080 --https-port 8443

# use Cilium instead of the default CNI
k8slocalcli create --name dev --cni cilium

# use Kube-OVN instead of the default CNI
k8slocalcli create --name dev --cni kube-ovn
```

Flags:

```
--name             cluster name (omit for interactive TUI)
--provider         kind | talos                     (default kind)
--control-planes   number of control planes         (default 1)
--workers          number of workers                (default 0)
--k8s-version      Kubernetes version               (default: provider's newest)
--cni              default | cilium | calico | kube-ovn  (default default)
--http-port        host port mapped to ingress :80  (default 80)
--https-port       host port mapped to ingress :443 (default 443)
```

### List and delete

```sh
k8slocalcli list                 # clusters from both providers + kubectl context
k8slocalcli delete               # interactive picker of existing clusters
k8slocalcli delete dev           # delete by name (provider auto-detected)
k8slocalcli delete dev --provider talos
```

`list` is aliased to `ls`, and `delete` to `d` / `rm`. Running `delete` with no
name opens a picker (`↑`/`↓` to move, `Enter` to select, then `y` to confirm,
`Esc` to cancel).

### Kubeconfig

Fetch a cluster's admin kubeconfig from its provider (`kind get kubeconfig` for
kind, `talosctl kubeconfig` for talos — with the API server URL rewritten to the
host-mapped port so it is reachable from the host):

```sh
k8slocalcli kubeconfig dev                        # print to stdout
k8slocalcli kubeconfig dev -o ~/.kube/dev.yaml     # write to a file (mode 0600)
k8slocalcli kubeconfig dev --merge                 # merge into ~/.kube/config and switch context
k8slocalcli kubeconfig                             # pick a cluster interactively
k8slocalcli kubeconfig talosdev --provider talos
```

The provider is auto-detected like `delete`. `kubeconfig` is aliased to `kc`;
`--output` and `--merge` are mutually exclusive.

## Provider notes

- **kind**: the first control-plane node is labelled `ingress-ready` and gets the
  `80`/`443` host port mappings so you can run an ingress controller. Node images
  are pinned by digest per Kubernetes version (see
  `internal/provider/versions.go`).
- **talos**: clusters are created with `talosctl cluster create docker`. The
  Docker backend provisions a **single control plane** — if you request more,
  the tool warns and creates one (use the QEMU backend, not covered here, for
  multi-control-plane Talos). State is stored under `~/.k8slocalcli/clusters`.
  The Kubernetes version defaults to the newest entry in the Talos support
  matrix for your installed `talosctl` (Talos 1.14 → Kubernetes 1.37.0, with
  1.33–1.37 selectable).
- **CNI**: the default keeps the provider's built-in CNI (kindnet for kind,
  Flannel for Talos). Choosing **Cilium**, **Calico** or **Kube-OVN** disables
  the default CNI (via the kind config / a Talos machine-config patch) and
  installs the chosen one with Helm using the values ported from
  `createlocalk8s`. On Talos 1.14+ the patch deletes the `KubeFlannelCNIConfig`
  document (and sets `KubeProxyConfig` `enabled: false` for Cilium); older
  `talosctl` versions get the legacy `cluster.network.cni` / `cluster.proxy`
  patch, which 1.14 rejects. With a custom CNI,
  nodes stay `NotReady` until it finishes installing — this is expected.
- **Kube-OVN**: control-plane nodes are labelled `kube-ovn/role=master` before
  the Helm install, because the chart resolves the `ovn-central` members with a
  `lookup` over that label and fails to render without it. The pod/service
  CIDRs are passed explicitly to match each provider (kind is dual-stack
  `10.244.0.0/16,fd00:10:244::/56` + `10.96.0.0/16,fd00:10:96::/112`; the
  talosctl Docker backend is IPv4-only `10.244.0.0/16` + `10.96.0.0/12`).
  Kube-proxy stays enabled — Kube-OVN does not replace it.

  > `DISABLE_MODULES_MANAGEMENT=true` is set for **both** providers. `ovs-ovn`
  > otherwise runs `ovs-ctl load-kmod` at startup and exits when the modprobe
  > fails, crash-looping while the control plane still looks perfectly healthy.
  > Talos needs it because it manages modules declaratively and has a read-only
  > rootfs (hence the `/var/lib` host paths too); kind needs it on Docker
  > Desktop, where the shared LinuxKit kernel has `openvswitch` compiled *in* —
  > listed in `modules.builtin` with no `.ko` to load — so the modprobe both
  > fails and is unnecessary. OVS then comes up on the kernel datapath.
- **Several clusters at once**: clusters run side by side in Docker. kind puts
  them all on the shared `kind` network and Docker picks a random host port per
  API server, so the only thing that cannot be shared is the ingress host ports.
  They are resolved *before* creation starts (kind would otherwise pull the node
  image and start every node before Docker rejects the bind, then roll the
  cluster back):
  - a port left at its default that is taken moves to the next free fallback —
    `80` → `8080` → `8081`…, `443` → `8443` → `8444`… — and the new port is
    printed, so a second cluster needs no flags;
  - a port you passed explicitly is never moved: that is an error, since
    publishing on a port you did not ask for is worse than failing.

  For **talos** the Docker network subnet also collides — every Talos cluster
  defaults to `10.5.0.0/24` and Docker refuses overlapping pools — so a free
  `10.5.x.0/24` is passed to `talosctl --subnet` when the default is in use.
- **No workers**: when a cluster has 0 workers, the control-plane `NoSchedule`
  taint is removed so workloads can run on it.

## Development

```sh
make build         # build all packages
make test          # unit tests with coverage
make lint          # golangci-lint
make fix           # auto-fix lint issues
make gosec         # security static analysis
make govulncheck   # known-vulnerability scan
```

### Layout

```
cmd/k8slocalcli      entrypoint
internal/cluster     provider-agnostic Spec + validation
internal/runner      shared streaming command runner
internal/provider    Provider interface, registry, kind + talos, version tables
internal/tui         termui/v3 interactive form
internal/cli         cobra command tree (create / list / delete / kubeconfig)
```

Both providers share `internal/runner` and the `internal/provider` interface, and
`cluster.Spec.Validate()` is the single validation path for both the TUI and the
flag-driven entry point.
