# API Gateway Installer

Automatic installer for Nginx-based API Gateway system running on port 422.

## What This Installer Does

- ✅ Installs all required dependencies (nginx, jq, inotify-tools)
- ✅ Creates configuration directory and JSON files
- ✅ Generates Nginx configuration automatically
- ✅ Creates management CLI tool (`api-manage`)
- ✅ Sets up auto-reload service (changes apply automatically)
- ✅ Configures Nginx to serve on port 422
- ✅ Installs OpenObserve for log analytics and monitoring
- ✅ Backs up existing configurations
- ✅ Handles re-installation (won't break existing setup)

## Requirements

- Ubuntu/Debian-based Linux system
- Root access (sudo)
- Port 422 available

## Installation

### 1. Copy files to your server

```bash
# Replace USER and SERVER_IP with your values
scp install.sh user@SERVER_IP:~/

# Or upload to server manually
```

### 2. Run the installer on server

```bash
ssh user@SERVER_IP
cd ~
chmod +x install.sh
sudo ./install.sh
```

That's it! The script will:
- Check what's already installed
- Install missing components
- Create all necessary scripts and configs
- Start the API Gateway

## Usage After Installation

### View all APIs
```bash
# No sudo needed for viewing
api-manage list
```

### Add new API
```bash
# Basic: sudo api-manage add <name> <port>
sudo api-manage add my-service 3000

# With custom path and description
sudo api-manage add payment-api 4000 /payments "Payment processing API"
```

### Remove API
```bash
# Requires sudo
sudo api-manage remove my-service
```

### Enable/Disable API
```bash
# Requires sudo
sudo api-manage disable my-service
sudo api-manage enable my-service
```

### Reload configuration manually
```bash
# Requires sudo
sudo api-manage reload
```

> **Note:** All configuration changes require root privileges. Use `sudo` for add/remove/enable/disable/reload commands. Only `list` can be run without sudo.

## Configuration

### Main config file
```bash
sudo nano /etc/api-gateway/apis.json
```

Example structure:
```json
{
  "apis": [
    {
      "name": "my-api",
      "path": "/my-api",
      "port": 3000,
      "description": "My API Service",
      "enabled": true
    },
    {
      "name": "another-api",
      "path": "/another",
      "port": 3001,
      "description": "Another Service",
      "enabled": false
    }
  ]
}
```

### Auto-reload

The system automatically watches for changes to `apis.json` and regenerates Nginx configuration.

To check auto-reload service status:
```bash
sudo systemctl status api-gateway-watch
```

## Accessing Your APIs

After installation, access your gateway at:
```
http://YOUR_SERVER_IP:422
```

Each API will be available at:
```
http://YOUR_SERVER_IP:422/api-path/
```

Example:
- Main page: `http://YOUR_SERVER_IP:422`
- API 1: `http://YOUR_SERVER_IP:422/my-api/`
- API 2: `http://YOUR_SERVER_IP:422/payments/`

## Troubleshooting

### Check Nginx status
```bash
sudo systemctl status nginx
sudo nginx -t
```

### Check which ports are listening
```bash
sudo netstat -tlnp | grep 422
```

### View logs in OpenObserve
Access the dashboard at:
```
http://YOUR_SERVER_IP:422/observe/
```

Or view raw Nginx logs:
```bash
sudo tail -f /var/log/nginx/error.log
sudo tail -f /var/log/nginx/access.log
```

### Manually regenerate config
```bash
sudo generate-nginx-config
```

### Restart everything
```bash
sudo systemctl restart nginx
sudo systemctl restart api-gateway-watch
sudo systemctl restart openobserve
sudo systemctl restart fluent-bit
```

## Uninstallation

To completely remove the API Gateway:

```bash
chmod +x uninstall.sh
sudo ./uninstall.sh
```

This will remove:
- API Gateway configuration
- Nginx configuration
- Management scripts
- OpenObserve and Fluent Bit
- Auto-reload service

Your backend services will remain untouched.

## Files Created

| File | Purpose |
|------|---------|
| `/etc/api-gateway/apis.json` | API configuration database |
| `/etc/nginx/sites-available/apis` | Nginx configuration |
| `/usr/local/bin/generate-nginx-config` | Config generator script |
| `/usr/local/bin/api-manage` | Management CLI tool |
| `/usr/local/bin/api-gateway-watch` | Auto-reload watcher |
| `/etc/systemd/system/api-gateway-watch.service` | Systemd service |
| `/opt/openobserve/` | OpenObserve installation and data |
| `/etc/fluent-bit/fluent-bit.conf` | Log collector configuration |

## Re-installation

Safe to run multiple times! The installer will:
- Skip already installed packages
- Backup existing configurations
- Preserve your API list
- Update scripts to latest version

## Support

For issues or questions, check:
1. Nginx error logs: `/var/log/nginx/error.log`
2. Service status: `sudo systemctl status api-gateway-watch`
3. Config test: `sudo nginx -t`

## License

Free to use and modify.
