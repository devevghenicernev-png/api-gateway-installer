#!/bin/sh
#
# apigw install shim — downloads the latest release tarball, verifies its
# cosign signature + sha256, and drops the binary at /usr/local/bin/apigw.
#
# Cosign is the trust root for releases. If it isn't on PATH the installer
# downloads a pinned cosign build from sigstore (sha256-pinned to embedded
# values below) into a temp dir, uses it for one verification, and discards
# it. No host install of cosign is required.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/devevghenicernev-png/apigw/main/scripts/install.sh | sudo sh
#
# Env overrides:
#   APIGW_VERSION=v0.1.0          pin a version (default: latest)
#   APIGW_REPO=owner/repo         pull from a fork
#   APIGW_BINDIR=/usr/local/bin   install location
#   APIGW_SKIP_COSIGN=1           DANGEROUS — disables signature verification.
#                                 Requires APIGW_ACK_UNSAFE=yes to confirm.

set -eu

# Strip proxy variables before any curl invocation. A proxy here could redirect
# the release download; cosign would still defend, but we don't want the binary
# to land on disk via an attacker-controlled hop in the first place.
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY all_proxy

VERSION="${APIGW_VERSION:-}"
REPO="${APIGW_REPO:-devevghenicernev-png/apigw}"
BINDIR="${APIGW_BINDIR:-/usr/local/bin}"

# Pinned cosign release. Bump together with the per-platform sha256s below.
# Source of truth: https://github.com/sigstore/cosign/releases
COSIGN_VERSION="v3.0.6"
COSIGN_SHA_linux_amd64="c956e5dfcac53d52bcf058360d579472f0c1d2d9b69f55209e256fe7783f4c74"
COSIGN_SHA_linux_arm64="bedac92e8c3729864e13d4a17048007cfafa79d5deca993a43a90ffe018ef2b8"
COSIGN_SHA_linux_armv7="67bd25d32daff5664caf51208c95defcb2ad7ac1296f394fa677bb8bacee62f5"
COSIGN_SHA_darwin_amd64="4c3e7af8372d3ca3296e62fa56f23fcbb5721cc6ac1827900d398f110d7cd280"
COSIGN_SHA_darwin_arm64="5fadd012ae6381a6a29ff86a7d39aa873878852f1073fc90b15995961ecfb084"

# Portable sha256: GNU coreutils ships sha256sum; macOS ships shasum.
# Reads "<hex>  <path>" lines on stdin, exits non-zero on mismatch.
sha256_check() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c -
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c -
  else
    echo >&2 "ERROR: neither sha256sum nor shasum is available on this host."
    exit 1
  fi
}

# bootstrap_cosign sets COSIGN_BIN to a working cosign:
#   - if cosign is on PATH, uses it (any version, the user picked it)
#   - otherwise downloads the pinned cosign for the current OS/ARCH into
#     ${TMP}/cosign and verifies its sha256 against the embedded value
#
# The trust root is this script itself: it was fetched over TLS from the
# repo on main and contains the expected sha256. If GitHub raw is
# compromised the script can lie regardless of any cosign verification.
bootstrap_cosign() {
  if command -v cosign >/dev/null 2>&1; then
    COSIGN_BIN="cosign"
    return 0
  fi

  CASSET=""
  CSHA=""
  case "${OS}_${ARCH}" in
    linux_amd64)   CASSET="cosign-linux-amd64";  CSHA="$COSIGN_SHA_linux_amd64"  ;;
    linux_arm64)   CASSET="cosign-linux-arm64";  CSHA="$COSIGN_SHA_linux_arm64"  ;;
    linux_armv7)   CASSET="cosign-linux-arm";    CSHA="$COSIGN_SHA_linux_armv7"  ;;
    darwin_amd64)  CASSET="cosign-darwin-amd64"; CSHA="$COSIGN_SHA_darwin_amd64" ;;
    darwin_arm64)  CASSET="cosign-darwin-arm64"; CSHA="$COSIGN_SHA_darwin_arm64" ;;
    *)
      echo >&2 "no pinned cosign binary for ${OS}/${ARCH}."
      echo >&2 "install cosign manually: https://docs.sigstore.dev/cosign/installation/"
      echo >&2 "or rerun with APIGW_SKIP_COSIGN=1 APIGW_ACK_UNSAFE=yes (sha256-only)."
      exit 1
      ;;
  esac

  echo "==> cosign not found, fetching ${COSIGN_VERSION} (${CASSET})"
  curl -fsSL --tlsv1.2 --proto '=https' \
    -o "${TMP}/cosign" \
    "https://github.com/sigstore/cosign/releases/download/${COSIGN_VERSION}/${CASSET}"

  echo "==> verifying cosign sha256"
  printf '%s  %s\n' "${CSHA}" "${TMP}/cosign" | sha256_check >/dev/null || {
    echo >&2 "ERROR: cosign sha256 mismatch — refusing to proceed."
    echo >&2 "       Either the sigstore release was tampered with or this"
    echo >&2 "       install.sh is out of date. Bug-report welcome."
    exit 1
  }
  chmod +x "${TMP}/cosign"
  COSIGN_BIN="${TMP}/cosign"
}

main() {
  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
  ARCH=$(uname -m)
  case "$ARCH" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    armv7l)        ARCH=armv7 ;;
    *) echo >&2 "unsupported architecture: $ARCH"; exit 1 ;;
  esac

  if [ -z "$VERSION" ]; then
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
      | sed -n 's/.*"tag_name": "\(v[^"]*\)".*/\1/p' | head -1)
    [ -n "$VERSION" ] || { echo >&2 "could not resolve latest release"; exit 1; }
  fi

  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT

  VERSION_NO_V="${VERSION#v}"
  TARBALL="apigw_${VERSION_NO_V}_${OS}_${ARCH}.tar.gz"
  BASE="https://github.com/${REPO}/releases/download/${VERSION}"

  echo "==> downloading apigw ${VERSION} (${OS}/${ARCH})"
  for f in "$TARBALL" checksums.txt; do
    curl -fsSL --tlsv1.2 --proto '=https' \
      -o "${TMP}/${f}" "${BASE}/${f}"
  done

  if [ -z "${APIGW_SKIP_COSIGN:-}" ]; then
    bootstrap_cosign

    curl -fsSL --tlsv1.2 --proto '=https' \
      -o "${TMP}/checksums.txt.sig" "${BASE}/checksums.txt.sig"
    curl -fsSL --tlsv1.2 --proto '=https' \
      -o "${TMP}/checksums.txt.pem" "${BASE}/checksums.txt.pem"
    echo "==> verifying cosign signature"
    "$COSIGN_BIN" verify-blob \
      --certificate "${TMP}/checksums.txt.pem" \
      --signature   "${TMP}/checksums.txt.sig" \
      --certificate-identity-regexp "https://github.com/${REPO}/.github/workflows/release.yml@.*" \
      --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
      "${TMP}/checksums.txt"
  else
    # Skipping cosign drops a critical supply-chain control. Require the user
    # to ALSO set APIGW_ACK_UNSAFE=yes — `curl … | sh` pipelines can't set env
    # vars by mistake, so this turns a silent footgun into an explicit decision.
    if [ "${APIGW_ACK_UNSAFE:-}" != "yes" ]; then
      echo >&2 "ERROR: APIGW_SKIP_COSIGN=1 disables signature verification."
      echo >&2 "       To proceed anyway, also set APIGW_ACK_UNSAFE=yes."
      echo >&2 "       In nearly all cases the right thing is just to NOT set"
      echo >&2 "       APIGW_SKIP_COSIGN — the installer auto-bootstraps cosign."
      exit 1
    fi
    echo >&2 ""
    echo >&2 "  !! WARNING: cosign verification skipped. !!"
    echo >&2 "  !! Falling back to sha256-only — this does NOT defend against"
    echo >&2 "  !! a compromised release mirror or hijacked GitHub account. !!"
    echo >&2 ""
    # Short pause so a sleepwalking operator notices.
    sleep 3
  fi

  echo "==> verifying sha256"
  (cd "$TMP" && grep " ${TARBALL}\$" checksums.txt | sha256_check)

  echo "==> extracting"
  tar -xzf "${TMP}/${TARBALL}" -C "$TMP"

  echo "==> installing to ${BINDIR}/apigw"
  install -m 0755 "${TMP}/apigw" "${BINDIR}/apigw"

  echo
  "${BINDIR}/apigw" version
  echo
  echo "==> next: ${BINDIR}/apigw install"
}

main "$@"
