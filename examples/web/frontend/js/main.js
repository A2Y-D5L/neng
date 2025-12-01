// CI Pipeline Dashboard - Main Application Entry Point

import { CIDashboard } from './dashboard.js';
import { PlanComposer } from './composer.js';
import { PlanRunner } from './runner.js';
import * as api from './api.js';

// Global instances - exposed on window for onclick handlers in HTML
let dashboard;
let composer;
let planRunner;

/**
 * Switch between tabs (composer and execution).
 * @param {string} tabName - The tab to switch to ('composer' or 'execution').
 */
function switchTab(tabName) {
    // Update tab buttons
    document.querySelectorAll('.tab-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.tab === tabName);
    });
    
    // Update tab content
    document.querySelectorAll('.tab-content').forEach(content => {
        content.classList.toggle('active', content.id === tabName + 'Tab');
    });
}

/**
 * Start a demo pipeline run.
 * @param {boolean} withFailure - Whether to simulate a failure.
 */
async function startRun(withFailure) {
    try {
        switchTab('execution');
        dashboard.clear();
        const data = await api.startDemoRun(withFailure);
        
        if (data.status === 'error') {
            showToast(data.message, 'error');
        }
    } catch (err) {
        console.error('Failed to start run:', err);
        showToast('Failed to start run: ' + err.message, 'error');
    }
}

/**
 * Cancel the current pipeline run.
 */
async function cancelRun() {
    try {
        const data = await api.cancelRun();
        
        if (data.status === 'error') {
            showToast(data.message, 'error');
        } else {
            showToast('Run cancelled', 'info');
        }
    } catch (err) {
        console.error('Failed to cancel run:', err);
        showToast('Failed to cancel run: ' + err.message, 'error');
    }
}

/**
 * Clear the dashboard.
 */
function clearDashboard() {
    if (dashboard) {
        dashboard.clear();
    }
}

/**
 * Select all targets in the composer.
 */
function selectAllTargets() {
    if (composer) {
        composer.selectAllTargets();
    }
}

/**
 * Clear target selection in the composer.
 */
function clearSelection() {
    if (composer) {
        composer.clearSelection();
    }
}

/**
 * Run the custom plan from the composer.
 */
function runCustomPlan() {
    if (composer) {
        composer.runPlan(switchTab, dashboard);
    }
}

/**
 * Save the current plan from the composer.
 */
function savePlan() {
    if (!composer || composer.selectedTargets.size === 0) {
        showToast('Please select at least one target to save.', 'error');
        return;
    }

    const modal = document.getElementById('savePlanModal');
    const previewContainer = document.getElementById('savePlanPreview');
    
    const planData = composer.buildPlan();
    if (!planData || !planData.valid) {
        previewContainer.innerHTML = '<p class="error">Plan is not valid. Cannot save.</p>';
    } else {
        previewContainer.innerHTML = `
            <p><strong>${planData.targets.length} targets</strong> selected.</p>
            <p><strong>${planData.stages.length} stages</strong> will be created.</p>
            <p><strong>${planData.rootTargets.length > 0 ? planData.rootTargets.length : 'Default'} root targets</strong>.</p>
        `;
    }

    const nameInput = document.getElementById('planName');
    const descInput = document.getElementById('planDescription');
    
    nameInput.value = '';
    descInput.value = '';
    clearFieldError(nameInput);
    
    modal.style.display = 'flex';
    
    // Auto-focus the name input
    requestAnimationFrame(() => {
        nameInput.focus();
    });
    
    // Set up focus trap and keyboard handling
    setupModalAccessibility(modal);
}

/**
 * Close the save plan modal.
 */
function closeSavePlanModal() {
    const modal = document.getElementById('savePlanModal');
    modal.style.display = 'none';
    
    // Clean up modal accessibility
    cleanupModalAccessibility(modal);
}

/**
 * Set up accessibility features for modal (focus trap, keyboard handling).
 * @param {HTMLElement} modal - The modal element.
 */
function setupModalAccessibility(modal) {
    // Store reference for cleanup
    modal._keydownHandler = (e) => {
        if (e.key === 'Escape') {
            closeSavePlanModal();
            return;
        }
        
        // Focus trap
        if (e.key === 'Tab') {
            const focusableElements = modal.querySelectorAll(
                'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
            );
            
            if (focusableElements.length === 0) return;
            
            const firstFocusable = focusableElements[0];
            const lastFocusable = focusableElements[focusableElements.length - 1];
            
            if (e.shiftKey) {
                // Shift + Tab
                if (document.activeElement === firstFocusable) {
                    e.preventDefault();
                    lastFocusable.focus();
                }
            } else {
                // Tab
                if (document.activeElement === lastFocusable) {
                    e.preventDefault();
                    firstFocusable.focus();
                }
            }
        }
    };
    
    // Click outside to close
    modal._clickHandler = (e) => {
        if (e.target === modal) {
            closeSavePlanModal();
        }
    };
    
    document.addEventListener('keydown', modal._keydownHandler);
    modal.addEventListener('click', modal._clickHandler);
}

/**
 * Clean up modal accessibility event listeners.
 * @param {HTMLElement} modal - The modal element.
 */
function cleanupModalAccessibility(modal) {
    if (modal._keydownHandler) {
        document.removeEventListener('keydown', modal._keydownHandler);
        modal._keydownHandler = null;
    }
    if (modal._clickHandler) {
        modal.removeEventListener('click', modal._clickHandler);
        modal._clickHandler = null;
    }
}

/**
 * Confirm and save the plan from the modal.
 */
async function confirmSavePlan() {
    const nameInput = document.getElementById('planName');
    const name = nameInput.value.trim();
    
    // Clear previous errors
    clearFieldError(nameInput);
    
    if (!name) {
        showFieldError(nameInput, 'Plan name is required.');
        nameInput.focus();
        return;
    }

    const description = document.getElementById('planDescription').value.trim();
    const planData = composer.buildPlan();

    if (!planData || !planData.valid) {
        showInlineError('saveValidationError', 'Cannot save an invalid or empty plan.');
        return;
    }

    // Disable button during save
    const saveBtn = document.getElementById('confirmSaveBtn');
    const originalText = saveBtn.innerHTML;
    saveBtn.disabled = true;
    saveBtn.innerHTML = '<span class="spinner"></span> Saving...';

    try {
        const result = await api.savePlan({
            name: name,
            description: description,
            targets: planData.targets,
        });

        if (result.status === 'success') {
            closeSavePlanModal();
            showToast(`Plan "${result.plan.name}" saved successfully!`, 'success');
            if (planRunner) {
                planRunner.refreshPlans();
            }
        } else {
            showFieldError(nameInput, result.message || 'Failed to save plan.');
        }
    } catch (err) {
        console.error('Failed to save plan:', err);
        showInlineError('saveValidationError', 'An error occurred while saving the plan.');
    } finally {
        saveBtn.disabled = false;
        saveBtn.innerHTML = originalText;
    }
}

/**
 * Show an error message below a form field.
 * @param {HTMLElement} input - The input element.
 * @param {string} message - The error message.
 */
function showFieldError(input, message) {
    input.classList.add('input-error');
    
    // Create or update error message
    let errorEl = input.parentElement.querySelector('.field-error');
    if (!errorEl) {
        errorEl = document.createElement('div');
        errorEl.className = 'field-error';
        input.parentElement.appendChild(errorEl);
    }
    errorEl.textContent = message;
    
    // Clear error on input
    input.addEventListener('input', () => clearFieldError(input), { once: true });
}

/**
 * Clear error state from a form field.
 * @param {HTMLElement} input - The input element.
 */
function clearFieldError(input) {
    input.classList.remove('input-error');
    const errorEl = input.parentElement.querySelector('.field-error');
    if (errorEl) {
        errorEl.remove();
    }
}

/**
 * Show an inline error in a specific container.
 * @param {string} containerId - The ID of the container element (will be created if needed).
 * @param {string} message - The error message.
 */
function showInlineError(containerId, message) {
    let container = document.getElementById(containerId);
    if (!container) {
        // Create container in modal content if it doesn't exist
        const modalContent = document.querySelector('#savePlanModal .modal-content');
        if (modalContent) {
            container = document.createElement('div');
            container.id = containerId;
            container.className = 'inline-error';
            modalContent.insertBefore(container, modalContent.firstChild);
        }
    }
    if (container) {
        container.textContent = message;
        container.style.display = 'block';
        // Auto-hide after 5 seconds
        setTimeout(() => {
            container.style.display = 'none';
        }, 5000);
    }
}

/**
 * Show a toast notification.
 * @param {string} message - The message to display.
 * @param {string} type - The type of toast ('success', 'error', 'info', 'warning').
 */
function showToast(message, type = 'info') {
    // Create toast container if it doesn't exist
    let toastContainer = document.getElementById('toastContainer');
    if (!toastContainer) {
        toastContainer = document.createElement('div');
        toastContainer.id = 'toastContainer';
        toastContainer.className = 'toast-container';
        document.body.appendChild(toastContainer);
    }
    
    const toast = document.createElement('div');
    toast.className = `toast toast-${type}`;
    
    const messageSpan = document.createElement('span');
    messageSpan.className = 'toast-message';
    messageSpan.textContent = message; // Use textContent for XSS safety
    
    const closeBtn = document.createElement('button');
    closeBtn.className = 'toast-close';
    closeBtn.textContent = '✕';
    closeBtn.onclick = () => toast.remove();
    
    toast.appendChild(messageSpan);
    toast.appendChild(closeBtn);
    toastContainer.appendChild(toast);
    
    // Auto-remove after 4 seconds
    setTimeout(() => {
        toast.classList.add('toast-fade-out');
        setTimeout(() => toast.remove(), 300);
    }, 4000);
}

/**
 * Show or update a persistent toast notification that doesn't auto-dismiss.
 * @param {string} id - Unique identifier for the toast.
 * @param {string} message - The message to display (supports newlines).
 * @param {string} type - The type of toast ('success', 'error', 'info', 'warning').
 */
function showPersistentToast(id, message, type = 'info') {
    // Create toast container if it doesn't exist
    let toastContainer = document.getElementById('toastContainer');
    if (!toastContainer) {
        toastContainer = document.createElement('div');
        toastContainer.id = 'toastContainer';
        toastContainer.className = 'toast-container';
        document.body.appendChild(toastContainer);
    }
    
    // Check if toast with this ID already exists
    let toast = document.getElementById(id);
    
    if (toast) {
        // Update existing toast
        const messageSpan = toast.querySelector('.toast-message');
        if (messageSpan) {
            // Clear existing content
            messageSpan.innerHTML = '';
            // Split message by newlines and create elements
            const lines = message.split('\n');
            lines.forEach((line, index) => {
                const textNode = document.createTextNode(line);
                messageSpan.appendChild(textNode);
                if (index < lines.length - 1) {
                    messageSpan.appendChild(document.createElement('br'));
                }
            });
        }
        // Update class in case type changed
        toast.className = `toast toast-${type} persistent`;
    } else {
        // Create new persistent toast
        toast = document.createElement('div');
        toast.id = id;
        toast.className = `toast toast-${type} persistent`;
        
        const messageSpan = document.createElement('span');
        messageSpan.className = 'toast-message';
        // Split message by newlines and create elements
        const lines = message.split('\n');
        lines.forEach((line, index) => {
            const textNode = document.createTextNode(line);
            messageSpan.appendChild(textNode);
            if (index < lines.length - 1) {
                messageSpan.appendChild(document.createElement('br'));
            }
        });
        
        const closeBtn = document.createElement('button');
        closeBtn.className = 'toast-close';
        closeBtn.textContent = '✕';
        closeBtn.onclick = () => toast.remove();
        
        toast.appendChild(messageSpan);
        toast.appendChild(closeBtn);
        toastContainer.appendChild(toast);
    }
}

/**
 * Dismiss a persistent toast by ID.
 * @param {string} id - The ID of the toast to dismiss.
 */
function dismissPersistentToast(id) {
    const toast = document.getElementById(id);
    if (toast) {
        toast.classList.add('toast-fade-out');
        setTimeout(() => toast.remove(), 300);
    }
}

/**
 * Initialize the application when DOM is loaded.
 */
function init() {
    dashboard = new CIDashboard();
    composer = new PlanComposer();
    planRunner = new PlanRunner();
    
    // Expose instances and functions globally for onclick handlers
    window.dashboard = dashboard;
    window.composer = composer;
    window.planRunner = planRunner;
    
    window.switchTab = switchTab;
    window.startRun = startRun;
    window.cancelRun = cancelRun;
    window.clearDashboard = clearDashboard;
    window.selectAllTargets = selectAllTargets;
    window.clearSelection = clearSelection;
    window.runCustomPlan = runCustomPlan;
    window.savePlan = savePlan;
    window.closeSavePlanModal = closeSavePlanModal;
    window.confirmSavePlan = confirmSavePlan;
    window.showToast = showToast;
    window.showPersistentToast = showPersistentToast;
    window.dismissPersistentToast = dismissPersistentToast;
}

// Initialize on DOM load
document.addEventListener('DOMContentLoaded', init);
