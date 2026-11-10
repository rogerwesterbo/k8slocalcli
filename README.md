# k8slocalcli

Local Kubernetes clusters in Docker — **kind** or **Talos** — with web UIs and
APIs reachable from your machine at `http://<app>.localtest.me`, and apps such
as Argo CD installed for you.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/rogerwesterbo/k8slocalcli/main/install.sh | bash
```

Installs into `~/.local/bin` (no sudo). The SHA-256 checksum is always
verified, and the Sigstore signature too when
[cosign](https://docs.sigstore.dev/cosign/installation/) is installed. Options:
`--version v0.1.0`, `--prefix <dir>`; see `install.sh --help`. Linux and macOS,
amd64 and arm64.

From source: `make install` (needs Go).

### Updating

```sh
k8slocalcli upgrade            # replace this binary with the latest release (verified like install.sh)
k8slocalcli upgrade --check    # only tell me if there is a newer one
```

After other commands k8slocalcli mentions a newer release, checking GitHub at
most once a day. Set `K8SLOCALCLI_NO_UPDATE_CHECK=1` to turn that off.

### You also need

| Tool | Why | Install |
|------|-----|---------|
| Docker (running) | nodes are containers | [docs.docker.com](https://docs.docker.com/get-docker/) |
| `kind` **or** `talosctl` | the cluster provider | `brew install kind` / `brew install siderolabs/tap/talosctl` |
| `kubectl` | talks to the cluster | `brew install kubectl` |
| `helm` | default install method for apps | `brew install helm` |

`k8slocalcli create` checks for these before it creates anything.

## Quick start

```sh
k8slocalcli create          # interactive form: name, provider, nodes, apps, ...
```

Pick **Argo CD** in the form (or run
`k8slocalcli create --name dev --app argocd`). When it finishes, open
<http://argocd.localtest.me> and log in as `admin` with the password printed at
the end. Later:

```sh
k8slocalcli info dev       # URLs and passwords again, any time
k8slocalcli delete dev
```

## Everyday commands

| Command | What it does |
|---------|--------------|
| `k8slocalcli create` | create a cluster (form; or flags with `--name`) |
| `k8slocalcli list` | list clusters |
| `k8slocalcli info [name]` | URLs, installed apps and their logins |
| `k8slocalcli app list` | apps and presets you can install |
| `k8slocalcli app install` | form: pick a cluster, profile, preset and apps |
| `k8slocalcli app install argocd --cluster dev` | install apps into an existing cluster |
| `k8slocalcli app uninstall` | form: pick a cluster and the installed apps to remove |
| `k8slocalcli app uninstall argocd --cluster dev` | remove apps (`--purge` also deletes their data) |
| `k8slocalcli kubeconfig dev --merge` | add the cluster to `~/.kube/config` and switch to it |
| `k8slocalcli delete [name]` | delete a cluster |
| `k8slocalcli upgrade` | update k8slocalcli itself |

Without a name, `info`, `kubeconfig` and `delete` show a picker. Add `-v` to
any command to see the full output of every tool it runs.

### Common `create` flags

```sh
k8slocalcli create --name dev --provider talos --workers 2
k8slocalcli create --name dev --app argocd --tls verified       # HTTPS trusted by your browser
k8slocalcli create --name dev --app argocd:manifest=cd.localtest.me
k8slocalcli create --name dev --cni cilium                      # cilium | calico | kube-ovn
k8slocalcli create --name dev --expose=false                    # nothing reachable from the host
```

`k8slocalcli create --help` lists all flags (Kubernetes version, ports, Gateway
API channel, install methods, ...).

## Apps

Install a catalog of apps into a cluster — MetalLB, metrics-server,
cert-manager, local-path, NFS, Longhorn, Flux, Argo CD, Crossplane,
CloudNativePG, Valkey, OpenBao, Keycloak, Trivy Operator,
kube-prometheus-stack, k8sdockside and nyancat — or a preset bundle of them:

```sh
k8slocalcli app install                                      # form: cluster, profile, preset, apps
k8slocalcli app install keycloak valkey --cluster dev         # keycloak brings cnpg
k8slocalcli app install nyancat:flux --cluster dev            # via Flux (brings flux)
k8slocalcli create --name dev --preset observability --app nyancat
k8slocalcli app uninstall                                    # form: pick what to remove
```

Each app can be installed with `helm`, `manifest`, `flux` or `argo`, sized for
`dev` (testing) or `production` (replicas, persistence), tuned with
`--values <app>=<file.yaml>`, and installed into any kube context with
`--context`. **[docs/apps.md](docs/apps.md)** describes every app, its logins
and what the profiles change, plus presets, uninstalling and the kind/Talos
differences.

## Reaching services in the cluster

- Every cluster gets a gateway. Apps are served at `http://<app>.localtest.me`
  and `https://<app>.localtest.me` — `localtest.me` always points at
  `127.0.0.1`, so there is nothing to configure.
- The **first** cluster uses ports 80/443. The **next** ones get 8080/8443,
  8081/8444, ... — e.g. `http://argocd.localtest.me:8080`. The ports are
  printed when the cluster is created and by `info`.
- **HTTPS**: `self-signed` (default, browsers warn) or `verified` — a local CA
  in `~/.k8slocalcli/ca` that you are asked to trust once (macOS keychain;
  elsewhere the command to run is printed).
- `--expose=false` publishes no ports. This **cannot be changed later**; you
  are warned before the cluster is created. Use `kubectl port-forward` instead.

## Behind a corporate proxy

If image pulls fail with `x509: certificate signed by unknown authority`, your
network intercepts TLS. Save the proxy's root CA once and every new cluster
trusts it:

```sh
mkdir -p ~/.k8slocalcli/ca-certificates
security find-certificate -c "<root CA name>" -p /Library/Keychains/System.keychain \
  > ~/.k8slocalcli/ca-certificates/corporate-root.pem     # macOS
```

Flux and Argo CD installed by k8slocalcli get the same CAs, so they can pull
charts through the proxy too.

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| A step seems stuck | The spinner line shows what it is waiting for; `-v` shows everything. |
| `ImagePullBackOff` with x509 | See [Behind a corporate proxy](#behind-a-corporate-proxy). |
| `<app>.localtest.me` does not load | The CLI warns if your DNS blocks `localtest.me`; add the printed `/etc/hosts` line. |
| Port 80 already in use | Expected with a second cluster — use the printed port, or pass `--http-port`. |
| Lost an app's password | `k8slocalcli info <name>` |
| An app's step timed out | The error names the step (install, verify, postcheck); `kubectl get events -n <namespace>` shows why. |

## Development and testing

```sh
make build-cli     # ./bin/k8slocalcli
make test          # unit tests
make lint          # golangci-lint
make gosec         # security scan
make precheck      # everything above + govulncheck
```

Unit tests cover the logic (validation, generated configs and manifests,
certificates, status parsing). Creating real clusters needs Docker, so check
changes end to end by hand:

```sh
./bin/k8slocalcli create --name e2e --provider kind --workers 1 --app argocd
curl -s -o /dev/null -w '%{http_code}\n' http://argocd.localtest.me/   # 200 (use :8080 if another cluster has 80)
./bin/k8slocalcli info e2e
./bin/k8slocalcli delete e2e
```

Repeat with `--provider talos`, and with `--tls verified` when touching TLS.

Test `install.sh` without publishing a release by serving archives locally:

```sh
./install.sh --version v0.0.0-test --base-url file:///path/to/dist --prefix /tmp/k8s-bin --skip-cosign
```

## Releasing

Push a tag; [`.github/workflows/release.yml`](.github/workflows/release.yml)
builds the archives, checksums and signs them, and publishes the GitHub release
that `install.sh` downloads:

```sh
git tag v0.1.0 && git push origin v0.1.0
```

## More

- [docs/apps.md](docs/apps.md) — every catalog app, methods, profiles, presets, uninstalling.
- [docs/how-it-works.md](docs/how-it-works.md) — the internals: provider
  details, CNIs, multi-cluster ports and subnets, the gateway and TLS setup,
  the app lifecycle.
