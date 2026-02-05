#!/usr/bin/env node

/**
 * API Gateway Dashboard Server
 * Node.js-based REST API for the web dashboard
 */

const express = require('express');
const { spawn, exec } = require('child_process');
const fs = require('fs').promises;
const path = require('path');
const cors = require('cors');

// Configuration
const API_PORT = process.env.PORT || 8080;
const DEPLOY_CONFIG_DIR = '/etc/api-gateway/deployments';
const DEPLOY_STATUS_FILE = '/var/lib/api-gateway/deployment-status.json';
const APIS_CONFIG = '/etc/api-gateway/apis.json';
const LOG_DIR = '/var/log/api-gateway';
const WEB_UI_DIR = __dirname;

// Express app setup
const app = express();
app.use(cors());
app.use(express.json());
app.use(express.static(WEB_UI_DIR));

// Utility functions
const runCommand = (command, options = {}) => {
    return new Promise((resolve, reject) => {
        exec(command, options, (error, stdout, stderr) => {
            if (error) {
                reject({ error, stderr });
            } else {
                resolve({ stdout, stderr });
            }
        });
    });
};

const runCommandBackground = (command) => {
    const child = spawn('bash', ['-c', command], {
        detached: true,
        stdio: 'ignore'
    });
    child.unref();
    return child.pid;
};

const loadJsonFile = async (filePath, defaultValue = {}) => {
    try {
        const data = await fs.readFile(filePath, 'utf8');
        return JSON.parse(data);
    } catch (error) {
        return defaultValue;
    }
};

const saveJsonFile = async (filePath, data) => {
    try {
        await fs.writeFile(filePath, JSON.stringify(data, null, 2));
        return true;
    } catch (error) {
        console.error(`Error saving ${filePath}:`, error);
        return false;
    }
};

const checkServiceStatus = async (serviceName, processManager = 'systemd') => {
    try {
        if (processManager === 'pm2') {
            const { stdout } = await runCommand('pm2 list --no-color 2>/dev/null');
            return stdout.includes(serviceName) && stdout.includes('online');
        }
        const { stdout } = await runCommand(`systemctl is-active ${serviceName}`);
        return stdout.trim() === 'active';
    } catch {
        return false;
    }
};

// API Routes

// Serve home page
app.get('/', (req, res) => {
    res.sendFile(path.join(WEB_UI_DIR, 'index.html'));
});

// Serve deployment dashboard (no redirect to avoid loops)
const sendDashboard = (req, res) => res.sendFile(path.join(WEB_UI_DIR, 'dashboard.html'));
app.get('/deployments', sendDashboard);
app.get('/deployments/', sendDashboard);

// Backward compat: /dashboard serves same dashboard (no redirect to avoid loops)
app.get('/dashboard', sendDashboard);
app.get('/dashboard/', sendDashboard);

// Get APIs list for landing page
app.get('/api/landing-apis', async (req, res) => {
    try {
        const data = await loadJsonFile(APIS_CONFIG, { apis: [] });
        const apis = (data.apis || []).filter(a => a.enabled !== false);
        res.json({ success: true, apis });
    } catch (e) {
        res.json({ success: true, apis: [] });
    }
});

// Get all deployments
app.get('/api/deployments', async (req, res) => {
    try {
        const statusData = await loadJsonFile(DEPLOY_STATUS_FILE, { deployments: {} });
        const deployments = {};
        
        // Load deployment configurations
        try {
            const configFiles = await fs.readdir(DEPLOY_CONFIG_DIR);
            
            for (const configFile of configFiles) {
                if (configFile.endsWith('.json')) {
                    const serviceName = configFile.replace('.json', '');
                    const configPath = path.join(DEPLOY_CONFIG_DIR, configFile);
                    const config = await loadJsonFile(configPath);
                    
                    if (config) {
                        const deploymentStatus = statusData.deployments[serviceName] || {};
                        const processManager = config.process_manager || 'systemd';
                        const systemRunning = await checkServiceStatus(serviceName, processManager);
                        
                        deployments[serviceName] = {
                            config,
                            status: deploymentStatus.status || 'unknown',
                            message: deploymentStatus.message || '',
                            last_updated: deploymentStatus.last_updated,
                            last_deployment: deploymentStatus.last_deployment,
                            system_running: systemRunning
                        };
                    }
                }
            }
        } catch (error) {
            console.log('No deployment configurations found');
        }
        
        res.json({
            success: true,
            deployments,
            timestamp: new Date().toISOString()
        });
        
    } catch (error) {
        console.error('Error getting deployments:', error);
        res.status(500).json({ success: false, error: error.message });
    }
});

// Get specific deployment
app.get('/api/deployments/:serviceName', async (req, res) => {
    try {
        const { serviceName } = req.params;
        const configPath = path.join(DEPLOY_CONFIG_DIR, `${serviceName}.json`);
        const config = await loadJsonFile(configPath);
        
        if (!config.service_name) {
            return res.status(404).json({ success: false, error: 'Service not found' });
        }
        
        const statusData = await loadJsonFile(DEPLOY_STATUS_FILE, { deployments: {} });
        const deploymentStatus = statusData.deployments[serviceName] || {};
        const processManager = config.process_manager || 'systemd';
        const systemRunning = await checkServiceStatus(serviceName, processManager);
        
        res.json({
            success: true,
            deployment: {
                config,
                status: deploymentStatus.status || 'unknown',
                message: deploymentStatus.message || '',
                last_updated: deploymentStatus.last_updated,
                last_deployment: deploymentStatus.last_deployment,
                system_running: systemRunning
            }
        });
        
    } catch (error) {
        console.error(`Error getting deployment ${req.params.serviceName}:`, error);
        res.status(500).json({ success: false, error: error.message });
    }
});

// Remove deployment (use script so bash + common.sh are correct)
app.delete('/api/deployments/:serviceName', async (req, res) => {
    try {
        const { serviceName } = req.params;
        const removeScript = '/opt/api-gateway/scripts/remove-service.sh';
        await runCommand(`/bin/bash ${removeScript} ${serviceName}`);
        res.json({ success: true, message: `Deployment ${serviceName} removed` });
    } catch (error) {
        console.error(`Error removing deployment ${req.params.serviceName}:`, error);
        res.status(500).json({ success: false, error: error.message });
    }
});

// Deploy service
app.post('/api/deploy/:serviceName', async (req, res) => {
    try {
        const { serviceName } = req.params;
        const configPath = path.join(DEPLOY_CONFIG_DIR, `${serviceName}.json`);
        const config = await loadJsonFile(configPath);
        
        if (!config.service_name) {
            return res.status(404).json({ success: false, error: 'Service not found' });
        }
        
        // Trigger deployment in background (same script as webhook - has correct env)
        const deployScript = '/opt/api-gateway/scripts/deploy-service.sh';
        runCommandBackground(`/bin/bash ${deployScript} ${serviceName}`);
        
        res.json({
            success: true,
            message: `Deployment started for ${serviceName}`
        });
        
    } catch (error) {
        console.error(`Error deploying ${req.params.serviceName}:`, error);
        res.status(500).json({ success: false, error: error.message });
    }
});

// Restart service
app.post('/api/restart/:serviceName', async (req, res) => {
    try {
        const { serviceName } = req.params;
        const configPath = path.join(DEPLOY_CONFIG_DIR, `${serviceName}.json`);
        const config = await loadJsonFile(configPath);
        const processManager = config?.process_manager || 'systemd';
        
        if (processManager === 'pm2') {
            await runCommand(`pm2 restart ${serviceName}`);
        } else {
            await runCommand(`systemctl restart ${serviceName}`);
        }
        
        res.json({ success: true, message: `Service ${serviceName} restarted` });
        
    } catch (error) {
        console.error(`Error restarting ${req.params.serviceName}:`, error);
        res.status(500).json({ 
            success: false, 
            error: `Failed to restart: ${error.stderr || error.message}` 
        });
    }
});

// ============ Server-Sent Events (SSE) ============

const setupSSE = (res) => {
    res.setHeader('Content-Type', 'text/event-stream');
    res.setHeader('Cache-Control', 'no-cache');
    res.setHeader('Connection', 'keep-alive');
    res.setHeader('X-Accel-Buffering', 'no'); // disable nginx buffering
    res.flushHeaders();
};

const sendSSE = (res, event, data) => {
    const payload = typeof data === 'string' ? data : JSON.stringify(data);
    res.write(`event: ${event}\ndata: ${payload.replace(/\n/g, '\ndata: ')}\n\n`);
    if (typeof res.flush === 'function') res.flush();
};

// SSE: deployments list (push every 5s)
app.get('/api/sse/deployments', async (req, res) => {
    setupSSE(res);
    const pushDeployments = async () => {
        try {
            const statusData = await loadJsonFile(DEPLOY_STATUS_FILE, { deployments: {} });
            const deployments = {};
            const configFiles = await fs.readdir(DEPLOY_CONFIG_DIR).catch(() => []);
            for (const configFile of configFiles) {
                if (!configFile.endsWith('.json')) continue;
                const serviceName = configFile.replace('.json', '');
                const configPath = path.join(DEPLOY_CONFIG_DIR, configFile);
                const config = await loadJsonFile(configPath);
                if (!config) continue;
                const deploymentStatus = statusData.deployments[serviceName] || {};
                const processManager = config.process_manager || 'systemd';
                const systemRunning = await checkServiceStatus(serviceName, processManager);
                deployments[serviceName] = {
                    config,
                    status: deploymentStatus.status || 'unknown',
                    message: deploymentStatus.message || '',
                    last_updated: deploymentStatus.last_updated,
                    last_deployment: deploymentStatus.last_deployment,
                    system_running: systemRunning
                };
            }
            sendSSE(res, 'deployments', { success: true, deployments, timestamp: new Date().toISOString() });
        } catch (e) {
            sendSSE(res, 'error', { message: e.message });
        }
    };
    await pushDeployments();
    const interval = setInterval(pushDeployments, 5000);
    req.on('close', () => clearInterval(interval));
});

// SSE: deploy log stream (tail -f)
app.get('/api/sse/deploy-log/:serviceName', (req, res) => {
    const { serviceName } = req.params;
    const deploymentLogDir = path.join(LOG_DIR, 'deployments');
    setupSSE(res);
    let closed = false;
    req.on('close', () => { closed = true; });

    const waitForLog = (retries = 30) => {
        if (closed) return;
        fs.readdir(deploymentLogDir).then(files => {
            if (closed) return;
            const matches = files
                .filter(f => f.startsWith(`${serviceName}-`) && f.endsWith('.log'))
                .map(f => path.join(deploymentLogDir, f));
            if (matches.length === 0) {
                if (retries > 0) return setTimeout(() => waitForLog(retries - 1), 1000);
                sendSSE(res, 'log', 'Waiting for deployment log (start deploy if not started)...\n');
                return;
            }
            return Promise.all(matches.map(f => fs.stat(f).then(s => ({ f, mtime: s.mtime }))))
                .then(stats => {
                    if (closed) return;
                    const latest = stats.sort((a, b) => b.mtime - a.mtime)[0];
                    const tail = spawn('tail', ['-f', '-n', '100', latest.f], { stdio: ['ignore', 'pipe', 'pipe'] });
                    tail.stdout.on('data', chunk => !closed && sendSSE(res, 'log', chunk.toString()));
                    tail.stderr.on('data', chunk => !closed && sendSSE(res, 'log', chunk.toString()));
                    tail.on('error', () => !closed && sendSSE(res, 'done', {}));
                    tail.on('exit', () => !closed && sendSSE(res, 'done', {}));
                    req.on('close', () => tail.kill('SIGTERM'));
                });
        }).catch(() => {
            if (!closed && retries > 0) setTimeout(() => waitForLog(retries - 1), 1000);
        });
    };
    waitForLog();
});

// SSE: service logs stream
app.get('/api/sse/logs/:serviceName', async (req, res) => {
    const { serviceName } = req.params;
    setupSSE(res);
    try {
        const configPath = path.join(DEPLOY_CONFIG_DIR, `${serviceName}.json`);
        const config = await loadJsonFile(configPath);
        const processManager = config?.process_manager || 'systemd';
        let child;
        if (processManager === 'pm2') {
            child = spawn('pm2', ['logs', serviceName, '--raw', '--lines', '50'], { stdio: ['ignore', 'pipe', 'pipe'] });
        } else {
            child = spawn('journalctl', ['-u', serviceName, '-f', '-n', '50'], { stdio: ['ignore', 'pipe', 'pipe'] });
        }
        const sendChunk = (chunk) => sendSSE(res, 'log', chunk.toString());
        child.stdout.on('data', sendChunk);
        child.stderr.on('data', sendChunk);
        child.on('error', () => sendSSE(res, 'done', {}));
        child.on('exit', () => sendSSE(res, 'done', {}));
        req.on('close', () => child.kill('SIGTERM'));
    } catch (e) {
        sendSSE(res, 'error', { message: e.message });
    }
});

// Get latest deployment log (for live progress during deploy)
app.get('/api/deployments/:serviceName/deploy-log', async (req, res) => {
    try {
        const { serviceName } = req.params;
        const deploymentLogDir = path.join(LOG_DIR, 'deployments');
        const files = await fs.readdir(deploymentLogDir).catch(() => []);
        const serviceLogFiles = files
            .filter(file => file.startsWith(`${serviceName}-`) && file.endsWith('.log'))
            .map(file => path.join(deploymentLogDir, file));
        if (serviceLogFiles.length === 0) {
            return res.type('text/plain').send('');
        }
        const stats = await Promise.all(
            serviceLogFiles.map(async file => ({ file, mtime: (await fs.stat(file)).mtime }))
        );
        const latest = stats.sort((a, b) => b.mtime - a.mtime)[0];
        const content = await fs.readFile(latest.file, 'utf8');
        res.type('text/plain').send(content);
    } catch (error) {
        res.type('text/plain').send('');
    }
});

// Get service logs
app.get('/api/logs/:serviceName', async (req, res) => {
    try {
        const { serviceName } = req.params;
        let logs = '';
        const configPath = path.join(DEPLOY_CONFIG_DIR, `${serviceName}.json`);
        const config = await loadJsonFile(configPath);
        const processManager = config?.process_manager || 'systemd';
        
        try {
            if (processManager === 'pm2') {
                const { stdout } = await runCommand(`pm2 logs ${serviceName} --lines 100 --nostream 2>/dev/null`);
                logs = stdout || 'No PM2 logs';
            } else {
                const { stdout } = await runCommand(`journalctl -u ${serviceName} --no-pager -n 100`);
                logs = stdout;
            }
        } catch {
            // Fallback to deployment logs
            try {
                const deploymentLogDir = path.join(LOG_DIR, 'deployments');
                const files = await fs.readdir(deploymentLogDir);
                const serviceLogFiles = files
                    .filter(file => file.startsWith(`${serviceName}-`))
                    .map(file => path.join(deploymentLogDir, file));
                
                if (serviceLogFiles.length > 0) {
                    // Get the most recent log file
                    const stats = await Promise.all(
                        serviceLogFiles.map(async file => ({
                            file,
                            mtime: (await fs.stat(file)).mtime
                        }))
                    );
                    
                    const latestLog = stats.sort((a, b) => b.mtime - a.mtime)[0];
                    logs = await fs.readFile(latestLog.file, 'utf8');
                }
            } catch {
                logs = 'No logs available';
            }
        }
        
        res.type('text/plain').send(logs || 'No logs available');
        
    } catch (error) {
        console.error(`Error getting logs for ${req.params.serviceName}:`, error);
        res.type('text/plain').send(`Error loading logs: ${error.message}`);
    }
});

// Add new deployment
app.post('/api/deployments', async (req, res) => {
    try {
        const {
            service_name,
            github_repo,
            branch = 'main',
            port,
            build_command = 'npm install && npm run build',
            start_command = 'npm start'
        } = req.body;
        
        if (!service_name || !github_repo || !port) {
            return res.status(400).json({
                success: false,
                error: 'Missing required fields: service_name, github_repo, port'
            });
        }
        
        const command = `source /opt/api-gateway/modules/deployment-manager.sh && add_deployment '${service_name}' '${github_repo}' '${branch}' ${port} '${build_command}' '${start_command}'`;
        await runCommand(command);
        
        res.json({
            success: true,
            message: `Deployment configuration created for ${service_name}`
        });
        
    } catch (error) {
        console.error('Error adding deployment:', error);
        res.status(500).json({ success: false, error: error.message });
    }
});

// Get system information
app.get('/api/system/info', async (req, res) => {
    try {
        const info = {};
        
        // Load average
        const loadAvg = await fs.readFile('/proc/loadavg', 'utf8');
        info.load_average = loadAvg.trim().split(' ').slice(0, 3);
        
        // Memory info
        const meminfo = await fs.readFile('/proc/meminfo', 'utf8');
        const memData = {};
        meminfo.split('\n').forEach(line => {
            const [key, value] = line.split(':');
            if (key && value) {
                memData[key.trim()] = value.trim();
            }
        });
        
        const totalMem = parseInt(memData.MemTotal?.split(' ')[0] || 0);
        const freeMem = parseInt(memData.MemFree?.split(' ')[0] || 0);
        const availableMem = parseInt(memData.MemAvailable?.split(' ')[0] || 0);
        
        info.memory = {
            total: totalMem,
            free: freeMem,
            available: availableMem,
            used: totalMem - freeMem,
            usage_percent: Math.round((totalMem - availableMem) / totalMem * 100 * 10) / 10
        };
        
        // Disk usage
        try {
            const { stdout } = await runCommand('df -h / | tail -1');
            const diskInfo = stdout.trim().split(/\s+/);
            info.disk = {
                total: diskInfo[1],
                used: diskInfo[2],
                available: diskInfo[3],
                usage_percent: diskInfo[4]
            };
        } catch {
            info.disk = { error: 'Unable to get disk info' };
        }
        
        // Uptime
        const uptime = await fs.readFile('/proc/uptime', 'utf8');
        const uptimeSeconds = parseFloat(uptime.split(' ')[0]);
        const days = Math.floor(uptimeSeconds / 86400);
        const hours = Math.floor((uptimeSeconds % 86400) / 3600);
        const minutes = Math.floor((uptimeSeconds % 3600) / 60);
        
        info.uptime = {
            seconds: uptimeSeconds,
            formatted: `${days}d ${hours}h ${minutes}m`
        };
        
        res.json({
            success: true,
            system_info: info,
            timestamp: new Date().toISOString()
        });
        
    } catch (error) {
        console.error('Error getting system info:', error);
        res.status(500).json({ success: false, error: error.message });
    }
});

// Health check
app.get('/api/health', (req, res) => {
    res.json({
        status: 'healthy',
        timestamp: new Date().toISOString(),
        version: '1.0.0'
    });
});

// Error handling middleware
app.use((error, req, res, next) => {
    console.error('Unhandled error:', error);
    res.status(500).json({
        success: false,
        error: 'Internal server error'
    });
});

// Start server
const server = app.listen(API_PORT, '0.0.0.0', () => {
    console.log(`API Gateway Dashboard Server running on port ${API_PORT}`);
    console.log(`Dashboard URL: http://localhost:${API_PORT}`);
});

// Graceful shutdown
process.on('SIGTERM', () => {
    console.log('Received SIGTERM, shutting down gracefully');
    server.close(() => {
        console.log('Server closed');
        process.exit(0);
    });
});

process.on('SIGINT', () => {
    console.log('Received SIGINT, shutting down gracefully');
    server.close(() => {
        console.log('Server closed');
        process.exit(0);
    });
});