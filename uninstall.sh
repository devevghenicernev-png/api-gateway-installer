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
    echo "  - OpenObserve + Fluent Bit"
    echo ""
    print_info "Your services on ports 3000, 3001, 3002, etc. will NOT be touched"
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
    print_info "Stopping API Gateway services..."
    
    # Stop API Gateway services
    if systemctl is-active --quiet api-gateway-watch; then
        systemctl stop api-gateway-watch
        print_success "Stopped api-gateway-watch service"
    fi
    
    if systemctl is-enabled --quiet api-gateway-watch 2>/dev/null; then
        systemctl disable api-gateway-watch
        print_success "Disabled api-gateway-watch service"
    fi
    
    # Stop OpenObserve/Fluent Bit services (NOT user services!)
    for service in openobserve fluent-bit goaccess-dashboard; do
        if systemctl is-active --quiet $service 2>/dev/null; then
            systemctl stop $service
            print_success "Stopped $service service"
        fi
        
        if systemctl is-enabled --quiet $service 2>/dev/null; then
            systemctl disable $service
            print_success "Disabled $service service"
        fi
    done
    
    print_info "Your services on ports 3000, 3001, 3002 etc. are still running"
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
    
    # Remove systemd services
    for service in api-gateway-watch openobserve fluent-bit goaccess-dashboard; do
        if [ -f /etc/systemd/system/$service.service ]; then
            rm /etc/systemd/system/$service.service
            print_success "Removed $service.service"
        fi
    done
    
    systemctl daemon-reload
    print_success "Systemd reloaded"
    
    # Remove OpenObserve/Fluent Bit directories
    for dir in /opt/openobserve /var/www/dashboard /etc/goaccess /etc/fluent-bit; do
        if [ -d "$dir" ]; then
            rm -rf "$dir"
            print_success "Removed $dir"
        fi
    done
    
    # Remove dashboard scripts
    for script in goaccess-dashboard fluent-bit; do
        if [ -f /usr/local/bin/$script ]; then
            rm /usr/local/bin/$script
            print_success "Removed /usr/local/bin/$script"
        fi
    done
    
    # Remove password file
    if [ -f /etc/nginx/.htpasswd ]; then
        rm /etc/nginx/.htpasswd
        print_success "Removed dashboard password file"
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
    print_info "Note: The following were NOT removed:"
    echo "  - System packages (nginx, jq, inotify-tools, curl, wget)"
    echo "  - Your services running on ports 3000, 3001, 3002, etc."
    echo ""
    print_info "To remove system packages manually:"
    echo "  sudo apt-get remove nginx jq inotify-tools"
    echo ""
    print_info "Your backend services are still running and untouched!"
    echo ""
}

main "$@"
