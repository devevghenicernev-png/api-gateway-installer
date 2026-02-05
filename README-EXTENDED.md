# API Gateway with GitHub Auto-Deploy

Enhanced API Gateway with GitHub integration, automatic deployment, and web dashboard.

## 🚀 Features

### Core Features
- **Nginx Reverse Proxy** - Route requests to multiple backend services
- **OpenObserve Integration** - Centralized logging and analytics
- **Fluent Bit** - Log collection and forwarding
- **Auto-reload** - Configuration changes applied automatically

### Extended Features
- **🔄 GitHub Auto-Deploy** - Automatic deployment from GitHub repositories
- **🪝 Webhook Server** - GitHub webhook integration for CI/CD
- **📊 Web Dashboard** - Modern UI for deployment management
- **📦 Service Management** - Deploy, monitor, and manage services
- **📋 Real-time Logs** - View deployment and service logs
- **🔧 System Monitoring** - Resource usage and service status

## 📁 Project Structure

```
api-gateway-installer/
├── install.sh              # Main installation script
├── uninstall.sh            # Uninstallation script
├── modules/                 # Modular components
│   ├── common.sh           # Shared utilities and functions
│   ├── deployment-manager.sh # Deployment management
│   └── webhook-handler.sh   # GitHub webhook handling
├── web-ui/                 # Web dashboard
│   ├── dashboard.html      # Main dashboard interface
│   ├── server.js           # Backend API server (Node.js)
│   ├── webhook-server.js   # GitHub webhook server (Node.js)
│   └── package.json        # Node.js dependencies
├── scripts/                # Management scripts
│   └── api-manage-extended # Extended management tool
├── configs/                # Configuration templates
├── templates/              # File templates
└── docs/                   # Documentation
```

## 🛠️ Installation

### Requirements
- Ubuntu/Debian Linux
- Node.js 14+ (automatically installed)
- Root/sudo access

### Quick Install
```bash
curl -fsSL https://raw.githubusercontent.com/your-repo/api-gateway-installer/main/install.sh | sudo bash
```

### Manual Install
```bash
git clone https://github.com/your-repo/api-gateway-installer.git
cd api-gateway-installer
sudo ./install.sh
```

## 📖 Usage

### Basic API Management
```bash
# Add API service
sudo api-manage add my-api 3000

# List services
sudo api-manage list

# Remove service
sudo api-manage remove my-api
```

### Extended Management
```bash
# Show all available commands
api-manage-extended help

# System status
api-manage-extended status
```

### GitHub Auto-Deploy Setup

#### 1. Add Deployment Configuration
```bash
api-manage-extended deploy add my-app https://github.com/user/repo main 3000 "npm install && npm run build" "npm start"
```

#### 2. Start Webhook Server
```bash
api-manage-extended webhook start
```

#### 3. Configure GitHub Webhook
```bash
# Get webhook setup instructions
api-manage-extended webhook setup my-app
```

#### 4. Start Web Dashboard
```bash
api-manage-extended dashboard start
```

### Web Dashboard

Access the web dashboard at: `http://your-server-ip:8080`

Features:
- 📊 Service status overview
- 🚀 One-click deployments
- 📋 Real-time log viewing
- 🔄 Service restart/management
- 📈 System resource monitoring

## 🔧 Configuration

### API Configuration
Edit `/etc/api-gateway/apis.json`:
```json
{
  "apis": [
    {
      "name": "my-api",
      "port": 3000,
      "path": "/api",
      "enabled": true
    }
  ]
}
```

### Deployment Configuration
Located in `/etc/api-gateway/deployments/`:
```json
{
  "service_name": "my-app",
  "github_repo": "https://github.com/user/repo",
  "branch": "main",
  "port": 3000,
  "build_command": "npm install && npm run build",
  "start_command": "npm start",
  "auto_deploy": true
}
```

## 📋 Commands Reference

### Deployment Management
```bash
# Add new deployment
api-manage-extended deploy add <name> <repo> [branch] <port> [build_cmd] [start_cmd]

# Deploy service
api-manage-extended deploy run <name>

# List deployments
api-manage-extended deploy list

# Show deployment status
api-manage-extended deploy status [name]

# Remove deployment
api-manage-extended deploy remove <name>

# View deployment logs
api-manage-extended deploy logs <name>
```

### Webhook Management
```bash
# Start webhook server
api-manage-extended webhook start

# Stop webhook server
api-manage-extended webhook stop

# Show webhook status
api-manage-extended webhook status

# Get webhook URL for service
api-manage-extended webhook url <name>

# Show GitHub setup instructions
api-manage-extended webhook setup <name>
```

### Dashboard Management
```bash
# Start web dashboard
api-manage-extended dashboard start

# Stop web dashboard
api-manage-extended dashboard stop

# Show dashboard status
api-manage-extended dashboard status

# Get dashboard URL
api-manage-extended dashboard url
```

### System Management
```bash
# Show overall system status
api-manage-extended status

# View system logs
api-manage-extended logs [service]

# Create configuration backup
api-manage-extended backup

# Restore from backup
api-manage-extended restore <backup_file>
```

## 🔍 Monitoring & Logs

### OpenObserve Dashboard
- URL: `http://your-server-ip:5080`
- Username: Your configured email
- Password: Your configured password

### Log Locations
- System logs: `/var/log/api-gateway/system.log`
- Webhook logs: `/var/log/api-gateway/webhook.log`
- Dashboard logs: `/var/log/api-gateway/dashboard.log`
- Deployment logs: `/var/log/api-gateway/deployments/`
- Service logs: `/var/log/api-gateway/services/`

### Service Status
```bash
# Check individual services
systemctl status nginx
systemctl status openobserve
systemctl status fluent-bit
systemctl status api-gateway-webhook
systemctl status api-gateway-dashboard

# Check deployed services
systemctl status my-app
```

## 🛡️ Security

### Webhook Security
- Each service has a unique webhook secret
- Webhook signatures are verified using HMAC-SHA256
- Only push events to configured branches trigger deployments

### Service Security
- Services run as `www-data` user
- Restricted file system access
- No new privileges allowed
- Protected system directories

### Network Security
- Services bind to localhost by default
- Nginx handles external access
- Configurable port ranges
- Rate limiting available

## 🚨 Troubleshooting

### Common Issues

#### Webhook Not Triggering
```bash
# Check webhook server status
api-manage-extended webhook status

# View webhook logs
tail -f /var/log/api-gateway/webhook.log

# Verify GitHub webhook configuration
api-manage-extended webhook setup <service-name>
```

#### Deployment Fails
```bash
# Check deployment logs
api-manage-extended deploy logs <service-name>

# Check service status
systemctl status <service-name>

# Manual deployment
api-manage-extended deploy run <service-name>
```

#### Dashboard Not Loading
```bash
# Check dashboard status
api-manage-extended dashboard status

# View dashboard logs
tail -f /var/log/api-gateway/dashboard.log

# Restart dashboard
api-manage-extended dashboard stop
api-manage-extended dashboard start
```

### Log Analysis
```bash
# View all system logs
journalctl -u nginx -u openobserve -u fluent-bit --since "1 hour ago"

# Monitor deployment in real-time
tail -f /var/log/api-gateway/deployments/<service-name>-*.log

# Check resource usage
api-manage-extended status
```

## 🔄 Updates

### Updating the System
```bash
# Create backup before updating
api-manage-extended backup

# Pull latest changes
git pull

# Run installation to update
sudo ./install.sh
```

### Updating Services
Services are automatically updated when:
- GitHub webhook is triggered (push to configured branch)
- Manual deployment is run
- Auto-deploy is enabled and repository changes

## 📚 Examples

### Node.js Application
```bash
# Add Node.js app with custom build
api-manage-extended deploy add my-node-app \
  https://github.com/user/node-app \
  main \
  3000 \
  "npm ci && npm run build" \
  "npm start"
```

### Python Flask Application
```bash
# Add Python Flask app
api-manage-extended deploy add my-flask-app \
  https://github.com/user/flask-app \
  main \
  5000 \
  "pip install -r requirements.txt" \
  "python app.py"
```

### Static Website
```bash
# Add static site with nginx serving
api-manage-extended deploy add my-website \
  https://github.com/user/website \
  main \
  8080 \
  "npm install && npm run build" \
  "npx serve -s dist -l 8080"
```

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Test thoroughly
5. Submit a pull request

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🆘 Support

- 📖 Documentation: Check this README and inline help
- 🐛 Issues: Report bugs on GitHub
- 💬 Discussions: Join GitHub Discussions
- 📧 Email: Contact maintainers

---

**Made with ❤️ for modern DevOps workflows**