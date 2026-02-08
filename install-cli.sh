#!/bin/bash

set -e

# API Gateway Full Installer via curl
# Usage: sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/devevghenicernev-png/api-gateway-installer/main/install-cli.sh)"

REPO="https://github.com/devevghenicernev-png/api-gateway-installer.git"
BRANCH="${BRANCH:-main}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info()    { echo -e "${BLUE}ℹ${NC} $1"; }
success() { echo -e "${GREEN}✓${NC} $1"; }
error()   { echo -e "${RED}✗${NC} $1"; }
warn()    { echo -e "${YELLOW}⚠${NC} $1"; }

if [ "$EUID" -ne 0 ]; then
    error "This script must be run as root"
    echo ""
    echo "  sudo bash -c \"\$(curl -fsSL https://raw.githubusercontent.com/devevghenicernev-png/api-gateway-installer/main/install-cli.sh)\""
    echo ""
    exit 1
fi

echo ""
echo -e "${BLUE}╔════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║       API Gateway Installer            ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════╝${NC}"
echo ""

# Create temp directory for the full repo
TMP_DIR=$(mktemp -d)
cleanup() { rm -rf "$TMP_DIR"; }
trap cleanup EXIT

# Clone the full repo (needed for modules, scripts, web-ui)
if command -v git >/dev/null 2>&1; then
    info "Cloning repository..."
    git clone --depth 1 --branch "$BRANCH" "$REPO" "$TMP_DIR/repo" 2>/dev/null
    success "Repository cloned"
else
    # No git — download tarball
    info "Downloading repository archive..."
    curl -fsSL "https://github.com/devevghenicernev-png/api-gateway-installer/archive/refs/heads/$BRANCH.tar.gz" \
        | tar -xz -C "$TMP_DIR"
    mv "$TMP_DIR/api-gateway-installer-$BRANCH" "$TMP_DIR/repo"
    success "Repository downloaded"
fi

echo ""
info "Starting interactive installation..."
echo ""

# Run the installer from within the repo directory
# stdin is free because we used bash -c "$(curl ...)" pattern
cd "$TMP_DIR/repo"
bash install.sh
