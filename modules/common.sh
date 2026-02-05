#!/bin/bash

# Common functions and utilities
# Shared across all modules

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Print functions
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

print_info() {
    echo -e "${CYAN}ℹ${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
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

# Check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Check if service is running
service_is_running() {
    systemctl is-active --quiet "$1"
}

# Check if service is enabled
service_is_enabled() {
    systemctl is-enabled --quiet "$1" 2>/dev/null
}

# Get service status with color coding
get_service_status() {
    local service_name="$1"
    
    if service_is_running "$service_name"; then
        echo -e "${GREEN}✓ Running${NC}"
    else
        echo -e "${RED}✗ Stopped${NC}"
    fi
}

# Validate JSON file
validate_json() {
    local file="$1"
    
    if [ ! -f "$file" ]; then
        return 1
    fi
    
    jq empty "$file" 2>/dev/null
}

# Generate random string
generate_random_string() {
    local length="${1:-32}"
    openssl rand -hex "$length"
}

# Get current timestamp in ISO format
get_timestamp() {
    date -Iseconds
}

# Check if port is available
port_is_available() {
    local port="$1"
    ! netstat -tuln | grep -q ":$port "
}

# Find available port starting from given port
find_available_port() {
    local start_port="$1"
    local port="$start_port"
    
    while ! port_is_available "$port"; do
        port=$((port + 1))
        if [ "$port" -gt 65535 ]; then
            return 1
        fi
    done
    
    echo "$port"
}

# Create directory with proper permissions
create_secure_directory() {
    local dir="$1"
    local owner="${2:-root:root}"
    local permissions="${3:-755}"
    
    mkdir -p "$dir"
    chown "$owner" "$dir"
    chmod "$permissions" "$dir"
}

# Backup file with timestamp
backup_file() {
    local file="$1"
    local backup_dir="${2:-/var/backups/api-gateway}"
    
    if [ -f "$file" ]; then
        mkdir -p "$backup_dir"
        local backup_name="$(basename "$file").$(date +%Y%m%d-%H%M%S).bak"
        cp "$file" "$backup_dir/$backup_name"
        print_info "Backup created: $backup_dir/$backup_name"
    fi
}

# Validate GitHub repository URL
validate_github_repo() {
    local repo_url="$1"
    
    # Check if it's a valid GitHub URL format
    if [[ "$repo_url" =~ ^https://github\.com/[^/]+/[^/]+/?$ ]]; then
        return 0
    elif [[ "$repo_url" =~ ^git@github\.com:[^/]+/[^/]+\.git$ ]]; then
        return 0
    elif [[ "$repo_url" =~ ^[^/]+/[^/]+$ ]]; then
        # Short format like "user/repo"
        return 0
    else
        return 1
    fi
}

# Convert GitHub repo to HTTPS URL
normalize_github_repo() {
    local repo="$1"
    
    if [[ "$repo" =~ ^https://github\.com/ ]]; then
        echo "$repo"
    elif [[ "$repo" =~ ^git@github\.com:(.+)\.git$ ]]; then
        echo "https://github.com/${BASH_REMATCH[1]}"
    elif [[ "$repo" =~ ^[^/]+/[^/]+$ ]]; then
        echo "https://github.com/$repo"
    else
        echo "$repo"
    fi
}

# Check if user has sudo privileges
check_sudo() {
    if [ "$EUID" -ne 0 ]; then
        print_error "This script must be run as root or with sudo"
        exit 1
    fi
}

# Get system information
get_system_info() {
    echo "OS: $(lsb_release -d 2>/dev/null | cut -f2 || uname -s)"
    echo "Kernel: $(uname -r)"
    echo "Architecture: $(uname -m)"
    echo "Memory: $(free -h | awk '/^Mem:/ {print $2}')"
    echo "Disk: $(df -h / | awk 'NR==2 {print $4 " available"}')"
}

# Log function with timestamp
log_message() {
    local level="$1"
    local message="$2"
    local log_file="${3:-/var/log/api-gateway/system.log}"
    
    mkdir -p "$(dirname "$log_file")"
    echo "$(get_timestamp) [$level] $message" >> "$log_file"
}

# Cleanup function for temporary files
cleanup_temp_files() {
    local temp_dir="/tmp/api-gateway-$$"
    if [ -d "$temp_dir" ]; then
        rm -rf "$temp_dir"
    fi
}

# Set trap for cleanup on exit
set_cleanup_trap() {
    trap cleanup_temp_files EXIT
}

# Check network connectivity
check_network() {
    if ! ping -c 1 8.8.8.8 >/dev/null 2>&1; then
        print_warning "Network connectivity check failed"
        return 1
    fi
    return 0
}

# Wait for service to be ready
wait_for_service() {
    local service_name="$1"
    local max_wait="${2:-60}"
    local wait_time=0
    
    while [ $wait_time -lt $max_wait ]; do
        if service_is_running "$service_name"; then
            return 0
        fi
        sleep 2
        wait_time=$((wait_time + 2))
    done
    
    return 1
}