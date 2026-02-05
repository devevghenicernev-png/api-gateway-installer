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

command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Safe apt function with lock checking and retries
safe_apt() {
    local operation="$1"
    shift
    local packages="$@"
    
    # Wait for apt lock to be released
    local wait_count=0
    while fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1; do
        if [ $wait_count -eq 0 ]; then
            print_info "Another package manager is running, waiting..."
        fi
        wait_count=$((wait_count + 1))
        if [ $wait_count -gt 30 ]; then
            print_warning "Waited too long for package manager lock, proceeding anyway..."
            break
        fi
        sleep 10
    done
    
    if [ $wait_count -gt 0 ]; then
        print_success "Package manager is now available"
    fi
    
    # Try apt command with retries
    local retry_count=0
    while [ $retry_count -lt 3 ]; do
        case "$operation" in
            "update")
                if apt-get update -qq; then
                    return 0
                fi
                ;;
            "install")
                if apt-get install -y $packages; then
                    return 0
                fi
                ;;
        esac
        
        retry_count=$((retry_count + 1))
        if [ $retry_count -lt 3 ]; then
            print_warning "Operation attempt $retry_count failed, retrying in 10 seconds..."
            sleep 10
        else
            print_error "Failed to $operation after 3 attempts"
            return 1
        fi
    done
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
    safe_apt update
    
    # Check and install nginx
    if command -v nginx &> /dev/null; then
        print_success "Nginx already installed"
    else
        print_info "Installing Nginx..."
        if safe_apt install nginx; then
            print_success "Nginx installed"
        else
            print_error "Failed to install Nginx"
            return 1
        fi
    fi
    
    # Check and install jq
    if command -v jq &> /dev/null; then
        print_success "jq already installed"
    else
        print_info "Installing jq..."
        if safe_apt install jq; then
            print_success "jq installed"
        else
            print_error "Failed to install jq"
            return 1
        fi
    fi
    
    # Check and install inotify-tools
    if command -v inotifywait &> /dev/null; then
        print_success "inotify-tools already installed"
    else
        print_info "Installing inotify-tools..."
        if safe_apt install inotify-tools; then
            print_success "inotify-tools installed"
        else
            print_error "Failed to install inotify-tools"
            return 1
        fi
    fi
    
    # Check and install curl, wget, unzip for Grafana/Loki
    MISSING_TOOLS=""
    command -v curl &> /dev/null || MISSING_TOOLS="$MISSING_TOOLS curl"
    command -v wget &> /dev/null || MISSING_TOOLS="$MISSING_TOOLS wget"
    command -v unzip &> /dev/null || MISSING_TOOLS="$MISSING_TOOLS unzip"
    
    if [ -n "$MISSING_TOOLS" ]; then
        print_info "Installing required tools:$MISSING_TOOLS..."
        if safe_apt install $MISSING_TOOLS; then
            print_success "Required tools installed"
        else
            print_error "Failed to install required tools"
            return 1
        fi
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
  "apis": []
}
EOF
        print_success "Default configuration created"
        print_info "Add APIs: api-manage add <name> <port> [path]"
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

# Create nginx config
/bin/cat > "\$NGINX_CONFIG" << 'ENDCONFIG'
# ---- API Gateway verbose logging ----
# Nginx cannot log "all headers" via a single variable; we list the important ones
# and redact sensitive values (auth/cookie).
map \$http_authorization \$apigw_auth {
    default "<redacted>";
    "" "";
}
map \$http_cookie \$apigw_cookie {
    default "<redacted>";
    "" "";
}
map \$sent_http_set_cookie \$apigw_set_cookie {
    default "<redacted>";
    "" "";
}
ENDCONFIG

# Create map for API name based on URI (BEFORE server block!)
/bin/cat >> "\$NGINX_CONFIG" << 'MAPSTART'

# Map URI to API name for logging
map \$uri \$api_name {
    default "";
MAPSTART

# Add API mappings
while IFS= read -r api; do
    NAME=\$(/bin/echo "\$api" | /usr/bin/jq -r '.name')
    APATH=\$(/bin/echo "\$api" | /usr/bin/jq -r '.path')
    /bin/echo "    ~^\${APATH}/ \"\${NAME}\";" >> "\$NGINX_CONFIG"
done < <(/usr/bin/jq -c '.apis[] | select(.enabled == true)' "\$CONFIG_FILE")

/bin/echo '}' >> "\$NGINX_CONFIG"

# Continue with log format and server block
/bin/cat >> "\$NGINX_CONFIG" << 'ENDCONFIG'

# JSON access log (Loki/Grafana friendly)
log_format apigw_json escape=json '{'
    '"ts":"\$time_iso8601",'
    '"remote_addr":"\$remote_addr",'
    '"xff":"\$http_x_forwarded_for",'
    '"method":"\$request_method",'
    '"uri":"\$request_uri",'
    '"args":"\$args",'
    '"request_id":"\$request_id",'
    '"status":\$status,'
    '"request_time":\$request_time,'
    '"upstream_addr":"\$upstream_addr",'
    '"upstream_status":"\$upstream_status",'
    '"upstream_response_time":"\$upstream_response_time",'
    '"upstream_connect_time":"\$upstream_connect_time",'
    '"upstream_header_time":"\$upstream_header_time",'
    '"upstream_response_length":"\$upstream_response_length",'
    '"request_length":\$request_length,'
    '"bytes_sent":\$bytes_sent,'
    '"body_bytes_sent":\$body_bytes_sent,'
    '"host":"\$host",'
    '"http_host":"\$http_host",'
    '"scheme":"\$scheme",'
    '"server_port":"\$server_port",'
    '"http_referer":"\$http_referer",'
    '"http_user_agent":"\$http_user_agent",'
    '"http_origin":"\$http_origin",'
    '"http_content_type":"\$http_content_type",'
    '"http_content_length":"\$http_content_length",'
    '"http_accept":"\$http_accept",'
    '"http_accept_encoding":"\$http_accept_encoding",'
    '"http_accept_language":"\$http_accept_language",'
    '"http_x_request_id":"\$http_x_request_id",'
    '"api":"\$api_name",'
    '"auth":"\$apigw_auth",'
    '"cookie":"\$apigw_cookie",'
    '"request_body":"\$request_body",'
    '"request_body_file":"\$request_body_file",'
    '"resp_content_type":"\$sent_http_content_type",'
    '"resp_content_length":"\$sent_http_content_length",'
    '"resp_location":"\$sent_http_location",'
    '"resp_cache_control":"\$sent_http_cache_control",'
    '"resp_set_cookie":"\$apigw_set_cookie"'
'}';

# You can change the log path/file if needed
access_log /var/log/nginx/access.log apigw_json;

server {
    listen $LISTEN_PORT default_server;
    server_name _;

    # Gateway UI (home, deployments) - Node.js
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header X-Forwarded-Prefix "";
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
        # Request body logging:
        # - \$request_body is often empty for large bodies (nginx spills to a temp file)
        # - if needed, you can force storing the body in a file:
        #   client_body_in_file_only on;  # WARNING: disk + secret leakage risk
        client_body_in_single_buffer on;
        client_body_buffer_size 1m;

        proxy_pass http://localhost:\$PORT/;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \\\$http_upgrade;
        proxy_set_header Connection 'upgrade';
        proxy_set_header Host \\\$host;
        proxy_set_header X-Request-ID \\\$request_id;
        proxy_set_header X-Real-IP \\\$remote_addr;
        proxy_set_header X-Forwarded-For \\\$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \\\$scheme;
        proxy_set_header X-Forwarded-Host \\\$host;
        proxy_set_header X-Forwarded-Port \\\$server_port;
        proxy_set_header X-Forwarded-Prefix \$APATH;
        proxy_cache_bypass \\\$http_upgrade;

        add_header X-Request-ID \\\$request_id always;
        add_header X-Api-Name "\$NAME" always;
    }
PROXY
done < <(/usr/bin/jq -c '.apis[] | select(.enabled == true)' "\$CONFIG_FILE")

# Add dashboard location
/bin/cat >> "\$NGINX_CONFIG" << 'DASHBOARD'

    # OpenObserve - with ZO_BASE_URI=/observe
    location /observe/ {
        proxy_pass http://localhost:5080/observe/;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        
        # WebSocket support
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        
        # Buffering settings
        proxy_buffering off;
        proxy_request_buffering off;
    }
    
    # Redirect /observe to /observe/
    location = /observe {
        return 301 /observe/;
    }
    
    # Dashboard API (dedicated path to avoid conflict with user APIs at /api/)
    location /gateway-api/ {
        proxy_pass http://127.0.0.1:8080/api/;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        # SSE: disable buffering for real-time streams
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
    }
    
    # Dashboard (Node.js on 8080) - /deployments/ is main, /dashboard/ redirects
    location /dashboard/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }
    location = /dashboard {
        return 301 /deployments/;
    }
    
    # GitHub Webhook (Node.js on 9876) - single port access
    location /webhook/ {
        proxy_pass http://127.0.0.1:9876/webhook/;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_request_buffering off;
        client_max_body_size 10m;
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

create_fluentbit_generator_script() {
    print_header "Creating Fluent Bit Config Generator Script"
    
    cat > "$SCRIPT_DIR/generate-fluentbit-config" << 'EOF'
#!/bin/bash

CONFIG_FILE="/etc/api-gateway/apis.json"
FLUENTBIT_CONFIG="/etc/fluent-bit/fluent-bit.conf"

# Get OpenObserve credentials
if [ -f /opt/openobserve/.credentials ]; then
    source /opt/openobserve/.credentials
    OO_USER="$EMAIL"
    OO_PASS="$PASSWORD"
else
    OO_USER="admin@localhost"
    OO_PASS="admin"
fi

# Create Fluent Bit config header
/bin/cat > "$FLUENTBIT_CONFIG" << 'FLUENTBIT_HEADER'
[SERVICE]
    Flush         5
    Daemon        Off
    Log_Level     info
    Parsers_File  /etc/fluent-bit/parsers.conf

[INPUT]
    Name              tail
    Path              /var/log/nginx/access.log
    Tag               nginx.access
    Parser            json
    Refresh_Interval  5
    DB                /var/lib/fluent-bit/nginx-access.db
    Skip_Long_Lines   On
    Skip_Empty_Lines  On

[INPUT]
    Name              tail
    Path              /var/log/nginx/error.log
    Tag               nginx.error
    Refresh_Interval  5
    DB                /var/lib/fluent-bit/nginx-error.db
    Skip_Empty_Lines  On

FLUENTBIT_HEADER

# Generate rewrite_tag filters for each API
while IFS= read -r api; do
    API_NAME=$(/bin/echo "$api" | /usr/bin/jq -r '.name')
    /bin/cat >> "$FLUENTBIT_CONFIG" << FILTER_EOF
# Route logs for API: ${API_NAME}
[FILTER]
    Name          rewrite_tag
    Match         nginx.access
    Rule          \$api ^${API_NAME}\$ api.${API_NAME} false
    Emitter_Name  re_emitted_${API_NAME}

FILTER_EOF
done < <(/usr/bin/jq -c '.apis[] | select(.enabled == true)' "$CONFIG_FILE")

# Generate OUTPUT for each API
while IFS= read -r api; do
    API_NAME=$(/bin/echo "$api" | /usr/bin/jq -r '.name')
    /bin/cat >> "$FLUENTBIT_CONFIG" << OUTPUT_EOF
# Send logs for API: ${API_NAME}
[OUTPUT]
    Name          http
    Match         api.${API_NAME}
    Host          localhost
    Port          5080
    URI           /observe/api/default/${API_NAME}_logs/_json
    Format        json
    HTTP_User     ${OO_USER}
    HTTP_Passwd   ${OO_PASS}
    compress      gzip

OUTPUT_EOF
done < <(/usr/bin/jq -c '.apis[] | select(.enabled == true)' "$CONFIG_FILE")

# Add nginx error and catch-all outputs
/bin/cat >> "$FLUENTBIT_CONFIG" << FLUENTBIT_FOOTER
# Nginx error logs
[OUTPUT]
    Name          http
    Match         nginx.error
    Host          localhost
    Port          5080
    URI           /observe/api/default/nginx_error/_json
    Format        json
    HTTP_User     ${OO_USER}
    HTTP_Passwd   ${OO_PASS}
    compress      gzip

# Catch-all for other requests (OpenObserve UI, main page, etc)
[OUTPUT]
    Name          http
    Match         nginx.access
    Host          localhost
    Port          5080
    URI           /observe/api/default/nginx_other/_json
    Format        json
    HTTP_User     ${OO_USER}
    HTTP_Passwd   ${OO_PASS}
    compress      gzip
FLUENTBIT_FOOTER

# Restart Fluent Bit
/usr/bin/systemctl restart fluent-bit

if [ $? -eq 0 ]; then
    /bin/echo "✅ Fluent Bit configuration regenerated!"
else
    /bin/echo "❌ Fluent Bit restart failed!"
    exit 1
fi
EOF

    chmod +x "$SCRIPT_DIR/generate-fluentbit-config"
    print_success "Fluent Bit generator script created"
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
        
        # Check if API already exists
        if /usr/bin/jq -e ".apis[] | select(.name == \"$NAME\")" "$CONFIG_FILE" > /dev/null 2>&1; then
            echo "⚠️  API '$NAME' already exists!"
            echo ""
            echo "Current configuration:"
            /usr/bin/jq -r ".apis[] | select(.name == \"$NAME\") | \"  Name: \(.name)\n  Path: \(.path)\n  Port: \(.port)\n  Status: \(if .enabled then \"✓ ENABLED\" else \"✗ DISABLED\" end)\"" "$CONFIG_FILE"
            echo ""
            echo "Use 'sudo api-manage remove $NAME' to remove it first"
            echo "Or 'sudo api-manage enable/disable $NAME' to change status"
            exit 1
        fi
        
        /usr/bin/jq ".apis += [{\"name\": \"$NAME\", \"path\": \"$PATH\", \"port\": $PORT, \"description\": \"$DESC\", \"enabled\": true}]" "$CONFIG_FILE" > /tmp/apis.json
        /bin/mv /tmp/apis.json "$CONFIG_FILE"
        
        echo "✅ API '$NAME' added on port $PORT"
        /usr/local/bin/generate-nginx-config
        /usr/local/bin/generate-fluentbit-config
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
        /usr/local/bin/generate-fluentbit-config
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
        /usr/local/bin/generate-fluentbit-config
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
        /usr/local/bin/generate-fluentbit-config
        ;;
    
    list)
        echo "📋 Registered APIs:"
        /usr/bin/jq -r '.apis[] | "  \(.name) -> \(.path) (port \(.port)) [\(if .enabled then "✓ ACTIVE" else "✗ DISABLED" end)]"' "$CONFIG_FILE"
        ;;
    
    reload)
        /usr/local/bin/generate-nginx-config
        /usr/local/bin/generate-fluentbit-config
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

install_extended_modules() {
    print_header "Installing Extended Modules"
    
    # Create module directory structure
    mkdir -p /opt/api-gateway/{modules,scripts,web-ui}
    
    # Copy modules from installer directory
    local installer_dir="$(dirname "$0")"
    
    if [ -d "$installer_dir/modules" ]; then
        print_info "Installing deployment and webhook modules..."
        cp -r "$installer_dir/modules/"* /opt/api-gateway/modules/
        chmod +x /opt/api-gateway/modules/*.sh
        print_success "Modules installed"
    fi
    
    if [ -d "$installer_dir/scripts" ]; then
        print_info "Installing extended scripts..."
        cp -r "$installer_dir/scripts/"* /opt/api-gateway/scripts/
        chmod +x /opt/api-gateway/scripts/*
        print_success "Scripts installed"
    fi
    
    if [ -d "$installer_dir/web-ui" ]; then
        print_info "Installing web dashboard..."
        cp -r "$installer_dir/web-ui/"* /opt/api-gateway/web-ui/
        chmod +x /opt/api-gateway/web-ui/*.js 2>/dev/null || true
        print_success "Web dashboard installed"
    fi
    
    # Install Node.js for web dashboard and webhook server
    if ! command_exists node; then
        print_info "Installing Node.js 22 LTS..."
        curl -fsSL https://deb.nodesource.com/setup_22.x | bash - >/dev/null 2>&1
        if safe_apt install nodejs; then
            print_success "Node.js installed"
        else
            print_warning "Failed to install Node.js"
            print_info "You may need to install Node.js manually"
        fi
    else
        print_success "Node.js already installed"
    fi
    
    # Install npm dependencies for web dashboard
    if [ -f "/opt/api-gateway/web-ui/package.json" ] && command_exists npm; then
        print_info "Installing npm dependencies..."
        cd /opt/api-gateway/web-ui
        npm install --production --silent >/dev/null 2>&1 || {
            print_warning "Failed to install npm dependencies"
        }
    fi
    
    # Install PM2 for deployments (used by deploy add / webhook)
    if command_exists npm && ! command_exists pm2; then
        print_info "Installing PM2..."
        npm install -g pm2 >/dev/null 2>&1 && print_success "PM2 installed" || {
            print_warning "Failed to install PM2 (deployments will use systemd)"
        }
    fi
    
    # Install nvm for multi-Node-version support (deploy reads .nvmrc from repos)
    local nvm_dir="${NVM_DIR:-$HOME/.nvm}"
    if [ ! -d "$nvm_dir" ] && [ -n "$HOME" ]; then
        print_info "Installing nvm (Node Version Manager) for .nvmrc support..."
        export NVM_DIR="$nvm_dir"
        curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | bash -s >/dev/null 2>&1 && {
            print_success "nvm installed (use .nvmrc in repos to pin Node version)"
        } || print_warning "nvm install skipped (optional)"
    fi
    
    # Create symlink for extended API manager
    if [ -f "/opt/api-gateway/scripts/api-manage-extended" ]; then
        ln -sf /opt/api-gateway/scripts/api-manage-extended /usr/local/bin/api-manage-extended
        print_success "Extended API manager available as 'api-manage-extended'"
    fi
    
    # Initialize deployment manager
    if [ -f "/opt/api-gateway/modules/deployment-manager.sh" ]; then
        source /opt/api-gateway/modules/common.sh
        source /opt/api-gateway/modules/deployment-manager.sh
        init_deployment_manager
    fi
    
    # Create deploy-service.sh and remove-service.sh (used by dashboard and webhook)
    mkdir -p /opt/api-gateway/scripts
    cat > /opt/api-gateway/scripts/deploy-service.sh << 'DEPLOY_SCRIPT'
#!/bin/bash
SERVICE_NAME="$1"
[ -z "$SERVICE_NAME" ] && echo "Usage: $0 <service_name>" && exit 1
source /opt/api-gateway/modules/common.sh
source /opt/api-gateway/modules/deployment-manager.sh
deploy_service "$SERVICE_NAME"
DEPLOY_SCRIPT
    cat > /opt/api-gateway/scripts/remove-service.sh << 'REMOVE_SCRIPT'
#!/bin/bash
SERVICE_NAME="$1"
[ -z "$SERVICE_NAME" ] && echo "Usage: $0 <service_name>" && exit 1
source /opt/api-gateway/modules/common.sh
source /opt/api-gateway/modules/deployment-manager.sh
remove_deployment "$SERVICE_NAME"
REMOVE_SCRIPT
    chmod +x /opt/api-gateway/scripts/deploy-service.sh
    chmod +x /opt/api-gateway/scripts/remove-service.sh
    
    # Start dashboard automatically so it's available right after install
    if [ -f /opt/api-gateway/web-ui/server.js ] && command_exists node; then
        print_info "Setting up and starting web dashboard..."
        mkdir -p /var/log/api-gateway
        cat > /etc/systemd/system/api-gateway-dashboard.service << 'DASHBOARD_SVC'
[Unit]
Description=API Gateway Web Dashboard
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/api-gateway/web-ui
ExecStart=/usr/bin/node server.js
Restart=always
RestartSec=10
Environment=NODE_ENV=production

StandardOutput=append:/var/log/api-gateway/dashboard.log
StandardError=append:/var/log/api-gateway/dashboard.log

[Install]
WantedBy=multi-user.target
DASHBOARD_SVC
        systemctl daemon-reload
        systemctl enable api-gateway-dashboard
        systemctl start api-gateway-dashboard
        if systemctl is-active --quiet api-gateway-dashboard 2>/dev/null; then
            print_success "Web dashboard started (available at / and /deployments/)"
        else
            print_warning "Dashboard failed to start. Run: api-manage-extended dashboard start"
        fi
    fi
    
    # Start webhook server automatically for auto-deploy on push
    if [ -f /opt/api-gateway/web-ui/webhook-server.js ] && command_exists node; then
        print_info "Setting up and starting webhook server for auto-deploy..."
        mkdir -p /var/log/api-gateway
        cat > /etc/systemd/system/api-gateway-webhook.service << 'WEBHOOK_SVC'
[Unit]
Description=API Gateway GitHub Webhook Server
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/api-gateway/web-ui
ExecStart=/usr/bin/node webhook-server.js
Restart=always
RestartSec=10
Environment=NODE_ENV=production

StandardOutput=append:/var/log/api-gateway/webhook.log
StandardError=append:/var/log/api-gateway/webhook.log

[Install]
WantedBy=multi-user.target
WEBHOOK_SVC
        systemctl daemon-reload
        systemctl enable api-gateway-webhook
        systemctl start api-gateway-webhook
        if systemctl is-active --quiet api-gateway-webhook 2>/dev/null; then
            print_success "Webhook server started (auto-deploy on push when GitHub webhook is configured)"
        else
            print_warning "Webhook failed to start. Run: api-manage-extended webhook start"
        fi
    fi
    
    print_success "Extended modules installed successfully"
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

setup_openobserve() {
    print_header "Setting Up OpenObserve Dashboard"
    
    # Check if OpenObserve data exists
    if [ -d /opt/openobserve/data ]; then
        print_warning "OpenObserve data already exists"
        echo -e "${YELLOW}Do you want to:${NC}"
        echo "  1) Keep existing data (preserve settings)"
        echo "  2) Reset to default"
        read -p "Choose [1/2] (default: 1): " OO_CHOICE
        OO_CHOICE=${OO_CHOICE:-1}
        
        if [ "$OO_CHOICE" = "2" ]; then
            print_info "Removing OpenObserve data..."
            systemctl stop openobserve 2>/dev/null || true
            rm -rf /opt/openobserve/data
            print_success "OpenObserve data removed"
        else
            print_info "Keeping existing OpenObserve data"
        fi
    fi
    
    # Create directories
    mkdir -p /opt/openobserve/data /opt/openobserve/logs
    print_success "Directories created"
    
    # Detect architecture
    ARCH=$(uname -m)
    if [ "$ARCH" = "aarch64" ]; then
        OO_ARCH="arm64"
    elif [ "$ARCH" = "x86_64" ]; then
        OO_ARCH="amd64"
    else
        print_warning "Unknown architecture: $ARCH, using amd64"
        OO_ARCH="amd64"
    fi
    
    # Download OpenObserve
    if [ ! -f /opt/openobserve/openobserve ]; then
        print_info "Downloading OpenObserve..."
        OO_VERSION="v0.10.8"
        wget -q https://github.com/openobserve/openobserve/releases/download/${OO_VERSION}/openobserve-${OO_VERSION}-linux-${OO_ARCH}.tar.gz -O /tmp/openobserve.tar.gz
        tar -xzf /tmp/openobserve.tar.gz -C /opt/openobserve/
        chmod +x /opt/openobserve/openobserve
        print_success "OpenObserve downloaded"
    else
        print_success "OpenObserve already exists"
    fi
    
    # Configure OpenObserve credentials
    echo ""
    echo -e "${CYAN}OpenObserve Admin Credentials Configuration${NC}"
    
    # Email
    read -p "Admin email (default: admin@localhost): " OO_EMAIL
    OO_EMAIL=${OO_EMAIL:-admin@localhost}
    
    # Password
    echo ""
    echo "Password options:"
    echo "  1) Generate random secure password (recommended)"
    echo "  2) Enter custom password"
    read -p "Choose [1/2] (default: 1): " PASSWORD_CHOICE
    PASSWORD_CHOICE=${PASSWORD_CHOICE:-1}
    
    if [ "$PASSWORD_CHOICE" = "2" ]; then
        while true; do
            read -sp "Enter password (min 8 characters): " OO_PASSWORD
            echo ""
            if [ ${#OO_PASSWORD} -lt 8 ]; then
                print_error "Password must be at least 8 characters"
                continue
            fi
            read -sp "Confirm password: " OO_PASSWORD_CONFIRM
            echo ""
            if [ "$OO_PASSWORD" = "$OO_PASSWORD_CONFIRM" ]; then
                break
            else
                print_error "Passwords do not match, try again"
            fi
        done
        print_success "Custom password set"
    else
        OO_PASSWORD=$(openssl rand -base64 16 | tr -d '/+=' | head -c 16)
        print_success "Generated random password: ${YELLOW}${OO_PASSWORD}${NC}"
    fi
    
    # Create OpenObserve configuration
    cat > /opt/openobserve/.env << EOF
ZO_ROOT_USER_EMAIL=${OO_EMAIL}
ZO_ROOT_USER_PASSWORD=${OO_PASSWORD}
ZO_DATA_DIR=/opt/openobserve/data
ZO_HTTP_PORT=5080
ZO_HTTP_ADDR=127.0.0.1
ZO_BASE_URI=/observe
EOF
    
    # Save credentials for display later
    cat > /opt/openobserve/.credentials << EOF
EMAIL=${OO_EMAIL}
PASSWORD=${OO_PASSWORD}
EOF
    chmod 600 /opt/openobserve/.credentials
    
    print_success "OpenObserve configured"
    print_info "Admin email: ${CYAN}${OO_EMAIL}${NC}"
    print_info "Admin password: ${YELLOW}${OO_PASSWORD}${NC}"

    # Create OpenObserve systemd service
    cat > /etc/systemd/system/openobserve.service << 'OPENOBSERVE_SERVICE'
[Unit]
Description=OpenObserve Log Analytics Platform
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/openobserve
EnvironmentFile=/opt/openobserve/.env
ExecStart=/opt/openobserve/openobserve
Restart=always
RestartSec=10
StandardOutput=append:/opt/openobserve/logs/openobserve.log
StandardError=append:/opt/openobserve/logs/openobserve-error.log

[Install]
WantedBy=multi-user.target
OPENOBSERVE_SERVICE

    # Start service
    systemctl daemon-reload
    systemctl enable openobserve
    systemctl start openobserve
    
    # Wait for OpenObserve to start
    print_info "Waiting for OpenObserve to start..."
    sleep 5
    
    if systemctl is-active --quiet openobserve; then
        print_success "OpenObserve service started successfully"
    else
        print_warning "OpenObserve failed to start (check with: systemctl status openobserve)"
    fi
    
    # Cleanup
    rm -f /tmp/openobserve.tar.gz
}

configure_openobserve_ingestion() {
    print_header "Configuring OpenObserve Log Ingestion"
    
    # Install Fluent Bit
    print_info "Installing Fluent Bit..."
    
    if ! command -v fluent-bit &> /dev/null; then
        # Add Fluent Bit repository
        curl -fsSL https://packages.fluentbit.io/fluentbit.key | gpg --dearmor -o /usr/share/keyrings/fluentbit-keyring.gpg 2>/dev/null
        
        DISTRO_CODENAME=$(lsb_release -cs)
        echo "deb [signed-by=/usr/share/keyrings/fluentbit-keyring.gpg] https://packages.fluentbit.io/ubuntu/${DISTRO_CODENAME} ${DISTRO_CODENAME} main" | tee /etc/apt/sources.list.d/fluent-bit.list > /dev/null
        
        # Update package list and install Fluent Bit
        if safe_apt update && safe_apt install fluent-bit; then
            print_success "Fluent Bit installed"
        else
            print_error "Failed to install Fluent Bit"
            print_info "You may need to run: sudo apt-get install fluent-bit manually"
            return 1
        fi
    else
        print_success "Fluent Bit already installed"
    fi
    
    # Get OpenObserve credentials
    if [ -f /opt/openobserve/.credentials ]; then
        source /opt/openobserve/.credentials
        OO_USER="$EMAIL"
        OO_PASS="$PASSWORD"
    else
        print_warning "OpenObserve credentials not found, using defaults"
        OO_USER="admin@localhost"
        OO_PASS="admin"
    fi
    
    # Create Fluent Bit config for nginx logs → OpenObserve
    print_info "Configuring Fluent Bit for log collection..."
    
    mkdir -p /etc/fluent-bit
    
    # Create parser config
    cat > /etc/fluent-bit/parsers.conf << 'PARSERS_EOF'
[PARSER]
    Name        json
    Format      json
    Time_Key    ts
    Time_Format %Y-%m-%dT%H:%M:%S%z
PARSERS_EOF
    
    # Create main config with automatic routing for each API
    cat > /etc/fluent-bit/fluent-bit.conf << 'FLUENTBIT_HEADER'
[SERVICE]
    Flush         5
    Daemon        Off
    Log_Level     info
    Parsers_File  /etc/fluent-bit/parsers.conf

[INPUT]
    Name              tail
    Path              /var/log/nginx/access.log
    Tag               nginx.access
    Parser            json
    Refresh_Interval  5
    DB                /var/lib/fluent-bit/nginx-access.db
    Skip_Long_Lines   On
    Skip_Empty_Lines  On

[INPUT]
    Name              tail
    Path              /var/log/nginx/error.log
    Tag               nginx.error
    Refresh_Interval  5
    DB                /var/lib/fluent-bit/nginx-error.db
    Skip_Empty_Lines  On

FLUENTBIT_HEADER

    # Generate rewrite_tag filters for each API
    while IFS= read -r api; do
        API_NAME=$(echo "$api" | jq -r '.name')
        cat >> /etc/fluent-bit/fluent-bit.conf << FILTER_EOF
# Route logs for API: ${API_NAME}
[FILTER]
    Name          rewrite_tag
    Match         nginx.access
    Rule          \$api ^${API_NAME}\$ api.${API_NAME} false
    Emitter_Name  re_emitted_${API_NAME}

FILTER_EOF
    done < <(jq -c '.apis[] | select(.enabled == true)' "$CONFIG_FILE")

    # Generate OUTPUT for each API
    while IFS= read -r api; do
        API_NAME=$(echo "$api" | jq -r '.name')
        cat >> /etc/fluent-bit/fluent-bit.conf << OUTPUT_EOF
# Send logs for API: ${API_NAME}
[OUTPUT]
    Name          http
    Match         api.${API_NAME}
    Host          localhost
    Port          5080
    URI           /observe/api/default/${API_NAME}_logs/_json
    Format        json
    HTTP_User     ${OO_USER}
    HTTP_Passwd   ${OO_PASS}
    compress      gzip

OUTPUT_EOF
    done < <(jq -c '.apis[] | select(.enabled == true)' "$CONFIG_FILE")

    # Add nginx error and catch-all outputs
    cat >> /etc/fluent-bit/fluent-bit.conf << 'FLUENTBIT_FOOTER'
# Nginx error logs
[OUTPUT]
    Name          http
    Match         nginx.error
    Host          localhost
    Port          5080
    URI           /observe/api/default/nginx_error/_json
    Format        json
    HTTP_User     ${OO_USER}
    HTTP_Passwd   ${OO_PASS}
    compress      gzip

# Catch-all for other requests (OpenObserve UI, main page, etc)
[OUTPUT]
    Name          http
    Match         nginx.access
    Host          localhost
    Port          5080
    URI           /observe/api/default/nginx_other/_json
    Format        json
    HTTP_User     ${OO_USER}
    HTTP_Passwd   ${OO_PASS}
    compress      gzip
FLUENTBIT_FOOTER
    
    # Create DB directory for Fluent Bit
    mkdir -p /var/lib/fluent-bit
    chown fluent-bit:fluent-bit /var/lib/fluent-bit 2>/dev/null || chown root:root /var/lib/fluent-bit
    
    print_success "Fluent Bit configured"
    
    # Wait for OpenObserve to be fully ready
    print_info "Waiting for OpenObserve to be ready..."
    OO_READY=false
    for i in {1..12}; do
        if curl -sf --max-time 2 http://localhost:5080/healthz > /dev/null 2>&1; then
            OO_READY=true
            break
        fi
        sleep 5
    done
    
    if [ "$OO_READY" = false ]; then
        print_warning "OpenObserve took longer than expected to start"
        print_info "Fluent Bit will start collecting logs once OpenObserve is ready"
    else
        print_success "OpenObserve is ready"
    fi
    
    # Start Fluent Bit
    systemctl daemon-reload
    systemctl enable fluent-bit
    systemctl restart fluent-bit
    
    sleep 2
    
    if systemctl is-active --quiet fluent-bit; then
        print_success "Fluent Bit service started successfully"
        print_info "Nginx logs are now being collected to OpenObserve"
    else
        print_warning "Fluent Bit failed to start (check with: systemctl status fluent-bit)"
        print_info "Check logs: sudo journalctl -u fluent-bit -n 50"
    fi
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
    echo -e "${GREEN}🖥️ Gateway UI:${NC}"
    echo -e "  ${BLUE}http://$SERVER_IP:$LISTEN_PORT/${NC} (home)"
    echo -e "  ${BLUE}http://$SERVER_IP:$LISTEN_PORT/deployments/${NC} (deployments)"
    echo ""
    echo -e "${GREEN}📊 OpenObserve:${NC}"
    echo -e "  ${BLUE}http://$SERVER_IP:$LISTEN_PORT/observe/${NC}"
    
    # Display OpenObserve credentials
    if [ -f /opt/openobserve/.credentials ]; then
        source /opt/openobserve/.credentials
        echo -e "  ${CYAN}Email: ${EMAIL}${NC}"
        echo -e "  ${YELLOW}Password: ${PASSWORD}${NC}"
        echo -e "  ${CYAN}(Change password after first login in Settings)${NC}"
    else
        echo -e "  ${CYAN}Email: (check /opt/openobserve/.credentials)${NC}"
        echo -e "  ${YELLOW}Password: (check /opt/openobserve/.credentials)${NC}"
    fi
    
    echo -e "  ${CYAN}SQL queries, real-time search, beautiful dashboards${NC}"
    echo ""
    echo -e "${GREEN}Management Commands:${NC}"
    echo -e "  ${YELLOW}api-manage list${NC}                   - List all APIs (no sudo needed)"
    echo -e "  ${YELLOW}sudo api-manage add <name> <port>${NC} - Add new API"
    echo -e "  ${YELLOW}sudo api-manage remove <name>${NC}     - Remove API"
    echo -e "  ${YELLOW}sudo api-manage reload${NC}            - Reload configuration"
    echo ""
    echo -e "${GREEN}Configuration Files:${NC}"
    echo -e "  APIs config:       ${BLUE}$CONFIG_FILE${NC}"
    echo -e "  Nginx config:      ${BLUE}$NGINX_SITES_AVAILABLE/apis${NC}"
    echo -e "  OpenObserve data:  ${BLUE}/opt/openobserve/data${NC}"
    echo -e "  Fluent Bit config: ${BLUE}/etc/fluent-bit/fluent-bit.conf${NC}"
    echo ""
    echo -e "${YELLOW}Next Steps:${NC}"
    echo "  1. Edit $CONFIG_FILE to add your APIs"
    echo "  2. Or use: sudo api-manage add my-service 3000"
    echo "  3. Configuration will auto-reload on changes"
    echo "  4. Access OpenObserve to search and analyze logs"
    echo "  5. In OpenObserve: Logs → Select stream → Run SQL queries"
    echo ""
    echo -e "${YELLOW}Extended Features:${NC}"
    echo "  • GitHub Auto-Deploy: api-manage-extended deploy add <name> <repo> <branch> <port>"
    echo "  • Webhook Server: auto-started (configure GitHub: api-manage-extended webhook setup <name>)"
    echo "  • Web UI: http://$SERVER_IP:$LISTEN_PORT/ (auto-started)"
    echo "  • System Status: api-manage-extended status"
    echo ""
    echo -e "${YELLOW}Quick Start with Auto-Deploy:${NC}"
    echo "  1. Add deployment: api-manage-extended deploy add my-app https://github.com/user/repo main 3000"
    echo "  2. Setup GitHub webhook: api-manage-extended webhook setup <name>"
    echo "  3. UI ready at / and /deployments/"
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
    create_fluentbit_generator_script
    create_management_script
    install_extended_modules
    setup_auto_reload
    setup_openobserve
    configure_openobserve_ingestion
    configure_nginx
    # Ensure nginx config includes /dashboard/ and /webhook/ from the start (single port)
    print_info "Applying final Nginx config (dashboard + webhook on port $LISTEN_PORT)..."
    "$SCRIPT_DIR/generate-nginx-config"
    if nginx -t 2>/dev/null; then
        systemctl reload nginx
        print_success "Nginx config applied"
    fi
    print_completion_info
    
    echo ""
    print_success "Installation finished successfully!"
    echo ""
}

# Run main installation
main "$@"
