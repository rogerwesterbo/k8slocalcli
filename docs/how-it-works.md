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

Node-level trust does not reach containers. Flux's source-controller and Argo
CD's repo-server pull charts themselves, so when those CAs exist they get them
too: a `k8slocalcli-ca-bundle` ConfigMap mounted at `/etc/ssl/k8slocalcli`, plus
`SSL_CERT_DIR=/etc/ssl/certs:/etc/ssl/k8slocalcli` (`trustCABundle`, re-applied
on every install of flux/argocd).

## App lifecycle

(What each app is and how to use it: [apps.md](apps.md).)

Every catalog app (`internal/apps`) runs through the same steps
(`lifecycle.go`), each with its own timeout:

| Step | Timeout | What |
|------|---------|------|
| connect | 20s | the kube context exists and `/readyz` (or `/version`) answers |
| precheck | 1m | helm on PATH, Flux/Argo CD CRDs for the gitops methods, dependencies, app preflight (default StorageClass, Longhorn on container nodes), already installed? |
| install | 10m (15m for Longhorn, Keycloak, kube-prometheus-stack) | app `prepare` (Secrets, databases), then helm `--wait` / manifest / Flux HelmRelease Ready / Argo CD Synced+Healthy |
| verify | 5m | `rollout status` for every Deployment, StatefulSet and DaemonSet in the app namespace (or the app's `Workloads`) |
| postcheck | 5m | app `configure` (MetalLB pool, postgres cluster), CRD Established, app `check`, route |

An app describes only what is specific to it: a chart (which gives it the
helm, flux and argo methods; values are a map so all three use the same
description) and/or a manifest, plus the hooks above. `Plan` orders requests
and adds dependencies (`Requires`, and flux/argocd for the gitops methods);
added ones are skipped silently when already installed. An installed app that
Helm does not manage (kind's own local-path-provisioner) is never re-applied
with Helm. Unpinned charts (k8sdockside, nyancat) take the newest release; for
flux/argo it is resolved once with `helm show chart` so every method installs
the same version.

### Uninstall

Nothing is stored on disk; `app uninstall` reads how an app was installed
from the cluster (`DetectInstallation`): an Argo CD Application or Flux
HelmRelease named after the app → delete it (the controller removes what it
deployed; Argo CD through its resources finalizer); a Helm release → `helm
uninstall --wait`; a Deployment labelled `k8slocalcli.io/installed-by=manifest`
(set by every manifest install) → delete the same upstream manifest minus its
CRDs and Namespaces. Anything else present is reported as "not installed by
k8slocalcli" and left alone. Steps: connect → precheck all apps (method,
`Dependants`) before removing any → confirm → remove (5m) → wait until gone
(2m) → with `--purge`, delete the namespaces (5m; data namespaces such as
cnpg's `postgres` first, while the operator can still run finalizers).

