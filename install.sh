#!/usr/bin/env bash
# install.sh — download and install the k8slocalcli CLI into your user profile.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/rogerwesterbo/k8slocalcli/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/rogerwesterbo/k8slocalcli/main/install.sh | bash -s -- --version v0.1.0
#   ./install.sh --prefix /usr/local/bin
#
# Flags:
#   --version <tag>      install a specific release (default: latest)
#   --prefix  <dir>      install directory (default: $HOME/.local/bin)
#   --skip-cosign        skip Sigstore signature verification (SHA-256 still enforced)
#   --skip-checksum      skip SHA-256 verification (not recommended)
#   --base-url <url>     download from a mirror instead of GitHub releases
#   -h | --help          show this help

set -euo pipefail

REPO="rogerwesterbo/k8slocalcli"
BINARY="k8slocalcli"
VERSION=""
PREFIX=""
BASE_URL=""
SKIP_COSIGN=0
SKIP_CHECKSUM=0

log()  { printf '==> %s\n' "$*" >&2; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
install.sh — download and install the k8slocalcli CLI into your user profile.

Flags:
  --version <tag>      install a specific release (default: latest)
  --prefix  <dir>      install directory (default: $HOME/.local/bin)
  --skip-cosign        skip Sigstore signature verification (SHA-256 still enforced)
  --skip-checksum      skip SHA-256 verification (not recommended)
  --base-url <url>     download from a mirror instead of GitHub releases
  -h | --help          show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)       VERSION="${2:-}"; shift 2 ;;
    --version=*)     VERSION="${1#*=}"; shift ;;
    --prefix)        PREFIX="${2:-}"; shift 2 ;;
    --prefix=*)      PREFIX="${1#*=}"; shift ;;
    --base-url)      BASE_URL="${2:-}"; shift 2 ;;
    --base-url=*)    BASE_URL="${1#*=}"; shift ;;
    --skip-cosign)   SKIP_COSIGN=1; shift ;;
    --skip-checksum) SKIP_CHECKSUM=1; shift ;;
    -h|--help)       usage; exit 0 ;;
    *)               die "unknown flag: $1 (see --help)" ;;
  esac
done

require() {
  command -v "$1" >/dev/null 2>&1 || die "required tool not found on PATH: $1"
}
require curl
require tar
require mktemp
require uname

# -- Detect platform ---------------------------------------------------------
case "$(uname -s)" in
  Linux)  OS=linux ;;
  Darwin) OS=darwin ;;
  *) die "unsupported OS: $(uname -s) (Linux and macOS are supported)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

# -- Resolve version ---------------------------------------------------------
# Without a token the API allows 60 requests an hour per public address, shared
# by everyone behind the same NAT, so GH_TOKEN/GITHUB_TOKEN is used when set and
# the github.com release page (not rate limited) is the fallback.
latest_from_api() {
  local token="${GH_TOKEN:-${GITHUB_TOKEN:-}}" config=""
  # Passed as a curl config on stdin so the token never shows up in ps.
  if [[ -n "$token" ]]; then
    config="header = \"Authorization: Bearer ${token}\""
  fi
  printf '%s\n' "$config" \
    | curl -fsSL -K - -H "Accept: application/vnd.github+json" \
        "https://api.github.com/repos/${REPO}/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' \
    | head -n1
}

# /releases/latest redirects to the newest release's tag page.
latest_from_web() {
  curl -fsSI "https://github.com/${REPO}/releases/latest" \
    | sed -n 's#^location: .*/releases/tag/\([^[:space:]]*\).*#\1#Ip' \
    | head -n1
}

if [[ -z "$VERSION" ]]; then
  log "resolving latest release"
  VERSION=$(latest_from_api || true)
  if [[ -z "$VERSION" ]]; then
    log "GitHub API unavailable (rate limited?) — resolving from the release page"
    VERSION=$(latest_from_web || true)
  fi
  [[ -n "$VERSION" ]] || die "could not determine the latest release of ${REPO}; pass --version <tag>"
fi

[[ -n "$BASE_URL" ]] || BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
ASSET="${BINARY}-${VERSION}-${OS}-${ARCH}.tar.gz"
CHECKSUMS="${BINARY}-${VERSION}-SHA256SUMS"

# -- Install prefix: the user profile, no sudo needed -------------------------
[[ -n "$PREFIX" ]] || PREFIX="$HOME/.local/bin"
mkdir -p "$PREFIX" || die "cannot create install directory: $PREFIX"
[[ -w "$PREFIX" ]] || die "install directory is not writable: $PREFIX"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

log "installing ${BINARY} ${VERSION} for ${OS}/${ARCH} into ${PREFIX}"
curl -fSL --progress-bar "${BASE_URL}/${ASSET}" -o "${TMP}/${ASSET}" \
  || die "download failed: ${BASE_URL}/${ASSET}"

# -- Verify ------------------------------------------------------------------
if [[ $SKIP_CHECKSUM -eq 0 ]]; then
  curl -fsSL "${BASE_URL}/${CHECKSUMS}" -o "${TMP}/${CHECKSUMS}" \
    || die "could not download ${CHECKSUMS}"
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "${TMP}/${ASSET}" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "${TMP}/${ASSET}" | awk '{print $1}')
  else
    die "no sha256sum or shasum found on PATH (or pass --skip-checksum)"
  fi
  expected=$(awk -v f="${ASSET}" '$2 == f || $2 == "*"f {print $1; exit}' "${TMP}/${CHECKSUMS}")
  [[ -n "$expected" ]] || die "no SHA-256 entry for ${ASSET} in ${CHECKSUMS}"
  [[ "$expected" == "$actual" ]] || die "SHA-256 mismatch for ${ASSET}: expected ${expected}, got ${actual}"
  log "SHA-256 ok"
else
  warn "skipping SHA-256 verification"
fi

if [[ $SKIP_COSIGN -eq 1 ]]; then
  warn "skipping cosign signature verification"
elif ! command -v cosign >/dev/null 2>&1; then
  warn "cosign not found — skipping signature verification (install: https://docs.sigstore.dev/cosign/installation/)"
else
  log "verifying Sigstore signature with cosign"
  curl -fsSL "${BASE_URL}/${ASSET}.cosign.bundle" -o "${TMP}/${ASSET}.cosign.bundle" \
    || die "could not download ${ASSET}.cosign.bundle (or pass --skip-cosign)"
  cosign verify-blob \
    --bundle "${TMP}/${ASSET}.cosign.bundle" \
    --certificate-identity-regexp "^https://github.com/${REPO}/.github/workflows/release.yml@refs/tags/" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    "${TMP}/${ASSET}" >/dev/null \
    || die "cosign signature verification failed for ${ASSET}"
  log "cosign signature ok"
fi

# -- Install -----------------------------------------------------------------
tar -xzf "${TMP}/${ASSET}" -C "${TMP}"
staged="${TMP}/${BINARY}-${VERSION}-${OS}-${ARCH}/${BINARY}"
[[ -f "$staged" ]] || die "unexpected archive layout: ${staged} not found"
install -m 0755 "$staged" "${PREFIX}/${BINARY}"
log "installed ${PREFIX}/${BINARY}"

"${PREFIX}/${BINARY}" --version >/dev/null 2>&1 \
  || warn "${BINARY} was installed but does not run (wrong architecture or corrupted download?)"

# -- PATH hint ---------------------------------------------------------------
case ":$PATH:" in
  *":${PREFIX}:"*) ;;
  *)
    rc="~/.profile"
    case "${SHELL:-}" in
      */zsh)  rc="~/.zshrc" ;;
      */bash) rc="~/.bashrc" ;;
    esac
    warn "${PREFIX} is not on your PATH. Add it with:"
    printf "    echo 'export PATH=\"%s:\$PATH\"' >> %s && source %s\n" "$PREFIX" "$rc" "$rc" >&2
    ;;
esac

# -- What else is needed to create clusters ----------------------------------
missing=()
command -v docker  >/dev/null 2>&1 || missing+=("docker   — https://docs.docker.com/get-docker/ (must be running)")
command -v kubectl >/dev/null 2>&1 || missing+=("kubectl  — brew install kubectl, or https://kubernetes.io/docs/tasks/tools/")
command -v helm    >/dev/null 2>&1 || missing+=("helm     — brew install helm, or https://helm.sh/docs/intro/install/")
if ! command -v kind >/dev/null 2>&1 && ! command -v talosctl >/dev/null 2>&1; then
  missing+=("kind or talosctl — brew install kind, or brew install siderolabs/tap/talosctl")
fi
if [[ ${#missing[@]} -gt 0 ]]; then
  warn "to create clusters you also need:"
  for m in "${missing[@]}"; do printf '    %s\n' "$m" >&2; done
fi

log "get started: ${BINARY} create"
