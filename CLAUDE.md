# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

> A workspace-level `../CLAUDE.md` also applies (file-context rule, fact-checking,
> dependency policy, and the shared `make build/test/lint/gosec/fix/govulncheck`
> convention). This file covers what is specific to `k8slocalcli`.

## What this is

A Go CLI that creates local Kubernetes clusters whose nodes run as **Docker
containers**, via two providers — **kind** and **Talos Linux** — driven by an
interactive `termui/v3` TUI (or flags). The cluster-creation logic is a Go port
of the sibling bash project `../createlocalk8s`; when changing provider behavior,
that project is the reference of record (kind config shape, Talos support matrix,
ingress setup).

## Commands

```sh
make build-cli              # build ./bin/k8slocalcli (with version ldflags)
make run                    # go run ... create  (launches the TUI)
make test                   # all tests + coverage.out
make lint                   # golangci-lint (config in .golangci.yml)
make gosec                  # security scan; make govulncheck for vuln scan

go test ./internal/provider/ -run TestKindConfig -v   # run a single test
go test ./internal/cluster/ -run TestSpecValidate -v
```

Tools (`golangci-lint`, `gosec`, `govulncheck`) are auto-installed into `./bin`
by their make targets. The module path is `github.com/rogerwesterbo/k8slocalcli`.

## Architecture

The flow is **CLI/TUI → `cluster.Spec` → `Provider` → `runner` → external CLIs**.
Understanding these four seams is enough to be productive:

- **`internal/cluster` (`spec.go`)** — `Spec` is the provider-agnostic cluster
  description and `Spec.Validate()` is the **single validation path** used by
  *both* the TUI and the `--name` flag path. The `Provider` string enum and
  `Providers` slice defined here drive provider ordering everywhere else.

- **`internal/provider`** — the `Provider` interface plus an `init()`-populated
  `registry` (`Get`/`All`). `kind.go` and `talos.go` implement it; `versions.go`
  holds the pinned `kindest/node` image table and the Talos→Kubernetes support
  matrix. Adding a provider = implement the interface + `register()` it in
  `provider.go`'s `init()` + add its name to `cluster.Providers`.

- **`internal/runner`** — the shared streaming exec helper both providers use to
  invoke `kind`/`talosctl`/`kubectl`/`docker`. All subprocess launches funnel
  through `Runner.run`, which carries a justified `#nosec G204` (the command name
  is always a hardcoded tool; args are pre-validated). Add provider commands by
  calling `runner.New(out).Run/Capture`, not `os/exec` directly.

- **`internal/tui`** — `termui/v3` forms: `form.go` (create, custom
  two-column layout), `appinstall.go` and `appuninstall.go` (both a
  `simpleForm`: a slice of `formField`s with `text`/`adjust`/`enter` closures,
  plus `submit`/`summary`). termui has no text-input widget, so input is
  hand-rolled. Shared pieces: `frame.go` (`runLoop`, `formFrame`
  buttons/help/status, `styleField`), `checklist.go` (the scrolling tick list
  with per-row options/locks/hints, and the `overlay` that shows it over a
  form), `appselection.go` (preset browse/apply + catalog choices, shared by
  create and install). A new form = a `simpleForm` with fields; long lists =
  a `checklist`. `handleKey` is pure and unit-tested without a terminal; forms
  return data and never create, install or remove anything (validation that
  needs the cluster is passed in as a callback, e.g. uninstall's `Check`).
  To drive a form in a pty (e2e), set `TERM=xterm-256color`, wait for the
  alternate screen (`ESC[?1049h`) before typing, and send arrows as `ESC O B`
  (termbox enables keypad mode; `ESC [ B` arrives as Esc, "[", "B").

- **`internal/apps`** — everything installed *after* `Provider.Create`, via
  kubectl/helm only (never imports `provider`; callers pass an `apps.Target`).
  Two halves: the exposure stack (`gateway.go`, `envoy.go`, `tls.go`:
  Gateway API CRDs → controller → TLS secret → shared Gateway) and the app
  catalog. File map for the catalog — put new code where its kind lives:

  | File | Holds |
  |---|---|
  | `catalog.go` | `App` (fields in execution order, rules in its doc comment), `catalog`, `Plan`, presets |
  | `lifecycle.go` | install: `InstallAll`/`Install` → connect → precheck → prepare+install → verify → configure+postcheck → route/notes, each phase with its own timeout (`withTimeout`) |
  | `uninstall.go` | `PlanUninstall`, `DetectInstallation` (typed `InstallState`), `Dependants`, `Uninstall` |
  | `helm.go` / `gitops.go` | the three chart methods: `helmInstall`, `fluxInstall`, `argoInstall`; `renderChart` merges app values + `--values` |
  | `kube.go` | cluster building blocks: `kubectl*`, `apply*`, `exists`, Secrets, namespaces, `defaultStorageClass`, `isTalos`, `retry`, `remaining` |
  | `checks.go` | reusable preflights (`requireDefaultStorageClass`) and functional checks (`storageSmokeTest`) |
  | `notes.go` | `printAccess` = URL + app notes, used by install, skip and `info` |
  | `cabundle.go` | corporate CA bundle for pods that fetch over HTTPS |
  | `values.go` | `ParseSelection`, `ParseValuesFlags`, `mergeValues` |
  | `networking.go` … `demo.go` | one file per `Category` with that category's `App` vars (`xxxApp`) |

  Adding an app: an `xxxApp` var with a static `chart` (+ `values` func)
  and/or `manifestURL` (+ `afterManifest`), `Detect`, and only the hooks it
  needs; add it to `catalog`. Reuse before writing: a Secret →
  `ensurePasswordSecret`, a volume check → `storageSmokeTest`, a webhook
  that needs time → `applyYAMLRetry`/`retry`, a database → `ensurePostgres`,
  host access → `Privileged: true`. Pin versions after checking them
  (`helm show chart`); k8sdockside/nyancat are deliberately unpinned. Notes
  read live state, never `env.Profile` (`info` does not know the profile).
  User `--values` are deep-merged over app values, so values stay plain maps.
  Uninstall finds the method live; manifest installs are only recognised by the
  `k8slocalcli.io/installed-by=manifest` label. Never delete CRDs or protected
  namespaces. Verify new apps on **both** kind and Talos (see the Talos Pod
  Security gotcha below). Adding a gateway controller = implement
  `GatewayController` (contract in `gateway.go`), add it to `controllers` and
  `cluster.GatewayControllers`.

- **`internal/cli` target selection** — app commands embed `targetFlags`
  (`target.go`: `--cluster`/`--provider`/`--context` → `resolve` builds the
  `Env` and checks the connection); `newEnv` + `localTarget` are the only way
  an `apps.Env` is built.

- **`internal/progress`** — `Reporter.Step` runs a long step: on a terminal a
  single redrawn line (spinner · elapsed · status) with command output held
  back; otherwise (CI, pipes, `--verbose`) plain streamed output plus status
  lines. `apps.Env.step` wires it to a live pod summary (`summarizePods`, which
  adds the corporate-CA hint on x509 pull errors). Terminal detection uses the
  window size, not `ModeCharDevice` (`/dev/null` is a char device). Anything
  that prompts must run outside a step, or the prompt is hidden.

- **`internal/engine`** — the container-engine seam (Docker only). Engine CLI
  calls (`ps`, `port`, `network`, `rm`) go through `engine.Current()`, never a
  hardcoded `"docker"`; the package doc lists what a Podman engine needs.

- **`internal/cli` (`root.go`, `kubeconfig.go`, `app.go`)** — cobra tree
  (`create`/`list`/`delete`/`kubeconfig`/`info`/`app`). `info` is read-only and
  stores nothing: apps are found by `App.Detect`, hosts from their route,
  credentials via `App.notes` — keep it that way rather than caching secrets
  under `~/.k8slocalcli` (that dir's `clusters/` is talosctl state, talos-only). `create` launches the TUI **only when
  `--name` is empty**; otherwise it runs non-interactively from flags. `delete`
  and `kubeconfig` auto-detect the owning provider by asking each
  `Provider.Exists` (or show a picker with no name). `kubeconfig` prints
  `Provider.Kubeconfig` to stdout, writes it with `--output`, or calls
  `Provider.MergeKubeconfig` with `--merge`. `app install --context` targets
  any kube context (`Target.Provider == ""`, i.e. `External()`): no routes,
  secure metrics-server defaults, no kind/Talos workarounds. `list`/`delete` degrade gracefully when Docker
  is down.

## Provider-specific gotchas

- **DOCKER_HOST**: talosctl (Docker Go SDK) ignores Docker CLI contexts, so on
  Docker Desktop without `/var/run/docker.sock` it cannot reach the daemon.
  `cli.Execute` calls `runner.EnsureDockerHost`, which exports `DOCKER_HOST`
  from `docker context inspect` when it is unset.
- **Re-creating a name** (`clearExisting` in `cli/root.go`): an existing cluster
  is replaced only after a default-no prompt; leftovers from a failed attempt
  (providers implementing `provider.LeftoverChecker` — Talos: state dir, node
  containers, labelled network; talosctl refuses to create while the state dir
  exists, even empty) get a default-yes prompt. Both clean up via
  `Provider.Delete`. `--yes` skips prompts; without a terminal it errors.
- **Talos machine-config patches** (`talos.go` Create, `trust.go`): always
  disables discovery (1.14+: delete the `DiscoveryServiceConfig` doc; older:
  `cluster.discovery.enabled: false`); adds a `TrustedRootsConfig` from
  `--ca-cert` files plus `~/.k8slocalcli/ca-certificates/*.pem|*.crt` (for
  TLS-intercepting proxies — otherwise etcd's image pull fails with x509 and
  bootstrap hangs); then the CNI patch. Each goes as its own `--config-patch`.
  **kind** gets the same bundle right after `kind create cluster`
  (`kindTrustCAs`: `docker cp` to `/usr/local/share/ca-certificates`,
  `update-ca-certificates`, restart containerd — it caches system roots), before
  the CNI and apps pull anything; Kubernetes' own images are preloaded in the
  node image, so creation itself never needs it.
- **talosconfig contexts**: talosctl renames a new context to `<name>-N` when
  `<name>` already exists (e.g. from a failed attempt), and the old one points
  at a dead port. `talosContextFor` therefore picks the context whose endpoint
  is the live apid host port (`50000/tcp` on `<name>-controlplane-1`), and
  `removeStaleTalosContexts` (in Create and Delete) drops dead `<name>`/`<name>-N`
  contexts. talosctl won't remove the current context.
- **Talos Docker backend = single control plane.** `talosctl cluster create
  docker` ignores `--controlplanes`; the talos provider warns and creates one if
  more are requested. Talos state lives in `~/.k8slocalcli/clusters`.
- **Host exposure** (`Spec.Exposure`, default on): kind `extraPortMappings` /
  talos `--exposed-ports` publish `HTTPPort`/`HTTPSPort` to the fixed NodePorts
  `cluster.GatewayHTTPNodePort`/`GatewayHTTPSNodePort` (30080/30443) on the
  first control plane only — not to container 80/443 — so any controller can
  serve them via its Service. Ports cannot be added after creation,
  hence `Spec.Warnings()` (printed before creating, shown live in the TUI).
  Existing clusters' ports are read back with `provider.GatewayHostPorts`
  (`docker port <GatewayNode> 30080/tcp`).
- **Gateway API CRDs have one owner**: `SetupExposure` installs the upstream
  bundle at the controller's `GatewayAPIVersion()` on the chosen channel.
  Envoy Gateway bundles its own (experimental) copy in both install paths, so
  Helm uses `crds.enabled=false` + `envoy-gateway-crds.yaml` (EG-only CRDs),
  and manifest strips the bundle with `withoutGatewayAPIResources`. Pins:
  EG v1.9.3 ↔ Gateway API v1.6.1 (EG's go.mod) — bump them together. The
  `EnvoyProxy` makes the Service NodePort, **pins the Envoy pods to the
  gateway node** (`kubernetes.io/hostname` = `Provider.GatewayNode`, which is
  also the node name; tolerates the control-plane taint) and uses
  `externalTrafficPolicy: Local`. Never let host traffic be forwarded to another
  node: Docker Desktop's port forwarder (source `192.168.65.1`) delivers
  checksum-offloaded packets, and after kube-proxy NAT + flannel VXLAN the inner
  TCP checksum is never filled in, so the remote node drops every SYN
  (`tcpdump -vv` shows `cksum ... incorrect`) and curl hangs. It only shows with
  workers, since with none the Envoy pod is already local. The TLS secret must exist before the Gateway, or the
  HTTPS listener is not programmed and the NodePort patch has no port to hit.
  Envoy ignores the port in Host matching, so `app.localtest.me:8080` routes.
- **TLS**: self-signed is generated in Go per cluster. Verified = local CA in
  `~/.k8slocalcli/ca` (0600, name-constrained to `localtest.me`, shared by all
  clusters) → cert-manager CA `ClusterIssuer` → `Certificate`; the CA is added
  to the macOS login keychain only after a prompt (never in tests).
- **CNI** (`internal/provider/cni.go`): default keeps the provider's built-in CNI.
  Cilium/Calico/Kube-OVN disable it (kind `disableDefaultCNI`; Talos machine-config patch
  from `talosCNIPatch`, version-dependent: talosctl 1.14+ deletes the
  `KubeFlannelCNIConfig` doc and sets `KubeProxyConfig enabled: false`, older
  versions use the legacy `cluster.network.cni`/`cluster.proxy` fields that
  1.14 rejects) and install via Helm with per-provider values ported from
  `createlocalk8s/scripts/installers/helm.sh`. Talos disables kube-proxy only for
  Cilium (which replaces it). With a custom CNI on **Talos**, `talosctl cluster
  create` would block forever (health checks need Ready nodes), so
  `runTalosCreate` runs it in the background and returns once `apiReachable`,
  then the CNI install brings nodes Ready. Cilium/Calico/Kube-OVN require `helm`
  on PATH. **Kube-OVN** additionally labels the control planes `kube-ovn/role=master`
  *before* `helm upgrade --install` (the chart `lookup`s that label to resolve
  ovn-central and `fail`s the render without it) and passes provider-specific
  pod/service CIDRs from `kubeOVNValues` — they must match what the provider
  actually built (kind: dual-stack kind defaults; talos: IPv4 `10.244.0.0/16` +
  `10.96.0.0/12`). `DISABLE_MODULES_MANAGEMENT=true` goes to **both** providers
  — without it `ovs-ovn` exits on `ovs-ctl load-kmod` and crash-loops while the
  control plane looks healthy (Docker Desktop's LinuxKit kernel has openvswitch
  built in, so there is no `.ko` to modprobe). Talos additionally gets
  upstream's read-only-rootfs `/var/lib` paths.
- **Multi-cluster / host ports** (`internal/provider/ports.go`):
  `ResolveHostPorts` is called from `runCreate` *before* `Provider.Create` and
  **mutates the spec**, because both providers publish the ingress ports on their
  first node and kind only fails after starting every node. A defaulted port that
  is taken advances to `fallbackHTTPPort`/`fallbackHTTPSPort` and up; an
  explicitly chosen port errors instead (never silently reassigned). It asks
  Docker (`docker ps --filter publish=N`) rather than binding a probe socket —
  ports below 1024 need root on macOS and would look taken. Best effort: if
  Docker cannot be queried it returns nil and lets `CheckPrerequisites`/`Create`
  report the real problem.
- **Talos subnets** (`internal/provider/subnet.go`): kind shares one `kind`
  network, but talosctl makes one network per cluster all defaulting to
  `10.5.0.0/24`, and Docker rejects overlapping pools. `freeTalosSubnet` scans
  allocated Docker subnets and `Talos.Create` passes `--subnet` when the default
  is taken. `pickTalosSubnet` holds the pure logic and is the tested seam.
- **Talos Pod Security / read-only root (apps)**: Talos enforces Pod Security
  `baseline` everywhere but kube-system, and kind does not — so an app passing
  on kind can still fail on Talos. Apps needing hostPath, host network or extra
  capabilities set `privileged: true` (namespace labelled
  `pod-security.kubernetes.io/enforce=privileged` before install). `/opt` is
  read-only on Talos, so local-path uses `/var/local-path-provisioner` there
  (`isTalos` checks the nodes' osImage, so `--context` Talos clusters too).
- **Zero workers**: `makeControlPlaneSchedulable` (`schedule.go`) removes the
  control-plane `NoSchedule` taint so a control-plane-only cluster can run pods.
- **Version selection**: an empty `Spec.K8sVersion` means "provider's newest" —
  kind uses `kindNodeImages[0]`, talos uses the newest entry in the support
  matrix for the installed `talosctl` (parsed from `talosctl version --client`).
  `Provider.KubernetesVersions(ctx)` exposes the selectable list (newest first,
  index 0 = latest); the TUI uses it for the version field, and the create
  command pre-computes a `map[provider][]versions` to pass into `tui.Run`.

## Releases and docs

- `install.sh` + `.github/workflows/release.yml` (tag `v*`) publish
  `k8slocalcli-<tag>-<os>-<arch>.tar.gz` (dir `k8slocalcli-<tag>-<os>-<arch>/`
  inside), `k8slocalcli-<tag>-SHA256SUMS` and `<archive>.cosign.bundle` — the
  same layout `internal/pluginmgr` expects of plugins. Keep the three in sync.
  `install.sh --base-url file://...` tests it without a release.
- `k8slocalcli upgrade` (`cli/upgrade.go`) reuses `pluginmgr.Install` with
  `selfEntry()` (same asset names) and `BinaryName` = the running binary, so it
  replaces itself atomically next to where it lives; it refuses `go run` and
  Homebrew paths. `K8SLOCALCLI_RELEASE_URL=http://host/{version}` points it at
  a mirror — that is how to test it before a release exists. The passive
  notice (`startUpdateCheck` in `Execute`) runs only for `vX.Y.Z` builds on a
  terminal, caches GitHub's answer in `~/.k8slocalcli/update-check.json` for
  24h, waits at most 1.5s after the command, and is off with
  `K8SLOCALCLI_NO_UPDATE_CHECK`.
- README is need-to-know (install, usage, testing); internals go in
  `docs/how-it-works.md`, maintainer gotchas here. Per-app user docs live in
  `docs/apps.md` (what each app gives, logins, what dev/production change):
  adding or changing a catalog app means updating its entry there, the app list
  in the README's Apps section, and the presets table if presets changed.

## Testing notes

End-to-end creation (and everything in `internal/apps` that runs kubectl/helm)
requires a running Docker daemon and is **not** covered by `go test` (which only exercises pure logic: spec validation, registry, kind
config generation, version lookups, the runner against `echo`/`false`). Verify
real cluster creation manually with Docker running:
`./bin/k8slocalcli create --name demo --provider kind --workers 1`, then
`curl http://argocd.localtest.me[:port]` after `--app argocd`.
