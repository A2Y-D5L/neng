// CI Pipeline Dashboard - Main Dashboard Class

import { escapeHtml, formatDuration, getStatusIcon } from './utils.js';
import * as api from './api.js';

/**
 * CIDashboard manages the main dashboard state and UI for CI pipeline execution.
 * Handles SSE connections, event processing, and pipeline visualization.
 */
export class CIDashboard {
    constructor() {
        this.eventSource = null;
        this.targets = new Map();
        this.stages = [];
        this.events = [];
        this.stats = {
            total: 0,
            running: 0,
            passed: 0,
            failed: 0,
            skipped: 0
        };
        this.isRunning = false;
        this.wasConnected = false; // Track if we were previously connected
        this.reconnectAttempts = 0;
        this.maxReconnectAttempts = 10;
        this.reconnectDelay = 3000; // Base delay in ms
        
        // Connection timing tracking
        this.connectionStartTime = null;
        this.lastConnectionTime = null;
        this.connectionDurationInterval = null;
        this.reconnectCountdownInterval = null;
        
        this.init();
    }
    
    init() {
        this.connectSSE();
        this.checkStatus();
    }
    
    connectSSE() {
        if (this.eventSource) {
            this.eventSource.close();
        }
        
        // Clear any existing countdown interval
        if (this.reconnectCountdownInterval) {
            clearInterval(this.reconnectCountdownInterval);
            this.reconnectCountdownInterval = null;
        }
        
        // Show connecting state if we're reconnecting
        if (this.wasConnected && this.reconnectAttempts > 0) {
            this.updateConnectionStatus('connecting');
        }
        
        this.eventSource = new EventSource(api.getEventsEndpoint());
        
        this.eventSource.onopen = () => {
            const wasReconnect = this.wasConnected;
            this.wasConnected = true;
            this.reconnectAttempts = 0;
            
            // Track connection times
            this.connectionStartTime = Date.now();
            this.lastConnectionTime = Date.now();
            
            // Start updating connection duration tooltip
            this.startConnectionDurationUpdates();
            
            // Clear reconnection toast
            this.clearReconnectToast();
            
            this.updateConnectionStatus('connected');
            console.log('SSE connection established');
            
            if (wasReconnect) {
                // Show reconnected notification
                if (window.showToast) {
                    window.showToast('Connection restored', 'success');
                }
                // Refresh run status after reconnecting
                this.checkStatus();
            }
        };
        
        this.eventSource.onerror = (err) => {
            console.error('SSE error:', err);
            
            // Stop connection duration updates
            this.stopConnectionDurationUpdates();
            
            // Close the EventSource to prevent automatic browser reconnection
            // We'll manage reconnection manually with exponential backoff
            if (this.eventSource) {
                this.eventSource.close();
                this.eventSource = null;
            }
            
            this.reconnectAttempts++;
            
            // Exponential backoff for reconnection
            if (this.reconnectAttempts <= this.maxReconnectAttempts) {
                const delay = Math.min(this.reconnectDelay * Math.pow(1.5, this.reconnectAttempts - 1), 30000);
                console.log(`Reconnecting in ${delay}ms (attempt ${this.reconnectAttempts}/${this.maxReconnectAttempts})`);
                
                // Show connecting status and start countdown toast
                this.updateConnectionStatus('connecting');
                this.showReconnectCountdown(delay, this.reconnectAttempts, this.maxReconnectAttempts);
                
                setTimeout(() => this.connectSSE(), delay);
            } else {
                console.error('Max reconnection attempts reached');
                this.updateConnectionStatus('disconnected', true); // true = show refresh button
                this.showReconnectToast('Unable to reconnect. Click the refresh button to try again.', true);
            }
        };
        
        this.eventSource.onmessage = (event) => {
            try {
                const data = JSON.parse(event.data);
                this.handleEvent(data);
            } catch (err) {
                console.error('Failed to parse event:', err);
            }
        };
    }
    
    /**
     * Manually retry connection after max attempts reached.
     */
    retryConnection() {
        this.reconnectAttempts = 0;
        this.clearReconnectToast();
        this.updateConnectionStatus('connecting');
        this.connectSSE();
    }
    
    /**
     * Show reconnection countdown toast with live updates.
     */
    showReconnectCountdown(delayMs, attempt, maxAttempts) {
        let remainingSeconds = Math.ceil(delayMs / 1000);
        
        const updateMessage = () => {
            const message = `Attempting to reconnect...\nAttempt ${attempt}/${maxAttempts} failed. Retrying in ${remainingSeconds} second${remainingSeconds !== 1 ? 's' : ''}...`;
            this.showReconnectToast(message, false);
        };
        
        // Show initial message
        updateMessage();
        
        // Update countdown every second
        this.reconnectCountdownInterval = setInterval(() => {
            remainingSeconds--;
            if (remainingSeconds > 0) {
                updateMessage();
            } else {
                clearInterval(this.reconnectCountdownInterval);
                this.reconnectCountdownInterval = null;
            }
        }, 1000);
    }
    
    /**
     * Show or update the reconnection toast.
     */
    showReconnectToast(message, isFinal) {
        if (window.showPersistentToast) {
            window.showPersistentToast('reconnect-toast', message, isFinal ? 'error' : 'warning');
        }
    }
    
    /**
     * Clear the reconnection toast.
     */
    clearReconnectToast() {
        if (this.reconnectCountdownInterval) {
            clearInterval(this.reconnectCountdownInterval);
            this.reconnectCountdownInterval = null;
        }
        if (window.dismissPersistentToast) {
            window.dismissPersistentToast('reconnect-toast');
        }
    }
    
    /**
     * Start updating the connection duration tooltip.
     */
    startConnectionDurationUpdates() {
        // Update immediately
        this.updateConnectionDurationTooltip();
        
        // Update every minute
        this.connectionDurationInterval = setInterval(() => {
            this.updateConnectionDurationTooltip();
        }, 60000);
    }
    
    /**
     * Stop updating the connection duration tooltip.
     */
    stopConnectionDurationUpdates() {
        if (this.connectionDurationInterval) {
            clearInterval(this.connectionDurationInterval);
            this.connectionDurationInterval = null;
        }
    }
    
    /**
     * Update the connection status tooltip with duration.
     */
    updateConnectionDurationTooltip() {
        const statusEl = document.getElementById('connectionStatus');
        if (!statusEl || !this.connectionStartTime) return;
        
        const duration = Date.now() - this.connectionStartTime;
        const durationText = this.formatConnectionDuration(duration);
        statusEl.title = `Connection duration: ${durationText}`;
    }
    
    /**
     * Format a duration in milliseconds to a human-readable string.
     * @param {number} ms - Duration in milliseconds.
     * @returns {string} Formatted string like "1 day, 2 hr, 22 min"
     */
    formatConnectionDuration(ms) {
        const seconds = Math.floor(ms / 1000);
        const minutes = Math.floor(seconds / 60);
        const hours = Math.floor(minutes / 60);
        const days = Math.floor(hours / 24);
        
        const parts = [];
        if (days > 0) parts.push(`${days} day${days !== 1 ? 's' : ''}`);
        if (hours % 24 > 0) parts.push(`${hours % 24} hr`);
        if (minutes % 60 > 0 || parts.length === 0) parts.push(`${minutes % 60} min`);
        
        return parts.join(', ');
    }
    
    handleEvent(data) {
        switch (data.type) {
            case 'connected':
                console.log('Connected to server');
                break;
            case 'plan':
                this.handlePlanInfo(data);
                break;
            case 'started':
                this.handleTargetStarted(data);
                break;
            case 'completed':
                this.handleTargetCompleted(data);
                break;
            case 'skipped':
                this.handleTargetSkipped(data);
                break;
            case 'summary':
                this.handleSummary(data);
                break;
            default:
                console.log('Unknown event type:', data.type);
        }
    }
    
    handlePlanInfo(data) {
        this.targets.clear();
        this.stages = data.stages || [];
        this.stats = {
            total: data.targets.length,
            running: 0,
            passed: 0,
            failed: 0,
            skipped: 0
        };
        
        // Initialize targets
        for (const target of data.targets) {
            this.targets.set(target.name, {
                ...target,
                status: 'pending',
                result: null
            });
        }
        
        this.isRunning = true;
        this.updateUI();
        this.renderPipeline();
        this.updateStats();
        this.updatePipelineStatus('running');
        this.setButtonStates(true);
    }
    
    handleTargetStarted(data) {
        const target = this.targets.get(data.targetName);
        if (target) {
            target.status = 'running';
            target.startTime = new Date(data.time);
            this.stats.running++;
            this.updateStats();
            this.updateTargetCard(data.targetName);
        }
        
        this.addEvent({
            type: 'started',
            targetName: data.targetName,
            targetDesc: data.targetDesc,
            time: data.time
        });
    }
    
    handleTargetCompleted(data) {
        const target = this.targets.get(data.targetName);
        if (target) {
            const hasFailed = data.result && data.result.error;
            target.status = hasFailed ? 'failed' : 'success';
            target.result = data.result;
            target.endTime = new Date(data.time);
            
            this.stats.running = Math.max(0, this.stats.running - 1);
            if (hasFailed) {
                this.stats.failed++;
            } else {
                this.stats.passed++;
            }
            
            this.updateStats();
            this.updateTargetCard(data.targetName);
        }
        
        this.addEvent({
            type: 'completed',
            targetName: data.targetName,
            targetDesc: data.targetDesc,
            time: data.time,
            error: data.result?.error,
            durationMs: data.result?.durationMs
        });
    }
    
    handleTargetSkipped(data) {
        const target = this.targets.get(data.targetName);
        if (target) {
            target.status = 'skipped';
            target.result = data.result;
            this.stats.skipped++;
            this.updateStats();
            this.updateTargetCard(data.targetName);
        }
        
        this.addEvent({
            type: 'skipped',
            targetName: data.targetName,
            targetDesc: data.targetDesc,
            time: data.time
        });
    }
    
    handleSummary(data) {
        this.isRunning = false;
        this.setButtonStates(false);
        this.updatePipelineStatus(data.failed ? 'failed' : 'success');
        this.renderSummary(data);
    }
    
    /**
     * Update the connection status indicator.
     * @param {string} status - 'connected', 'connecting', or 'disconnected'
     * @param {boolean} showRefreshButton - Whether to show the refresh button (for final disconnected state)
     */
    updateConnectionStatus(status, showRefreshButton = false) {
        const statusEl = document.getElementById('connectionStatus');
        if (!statusEl) return;
        
        const dot = statusEl.querySelector('.status-dot');
        const text = statusEl.querySelector('.status-text');
        if (!dot || !text) return;
        
        // Remove existing refresh button if any
        const existingRefreshBtn = statusEl.querySelector('.connection-refresh-btn');
        if (existingRefreshBtn) {
            existingRefreshBtn.remove();
        }
        
        // Reset tooltip
        statusEl.title = '';
        
        switch (status) {
            case 'connected':
                dot.className = 'status-dot connected';
                text.textContent = 'Connected';
                // Tooltip will be updated by updateConnectionDurationTooltip()
                break;
            case 'connecting':
                dot.className = 'status-dot connecting';
                text.textContent = 'Connecting...';
                statusEl.title = 'Attempting to reconnect to server';
                break;
            case 'disconnected':
            default:
                dot.className = 'status-dot disconnected';
                text.textContent = 'Disconnected';
                
                if (showRefreshButton) {
                    // Update tooltip with last connection time
                    if (this.lastConnectionTime) {
                        const elapsed = Date.now() - this.lastConnectionTime;
                        const elapsedText = this.formatConnectionDuration(elapsed);
                        statusEl.title = `Last successful connection: ${elapsedText} ago`;
                    }
                    
                    // Add refresh button
                    const refreshBtn = document.createElement('button');
                    refreshBtn.className = 'connection-refresh-btn';
                    refreshBtn.title = 'Retry connection';
                    refreshBtn.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                        <path d="M23 4v6h-6M1 20v-6h6M3.51 9a9 9 0 0114.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0020.49 15"/>
                    </svg>`;
                    refreshBtn.onclick = () => this.retryConnection();
                    statusEl.appendChild(refreshBtn);
                    
                    // Start updating the elapsed time tooltip
                    this.startDisconnectedDurationUpdates();
                }
                break;
        }
    }
    
    /**
     * Start updating the disconnected duration tooltip.
     */
    startDisconnectedDurationUpdates() {
        // Clear any existing interval
        this.stopConnectionDurationUpdates();
        
        // Update every minute
        this.connectionDurationInterval = setInterval(() => {
            const statusEl = document.getElementById('connectionStatus');
            if (statusEl && this.lastConnectionTime) {
                const elapsed = Date.now() - this.lastConnectionTime;
                const elapsedText = this.formatConnectionDuration(elapsed);
                statusEl.title = `Last successful connection: ${elapsedText} ago`;
            }
        }, 60000);
    }
    
    updateUI() {
        // Hide summary panel when starting new run
        document.getElementById('summaryPanel').style.display = 'none';
    }
    
    updateStats() {
        document.getElementById('statTotal').textContent = this.stats.total;
        document.getElementById('statRunning').textContent = this.stats.running;
        document.getElementById('statPassed').textContent = this.stats.passed;
        document.getElementById('statFailed').textContent = this.stats.failed;
        document.getElementById('statSkipped').textContent = this.stats.skipped;
    }
    
    updatePipelineStatus(status) {
        const badge = document.getElementById('pipelineStatus');
        badge.className = `panel-badge ${status}`;
        
        switch (status) {
            case 'running':
                badge.textContent = 'Running';
                break;
            case 'success':
                badge.textContent = 'Passed';
                break;
            case 'failed':
                badge.textContent = 'Failed';
                break;
            default:
                badge.textContent = 'Idle';
        }
    }
    
    setButtonStates(running) {
        document.getElementById('runBtn').disabled = running;
        document.getElementById('runFailBtn').disabled = running;
        document.getElementById('cancelBtn').disabled = !running;
        
        // Update execution tab indicator
        this.updateExecutionTabIndicator(running);
        
        // Update header running badge
        this.updateHeaderRunningBadge(running);
        
        // Dispatch custom event so other modules can update their buttons
        window.dispatchEvent(new CustomEvent('dashboard:runningStateChanged', { 
            detail: { running } 
        }));
    }
    
    updateExecutionTabIndicator(running) {
        const executionTab = document.querySelector('.tab-btn[data-tab="execution"]');
        if (!executionTab) return;
        
        if (running) {
            executionTab.classList.add('running');
            // Add pulsing indicator if not already present
            if (!executionTab.querySelector('.tab-running-indicator')) {
                const indicator = document.createElement('span');
                indicator.className = 'tab-running-indicator';
                executionTab.appendChild(indicator);
            }
        } else {
            executionTab.classList.remove('running');
            // Remove pulsing indicator
            const indicator = executionTab.querySelector('.tab-running-indicator');
            if (indicator) {
                indicator.remove();
            }
        }
    }
    
    updateHeaderRunningBadge(running) {
        let badge = document.getElementById('headerRunningBadge');
        
        if (running) {
            if (!badge) {
                // Create badge if it doesn't exist
                badge = document.createElement('div');
                badge.id = 'headerRunningBadge';
                badge.className = 'header-running-badge';
                badge.innerHTML = '<span class="running-dot"></span><span class="running-text">Running</span>';
                
                // Insert before connection status
                const connectionStatus = document.getElementById('connectionStatus');
                if (connectionStatus && connectionStatus.parentNode) {
                    connectionStatus.parentNode.insertBefore(badge, connectionStatus);
                }
            }
            badge.style.display = 'flex';
        } else {
            if (badge) {
                badge.style.display = 'none';
            }
        }
    }
    
    renderPipeline() {
        const container = document.getElementById('stagesContainer');
        container.innerHTML = '';
        
        if (this.stages.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                        <path d="M9 17H7A5 5 0 017 7h2M15 7h2a5 5 0 010 10h-2M8 12h8"/>
                    </svg>
                    <p>Start a pipeline run to see the execution graph</p>
                </div>
            `;
            return;
        }
        
        for (const stage of this.stages) {
            const stageEl = document.createElement('div');
            stageEl.className = 'stage';
            stageEl.innerHTML = `
                <div class="stage-header">Stage ${stage.index}</div>
                <div class="stage-targets" id="stage-${stage.index}">
                    ${stage.targets.map(name => this.renderTargetCard(name)).join('')}
                </div>
            `;
            container.appendChild(stageEl);
        }
    }
    
    renderTargetCard(name) {
        const target = this.targets.get(name);
        if (!target) return '';
        
        const statusIcon = getStatusIcon(target.status);
        const deps = target.deps && target.deps.length > 0 
            ? `<div class="target-deps">← ${target.deps.join(', ')}</div>` 
            : '';
        const duration = target.result?.durationMs 
            ? `<div class="target-duration">${formatDuration(target.result.durationMs)}</div>`
            : '';
        
        return `
            <div class="target-card ${target.status}" id="target-${name}">
                <div class="target-name">
                    ${statusIcon}
                    ${name}
                </div>
                <div class="target-desc">${target.desc || ''}</div>
                ${deps}
                ${duration}
            </div>
        `;
    }
    
    updateTargetCard(name) {
        const target = this.targets.get(name);
        if (!target) return;
        
        const card = document.getElementById(`target-${name}`);
        if (card) {
            card.outerHTML = this.renderTargetCard(name);
        }
    }
    
    addEvent(event) {
        this.events.unshift(event);
        this.renderEvents();
    }
    
    renderEvents() {
        const container = document.getElementById('eventsList');
        const countEl = document.getElementById('eventCount');
        
        countEl.textContent = `${this.events.length} events`;
        
        if (this.events.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                        <path d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/>
                    </svg>
                    <p>Events will appear here during pipeline execution</p>
                </div>
            `;
            return;
        }
        
        container.innerHTML = this.events.map(event => this.renderEventItem(event)).join('');
    }
    
    renderEventItem(event) {
        let icon, message;
        const time = new Date(event.time).toLocaleTimeString('en-US', { 
            hour12: false, 
            hour: '2-digit', 
            minute: '2-digit', 
            second: '2-digit',
            fractionalSecondDigits: 3
        });
        
        switch (event.type) {
            case 'started':
                icon = `<svg class="event-icon started" viewBox="0 0 24 24" fill="currentColor">
                    <path d="M8 5v14l11-7z"/>
                </svg>`;
                message = `<strong>${event.targetName}</strong> started`;
                break;
            case 'completed':
                if (event.error) {
                    icon = `<svg class="event-icon completed failed" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                        <circle cx="12" cy="12" r="10"/>
                        <path d="M15 9l-6 6M9 9l6 6"/>
                    </svg>`;
                    message = `<strong>${event.targetName}</strong> failed: <span class="error">${escapeHtml(event.error)}</span>`;
                } else {
                    icon = `<svg class="event-icon completed" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                        <path d="M20 6L9 17l-5-5"/>
                    </svg>`;
                    message = `<strong>${event.targetName}</strong> completed (${formatDuration(event.durationMs)})`;
                }
                break;
            case 'skipped':
                icon = `<svg class="event-icon skipped" viewBox="0 0 24 24" fill="currentColor">
                    <path d="M5 4h4l10 8-10 8H5l10-8z"/>
                </svg>`;
                message = `<strong>${event.targetName}</strong> skipped`;
                break;
            default:
                icon = '';
                message = `Unknown event: ${event.type}`;
        }
        
        return `
            <div class="event-item fade-in">
                ${icon}
                <div class="event-content">
                    <div class="event-message">${message}</div>
                    <div class="event-time">${time}</div>
                </div>
            </div>
        `;
    }
    
    renderSummary(data) {
        const panel = document.getElementById('summaryPanel');
        const statusEl = document.getElementById('summaryStatus');
        const statsEl = document.getElementById('summaryStats');
        const detailsEl = document.getElementById('summaryDetails');
        
        panel.style.display = 'block';
        
        // Status badge
        statusEl.className = `summary-status ${data.failed ? 'failed' : 'success'}`;
        statusEl.textContent = data.failed ? '✗ Pipeline Failed' : '✓ Pipeline Passed';
        
        // Calculate total duration
        let totalDuration = 0;
        let minStart = null;
        let maxEnd = null;
        
        for (const [name, result] of Object.entries(data.results)) {
            if (result.startedAt && result.completedAt) {
                const start = new Date(result.startedAt);
                const end = new Date(result.completedAt);
                
                if (!minStart || start < minStart) minStart = start;
                if (!maxEnd || end > maxEnd) maxEnd = end;
            }
            if (result.durationMs) {
                totalDuration += result.durationMs;
            }
        }
        
        const wallTime = minStart && maxEnd ? maxEnd - minStart : 0;
        
        // Stats cards
        statsEl.innerHTML = `
            <div class="summary-stat-card">
                <div class="summary-stat-value" style="color: var(--text-primary)">${Object.keys(data.results).length}</div>
                <div class="summary-stat-label">Total Targets</div>
            </div>
            <div class="summary-stat-card">
                <div class="summary-stat-value" style="color: var(--status-success)">${this.stats.passed}</div>
                <div class="summary-stat-label">Passed</div>
            </div>
            <div class="summary-stat-card">
                <div class="summary-stat-value" style="color: var(--status-failed)">${this.stats.failed}</div>
                <div class="summary-stat-label">Failed</div>
            </div>
            <div class="summary-stat-card">
                <div class="summary-stat-value" style="color: var(--status-skipped)">${this.stats.skipped}</div>
                <div class="summary-stat-label">Skipped</div>
            </div>
            <div class="summary-stat-card">
                <div class="summary-stat-value" style="color: var(--accent-cyan)">${formatDuration(wallTime)}</div>
                <div class="summary-stat-label">Wall Time</div>
            </div>
            <div class="summary-stat-card">
                <div class="summary-stat-value" style="color: var(--accent-purple)">${formatDuration(totalDuration)}</div>
                <div class="summary-stat-label">Total CPU Time</div>
            </div>
        `;
        
        // Result details (sorted by completion time)
        const sortedResults = Object.entries(data.results)
            .sort((a, b) => {
                const aTime = a[1].completedAt ? new Date(a[1].completedAt) : new Date();
                const bTime = b[1].completedAt ? new Date(b[1].completedAt) : new Date();
                return aTime - bTime;
            });
        
        detailsEl.innerHTML = sortedResults.map(([name, result]) => {
            let status = 'success';
            if (result.skipped) status = 'skipped';
            else if (result.error) status = 'failed';
            
            const errorId = result.error ? `error-${name.replace(/[^a-zA-Z0-9]/g, '-')}` : '';
            
            return `
                <div class="summary-result">
                    <div class="summary-result-status ${status}"></div>
                    <div class="summary-result-name">${name}</div>
                    ${result.error ? `
                        <div class="summary-result-error-container">
                            <div class="summary-result-error" id="${errorId}" title="${escapeHtml(result.error)}">${escapeHtml(result.error)}</div>
                            <button class="error-expand-btn" onclick="window.dashboard.toggleErrorExpand('${errorId}')" title="Show full error">
                                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                                    <path d="M19 9l-7 7-7-7"/>
                                </svg>
                            </button>
                        </div>
                    ` : ''}
                    <div class="summary-result-duration">${result.skipped ? 'Skipped' : formatDuration(result.durationMs)}</div>
                </div>
            `;
        }).join('');
        
        // Scroll summary into view
        panel.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
    
    async checkStatus() {
        try {
            const data = await api.checkStatus();
            this.isRunning = data.running;
            this.setButtonStates(data.running);
        } catch (err) {
            console.error('Failed to check status:', err);
        }
    }
    
    clear() {
        this.targets.clear();
        this.stages = [];
        this.events = [];
        this.stats = {
            total: 0,
            running: 0,
            passed: 0,
            failed: 0,
            skipped: 0
        };
        
        this.renderPipeline();
        this.renderEvents();
        this.updateStats();
        this.updatePipelineStatus('idle');
        document.getElementById('summaryPanel').style.display = 'none';
    }
    
    /**
     * Toggle expansion of an error message in the summary.
     * @param {string} errorId - The ID of the error element.
     */
    toggleErrorExpand(errorId) {
        const errorEl = document.getElementById(errorId);
        if (!errorEl) return;
        
        const container = errorEl.closest('.summary-result-error-container');
        const btn = container?.querySelector('.error-expand-btn');
        
        if (errorEl.classList.contains('expanded')) {
            errorEl.classList.remove('expanded');
            if (btn) btn.classList.remove('expanded');
        } else {
            errorEl.classList.add('expanded');
            if (btn) btn.classList.add('expanded');
        }
    }
}
