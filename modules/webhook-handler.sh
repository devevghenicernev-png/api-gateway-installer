#!/bin/bash

# GitHub Webhook Handler Module
# Handles incoming GitHub webhooks for automatic deployment

WEBHOOK_PORT=9876
WEBHOOK_LOG="/var/log/api-gateway/webhook.log"
WEBHOOK_PID_FILE="/var/run/api-gateway-webhook.pid"

# Start webhook server
start_webhook_server() {
    print_header "Starting GitHub Webhook Server"
    
    # Check if already running
    if [ -f "$WEBHOOK_PID_FILE" ] && kill -0 "$(cat "$WEBHOOK_PID_FILE")" 2>/dev/null; then
        print_warning "Webhook server is already running"
        return 0
    fi
    
    # Create webhook handler script
    create_webhook_handler
    
    # Start webhook server using Node.js
    nohup node /opt/api-gateway/web-ui/webhook-server.js > "$WEBHOOK_LOG" 2>&1 &
    echo $! > "$WEBHOOK_PID_FILE"
    
    sleep 2
    
    if kill -0 "$(cat "$WEBHOOK_PID_FILE")" 2>/dev/null; then
        print_success "Webhook server started on port $WEBHOOK_PORT"
        print_info "Log file: $WEBHOOK_LOG"
    else
        print_error "Failed to start webhook server"
        return 1
    fi
}

# Stop webhook server
stop_webhook_server() {
    if [ -f "$WEBHOOK_PID_FILE" ]; then
        local pid=$(cat "$WEBHOOK_PID_FILE")
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid"
            rm "$WEBHOOK_PID_FILE"
            print_success "Webhook server stopped"
        else
            print_info "Webhook server was not running"
            rm -f "$WEBHOOK_PID_FILE"
        fi
    else
        print_info "Webhook server PID file not found"
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

# Get webhook URL for service
get_webhook_url() {
    local service_name="$1"
    local server_ip=$(curl -s ifconfig.me 2>/dev/null || echo "YOUR_SERVER_IP")
    
    echo "http://${server_ip}:${WEBHOOK_PORT}/webhook/${service_name}"
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
    
    print_header "GitHub Webhook Setup for $service_name"
    echo ""
    echo "1. Go to your GitHub repository settings"
    echo "2. Navigate to Settings > Webhooks"
    echo "3. Click 'Add webhook'"
    echo "4. Configure the webhook:"
    echo ""
    echo "   Payload URL: $webhook_url"
    echo "   Content type: application/json"
    echo "   Secret: $webhook_secret"
    echo "   Events: Just the push event"
    echo "   Active: ✓"
    echo ""
    echo "5. Click 'Add webhook'"
    echo ""
    print_info "The webhook will trigger automatic deployment on push to the configured branch"
}