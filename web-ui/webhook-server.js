#!/usr/bin/env node

/**
 * GitHub Webhook Server
 * Handles GitHub webhooks for automatic deployment
 */

const express = require('express');
const crypto = require('crypto');
const { spawn } = require('child_process');
const fs = require('fs').promises;
const path = require('path');

// Configuration
const WEBHOOK_PORT = process.env.WEBHOOK_PORT || 9876;
const DEPLOY_CONFIG_DIR = '/etc/api-gateway/deployments';
const LOG_FILE = '/var/log/api-gateway/webhook.log';

// Express app setup
const app = express();
app.use(express.raw({ type: 'application/json', limit: '10mb' }));

// Logging utility
const log = (level, message) => {
    const timestamp = new Date().toISOString();
    const logMessage = `${timestamp} - ${level} - ${message}\n`;
    console.log(logMessage.trim());
    
    // Append to log file
    fs.appendFile(LOG_FILE, logMessage).catch(err => {
        console.error('Failed to write to log file:', err);
    });
};

// Load deployment configuration
const loadDeploymentConfig = async (serviceName) => {
    try {
        const configPath = path.join(DEPLOY_CONFIG_DIR, `${serviceName}.json`);
        const data = await fs.readFile(configPath, 'utf8');
        return JSON.parse(data);
    } catch (error) {
        return null;
    }
};

// Verify GitHub webhook signature
const verifySignature = (body, secret, signature) => {
    if (!signature || !signature.startsWith('sha256=')) {
        return false;
    }
    
    const expectedSignature = 'sha256=' + crypto
        .createHmac('sha256', secret)
        .update(body)
        .digest('hex');
    
    return crypto.timingSafeEqual(
        Buffer.from(expectedSignature),
        Buffer.from(signature)
    );
};

// Trigger deployment
const triggerDeployment = (serviceName) => {
    const deployScript = '/opt/api-gateway/scripts/deploy-service.sh';
    
    const child = spawn('bash', [deployScript, serviceName], {
        detached: true,
        stdio: 'ignore'
    });
    
    child.unref();
    return child.pid;
};

// Routes

// Health check
app.get('/health', (req, res) => {
    res.json({
        status: 'healthy',
        timestamp: new Date().toISOString(),
        port: WEBHOOK_PORT
    });
});

// GitHub webhook handler
app.post('/webhook/:serviceName', async (req, res) => {
    const { serviceName } = req.params;
    const signature = req.headers['x-hub-signature-256'];
    const event = req.headers['x-github-event'];
    
    try {
        log('INFO', `Received ${event} webhook for ${serviceName}`);
        
        // Load service configuration
        const config = await loadDeploymentConfig(serviceName);
        if (!config) {
            log('ERROR', `Service configuration not found: ${serviceName}`);
            return res.status(404).json({
                success: false,
                error: `Service ${serviceName} not configured`
            });
        }
        
        // Verify webhook signature
        const webhookSecret = config.webhook_secret || '';
        if (!verifySignature(req.body, webhookSecret, signature)) {
            log('WARNING', `Invalid webhook signature for ${serviceName}`);
            return res.status(403).json({
                success: false,
                error: 'Invalid signature'
            });
        }
        
        // Parse webhook payload
        let payload;
        try {
            payload = JSON.parse(req.body.toString());
        } catch (error) {
            log('ERROR', `Invalid JSON payload for ${serviceName}`);
            return res.status(400).json({
                success: false,
                error: 'Invalid JSON payload'
            });
        }
        
        // Only handle push events
        if (event !== 'push') {
            log('INFO', `Ignoring ${event} event for ${serviceName}`);
            return res.json({
                success: true,
                message: `Ignoring ${event} event`
            });
        }
        
        // Check if push is to the configured branch
        const targetBranch = `refs/heads/${config.branch || 'main'}`;
        if (payload.ref !== targetBranch) {
            log('INFO', `Ignoring push to ${payload.ref}, expected ${targetBranch}`);
            return res.json({
                success: true,
                message: `Ignoring push to different branch: ${payload.ref}`
            });
        }
        
        // Check if auto-deploy is enabled
        if (!config.auto_deploy) {
            log('INFO', `Auto-deploy disabled for ${serviceName}`);
            return res.json({
                success: true,
                message: 'Auto-deploy disabled'
            });
        }
        
        // Trigger deployment
        const pid = triggerDeployment(serviceName);
        log('INFO', `Deployment triggered for ${serviceName} (PID: ${pid})`);
        
        const commitId = payload.head_commit?.id || 'unknown';
        const commitMessage = payload.head_commit?.message || 'No message';
        const pusher = payload.pusher?.name || 'unknown';
        
        log('INFO', `Commit: ${commitId} by ${pusher} - ${commitMessage}`);
        
        res.json({
            success: true,
            message: `Deployment triggered for ${serviceName}`,
            service: serviceName,
            commit: commitId,
            pusher: pusher,
            pid: pid
        });
        
    } catch (error) {
        log('ERROR', `Error handling webhook for ${serviceName}: ${error.message}`);
        res.status(500).json({
            success: false,
            error: error.message
        });
    }
});

// Catch all other routes
app.all('*', (req, res) => {
    res.status(404).json({
        success: false,
        error: 'Not found'
    });
});

// Error handling middleware
app.use((error, req, res, next) => {
    log('ERROR', `Unhandled error: ${error.message}`);
    res.status(500).json({
        success: false,
        error: 'Internal server error'
    });
});

// Start server
const server = app.listen(WEBHOOK_PORT, '0.0.0.0', () => {
    log('INFO', `GitHub Webhook Server started on port ${WEBHOOK_PORT}`);
});

// Graceful shutdown
const shutdown = (signal) => {
    log('INFO', `Received ${signal}, shutting down gracefully`);
    server.close(() => {
        log('INFO', 'Webhook server closed');
        process.exit(0);
    });
};

process.on('SIGTERM', () => shutdown('SIGTERM'));
process.on('SIGINT', () => shutdown('SIGINT'));

// Handle uncaught exceptions
process.on('uncaughtException', (error) => {
    log('ERROR', `Uncaught exception: ${error.message}`);
    process.exit(1);
});

process.on('unhandledRejection', (reason, promise) => {
    log('ERROR', `Unhandled rejection at ${promise}: ${reason}`);
    process.exit(1);
});