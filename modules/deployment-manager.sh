#!/bin/bash

# Deployment Manager Module
# Handles GitHub auto-deployment, service management, and deployment status tracking

DEPLOY_CONFIG_DIR="/etc/api-gateway/deployments"
DEPLOY_LOG_DIR="/var/log/api-gateway/deployments"
DEPLOY_STATUS_FILE="/var/lib/api-gateway/deployment-status.json"

# Initialize deployment manager
init_deployment_manager() {
    print_header "Initializing Deployment Manager"
    
    # Create required directories
    mkdir -p "$DEPLOY_CONFIG_DIR"
    mkdir -p "$DEPLOY_LOG_DIR"
    mkdir -p "$(dirname "$DEPLOY_STATUS_FILE")"
    
    # Initialize status file if not exists
    if [ ! -f "$DEPLOY_STATUS_FILE" ]; then
        echo '{"deployments": {}}' > "$DEPLOY_STATUS_FILE"
    fi
    
    print_success "Deployment manager initialized"
}

# Detect preferred process manager (pm2 if available, else systemd)
detect_process_manager() {
    if command -v pm2 &>/dev/null; then
        echo "pm2"
    else
        echo "systemd"
    fi
}

# Add new deployment configuration
add_deployment() {
    local service_name="$1"
    local github_repo="$2"
    local branch="${3:-main}"
    local port="$4"
    local build_command="${5:-npm install && npm run build}"
    local start_command="${6:-npm start}"
    
    if [ -z "$service_name" ] || [ -z "$github_repo" ] || [ -z "$port" ]; then
        print_error "Usage: add_deployment <service_name> <github_repo> [branch] <port> [build_command] [start_command]"
        return 1
    fi
    
    local config_file="$DEPLOY_CONFIG_DIR/${service_name}.json"
    local process_manager=$(detect_process_manager)
    
    if [ "$process_manager" = "pm2" ]; then
        print_info "Deployments will use PM2"
    fi
    
    # Create deployment configuration
    cat > "$config_file" << EOF
{
    "service_name": "$service_name",
    "github_repo": "$github_repo",
    "branch": "$branch",
    "port": $port,
    "build_command": "$build_command",
    "start_command": "$start_command",
    "deploy_path": "/opt/deployments/$service_name",
    "webhook_secret": "$(openssl rand -hex 32)",
    "auto_deploy": true,
    "process_manager": "$process_manager",
    "created_at": "$(date -Iseconds)",
    "status": "configured"
}
EOF
    
    print_success "Deployment configuration created for $service_name"
    print_info "Config file: $config_file"
    
    # Update deployment status
    update_deployment_status "$service_name" "configured" "Deployment configuration created"
}

# Deploy service from GitHub
deploy_service() {
    local service_name="$1"
    local force_deploy="${2:-false}"
    
    local config_file="$DEPLOY_CONFIG_DIR/${service_name}.json"
    if [ ! -f "$config_file" ]; then
        print_error "Deployment configuration not found for $service_name"
        return 1
    fi
    
    # Read configuration
    local github_repo=$(jq -r '.github_repo' "$config_file")
    local branch=$(jq -r '.branch' "$config_file")
    local port=$(jq -r '.port' "$config_file")
    local build_command=$(jq -r '.build_command' "$config_file")
    local start_command=$(jq -r '.start_command' "$config_file")
    local deploy_path=$(jq -r '.deploy_path' "$config_file")
    local process_manager=$(jq -r '.process_manager // "systemd"' "$config_file")
    
    local log_file="$DEPLOY_LOG_DIR/${service_name}-$(date +%Y%m%d-%H%M%S).log"
    
    print_header "Deploying $service_name"
    update_deployment_status "$service_name" "deploying" "Starting deployment process"
    
    {
        echo "=== Deployment started at $(date) ==="
        echo "Service: $service_name"
        echo "Repository: $github_repo"
        echo "Branch: $branch"
        echo "Deploy path: $deploy_path"
        echo ""
        
        # Create deployment directory
        mkdir -p "$deploy_path"
        cd "$deploy_path"
        
        # Clone or update repository
        if [ -d ".git" ] && [ "$force_deploy" != "true" ]; then
            echo "Updating existing repository..."
            git fetch origin
            git reset --hard "origin/$branch"
        else
            echo "Cloning repository..."
            rm -rf ./*
            git clone -b "$branch" "$github_repo" .
        fi
        
        # Run build command
        echo "Running build command: $build_command"
        eval "$build_command"
        
        if [ $? -eq 0 ]; then
            echo "Build completed successfully"
            
            if [ "$process_manager" = "pm2" ] && command -v pm2 &>/dev/null; then
                # Use PM2
                echo "Starting service with PM2..."
                if pm2 describe "$service_name" &>/dev/null; then
                    cd "$deploy_path" && PORT=$port NODE_ENV=production pm2 restart "$service_name" --update-env
                else
                    cd "$deploy_path" && PORT=$port NODE_ENV=production pm2 start npm --name "$service_name" -- start
                fi
                pm2 save
                sleep 2
                if pm2 list 2>/dev/null | grep -w "$service_name" | grep -q "online"; then
                    echo "Service started successfully (PM2)"
                    update_deployment_status "$service_name" "running" "Deployment completed successfully (PM2)"
                    print_success "Deployment of $service_name completed successfully"
                else
                    echo "Failed to start service with PM2"
                    update_deployment_status "$service_name" "failed" "PM2 failed to start service"
                    print_error "Failed to start $service_name with PM2"
                    return 1
                fi
            else
                # Use systemd
                if systemctl is-active --quiet "$service_name" 2>/dev/null; then
                    echo "Stopping existing service..."
                    systemctl stop "$service_name"
                fi
                create_systemd_service "$service_name" "$deploy_path" "$start_command" "$port"
                echo "Starting service..."
                systemctl daemon-reload
                systemctl enable "$service_name"
                systemctl start "$service_name"
                if systemctl is-active --quiet "$service_name"; then
                    echo "Service started successfully"
                    update_deployment_status "$service_name" "running" "Deployment completed successfully"
                    print_success "Deployment of $service_name completed successfully"
                else
                    echo "Failed to start service"
                    update_deployment_status "$service_name" "failed" "Service failed to start"
                    print_error "Failed to start $service_name service"
                    return 1
                fi
            fi
        else
            echo "Build failed"
            update_deployment_status "$service_name" "failed" "Build process failed"
            print_error "Build failed for $service_name"
            return 1
        fi
        
        echo "=== Deployment completed at $(date) ==="
        
    } 2>&1 | tee "$log_file"
}

# Create systemd service file
create_systemd_service() {
    local service_name="$1"
    local deploy_path="$2"
    local start_command="$3"
    local port="$4"
    
    cat > "/etc/systemd/system/${service_name}.service" << EOF
[Unit]
Description=$service_name API Service
After=network.target

[Service]
Type=simple
User=www-data
WorkingDirectory=$deploy_path
ExecStart=/bin/bash -c '$start_command'
Restart=always
RestartSec=10
Environment=NODE_ENV=production
Environment=PORT=$port

# Logging
StandardOutput=append:/var/log/api-gateway/services/${service_name}.log
StandardError=append:/var/log/api-gateway/services/${service_name}.error.log

# Security
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$deploy_path /tmp /var/log/api-gateway

[Install]
WantedBy=multi-user.target
EOF
    
    # Create log directory for service
    mkdir -p "/var/log/api-gateway/services"
    chown www-data:www-data "/var/log/api-gateway/services"
}

# Update deployment status in JSON file
update_deployment_status() {
    local service_name="$1"
    local status="$2"
    local message="$3"
    local timestamp=$(date -Iseconds)
    
    # Create temporary file with updated status
    jq --arg name "$service_name" \
       --arg status "$status" \
       --arg message "$message" \
       --arg timestamp "$timestamp" \
       '.deployments[$name] = {
           "status": $status,
           "message": $message,
           "last_updated": $timestamp,
           "last_deployment": (if $status == "running" then $timestamp else (.deployments[$name].last_deployment // null) end)
       }' "$DEPLOY_STATUS_FILE" > "${DEPLOY_STATUS_FILE}.tmp"
    
    mv "${DEPLOY_STATUS_FILE}.tmp" "$DEPLOY_STATUS_FILE"
}

# Get deployment status
get_deployment_status() {
    local service_name="$1"
    
    if [ -n "$service_name" ]; then
        jq -r ".deployments[\"$service_name\"] // {\"status\": \"not_found\", \"message\": \"Service not configured\"}" "$DEPLOY_STATUS_FILE"
    else
        jq -r '.deployments' "$DEPLOY_STATUS_FILE"
    fi
}

# List all deployments
list_deployments() {
    print_header "Deployment Status"
    
    if [ ! -f "$DEPLOY_STATUS_FILE" ]; then
        print_info "No deployments configured"
        return
    fi
    
    # Get all deployment configurations
    for config_file in "$DEPLOY_CONFIG_DIR"/*.json; do
        if [ -f "$config_file" ]; then
            local service_name=$(basename "$config_file" .json)
            local github_repo=$(jq -r '.github_repo' "$config_file")
            local port=$(jq -r '.port' "$config_file")
            local status=$(jq -r ".deployments[\"$service_name\"].status // \"not_deployed\"" "$DEPLOY_STATUS_FILE")
            local last_updated=$(jq -r ".deployments[\"$service_name\"].last_updated // \"never\"" "$DEPLOY_STATUS_FILE")
            
            local process_manager=$(jq -r '.process_manager // "systemd"' "$config_file")
            echo "Service: $service_name"
            echo "  Repository: $github_repo"
            echo "  Port: $port"
            echo "  Process manager: $process_manager"
            echo "  Status: $status"
            echo "  Last Updated: $last_updated"
            
            if [ "$process_manager" = "pm2" ]; then
                if pm2 list 2>/dev/null | grep -w "$service_name" | grep -q "online"; then
                    echo "  System Status: ✅ Running (PM2)"
                else
                    echo "  System Status: ❌ Not Running"
                fi
            else
                if systemctl is-active --quiet "$service_name" 2>/dev/null; then
                    echo "  System Status: ✅ Running"
                else
                    echo "  System Status: ❌ Not Running"
                fi
            fi
            echo ""
        fi
    done
}

# Remove deployment
remove_deployment() {
    local service_name="$1"
    
    if [ -z "$service_name" ]; then
        print_error "Service name required"
        return 1
    fi
    
    local config_file="$DEPLOY_CONFIG_DIR/${service_name}.json"
    
    if [ ! -f "$config_file" ]; then
        print_error "Deployment configuration not found for $service_name"
        return 1
    fi
    
    local deploy_path=$(jq -r '.deploy_path' "$config_file")
    local process_manager=$(jq -r '.process_manager // "systemd"' "$config_file")
    
    print_header "Removing deployment: $service_name"
    
    if [ "$process_manager" = "pm2" ] && command -v pm2 &>/dev/null; then
        if pm2 describe "$service_name" &>/dev/null; then
            print_info "Stopping and removing from PM2..."
            pm2 delete "$service_name" || true
            pm2 save
        fi
    else
        if systemctl is-active --quiet "$service_name" 2>/dev/null; then
            print_info "Stopping service..."
            systemctl stop "$service_name"
        fi
        if systemctl is-enabled --quiet "$service_name" 2>/dev/null; then
            print_info "Disabling service..."
            systemctl disable "$service_name"
        fi
        if [ -f "/etc/systemd/system/${service_name}.service" ]; then
            rm "/etc/systemd/system/${service_name}.service"
            systemctl daemon-reload
        fi
    fi
    
    # Remove deployment directory
    if [ -d "$deploy_path" ]; then
        print_info "Removing deployment files..."
        rm -rf "$deploy_path"
    fi
    
    # Remove configuration
    rm "$config_file"
    
    # Update status file
    jq --arg name "$service_name" 'del(.deployments[$name])' "$DEPLOY_STATUS_FILE" > "${DEPLOY_STATUS_FILE}.tmp"
    mv "${DEPLOY_STATUS_FILE}.tmp" "$DEPLOY_STATUS_FILE"
    
    print_success "Deployment $service_name removed successfully"
}