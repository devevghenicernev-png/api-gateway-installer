#!/bin/bash

###############################################################################
# API Gateway Uninstaller
# Removes all API Gateway components from the system
###############################################################################

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

print_info() {
    echo -e "${BLUE}ℹ${NC} $1"
}

check_root() {
    if [ "$EUID" -ne 0 ]; then 
        print_error "This script must be run as root (use sudo)"
        exit 1
    fi
}

confirm_uninstall() {
    echo ""
    echo -e "${RED}╔════════════════════════════════════════╗${NC}"
    echo -e "${RED}║     API Gateway Uninstaller           ║${NC}"
    echo -e "${RED}╚════════════════════════════════════════╝${NC}"
    echo ""
    print_warning "This will remove all API Gateway components!"
    echo ""
    echo "The following will be removed:"
    echo "  - Configuration directory: /etc/api-gateway"
    echo "  - Nginx site configuration"
    echo "  - Management scripts"
    echo "  - Auto-reload service"
    echo ""
    read -p "Are you sure you want to continue? (yes/no): " confirm
    
    if [ "$confirm" != "yes" ]; then
        echo ""
        print_info "Uninstallation cancelled"
        exit 0
    fi
}

backup_config() {
    if [ -f /etc/api-gateway/apis.json ]; then
        BACKUP_DIR="$HOME/api-gateway-backup-$(date +%Y%m%d_%H%M%S)"
        mkdir -p "$BACKUP_DIR"
        cp /etc/api-gateway/apis.json "$BACKUP_DIR/"
        print_success "Configuration backed up to $BACKUP_DIR"
    fi
}

stop_services() {
    print_info "Stopping services..."
    
    if systemctl is-active --quiet api-gateway-watch; then
        systemctl stop api-gateway-watch
        print_success "Stopped api-gateway-watch service"
    fi
    
    if systemctl is-enabled --quiet api-gateway-watch 2>/dev/null; then
        systemctl disable api-gateway-watch
        print_success "Disabled api-gateway-watch service"
    fi
}

remove_files() {
    print_info "Removing files..."
    
    # Remove config directory
    if [ -d /etc/api-gateway ]; then
        rm -rf /etc/api-gateway
        print_success "Removed /etc/api-gateway"
    fi
    
    # Remove scripts
    if [ -f /usr/local/bin/generate-nginx-config ]; then
        rm /usr/local/bin/generate-nginx-config
        print_success "Removed generate-nginx-config"
    fi
    
    if [ -f /usr/local/bin/api-manage ]; then
        rm /usr/local/bin/api-manage
        print_success "Removed api-manage"
    fi
    
    if [ -f /usr/local/bin/api-gateway-watch ]; then
        rm /usr/local/bin/api-gateway-watch
        print_success "Removed api-gateway-watch"
    fi
    
    # Remove systemd service
    if [ -f /etc/systemd/system/api-gateway-watch.service ]; then
        rm /etc/systemd/system/api-gateway-watch.service
        systemctl daemon-reload
        print_success "Removed systemd service"
    fi
    
    # Remove nginx config
    if [ -L /etc/nginx/sites-enabled/apis ]; then
        rm /etc/nginx/sites-enabled/apis
        print_success "Removed nginx site symlink"
    fi
    
    if [ -f /etc/nginx/sites-available/apis ]; then
        rm /etc/nginx/sites-available/apis
        print_success "Removed nginx site configuration"
    fi
}

restart_nginx() {
    print_info "Restarting Nginx..."
    
    if nginx -t 2>/dev/null; then
        systemctl restart nginx
        print_success "Nginx restarted successfully"
    else
        print_warning "Nginx configuration may need manual review"
        systemctl restart nginx || true
    fi
}

main() {
    check_root
    confirm_uninstall
    
    echo ""
    print_info "Starting uninstallation..."
    echo ""
    
    backup_config
    stop_services
    remove_files
    restart_nginx
    
    echo ""
    print_success "API Gateway has been completely removed"
    echo ""
    print_info "Note: Dependencies (nginx, jq, inotify-tools) were not removed"
    print_info "To remove them manually:"
    echo "  sudo apt-get remove nginx jq inotify-tools"
    echo ""
}

main "$@"
