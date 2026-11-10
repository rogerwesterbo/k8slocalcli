# Apps

k8slocalcli can install a catalog of apps into a cluster — one it created, or
any cluster in your kubeconfig. This page covers what each app gives you and
how installing, sizing and removing them works. How it works inside is in
[how-it-works.md](how-it-works.md#app-lifecycle).

- [Choosing apps](#choosing-apps)
- [Install methods](#install-methods)
- [Profiles: dev or production](#profiles-dev-or-production)
- [The catalog](#the-catalog)
- [Presets](#presets)
- [Your own chart values](#your-own-chart-values)
- [Other clusters](#other-clusters)
- [What is checked](#what-is-checked)
- [Uninstalling](#uninstalling)
- [kind and Talos differences](#kind-and-talos-differences)

## Choosing apps

```sh
k8slocalcli app install                       # form: cluster, profile, preset, apps
k8slocalcli app install keycloak valkey --cluster dev
k8slocalcli create --name dev --preset observability --app nyancat
```

An app is written `name[:method][=hostname]`, e.g. `argocd`,
`argocd:manifest`, `argocd=cd.localtest.me`. `k8slocalcli app list` shows every
app with its methods and default hostname.

Dependencies are added for you: `keycloak` brings `cnpg`, `<app>:flux` brings
`flux`, `<app>:argo` brings `argocd`. A dependency that is already installed is
left alone.

## Install methods

| Method | What happens |
|--------|--------------|
| `helm` | `helm upgrade --install` (default for apps with a chart) |
| `manifest` | the upstream install YAML is applied with `kubectl` |
| `flux` | a Flux `HelmRelease` (plus its source) — Flux installs and keeps it in sync |
| `argo` | an Argo CD `Application` — Argo CD installs and keeps it in sync |

`flux` and `argo` use the same chart and values as `helm`, so switching method
does not change what you get. Flux and Argo CD themselves are installed with
`helm` or `manifest`.

## Profiles: dev or production

| | `dev` (default) | `production` |
|--|--|--|
| Meant for | throwaway testing | testing upgrades, failover and persistence on something shaped like a real setup |
| Replicas | one | two or three where the app supports it |
| Data | ephemeral where the app allows it | persistent volumes on the default StorageClass |
| Modes | dev modes (OpenBao dev server, Keycloak `start-dev`) | real server modes |
| Needs | nothing | a default StorageClass (install `local-path` on Talos) |

`app install` asks when `--profile` is not given; `create --profile` and the
create form set it for the apps installed with the cluster. Nothing here is
meant to carry real production traffic.

## The catalog

Hostnames are under `localtest.me`, which resolves to `127.0.0.1`; on a second
cluster add its port, e.g. `:8080`. Logins are printed after installing and by
`k8slocalcli info <cluster>` at any time.

### Networking

**metallb** — MetalLB, `LoadBalancer` Services (L2). On a k8slocalcli cluster it
gets an address pool from the top of the cluster's Docker network (e.g.
`172.18.255.200-250`). On macOS/Windows those addresses are not reachable from
your machine — use the gateway or `port-forward`. On other clusters no pool is
created; add an `IPAddressPool` yourself.

### Storage

**local-path** — node-local volumes, StorageClass `local-path` (made the
default when there is none). kind already ships it, so on kind it is reported
as installed and left alone. On Talos volumes go to
`/var/local-path-provisioner`.

**nfs** — an NFS server inside the cluster plus its provisioner: StorageClass
`nfs` with **ReadWriteMany** volumes, no NFS server needed elsewhere. dev keeps
the exported data on the server pod's node; production on a 20Gi volume.

**longhorn** — replicated block storage, StorageClass `longhorn`, UI at
`longhorn.localtest.me`. Needs `open-iscsi` on every node, which kind and
Talos nodes running in Docker do not have — you are asked before trying. Meant
for real clusters (`--context`). Replicas: 1 (dev) / 3 (production).

### GitOps

**flux** — Flux, for `<app>:flux` and your own `GitRepository`/`Kustomization`s.

**argocd** — Argo CD, UI at `argocd.localtest.me`, login `admin` with the
password shown by `info`. CLI: `argocd login argocd.localtest.me --grpc-web
--plaintext`.

Behind a TLS-intercepting proxy both are given the CA files in
`~/.k8slocalcli/ca-certificates`, so they can pull charts.

### Platform

**crossplane** — Crossplane; add providers from the Upbound marketplace.

### Data

**cnpg** — the CloudNativePG operator **and** a Postgres cluster `postgres` in
namespace `postgres` (database `app`; login in Secret `postgres/postgres-app`).
1 instance / 1Gi (dev), 3 instances / 10Gi (production).

**valkey** — Valkey (the official chart, not Bitnami's) at
`valkey.valkey.svc:6379`. dev: one instance, no password. production: a
primary with two replicas on volumes and a password in Secret
`valkey/valkey-users`.

### Security

**cert-manager** — cert-manager. Also installed automatically with
`--tls verified`.

**openbao** — OpenBao, UI at `openbao.localtest.me`. dev: the in-memory dev
server, unsealed, token `root`, data lost on restart. production: a real
server with storage that starts **sealed and uninitialised** — run the `bao
operator init` / `unseal` commands that are printed.

**keycloak** — Keycloak at `keycloak.localtest.me` on its own CloudNativePG
database (brings `cnpg`). The admin password is generated (shown by `info`); it
is a bootstrap admin, so create a permanent one. dev: `start-dev`, 1 replica.
production: `start` behind the gateway, 2 replicas.

**trivy-operator** — vulnerability and configuration reports
(`kubectl get vulnerabilityreports -A`, after a few minutes). dev limits scans
to fixable vulnerabilities and two at a time.

### Observability

**metrics-server** — `kubectl top` and HPA metrics.

**kube-prometheus-stack** (also `prometheus`) — Prometheus, Alertmanager and
Grafana at `grafana.localtest.me` (login shown by `info`). Picks up
ServiceMonitors from every namespace. dev: 1 day retention, no volumes.
production: 15 days on a 20Gi volume, Alertmanager and Grafana on volumes.

### Demo

**k8sdockside** — a Kubernetes dashboard at `k8sdockside.localtest.me`, newest
release. Admin login generated (shown by `info`). Every signed-in user acts with
the dashboard's own permissions: `cluster-admin` on dev, read-only on
production — change it with [your own values](#your-own-chart-values). Its data
is on a volume whenever the cluster has a default StorageClass.

**nyancat** — a small demo web app at `nyancat.localtest.me`, newest release.

## Presets

| Preset | Apps |
|--------|------|
| `observability` | metrics-server, kube-prometheus-stack |
| `platform` | cert-manager, openbao, cnpg, keycloak, trivy-operator |
| `gitops` | flux, argocd, nyancat, k8sdockside |
| `data` | cnpg, valkey, nfs |
| `storage` | local-path, nfs |
| `demo` | nyancat, k8sdockside |

`--preset` can be repeated and combined with named apps. In the forms, ←/→
browses presets without changing your selection; Enter uses the one shown, and
`none` clears it.

## Your own chart values

`--values <app>=<file.yaml>` (repeatable) is merged over k8slocalcli's own
values the way Helm merges values files, for the `helm`, `flux` and `argo`
methods. For example, k8sdockside with edit rights instead of cluster-admin:

```yaml
# rbac.yaml → k8slocalcli app install k8sdockside --values k8sdockside=rbac.yaml --reinstall
rbac:
  mode: none                 # readonly | admin | custom (with rbac.rules) | none
  extraClusterRoles: [edit]
```

## Other clusters

`--context <kube-context>` installs into, or uninstalls from, any cluster in
your kubeconfig. Such a cluster gets no gateway routes (use `kubectl
port-forward`), no MetalLB pool, and metrics-server keeps its secure defaults.
You are asked which profile to use.

## What is checked

Every app goes through the same steps, each with its own time limit, and a
failure says which step it was:

1. the cluster answers;
2. tools, dependencies and requirements are in place, and whether it is
   already installed — you are asked before it is re-applied (`--reinstall`
   skips the question);
3. install (10 minutes; 15 for Longhorn, Keycloak and kube-prometheus-stack);
4. every workload finished rolling out (5 minutes);
5. it actually works (5 minutes) — e.g. a volume is written, `valkey-cli ping`
   answers, the metrics API responds, the cert-manager webhook accepts requests.

## Uninstalling

```sh
k8slocalcli app uninstall                     # form: pick the installed apps
k8slocalcli app uninstall keycloak cnpg --cluster dev --purge
```

An app is removed the way it was installed: its Application or HelmRelease is
deleted (Argo CD or Flux removes what they deployed), its Helm release is
uninstalled, or its manifest is deleted.

- **Kept** unless you pass `--purge`: the app's namespace, with its generated
  passwords and data volumes. CRDs are always kept — deleting one would delete
  every resource of that kind in the cluster.
- **Refused** while something still needs the app — keycloak needs cnpg, apps
  installed with `:flux`/`:argo` need Flux/Argo CD, verified HTTPS needs
  cert-manager. `--force` removes it anyway.
- **Never touched**: installs k8slocalcli did not make, such as kind's own
  local-path-provisioner. They are listed in the form but cannot be selected.

## kind and Talos differences

- Talos enforces Pod Security `baseline`; apps that need host access (local-path,
  NFS, Longhorn, MetalLB, kube-prometheus-stack, Trivy) get their namespace
  labelled `privileged` automatically.
- Talos has no default StorageClass: install `local-path` first for cnpg,
  Keycloak and the production profile.
- kind ships local-path already; it is detected and left alone.
