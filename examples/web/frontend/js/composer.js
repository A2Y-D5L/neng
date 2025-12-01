// CI Pipeline Dashboard - Plan Composer Class

import * as api from './api.js';

/**
 * PlanComposer handles building custom pipelines by selecting targets.
 * Provides UI for target selection, dependency auto-selection, and plan preview.
 */
export class PlanComposer {
    constructor() {
        this.availableTargets = [];
        this.selectedTargets = new Set();
        this.rootTargets = new Set();
        this.failTargets = new Set();
        this.previewData = null;
        this.autoSelectedDeps = new Map(); // Maps target name to the target that caused it to be auto-selected
        this.previewDebounceTimer = null;
        this.isLoadingPreview = false;
        
        this.init();
    }
    
    async init() {
        await this.loadAvailableTargets();
        
        // Listen for running state changes
        window.addEventListener('dashboard:runningStateChanged', (e) => {
            this.updateRunButton(e.detail.running);
        });
    }
    
    async loadAvailableTargets() {
        try {
            const data = await api.fetchAvailableTargets();
            this.availableTargets = data.targets || [];
            this.renderAvailableTargets();
        } catch (err) {
            console.error('Failed to load targets:', err);
            document.getElementById('availableTargets').innerHTML = 
                '<div class="empty-state"><p>Failed to load targets</p></div>';
        }
    }
    
    renderAvailableTargets() {
        const container = document.getElementById('availableTargets');
        
        if (this.availableTargets.length === 0) {
            container.innerHTML = '<div class="empty-state"><p>No targets available</p></div>';
            return;
        }
        
        container.innerHTML = this.availableTargets.map(target => {
            const isSelected = this.selectedTargets.has(target.name);
            const isAutoSelected = this.autoSelectedDeps.has(target.name);
            const autoSelectedBy = isAutoSelected ? this.autoSelectedDeps.get(target.name) : null;
            
            let classes = 'target-item';
            if (isSelected) classes += ' selected';
            if (isAutoSelected) classes += ' auto-selected';
            
            return `
            <div class="${classes}" 
                 data-target="${target.name}"
                 onclick="window.composer.toggleTarget('${target.name}')">
                <div class="target-checkbox">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3">
                        <path d="M20 6L9 17l-5-5"/>
                    </svg>
                </div>
                <div class="target-info">
                    <div class="target-info-name">
                        ${target.name}
                        ${isAutoSelected ? `<span class="auto-selected-badge" title="Auto-selected as dependency of ${autoSelectedBy}">↑ dep</span>` : ''}
                    </div>
                    <div class="target-info-desc">${target.desc}</div>
                    <div class="target-info-meta">
                        ${target.deps && target.deps.length > 0 
                            ? `<span>← ${target.deps.join(', ')}</span>` 
                            : '<span>No dependencies</span>'}
                        <span>~${target.durationMin}-${target.durationMax}ms</span>
                        ${target.canFail ? '<span style="color: var(--accent-orange)">Can fail</span>' : ''}
                    </div>
                </div>
            </div>
        `}).join('');
    }
    
    toggleTarget(name) {
        if (this.selectedTargets.has(name)) {
            // Check if other selected targets depend on this one
            const dependents = this.findSelectedDependents(name);
            
            if (dependents.length > 0) {
                const dependentNames = dependents.map(d => `"${d}"`).join(', ');
                const message = dependents.length === 1 
                    ? `The target "${dependents[0]}" depends on "${name}". Removing "${name}" will also remove "${dependents[0]}".`
                    : `The targets ${dependentNames} depend on "${name}". Removing "${name}" will also remove these targets.`;
                
                if (!confirm(message + '\n\nDo you want to continue?')) {
                    return;
                }
                
                // Remove all dependents recursively
                this.removeTargetAndDependents(name);
            } else {
                this.selectedTargets.delete(name);
                this.rootTargets.delete(name);
                this.failTargets.delete(name);
                this.autoSelectedDeps.delete(name);
            }
        } else {
            this.selectedTargets.add(name);
            // Auto-select dependencies
            const autoSelected = this.autoSelectDependencies(name);
            
            // Render first, then highlight auto-selected dependencies with animation
            this.renderAvailableTargets();
            this.updateSelectors();
            this.updatePreviewDebounced();
            this.updateRunButton();
            
            if (autoSelected.length > 0) {
                this.highlightAutoSelectedTargets(autoSelected);
            }
            return;
        }
        
        this.renderAvailableTargets();
        this.updateSelectors();
        this.updatePreviewDebounced();
        this.updateRunButton();
    }
    
    /**
     * Find all selected targets that depend (directly or transitively) on the given target.
     */
    findSelectedDependents(targetName) {
        const dependents = [];
        
        for (const selected of this.selectedTargets) {
            if (selected === targetName) continue;
            
            const target = this.availableTargets.find(t => t.name === selected);
            if (target && this.dependsOn(target, targetName, new Set())) {
                dependents.push(selected);
            }
        }
        
        return dependents;
    }
    
    /**
     * Check if a target depends (directly or transitively) on another target.
     */
    dependsOn(target, dependencyName, visited) {
        if (!target.deps || target.deps.length === 0) return false;
        if (visited.has(target.name)) return false;
        visited.add(target.name);
        
        for (const dep of target.deps) {
            if (dep === dependencyName) return true;
            
            const depTarget = this.availableTargets.find(t => t.name === dep);
            if (depTarget && this.dependsOn(depTarget, dependencyName, visited)) {
                return true;
            }
        }
        
        return false;
    }
    
    /**
     * Remove a target and all targets that depend on it.
     */
    removeTargetAndDependents(name) {
        const dependents = this.findSelectedDependents(name);
        
        // Remove the target itself
        this.selectedTargets.delete(name);
        this.rootTargets.delete(name);
        this.failTargets.delete(name);
        this.autoSelectedDeps.delete(name);
        
        // Remove all dependents
        for (const dependent of dependents) {
            this.selectedTargets.delete(dependent);
            this.rootTargets.delete(dependent);
            this.failTargets.delete(dependent);
            this.autoSelectedDeps.delete(dependent);
        }
    }
    
    /**
     * Highlight targets that were just auto-selected with a brief animation.
     */
    highlightAutoSelectedTargets(targetNames) {
        // Wait for render to complete, then add animation class
        requestAnimationFrame(() => {
            for (const name of targetNames) {
                const element = document.querySelector(`.target-item[data-target="${name}"]`);
                if (element) {
                    element.classList.add('auto-selected-highlight');
                    // Remove the animation class after it completes
                    setTimeout(() => {
                        element.classList.remove('auto-selected-highlight');
                    }, 800);
                }
            }
        });
    }
    
    autoSelectDependencies(name) {
        const target = this.availableTargets.find(t => t.name === name);
        if (!target || !target.deps) return [];
        
        const autoSelected = [];
        
        for (const dep of target.deps) {
            if (!this.selectedTargets.has(dep)) {
                this.selectedTargets.add(dep);
                // Track that this dependency was auto-selected due to 'name'
                if (!this.autoSelectedDeps.has(dep)) {
                    this.autoSelectedDeps.set(dep, name);
                }
                autoSelected.push(dep);
                // Recursively auto-select transitive dependencies
                autoSelected.push(...this.autoSelectDependencies(dep));
            }
        }
        
        return autoSelected;
    }
    
    selectAllTargets() {
        for (const target of this.availableTargets) {
            this.selectedTargets.add(target.name);
        }
        // Clear auto-selected tracking since user explicitly selected all
        this.autoSelectedDeps.clear();
        
        this.renderAvailableTargets();
        this.updateSelectors();
        this.updatePreviewDebounced();
        this.updateRunButton();
    }
    
    clearSelection() {
        this.selectedTargets.clear();
        this.rootTargets.clear();
        this.failTargets.clear();
        this.autoSelectedDeps.clear();
        this.previewData = null;
        
        // Clear any pending preview request
        if (this.previewDebounceTimer) {
            clearTimeout(this.previewDebounceTimer);
            this.previewDebounceTimer = null;
        }
        
        this.renderAvailableTargets();
        this.updateSelectors();
        this.renderPreview();
        this.updateRunButton();
    }
    
    updateSelectors() {
        this.renderRootSelector();
        this.renderFailSelector();
        this.updateSelectedCount();
    }
    
    updateSelectedCount() {
        document.getElementById('selectedCount').textContent = 
            `${this.selectedTargets.size} targets selected`;
    }
    
    renderRootSelector() {
        const container = document.getElementById('rootSelector');
        
        if (this.selectedTargets.size === 0) {
            container.innerHTML = '<div class="empty-hint">Select targets first</div>';
            return;
        }
        
        const selectedArray = Array.from(this.selectedTargets).sort();
        container.innerHTML = selectedArray.map(name => `
            <div class="selector-chip ${this.rootTargets.has(name) ? 'selected' : ''}"
                 onclick="window.composer.toggleRoot('${name}')">
                ${name}
            </div>
        `).join('');
    }
    
    renderFailSelector() {
        const container = document.getElementById('failSelector');
        
        if (this.selectedTargets.size === 0) {
            container.innerHTML = '<div class="empty-hint">Select targets first</div>';
            return;
        }
        
        // Only show targets that can fail
        const canFailTargets = Array.from(this.selectedTargets)
            .filter(name => {
                const target = this.availableTargets.find(t => t.name === name);
                return target && target.canFail;
            })
            .sort();
        
        if (canFailTargets.length === 0) {
            container.innerHTML = '<div class="empty-hint">No targets can be set to fail</div>';
            return;
        }
        
        container.innerHTML = canFailTargets.map(name => `
            <div class="selector-chip ${this.failTargets.has(name) ? 'fail-selected' : ''}"
                 onclick="window.composer.toggleFail('${name}')">
                ${name}
            </div>
        `).join('');
    }
    
    toggleRoot(name) {
        if (this.rootTargets.has(name)) {
            this.rootTargets.delete(name);
        } else {
            this.rootTargets.add(name);
        }
        this.renderRootSelector();
        this.renderPreview();
    }
    
    toggleFail(name) {
        if (this.failTargets.has(name)) {
            this.failTargets.delete(name);
        } else {
            this.failTargets.add(name);
        }
        this.renderFailSelector();
        this.renderPreview();
    }
    
    /**
     * Debounced version of updatePreview to avoid excessive API calls.
     */
    updatePreviewDebounced() {
        // Clear any pending preview request
        if (this.previewDebounceTimer) {
            clearTimeout(this.previewDebounceTimer);
        }
        
        // Debounce by 300ms
        this.previewDebounceTimer = setTimeout(() => {
            this.updatePreview();
        }, 300);
    }
    
    async updatePreview() {
        if (this.selectedTargets.size === 0) {
            this.previewData = null;
            this.isLoadingPreview = false;
            this.renderPreview();
            return;
        }
        
        const previewContainer = document.getElementById('previewStages');
        
        // Show loading state
        this.isLoadingPreview = true;
        previewContainer.classList.add('loading');
        
        try {
            this.previewData = await api.previewPlan({
                targets: Array.from(this.selectedTargets),
                rootTargets: Array.from(this.rootTargets)
            });
            this.renderPreview();
        } catch (err) {
            console.error('Failed to preview plan:', err);
            this.previewData = { valid: false, error: err.message };
            this.renderPreview();
        } finally {
            this.isLoadingPreview = false;
            previewContainer.classList.remove('loading');
        }
    }
    
    renderPreview() {
        const container = document.getElementById('previewStages');
        
        if (!this.previewData || this.selectedTargets.size === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                        <path d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2"/>
                    </svg>
                    <p>Select targets to preview the execution plan</p>
                </div>
            `;
            return;
        }
        
        if (!this.previewData.valid) {
            container.innerHTML = `
                <div class="empty-state">
                    <svg viewBox="0 0 24 24" fill="none" stroke="var(--accent-red)" stroke-width="1.5">
                        <circle cx="12" cy="12" r="10"/>
                        <path d="M15 9l-6 6M9 9l6 6"/>
                    </svg>
                    <p style="color: var(--accent-red)">${this.previewData.error}</p>
                </div>
            `;
            return;
        }
        
        const stages = this.previewData.stages || [];
        
        container.innerHTML = stages.map(stage => `
            <div class="preview-stage">
                <div class="preview-stage-header">Stage ${stage.index}</div>
                <div class="preview-stage-targets">
                    ${stage.targets.map(name => {
                        const isRoot = this.rootTargets.has(name);
                        const willFail = this.failTargets.has(name);
                        let classes = 'preview-target';
                        if (isRoot) classes += ' root';
                        if (willFail) classes += ' will-fail';
                        
                        return `<span class="${classes}">${name}${isRoot ? ' ⭐' : ''}${willFail ? ' 💥' : ''}</span>`;
                    }).join('')}
                </div>
            </div>
        `).join('');
    }
    
    updateRunButton(isRunning = false) {
        const btn = document.getElementById('runCustomBtn');
        const saveBtn = document.getElementById('saveCustomBtn');
        const hasTargets = this.selectedTargets.size > 0;
        btn.disabled = !hasTargets || isRunning;
        if (saveBtn) {
            saveBtn.disabled = !hasTargets || isRunning;
        }
    }
    
    buildPlan() {
        // Return the plan data from the preview
        if (!this.previewData || !this.previewData.valid) {
            return null;
        }
        
        // Convert preview stages to simple array format for saving
        const stages = (this.previewData.stages || []).map(stage => stage.targets);
        
        return {
            valid: true,
            stages: stages,
            targets: Array.from(this.selectedTargets),
            rootTargets: Array.from(this.rootTargets)
        };
    }
    
    async runPlan(switchTabFn, dashboard) {
        if (this.selectedTargets.size === 0) {
            if (window.showToast) {
                window.showToast('Please select at least one target', 'error');
            }
            return;
        }
        
        // Switch to execution tab
        switchTabFn('execution');
        
        // Clear previous run
        dashboard.clear();
        
        try {
            const data = await api.startCustomRun({
                targets: Array.from(this.selectedTargets),
                rootTargets: Array.from(this.rootTargets),
                failTargets: Array.from(this.failTargets)
            });
            
            if (data.status === 'error') {
                if (window.showToast) {
                    window.showToast(data.message, 'error');
                }
            }
        } catch (err) {
            console.error('Failed to run custom plan:', err);
            if (window.showToast) {
                window.showToast('Failed to run: ' + err.message, 'error');
            }
        }
    }
    
    async savePlan(planRunner) {
        if (this.selectedTargets.size === 0) {
            if (window.showToast) {
                window.showToast('Please select at least one target to save.', 'error');
            }
            return;
        }

        const name = prompt("Enter a name for this plan:");
        if (!name) {
            return;
        }

        const description = prompt("Enter an optional description:");

        const planData = this.buildPlan();
        if (!planData) {
            if (window.showToast) {
                window.showToast('Could not build plan data. Please ensure targets are selected and valid.', 'error');
            }
            return;
        }

        try {
            const result = await api.savePlan({
                name: name,
                description: description || '',
                targets: planData.targets,
            });

            if (result.status === 'success') {
                if (window.showToast) {
                    window.showToast(`Plan "${result.plan.name}" saved successfully!`, 'success');
                }
                // Refresh plan runner
                if (planRunner) {
                    planRunner.refreshPlans();
                }
            } else {
                if (window.showToast) {
                    window.showToast(`Error saving plan: ${result.message}`, 'error');
                }
            }
        } catch (err) {
            console.error('Failed to save plan:', err);
            if (window.showToast) {
                window.showToast('An error occurred while saving the plan.', 'error');
            }
        }
    }
}
