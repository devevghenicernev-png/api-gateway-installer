#!/bin/sh
#
# apigw install shim — downloads the latest release tarball, verifies its
# cosign signature + sha256, and drops the binary at /usr/local/bin/apigw.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/devevghenicernev-png/apigw/main/scripts/install.sh | sudo sh
#
# Env overrides:
#   APIGW_VERSION=v0.1.0          pin a version (default: latest)
#   APIGW_REPO=owner/repo          pull from a fork
#   APIGW_BINDIR=/usr/local/bin    install location
#   APIGW_SKIP_COSIGN=1            DANGEROUS — disables signature verification.
#                                  Requires APIGW_ACK_UNSAFE=yes to confirm.

set -eu

# Strip proxy variables before any curl invocation. A proxy here could redirect
# the release download; cosign would still defend, but we don't want the binary
# to land on disk via an attacker-controlled hop in the first place.
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY all_proxy

VERSION="${APIGW_VERSION:-}"
REPO="${APIGW_REPO:-devevghenicernev-png/apigw}"
BINDIR="${APIGW_BINDIR:-/usr/local/bin}"

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
    if command -v cosign >/dev/null 2>&1; then
      curl -fsSL --tlsv1.2 --proto '=https' \
        -o "${TMP}/checksums.txt.sig" "${BASE}/checksums.txt.sig"
      curl -fsSL --tlsv1.2 --proto '=https' \
        -o "${TMP}/checksums.txt.pem" "${BASE}/checksums.txt.pem"
      echo "==> verifying cosign signature"
      cosign verify-blob \
        --certificate "${TMP}/checksums.txt.pem" \
        --signature   "${TMP}/checksums.txt.sig" \
        --certificate-identity-regexp "https://github.com/${REPO}/.github/workflows/release.yml@.*" \
        --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
        "${TMP}/checksums.txt"
    else
      echo >&2 "cosign not installed; signature unverified."
      echo >&2 "  install: https://docs.sigstore.dev/cosign/installation/"
      echo >&2 "  or rerun with APIGW_SKIP_COSIGN=1 APIGW_ACK_UNSAFE=yes"
      echo >&2 "  (downgrades to sha256-only — supply-chain protection lost)"
      exit 1
    fi
  else
    # Skipping cosign drops a critical supply-chain control. Require the user
    # to ALSO set APIGW_ACK_UNSAFE=yes — `curl … | sh` pipelines can't set env
    # vars by mistake, so this turns a silent footgun into an explicit decision.
    if [ "${APIGW_ACK_UNSAFE:-}" != "yes" ]; then
      echo >&2 "ERROR: APIGW_SKIP_COSIGN=1 disables signature verification."
      echo >&2 "       To proceed anyway, also set APIGW_ACK_UNSAFE=yes."
      echo >&2 "       Strongly recommended instead: install cosign and re-run."
      echo >&2 "         https://docs.sigstore.dev/cosign/installation/"
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
  (cd "$TMP" && grep " ${TARBALL}\$" checksums.txt | sha256sum -c -)

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
