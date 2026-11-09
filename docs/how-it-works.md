# How k8slocalcli works

Background for contributors and the curious. Day-to-day usage is in the
[README](../README.md); the maintainer notes in [CLAUDE.md](../CLAUDE.md) go
deeper into the code.

## Flow

`create` (form or flags) → `cluster.Spec` → provider (`kind` / `talos`) creates
the nodes → `apps` installs the gateway stack and the chosen apps with
`kubectl`/`helm`.

```
cmd/k8slocalcli      entrypoint
internal/cluster     provider-agnostic Spec + validation (single path for form and flags)
internal/provider    Provider interface, kind + talos, version tables, host ports, CNIs
internal/apps        gateway stack (Gateway API, Envoy, TLS) and the app catalog
internal/engine      container-engine seam (Docker today; Podman later)
internal/progress    spinners and live status for long steps
internal/runner      shared command runner
internal/tui         termui/v3 form and pickers
internal/cli         cobra commands
```

## Exposing services

With host exposure (the default) the cluster gets:

1. the **Gateway API** CRDs on the chosen channel (`standard` or
   `experimental`; manifest only — upstream ships no Helm chart), pinned to the
   version the controller is built against,
2. **Envoy Gateway** (Helm or manifest) as the controller,
3. a certificate for `*.localtest.me` — self-signed, or issued by cert-manager
   from a local CA in `~/.k8slocalcli/ca` that is name-constrained to
   `localtest.me` (it cannot vouch for any other domain) and shared by all
   clusters,
4. a shared `Gateway` (`k8slocalcli-gateway/local`) with HTTP and HTTPS
   listeners that apps attach `HTTPRoute`s / `GRPCRoute`s to.

The host ports are published to fixed NodePorts **30080/30443** on the first
control-plane node (kind `extraPortMappings`, talos `--exposed-ports`). Envoy's
proxy pods are pinned to that node: Docker Desktop's port forwarder delivers
packets with checksums left to offload, and forwarding them to another node
over the CNI's VXLAN tunnel drops them. Host ports cannot be added after a
cluster is created, which is why exposure is decided up front.

`info` reads everything back from the live cluster (ports from Docker, hosts
from routes, credentials from secrets); nothing is cached on disk.

## Providers

- **kind**: node images are pinned by digest per Kubernetes version
  (`internal/provider/versions.go`).
- **talos**: `talosctl cluster create docker`. The Docker backend provisions a
  **single control plane**; asking for more warns and creates one. State lives
  in `~/.k8slocalcli/clusters`. The Kubernetes version defaults to the newest in
  the support matrix of your installed `talosctl`. The Talos discovery service
  is disabled; a single-host cluster does not need it.
- **No workers**: the control-plane `NoSchedule` taint is removed so workloads
  can run.

## CNIs

The default keeps the provider's built-in CNI (kindnet / Flannel). **Cilium**,
**Calico** and **Kube-OVN** disable it (kind config / Talos machine-config
patch) and are installed with Helm using values ported from `createlocalk8s`.
Nodes stay `NotReady` until the CNI is up — expected.

- Talos 1.14+ patches delete the `KubeFlannelCNIConfig` document (and set
  `KubeProxyConfig enabled: false` for Cilium); older `talosctl` gets the legacy
  `cluster.network.cni` / `cluster.proxy` patch, which 1.14 rejects.
- **Kube-OVN**: control planes are labelled `kube-ovn/role=master` before the
  Helm install (the chart `lookup`s it). Pod/service CIDRs are passed per
  provider (kind dual-stack `10.244.0.0/16,fd00:10:244::/56` +
  `10.96.0.0/16,fd00:10:96::/112`; talos IPv4 `10.244.0.0/16` + `10.96.0.0/12`).
  `DISABLE_MODULES_MANAGEMENT=true` is set for both providers: otherwise
  `ovs-ovn` runs `ovs-ctl load-kmod`, fails (Talos manages modules
  declaratively; Docker Desktop's kernel has `openvswitch` built in), and
  crash-loops while the control plane looks healthy.

## Several clusters at once

kind puts all clusters on the shared `kind` network and Docker picks a random
API-server port each, so only the gateway host ports collide. They are resolved
**before** creation (kind would otherwise start every node, fail the bind, and
roll back):

- a defaulted port that is taken moves to the next free fallback —
  `80` → `8080` → `8081`…, `443` → `8443` → `8444`… — and the new port is
  printed;
- a port passed explicitly is never moved; a conflict is an error.

Talos clusters each get their own Docker network, all defaulting to
`10.5.0.0/24`; a free `10.5.x.0/24` is passed to `talosctl --subnet` when the
default is in use.

## Corporate proxies

CA files in `~/.k8slocalcli/ca-certificates/*.pem|*.crt` (plus `--ca-cert`)
are trusted by every node: on Talos through a `TrustedRootsConfig` machine-config
patch (without it etcd's image pull fails and bootstrap hangs in `Preparing`),
on kind by installing them into each node's system store right after creation
and restarting containerd.
