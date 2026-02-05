#!/bin/bash

# GitHub Webhook Handler Module
# Handles incoming GitHub webhooks for automatic deployment

WEBHOOK_PORT=9876
WEBHOOK_LOG="/var/log/api-gateway/webhook.log"
WEBHOOK_PID_FILE="/var/run/api-gateway-webhook.pid"

# Check if webhook is running (PID file, systemd, or port)
is_webhook_running() {
    [ -f "$WEBHOOK_PID_FILE" ] && kill -0 "$(cat "$WEBHOOK_PID_FILE")" 2>/dev/null && return 0
    systemctl is-active --quiet api-gateway-webhook 2>/dev/null && return 0
    lsof -ti :"$WEBHOOK_PORT" >/dev/null 2>&1 && return 0
    return 1
}

# Start webhook server
start_webhook_server() {
    print_header "Starting GitHub Webhook Server"
    
    # Stop systemd service if running (uses same port)
    if systemctl is-active --quiet api-gateway-webhook 2>/dev/null; then
        print_info "Stopping systemd api-gateway-webhook service..."
        systemctl stop api-gateway-webhook 2>/dev/null || true
        sleep 1
    fi
    
    # Kill any process using port 9876 (stale webhook or orphan)
    local pid_on_port
    pid_on_port=$(lsof -ti :"$WEBHOOK_PORT" 2>/dev/null || true)
    if [ -n "$pid_on_port" ]; then
        print_info "Stopping existing process on port $WEBHOOK_PORT (PID: $pid_on_port)..."
        kill "$pid_on_port" 2>/dev/null || true
        sleep 2
    fi
    
    # Remove stale PID file
    if [ -f "$WEBHOOK_PID_FILE" ]; then
        if ! kill -0 "$(cat "$WEBHOOK_PID_FILE")" 2>/dev/null; then
            rm -f "$WEBHOOK_PID_FILE"
        else
            print_warning "Webhook server is already running (PID: $(cat "$WEBHOOK_PID_FILE"))"
            return 0
        fi
    fi
    
    # Create webhook handler script
    create_webhook_handler
    
    mkdir -p "$(dirname "$WEBHOOK_LOG")"
    
    # Start webhook server using Node.js
    nohup node /opt/api-gateway/web-ui/webhook-server.js >> "$WEBHOOK_LOG" 2>&1 &
    echo $! > "$WEBHOOK_PID_FILE"
    
    sleep 2
    
    if kill -0 "$(cat "$WEBHOOK_PID_FILE")" 2>/dev/null; then
        print_success "Webhook server started on port $WEBHOOK_PORT"
        print_info "Log file: $WEBHOOK_LOG"
    else
        print_error "Failed to start webhook server (check log: tail -20 $WEBHOOK_LOG)"
        return 1
    fi
}

# Stop webhook server
stop_webhook_server() {
    local stopped=0
    # Stop systemd service if running
    if systemctl is-active --quiet api-gateway-webhook 2>/dev/null; then
        systemctl stop api-gateway-webhook 2>/dev/null
        stopped=1
    fi
    # Stop by PID file
    if [ -f "$WEBHOOK_PID_FILE" ]; then
        local pid=$(cat "$WEBHOOK_PID_FILE")
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null
            stopped=1
        fi
        rm -f "$WEBHOOK_PID_FILE"
    fi
    # Fallback: kill process on port
    local pid_on_port
    pid_on_port=$(lsof -ti :"$WEBHOOK_PORT" 2>/dev/null || true)
    if [ -n "$pid_on_port" ]; then
        kill "$pid_on_port" 2>/dev/null
        stopped=1
    fi
    if [ "$stopped" -eq 1 ]; then
        print_success "Webhook server stopped"
    else
        print_info "Webhook server was not running"
    fi
}

# Create Node.js webhook server (already exists in web-ui directory)
create_webhook_handler() {
    # Webhook server is now part of web-ui module
    print_info "Webhook server available at /opt/api-gateway/web-ui/webhook-server.js"
}

# Create deployment trigger script
create_deploy_script() {
    mkdir -p /opt/api-gateway/scripts
    
    cat > /opt/api-gateway/scripts/deploy-service.sh << 'EOF'
#!/bin/bash

# Auto-deployment trigger script
# Called by webhook server to deploy services

SERVICE_NAME="$1"

if [ -z "$SERVICE_NAME" ]; then
    echo "Usage: $0 <service_name>"
    exit 1
fi

# Load print functions first (deployment-manager uses them)
source /opt/api-gateway/modules/common.sh
source /opt/api-gateway/modules/deployment-manager.sh

# Deploy the service
deploy_service "$SERVICE_NAME"
EOF

    chmod +x /opt/api-gateway/scripts/deploy-service.sh
}

# Create systemd service for webhook server
create_webhook_systemd_service() {
    cat > /etc/systemd/system/api-gateway-webhook.service << 'EOF'
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

# Logging
StandardOutput=append:/var/log/api-gateway/webhook.log
StandardError=append:/var/log/api-gateway/webhook.log

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
}

# Get webhook URL for service (external URL via nginx)
get_webhook_url() {
    local service_name="$1"
    local server_ip=$(curl -s ifconfig.me 2>/dev/null || echo "YOUR_SERVER_IP")
    local nginx_port
    nginx_port=$(grep 'listen' /etc/nginx/sites-available/apis 2>/dev/null | grep -oE 'listen\s+[0-9]+' | grep -oE '[0-9]+' | head -1)
    nginx_port=${nginx_port:-422}
    
    echo "http://${server_ip}:${nginx_port}/webhook/${service_name}"
}

# Show webhook setup instructions
show_webhook_instructions() {
    local service_name="$1"
    local config_file="$DEPLOY_CONFIG_DIR/${service_name}.json"
    
    if [ ! -f "$config_file" ]; then
        print_error "Service configuration not found"
        return 1
    fi
    
    local webhook_secret=$(jq -r '.webhook_secret' "$config_file")
    local webhook_url=$(get_webhook_url "$service_name")
    local github_repo=$(jq -r '.github_repo' "$config_file")
    local branch=$(jq -r '.branch // "main"' "$config_file")
    local repo_path
    repo_path=$(echo "$github_repo" | sed -E 's|^https?://github\.com/||;s|^git@github\.com:||;s|\.git$||')
    
    print_header "GitHub Webhook Setup for $service_name"
    echo ""
    echo -e "${CYAN}══════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}  Step-by-step: add webhook in GitHub${NC}"
    echo -e "${CYAN}══════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "  ${YELLOW}1. Open your repository on GitHub:${NC}"
    echo -e "     https://github.com/${repo_path}"
    echo ""
    echo -e "  ${YELLOW}2. Go to Settings → Webhooks:${NC}"
    echo -e "     Repository → Settings → Webhooks (left sidebar)"
    echo ""
    echo -e "  ${YELLOW}3. Click 'Add webhook'${NC}"
    echo ""
    echo -e "  ${YELLOW}4. Fill the form:${NC}"
    echo ""
    echo -e "     ${GREEN}Payload URL:${NC}"
    echo -e "     ${CYAN}$webhook_url${NC}"
    echo ""
    echo -e "     ${GREEN}Content type:${NC} application/json"
    echo ""
    echo -e "     ${GREEN}Secret:${NC}"
    echo -e "     ${CYAN}$webhook_secret${NC}"
    echo ""
    echo -e "     ${GREEN}Which events?${NC} Just the push event"
    echo ""
    echo -e "     ${GREEN}Active:${NC} ✓ (checked)"
    echo ""
    echo -e "     ${GREEN}SSL verification:${NC} Enable SSL verification (leave default)"
    echo ""
    echo -e "  ${YELLOW}5. Click 'Add webhook' (green button at bottom)${NC}"
    echo ""
    echo -e "${CYAN}══════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "  ${GREEN}Copy-paste values:${NC}"
    echo ""
    echo -e "  Payload URL:  ${CYAN}$webhook_url${NC}"
    echo -e "  Secret:       ${CYAN}$webhook_secret${NC}"
    echo ""
    echo -e "  ${CYAN}What happens:${NC} When you push to branch '${branch}', GitHub will send"
    echo -e "  a POST request to your server. The webhook will trigger a deploy."
    echo ""
    echo -e "  ${CYAN}Test:${NC} After adding, push a commit and check 'Recent Deliveries'"
    echo -e "  in the webhook settings — green ✓ means success."
    echo ""
    echo -e "  ${CYAN}Logs:${NC} tail -f /var/log/api-gateway/webhook.log"
    echo ""
}