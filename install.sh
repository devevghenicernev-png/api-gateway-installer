#!/bin/bash

###############################################################################
# API Gateway Installer
# Interactive setup script for Nginx-based API Gateway
# 
# This script will:
# - Detect server IP and ask for confirmation
# - Ask what port to use
# - Install all required dependencies
# - Create configuration directory and files
# - Generate management scripts
# - Setup auto-reload service
# - Configure Nginx
###############################################################################

set -e  # Exit on error

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Configuration (will be set interactively)
LISTEN_PORT=""
SERVER_IP=""
CONFIG_DIR="/etc/api-gateway"
CONFIG_FILE="$CONFIG_DIR/apis.json"
NGINX_SITES_AVAILABLE="/etc/nginx/sites-available"
NGINX_SITES_ENABLED="/etc/nginx/sites-enabled"
SCRIPT_DIR="/usr/local/bin"

###############################################################################
# Helper Functions
###############################################################################

print_header() {
    echo ""
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}========================================${NC}"
    echo ""
}

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

detect_server_ip() {
    print_header "Server IP Configuration"
    
    # Try to detect IPs
    DETECTED_IP=$(hostname -I | awk '{print $1}')
    PUBLIC_IP=$(curl -s --max-time 3 ifconfig.me 2>/dev/null || curl -s --max-time 3 icanhazip.com 2>/dev/null || echo "")
    
    echo -e "${CYAN}Detected Server Information:${NC}"
    echo ""
    
    if [ -n "$DETECTED_IP" ]; then
        echo -e "  Local IP:  ${YELLOW}$DETECTED_IP${NC}"
    fi
    
    if [ -n "$PUBLIC_IP" ] && [ "$PUBLIC_IP" != "$DETECTED_IP" ]; then
        echo -e "  Public IP: ${YELLOW}$PUBLIC_IP${NC}"
    fi
    
    echo ""
    echo -e "${CYAN}Which IP address should be used in configuration?${NC}"
    echo -e "${CYAN}(This will be displayed in the web interface)${NC}"
    echo ""
    
    if [ -n "$PUBLIC_IP" ] && [ "$PUBLIC_IP" != "$DETECTED_IP" ]; then
        echo "  1) Public IP: $PUBLIC_IP (for external access)"
        echo "  2) Local IP: $DETECTED_IP (for internal network)"
        echo "  3) Enter custom IP"
        echo ""
        read -p "Enter your choice [1]: " ip_choice
        ip_choice=${ip_choice:-1}
    else
        echo "  1) Use detected IP: ${DETECTED_IP:-localhost}"
        echo "  2) Enter custom IP"
        echo ""
        read -p "Enter your choice [1]: " ip_choice
        ip_choice=${ip_choice:-1}
    fi
    
    case $ip_choice in
        1)
            if [ -n "$PUBLIC_IP" ] && [ "$PUBLIC_IP" != "$DETECTED_IP" ]; then
                SERVER_IP="$PUBLIC_IP"
            else
                SERVER_IP="${DETECTED_IP:-localhost}"
            fi
            ;;
        2)
            if [ -n "$DETECTED_IP" ]; then
                SERVER_IP="$DETECTED_IP"
            else
                read -p "Enter IP address: " custom_ip
                SERVER_IP="$custom_ip"
            fi
            ;;
        3)
            read -p "Enter IP address: " custom_ip
            SERVER_IP="$custom_ip"
            ;;
        *)
            SERVER_IP="${PUBLIC_IP:-${DETECTED_IP:-localhost}}"
            ;;
    esac
    
    print_success "Server IP set to: $SERVER_IP"
    echo ""
}

configure_port() {
    print_header "Port Configuration"
    
    echo -e "${CYAN}What port should the API Gateway listen on?${NC}"
    echo ""
    echo "  Common options:"
    echo "    422  - Non-standard port (default, often unrestricted)"
    echo "    80   - HTTP standard (may require firewall configuration)"
    echo "    8080 - Alternative HTTP port"
    echo "    3000 - Development port"
    echo ""
    
    read -p "Enter port number [422]: " port_input
    LISTEN_PORT="${port_input:-422}"
    
    # Check if port is already in use
    if netstat -tuln 2>/dev/null | grep -q ":$LISTEN_PORT " || ss -tuln 2>/dev/null | grep -q ":$LISTEN_PORT "; then
        print_warning "Port $LISTEN_PORT appears to be in use!"
        echo ""
        read -p "Continue anyway? (yes/no) [no]: " continue_choice
        if [ "$continue_choice" != "yes" ]; then
            print_error "Installation cancelled"
            exit 1
        fi
    else
        print_success "Port $LISTEN_PORT is available"
    fi
    
    echo ""
}

###############################################################################
# Installation Steps
###############################################################################

install_dependencies() {
    print_header "Installing Dependencies"
    
    print_info "Updating package list..."
    apt-get update -qq
    
    # Check and install nginx
    if command -v nginx &> /dev/null; then
        print_success "Nginx already installed"
    else
        print_info "Installing Nginx..."
        apt-get install -y nginx
        print_success "Nginx installed"
    fi
    
    # Check and install jq
    if command -v jq &> /dev/null; then
        print_success "jq already installed"
    else
        print_info "Installing jq..."
        apt-get install -y jq
        print_success "jq installed"
    fi
    
    # Check and install inotify-tools
    if command -v inotifywait &> /dev/null; then
        print_success "inotify-tools already installed"
    else
        print_info "Installing inotify-tools..."
        apt-get install -y inotify-tools
        print_success "inotify-tools installed"
    fi
    
    # Check and install curl, wget, unzip for Grafana/Loki
    MISSING_TOOLS=""
    command -v curl &> /dev/null || MISSING_TOOLS="$MISSING_TOOLS curl"
    command -v wget &> /dev/null || MISSING_TOOLS="$MISSING_TOOLS wget"
    command -v unzip &> /dev/null || MISSING_TOOLS="$MISSING_TOOLS unzip"
    
    if [ -n "$MISSING_TOOLS" ]; then
        print_info "Installing required tools:$MISSING_TOOLS..."
        apt-get install -y $MISSING_TOOLS
        print_success "Required tools installed"
    fi
}

create_config_directory() {
    print_header "Creating Configuration Directory"
    
    if [ -d "$CONFIG_DIR" ]; then
        print_warning "Configuration directory already exists"
        
        if [ -f "$CONFIG_FILE" ]; then
            print_info "Backing up existing configuration..."
            cp "$CONFIG_FILE" "$CONFIG_FILE.backup.$(date +%Y%m%d_%H%M%S)"
            print_success "Backup created"
        fi
    else
        mkdir -p "$CONFIG_DIR"
        print_success "Created $CONFIG_DIR"
    fi
    
    chmod 755 "$CONFIG_DIR"
}

create_initial_config() {
    print_header "Creating Initial Configuration"
    
    if [ -f "$CONFIG_FILE" ] && [ -s "$CONFIG_FILE" ]; then
        print_warning "Configuration file already exists, keeping existing config"
        print_info "Current APIs:"
        jq -r '.apis[] | "  - \(.name) on port \(.port)"' "$CONFIG_FILE"
    else
        print_info "Creating default configuration..."
        cat > "$CONFIG_FILE" << 'EOF'
{
  "apis": [
    {
      "name": "example-api",
      "path": "/api",
      "port": 3000,
      "description": "Example API Service",
      "enabled": true
    }
  ]
}
EOF
        print_success "Default configuration created"
        print_info "Edit $CONFIG_FILE to add your APIs"
    fi
}

create_generator_script() {
    print_header "Creating Nginx Config Generator Script"
    
    if [ -f "$SCRIPT_DIR/generate-nginx-config" ]; then
        print_warning "Generator script already exists, will be overwritten"
    fi
    
    # Create script with configured port and IP
    cat > "$SCRIPT_DIR/generate-nginx-config" << EOF
#!/bin/bash

CONFIG_FILE="/etc/api-gateway/apis.json"
NGINX_CONFIG="/etc/nginx/sites-available/apis"
LISTEN_PORT="$LISTEN_PORT"
SERVER_IP="$SERVER_IP"

# Generate HTML content from JSON
HTML_APIS=""
while IFS= read -r api; do
    NAME=\$(echo "\$api" | /usr/bin/jq -r '.name')
    APATH=\$(echo "\$api" | /usr/bin/jq -r '.path')
    PORT=\$(echo "\$api" | /usr/bin/jq -r '.port')
    DESC=\$(echo "\$api" | /usr/bin/jq -r '.description')
    HTML_APIS="\${HTML_APIS}<div class=\\"api-item\\"><a href=\\"\${APATH}/\\">\${APATH}</a> - \${DESC} (port: \${PORT})</div>"
done < <(/usr/bin/jq -c '.apis[] | select(.enabled == true)' "\$CONFIG_FILE")

# Create nginx config
/bin/cat > "\$NGINX_CONFIG" << 'ENDCONFIG'
server {
    listen $LISTEN_PORT default_server;
    server_name _;

    location = / {
        add_header Content-Type text/html;
        return 200 '<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>API Gateway</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 40px; background: #f5f5f5; }
        .container { background: white; padding: 30px; border-radius: 8px; max-width: 800px; margin: 0 auto; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
        h1 { color: #333; margin-bottom: 10px; }
        .info { color: #666; margin-bottom: 20px; font-size: 14px; }
        .api-item { padding: 15px; margin: 10px 0; background: #f9f9f9; border-left: 4px solid #4CAF50; border-radius: 4px; }
        .api-item a { color: #2196F3; text-decoration: none; font-weight: bold; font-size: 16px; }
        .api-item a:hover { text-decoration: underline; }
        .footer { margin-top: 30px; padding-top: 20px; border-top: 1px solid #eee; color: #999; font-size: 12px; }
    </style>
</head>
<body>
    <div class="container">
        <h1>🚀 API Gateway</h1>
        <div class="info">Server: <strong>$SERVER_IP:$LISTEN_PORT</strong></div>
        <p>Available APIs:</p>
ENDCONFIG

# Add generated HTML
/bin/echo "        \${HTML_APIS}" >> "\$NGINX_CONFIG"

# Continue config
/bin/cat >> "\$NGINX_CONFIG" << 'ENDCONFIG'
        <div class="footer">Auto-generated configuration | Use: <code>api-manage</code></div>
    </div>
</body>
</html>';
    }
ENDCONFIG

# Add proxy locations
while IFS= read -r api; do
    NAME=\$(/bin/echo "\$api" | /usr/bin/jq -r '.name')
    APATH=\$(/bin/echo "\$api" | /usr/bin/jq -r '.path')
    PORT=\$(/bin/echo "\$api" | /usr/bin/jq -r '.port')
    
    /bin/cat >> "\$NGINX_CONFIG" << PROXY

    # \$NAME
    location \$APATH/ {
        proxy_pass http://localhost:\$PORT/;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \\\$http_upgrade;
        proxy_set_header Connection 'upgrade';
        proxy_set_header Host \\\$host;
        proxy_set_header X-Real-IP \\\$remote_addr;
        proxy_set_header X-Forwarded-For \\\$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \\\$scheme;
        proxy_cache_bypass \\\$http_upgrade;
    }
PROXY
done < <(/usr/bin/jq -c '.apis[] | select(.enabled == true)' "\$CONFIG_FILE")

# Add dashboard location
/bin/cat >> "\$NGINX_CONFIG" << 'DASHBOARD'

    # Grafana Dashboard
    location /grafana/ {
        proxy_pass http://localhost:3000/;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "Upgrade";
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }
DASHBOARD

# Close server block
/bin/echo '}' >> "\$NGINX_CONFIG"

# Test and reload
/usr/sbin/nginx -t && /usr/bin/systemctl reload nginx

if [ \$? -eq 0 ]; then
    /bin/echo "✅ Configuration generated and applied!"
    /bin/echo "🌐 Access at http://$SERVER_IP:$LISTEN_PORT"
else
    /bin/echo "❌ Nginx configuration error!"
    exit 1
fi
EOF

    chmod +x "$SCRIPT_DIR/generate-nginx-config"
    print_success "Generator script created"
}

create_management_script() {
    print_header "Creating API Management Script"
    
    if [ -f "$SCRIPT_DIR/api-manage" ]; then
        print_warning "Management script already exists, will be overwritten"
    fi
    
    cat > "$SCRIPT_DIR/api-manage" << 'SCRIPT_END'
#!/bin/bash

CONFIG_FILE="/etc/api-gateway/apis.json"

case "$1" in
    add)
        if [ -z "$2" ] || [ -z "$3" ]; then
            echo "Usage: api-manage add <name> <port> [path] [description]"
            exit 1
        fi
        
        NAME="$2"
        PORT="$3"
        PATH="${4:-/$NAME}"
        DESC="${5:-API service $NAME}"
        
        /usr/bin/jq ".apis += [{\"name\": \"$NAME\", \"path\": \"$PATH\", \"port\": $PORT, \"description\": \"$DESC\", \"enabled\": true}]" "$CONFIG_FILE" > /tmp/apis.json
        /bin/mv /tmp/apis.json "$CONFIG_FILE"
        
        echo "✅ API '$NAME' added on port $PORT"
        /usr/local/bin/generate-nginx-config
        ;;
    
    remove)
        if [ -z "$2" ]; then
            echo "Usage: api-manage remove <name>"
            exit 1
        fi
        
        NAME="$2"
        /usr/bin/jq ".apis |= map(select(.name != \"$NAME\"))" "$CONFIG_FILE" > /tmp/apis.json
        /bin/mv /tmp/apis.json "$CONFIG_FILE"
        
        echo "✅ API '$NAME' removed"
        /usr/local/bin/generate-nginx-config
        ;;
    
    enable)
        if [ -z "$2" ]; then
            echo "Usage: api-manage enable <name>"
            exit 1
        fi
        
        NAME="$2"
        /usr/bin/jq ".apis |= map(if .name == \"$NAME\" then .enabled = true else . end)" "$CONFIG_FILE" > /tmp/apis.json
        /bin/mv /tmp/apis.json "$CONFIG_FILE"
        
        echo "✅ API '$NAME' enabled"
        /usr/local/bin/generate-nginx-config
        ;;
    
    disable)
        if [ -z "$2" ]; then
            echo "Usage: api-manage disable <name>"
            exit 1
        fi
        
        NAME="$2"
        /usr/bin/jq ".apis |= map(if .name == \"$NAME\" then .enabled = false else . end)" "$CONFIG_FILE" > /tmp/apis.json
        /bin/mv /tmp/apis.json "$CONFIG_FILE"
        
        echo "✅ API '$NAME' disabled"
        /usr/local/bin/generate-nginx-config
        ;;
    
    list)
        echo "📋 Registered APIs:"
        /usr/bin/jq -r '.apis[] | "  \(.name) -> \(.path) (port \(.port)) [\(if .enabled then "✓ ACTIVE" else "✗ DISABLED" end)]"' "$CONFIG_FILE"
        ;;
    
    reload)
        /usr/local/bin/generate-nginx-config
        ;;
    
    *)
        echo "API Gateway Manager"
        echo ""
        echo "Usage: api-manage <command> [parameters]"
        echo ""
        echo "Commands:"
        echo "  add <name> <port> [path] [desc]  - Add new API (requires sudo)"
        echo "  remove <name>                     - Remove API (requires sudo)"
        echo "  enable <name>                     - Enable API (requires sudo)"
        echo "  disable <name>                    - Disable API (requires sudo)"
        echo "  list                              - Show all APIs"
        echo "  reload                            - Regenerate Nginx config (requires sudo)"
        echo ""
        echo "Examples:"
        echo "  sudo api-manage add my-api 3005"
        echo "  sudo api-manage add payment 4000 /payments 'Payment API'"
        echo "  api-manage list"
        echo "  sudo api-manage disable my-api"
        echo "  sudo api-manage remove my-api"
        echo ""
        echo "Note: Configuration changes require root privileges (use sudo)"
        ;;
esac
SCRIPT_END

    chmod +x "$SCRIPT_DIR/api-manage"
    print_success "Management script created"
}

setup_auto_reload() {
    print_header "Setting Up Auto-Reload Service"
    
    # Create watcher script
    cat > "$SCRIPT_DIR/api-gateway-watch" << 'WATCH_END'
#!/bin/bash
echo "👀 Watching for changes in /etc/api-gateway/apis.json"
while inotifywait -e modify /etc/api-gateway/apis.json; do
    echo "🔄 Changes detected, regenerating configuration..."
    /usr/local/bin/generate-nginx-config
done
WATCH_END

    chmod +x "$SCRIPT_DIR/api-gateway-watch"
    print_success "Watcher script created"
    
    # Create systemd service
    cat > /etc/systemd/system/api-gateway-watch.service << 'SERVICE_END'
[Unit]
Description=API Gateway Config Auto-Reload Service
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/api-gateway-watch
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
SERVICE_END

    print_success "Systemd service created"
    
    # Enable and start service
    systemctl daemon-reload
    systemctl enable api-gateway-watch.service
    systemctl start api-gateway-watch.service
    
    if systemctl is-active --quiet api-gateway-watch.service; then
        print_success "Auto-reload service started and enabled"
    else
        print_warning "Auto-reload service failed to start (non-critical)"
    fi
}

setup_grafana_loki() {
    print_header "Setting Up Grafana + Loki Dashboard"
    
    # Create directories
    mkdir -p /opt/loki /opt/promtail /opt/grafana/data
    print_success "Directories created"
    
    # Detect architecture
    ARCH=$(uname -m)
    if [ "$ARCH" = "aarch64" ]; then
        LOKI_ARCH="arm64"
    elif [ "$ARCH" = "x86_64" ]; then
        LOKI_ARCH="amd64"
    else
        print_warning "Unknown architecture: $ARCH, using amd64"
        LOKI_ARCH="amd64"
    fi
    
    # Download Loki
    print_info "Downloading Loki..."
    wget -q https://github.com/grafana/loki/releases/download/v2.9.3/loki-linux-${LOKI_ARCH}.zip -O /tmp/loki.zip
    unzip -q /tmp/loki.zip -d /opt/loki/
    chmod +x /opt/loki/loki-linux-${LOKI_ARCH}
    mv /opt/loki/loki-linux-${LOKI_ARCH} /opt/loki/loki
    print_success "Loki downloaded"
    
    # Download Promtail
    print_info "Downloading Promtail..."
    wget -q https://github.com/grafana/loki/releases/download/v2.9.3/promtail-linux-${LOKI_ARCH}.zip -O /tmp/promtail.zip
    unzip -q /tmp/promtail.zip -d /opt/promtail/
    chmod +x /opt/promtail/promtail-linux-${LOKI_ARCH}
    mv /opt/promtail/promtail-linux-${LOKI_ARCH} /opt/promtail/promtail
    print_success "Promtail downloaded"
    
    # Download Grafana
    print_info "Downloading Grafana..."
    if [ "$LOKI_ARCH" = "arm64" ]; then
        wget -q https://dl.grafana.com/oss/release/grafana-10.2.3.linux-arm64.tar.gz -O /tmp/grafana.tar.gz
    else
        wget -q https://dl.grafana.com/oss/release/grafana-10.2.3.linux-amd64.tar.gz -O /tmp/grafana.tar.gz
    fi
    tar -xzf /tmp/grafana.tar.gz -C /opt/
    mv /opt/grafana-*/ /opt/grafana-install
    print_success "Grafana downloaded"
    
    # Create Loki configuration
    cat > /opt/loki/loki-config.yaml << EOF
auth_enabled: false

server:
  http_listen_port: 3100

ingester:
  lifecycler:
    ring:
      kvstore:
        store: inmemory
      replication_factor: 1
  chunk_idle_period: 5m
  chunk_retain_period: 30s

schema_config:
  configs:
    - from: 2020-10-24
      store: boltdb
      object_store: filesystem
      schema: v11
      index:
        prefix: index_
        period: 168h

storage_config:
  boltdb:
    directory: /opt/loki/index
  filesystem:
    directory: /opt/loki/chunks

limits_config:
  enforce_metric_name: false
  reject_old_samples: true
  reject_old_samples_max_age: 168h

chunk_store_config:
  max_look_back_period: 0s

table_manager:
  retention_deletes_enabled: false
  retention_period: 0s
EOF

    # Create Promtail configuration
    cat > /opt/promtail/promtail-config.yaml << 'PROMTAIL_EOF'
server:
  http_listen_port: 9080

positions:
  filename: /opt/promtail/positions.yaml

clients:
  - url: http://localhost:3100/loki/api/v1/push

scrape_configs:
  - job_name: nginx
    static_configs:
      - targets:
          - localhost
        labels:
          job: nginx
          __path__: /var/log/nginx/access.log
PROMTAIL_EOF

    # Create Loki systemd service
    cat > /etc/systemd/system/loki.service << 'LOKI_SERVICE'
[Unit]
Description=Loki Log Aggregation System
After=network.target

[Service]
Type=simple
User=root
ExecStart=/opt/loki/loki -config.file=/opt/loki/loki-config.yaml
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
LOKI_SERVICE

    # Create Promtail systemd service
    cat > /etc/systemd/system/promtail.service << 'PROMTAIL_SERVICE'
[Unit]
Description=Promtail Log Collector
After=network.target loki.service

[Service]
Type=simple
User=root
ExecStart=/opt/promtail/promtail -config.file=/opt/promtail/promtail-config.yaml
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
PROMTAIL_SERVICE

    # Create Grafana systemd service
    cat > /etc/systemd/system/grafana.service << 'GRAFANA_SERVICE'
[Unit]
Description=Grafana
After=network.target loki.service

[Service]
Type=simple
User=root
WorkingDirectory=/opt/grafana-install
Environment="GF_SERVER_HTTP_PORT=3000"
Environment="GF_PATHS_DATA=/opt/grafana/data"
ExecStart=/opt/grafana-install/bin/grafana-server
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
GRAFANA_SERVICE

    # Start services
    systemctl daemon-reload
    systemctl enable loki promtail grafana
    systemctl start loki
    sleep 3
    systemctl start promtail
    sleep 2
    systemctl start grafana
    
    if systemctl is-active --quiet loki && systemctl is-active --quiet promtail && systemctl is-active --quiet grafana; then
        print_success "Grafana + Loki services started successfully"
    else
        print_warning "Some services failed to start (check with: systemctl status loki/promtail/grafana)"
    fi
    
    # Cleanup
    rm -f /tmp/loki.zip /tmp/promtail.zip /tmp/grafana.tar.gz
}

configure_grafana_datasource() {
    print_header "Configuring Grafana Data Source"
    
    print_info "Waiting for Grafana to start..."
    sleep 10
    
    # Add Loki as datasource via API
    curl -s -X POST -H "Content-Type: application/json" \
        -d '{
            "name":"Loki",
            "type":"loki",
            "url":"http://localhost:3100",
            "access":"proxy",
            "isDefault":true
        }' \
        http://admin:admin@localhost:3000/api/datasources > /dev/null 2>&1
    
    print_success "Loki datasource configured in Grafana"
    
    print_info "Default Grafana credentials:"
    echo -e "  Username: ${YELLOW}admin${NC}"
    echo -e "  Password: ${YELLOW}admin${NC}"
    echo -e "  ${CYAN}(You will be asked to change password on first login)${NC}"
    echo ""
}

configure_nginx() {
    print_header "Configuring Nginx"
    
    # Remove default site if exists
    if [ -L "$NGINX_SITES_ENABLED/default" ]; then
        print_info "Removing default Nginx site..."
        rm "$NGINX_SITES_ENABLED/default"
        print_success "Default site removed"
    fi
    
    # Check for conflicting configurations
    if grep -r "listen.*$LISTEN_PORT" "$NGINX_SITES_ENABLED/" 2>/dev/null | grep -v apis; then
        print_warning "Found other configurations listening on port $LISTEN_PORT"
        print_info "Please review and remove conflicts manually"
    fi
    
    # Generate initial nginx config
    print_info "Generating Nginx configuration..."
    "$SCRIPT_DIR/generate-nginx-config"
    
    # Create symlink if doesn't exist
    if [ ! -L "$NGINX_SITES_ENABLED/apis" ]; then
        ln -s "$NGINX_SITES_AVAILABLE/apis" "$NGINX_SITES_ENABLED/apis"
        print_success "Enabled API Gateway site"
    else
        print_success "API Gateway site already enabled"
    fi
    
    # Test and restart nginx
    print_info "Testing Nginx configuration..."
    if nginx -t; then
        print_success "Nginx configuration is valid"
        systemctl restart nginx
        print_success "Nginx restarted"
    else
        print_error "Nginx configuration test failed"
        exit 1
    fi
}

print_completion_info() {
    print_header "Installation Complete!"
    
    echo ""
    print_success "API Gateway is now running on port $LISTEN_PORT"
    echo ""
    echo -e "${GREEN}Access your API Gateway at:${NC}"
    echo -e "  ${BLUE}http://$SERVER_IP:$LISTEN_PORT${NC}"
    echo ""
    echo -e "${GREEN}📊 Grafana Dashboard:${NC}"
    echo -e "  ${BLUE}http://$SERVER_IP:$LISTEN_PORT/grafana${NC}"
    echo -e "  ${CYAN}Username: admin / Password: admin${NC}"
    echo -e "  ${CYAN}Search logs, filter requests, monitor in real-time${NC}"
    echo ""
    echo -e "${GREEN}Management Commands:${NC}"
    echo -e "  ${YELLOW}api-manage list${NC}                   - List all APIs (no sudo needed)"
    echo -e "  ${YELLOW}sudo api-manage add <name> <port>${NC} - Add new API"
    echo -e "  ${YELLOW}sudo api-manage remove <name>${NC}     - Remove API"
    echo -e "  ${YELLOW}sudo api-manage reload${NC}            - Reload configuration"
    echo ""
    echo -e "${GREEN}Configuration Files:${NC}"
    echo -e "  APIs config:     ${BLUE}$CONFIG_FILE${NC}"
    echo -e "  Nginx config:    ${BLUE}$NGINX_SITES_AVAILABLE/apis${NC}"
    echo -e "  Loki config:     ${BLUE}/opt/loki/loki-config.yaml${NC}"
    echo -e "  Promtail config: ${BLUE}/opt/promtail/promtail-config.yaml${NC}"
    echo ""
    echo -e "${YELLOW}Next Steps:${NC}"
    echo "  1. Edit $CONFIG_FILE to add your APIs"
    echo "  2. Or use: api-manage add my-service 3000"
    echo "  3. Configuration will auto-reload on changes"
    echo "  4. Access Grafana to search and monitor logs"
    echo "  5. In Grafana: Explore → Select Loki → Search logs"
    echo ""
}

###############################################################################
# Main Installation Flow
###############################################################################

main() {
    clear
    echo ""
    echo -e "${BLUE}╔════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║     API Gateway Installer v1.0        ║${NC}"
    echo -e "${BLUE}║   Interactive Nginx Setup Script      ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════╝${NC}"
    echo ""
    
    check_root
    
    # Interactive configuration
    detect_server_ip
    configure_port
    
    print_info "Starting installation with:"
    echo "  - Server IP: $SERVER_IP"
    echo "  - Port: $LISTEN_PORT"
    echo ""
    read -p "Press Enter to continue or Ctrl+C to cancel..."
    
    install_dependencies
    create_config_directory
    create_initial_config
    create_generator_script
    create_management_script
    setup_auto_reload
    setup_grafana_loki
    configure_grafana_datasource
    configure_nginx
    print_completion_info
    
    echo ""
    print_success "Installation finished successfully!"
    echo ""
}

# Run main installation
main "$@"
