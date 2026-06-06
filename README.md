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

You'll get a form with four fields:

| Field          | Keys                                              |
|----------------|---------------------------------------------------|
| Provider       | `←` / `→` to toggle **kind** ↔ **talos**          |
| Cluster name   | type (lowercase letters, digits, `-`)             |
| Control planes | `←` / `→` to adjust, or type a number             |
| Workers        | `←` / `→` to adjust, or type a number             |

`↑`/`↓` or `Tab` move between fields, `Enter` creates the cluster, `Esc` cancels.

### Non-interactive (flags)

Passing `--name` skips the TUI:

```sh
# kind cluster with 1 control plane and 2 workers
k8slocalcli create --name dev --provider kind --workers 2

# talos cluster
k8slocalcli create --name talosdev --provider talos --workers 1

# pin a Kubernetes version and custom ingress host ports
k8slocalcli create --name dev --k8s-version v1.33.12 --http-port 8080 --https-port 8443
```

Flags:

```
--name             cluster name (omit for interactive TUI)
--provider         kind | talos              (default kind)
--control-planes   number of control planes  (default 1)
--workers          number of workers         (default 0)
--k8s-version      Kubernetes version        (default: provider's newest)
--http-port        host port mapped to ingress :80   (default 80)
--https-port       host port mapped to ingress :443  (default 443)
```

### List and delete

```sh
k8slocalcli list                 # clusters from both providers + kubectl context
k8slocalcli delete dev           # provider auto-detected
k8slocalcli delete dev --provider talos
```

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
  matrix for your installed `talosctl`.

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
internal/cli         cobra command tree (create / list / delete)
```

Both providers share `internal/runner` and the `internal/provider` interface, and
`cluster.Spec.Validate()` is the single validation path for both the TUI and the
flag-driven entry point.
