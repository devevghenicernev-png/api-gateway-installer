# 🚀 Quick Start Guide - Extended API Gateway

## Installation

```bash
sudo ./install.sh
```

## Basic Usage

### 1. Add Your First Auto-Deploy Service

```bash
# Add Node.js app from GitHub
api-manage-extended deploy add my-app https://github.com/user/my-node-app main 3000

# Add with custom build commands
api-manage-extended deploy add my-api \
  https://github.com/user/my-api \
  main \
  3001 \
  "npm install && npm run build" \
  "npm start"
```

### 2. Start Webhook Server (for auto-deploy)

```bash
api-manage-extended webhook start
```

### 3. Setup GitHub Webhook

```bash
# Get webhook configuration
api-manage-extended webhook setup my-app
```

Follow the instructions to add webhook to your GitHub repository.

### 4. Start Web Dashboard

```bash
api-manage-extended dashboard start
```

Access dashboard at: `http://your-server-ip:8080`

### 5. Deploy Your Service

```bash
# Manual deployment
api-manage-extended deploy run my-app

# Or push to GitHub (if webhook is configured)
git push origin main
```

## Quick Commands

```bash
# System status
api-manage-extended status

# List all deployments
api-manage-extended deploy list

# View service logs
api-manage-extended deploy logs my-app

# Restart service
systemctl restart my-app

# View all logs
api-manage-extended logs
```

## Access Points

- **Web Dashboard**: `http://your-server-ip:8080`
- **OpenObserve**: `http://your-server-ip:5080`
- **Your APIs**: `http://your-server-ip/api-path/`

## Example Workflow

1. **Push code** to GitHub repository
2. **Webhook triggers** automatic deployment
3. **Service builds** and starts automatically  
4. **Monitor** via web dashboard
5. **View logs** in OpenObserve or dashboard

That's it! Your API Gateway with auto-deploy is ready! 🎉