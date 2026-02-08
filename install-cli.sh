#!/bin/bash

set -e

# API Gateway Full Installer via curl
# One-liner: curl -fsSL https://raw.githubusercontent.com/devevghenicernev-png/api-gateway-installer/main/install-cli.sh | sudo bash
# This downloads and runs the full install.sh script

VERSION="${VERSION:-latest}"
REPO_URL="${REPO_URL:-https://raw.githubusercontent.com/devevghenicernev-png/api-gateway-installer}"
BRANCH="${BRANCH:-main}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_info() {
    echo -e "${BLUE}ℹ${NC} $1"
}

print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
}

check_root() {
    if [ "$EUID" -ne 0 ]; then 
        print_error "This script must be run as root (use sudo)"
        print_info "Run: curl -fsSL $REPO_URL/$BRANCH/install-cli.sh | sudo bash"
        exit 1
    fi
}

main() {
    echo ""
    echo -e "${BLUE}╔════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║   API Gateway Installer              ║${NC}"
    echo -e "${BLUE}║   (Downloading full installer...)    ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════╝${NC}"
    echo ""
    
    check_root
    
    # Create temp directory
    local tmp_dir=$(mktemp -d)
    trap "rm -rf $tmp_dir" EXIT
    
    # Download install.sh
    local install_script="$tmp_dir/install.sh"
    print_info "Downloading installer from GitHub..."
    
    if ! curl -fsSL "$REPO_URL/$BRANCH/install.sh" -o "$install_script"; then
        print_error "Failed to download installer"
        exit 1
    fi
    
    if [ ! -s "$install_script" ]; then
        print_error "Downloaded installer is empty"
        exit 1
    fi
    
    chmod +x "$install_script"
    print_success "Installer downloaded"
    echo ""
    
    # Run the installer
    print_info "Starting full installation..."
    echo ""
    "$install_script"
}

main "$@"
