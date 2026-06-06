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

- **`internal/tui` (`form.go`)** — the `termui/v3` form. termui has no text-input
  widget, so input is hand-rolled: a single event loop dispatches keys to a
  focused field index. It returns a `cluster.Spec` and never creates clusters
  itself.

- **`internal/cli` (`root.go`)** — cobra tree (`create`/`list`/`delete`).
  `create` launches the TUI **only when `--name` is empty**; otherwise it runs
  non-interactively from flags. `delete` auto-detects the owning provider by
  asking each `Provider.Exists`. `list`/`delete` degrade gracefully when Docker
  is down.

## Provider-specific gotchas

- **Talos Docker backend = single control plane.** `talosctl cluster create
  docker` ignores `--controlplanes`; the talos provider warns and creates one if
  more are requested. Talos state lives in `~/.k8slocalcli/clusters`.
- **kind** puts the `ingress-ready` label and the `80`/`443` host port mappings
  on the *first* control-plane node only (`kindContext`/`kindConfig`).
- **Version selection**: an empty `Spec.K8sVersion` means "provider's newest" —
  kind uses `kindNodeImages[0]`, talos uses the newest entry in the support
  matrix for the installed `talosctl` (parsed from `talosctl version --client`).

## Testing notes

End-to-end creation requires a running Docker daemon and is **not** covered by
`go test` (which only exercises pure logic: spec validation, registry, kind
config generation, version lookups, the runner against `echo`/`false`). Verify
real cluster creation manually with Docker running:
`./bin/k8slocalcli create --name demo --provider kind --workers 1`.
