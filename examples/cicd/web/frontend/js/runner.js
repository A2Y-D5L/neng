// CI Pipeline Dashboard - Plan Runner Class

import { escapeHtml } from './utils.js';
import * as api from './api.js';

/**
 * PlanRunner handles loading, displaying, and running saved plans.
 * Supports running all targets, specific targets, or specific stages.
 */
export class PlanRunner {
    constructor() {
        this.plans = [];
        this.selectedPlan = null;
        this.runMode = 'all'; // 'all', 'target', 'stage'
        this.selectedTarget = null;
        this.selectedStage = null;
        this.failTargets = new Set();
        
        this.init();
    }

    async init() {
        await this.loadPlans();
    }

    async loadPlans() {
        try {
            const data = await api.fetchSavedPlans();
            this.plans = data.plans || [];
            this.renderPlans();
        } catch (err) {
            console.error('Failed to load plans:', err);
            this.showError('Failed to load plans');
        }
    }

    refreshPlans() {
        this.loadPlans();
    }

    renderPlans() {
        const container = document.getElementById('savedPlansList');
        if (!container) return;

        if (this.plans.length === 0) {
            container.innerHTML = `
                <div class="empty-plans">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                        <path d="M9 12h6M9 16h6M17 21H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/>
                    </svg>
                    <p>No saved plans yet.<br>Create one in the Composer tab!</p>
                </div>
            `;
            return;
        }

        container.innerHTML = this.plans.map(plan => `
            <div class="plan-card ${plan.id === this.selectedPlan?.id ? 'selected' : ''} ${plan.isBuiltin ? 'builtin' : 'user-created'}" 
                 onclick="window.planRunner.selectPlan('${plan.id}')">
                ${!plan.isBuiltin ? `<button class="plan-card-delete" onclick="event.stopPropagation(); window.planRunner.deletePlan('${plan.id}')" title="Delete plan">✕</button>` : ''}
                <div class="plan-card-header">
                    <span class="plan-card-name">${escapeHtml(plan.name)}</span>
                    <span class="plan-card-badge ${plan.isBuiltin ? 'builtin' : 'user'}">${plan.isBuiltin ? 'Built-in' : 'Custom'}</span>
                </div>
                <div class="plan-card-description">${escapeHtml(plan.description || 'No description')}</div>
                <div class="plan-card-meta">
                    <span>${plan.targets?.length || 0} targets</span>
                </div>
            </div>
        `).join('');
    }

    async selectPlan(planId) {
        try {
            const data = await api.fetchPlanDetails(planId);
            
            if (data.status === 'error') {
                this.showError(data.message || 'Failed to load plan details');
                return;
            }
            
            if (data.status === 'success') {
                this.selectedPlan = {
                    ...data.plan,
                    stages: data.stages?.map(s => s.targets) || [],
                    targets: data.targets || []
                };
                this.runMode = 'all';
                this.selectedTarget = null;
                this.selectedStage = null;
                this.failTargets.clear();
                
                this.renderPlans();
                this.showPlanDetails();
            }
        } catch (err) {
            console.error('Failed to load plan details:', err);
            this.showError('Failed to load plan details: ' + err.message);
        }
    }

    showPlanDetails() {
        const panel = document.getElementById('planDetailsPanel');
        if (!panel || !this.selectedPlan) return;

        panel.style.display = 'block';
        
        document.getElementById('selectedPlanName').textContent = this.selectedPlan.name;
        document.getElementById('selectedPlanDescription').textContent = 
            this.selectedPlan.description || 'No description provided.';

        // Reset mode buttons
        document.querySelectorAll('.mode-btn').forEach(btn => {
            btn.classList.toggle('active', btn.dataset.mode === this.runMode);
        });

        // Render fail targets selector
        this.renderFailTargets();
        
        // Update run info text
        this.updateRunInfo();
        
        // Render stages preview
        this.renderStagesPreview();

        // Hide target/stage selector initially
        document.getElementById('targetStageSelector').style.display = 'none';
    }

    setRunMode(mode) {
        this.runMode = mode;
        this.selectedTarget = null;
        this.selectedStage = null;

        // Update button states
        document.querySelectorAll('.mode-btn').forEach(btn => {
            btn.classList.toggle('active', btn.dataset.mode === mode);
        });

        const selector = document.getElementById('targetStageSelector');
        const selectorLabel = document.getElementById('selectorLabel');
        const selectorOptions = document.getElementById('selectorOptions');

        if (mode === 'all') {
            selector.style.display = 'none';
            this.updateRunInfo();
            this.renderStagesPreview();
        } else if (mode === 'target') {
            selector.style.display = 'block';
            selectorLabel.textContent = 'Select Target';
            this.renderTargetSelector(selectorOptions);
            this.updateRunInfo();
        } else if (mode === 'stage') {
            selector.style.display = 'block';
            selectorLabel.textContent = 'Select Stage';
            this.renderStageSelector(selectorOptions);
            this.updateRunInfo();
        }
    }
    
    /**
     * Get all dependencies (transitive) for a target.
     */
    getTargetDependencies(targetName) {
        if (!this.selectedPlan?.targets) return [];
        
        const deps = new Set();
        const visited = new Set();
        
        const collectDeps = (name) => {
            if (visited.has(name)) return;
            visited.add(name);
            
            const target = this.selectedPlan.targets.find(t => t.name === name);
            if (target && target.deps) {
                for (const dep of target.deps) {
                    deps.add(dep);
                    collectDeps(dep);
                }
            }
        };
        
        collectDeps(targetName);
        return Array.from(deps);
    }
    
    /**
     * Get all targets that will run for the current mode and selection.
     */
    getTargetsToRun() {
        if (!this.selectedPlan?.stages) return [];
        
        if (this.runMode === 'all') {
            // All targets in the plan
            const allTargets = new Set();
            this.selectedPlan.stages.forEach(stage => {
                stage.forEach(target => allTargets.add(target));
            });
            return Array.from(allTargets);
        } else if (this.runMode === 'target' && this.selectedTarget) {
            // Selected target + its dependencies
            const deps = this.getTargetDependencies(this.selectedTarget);
            return [this.selectedTarget, ...deps];
        } else if (this.runMode === 'stage' && this.selectedStage !== null) {
            // All targets up to and including the selected stage
            const targets = new Set();
            for (let i = 0; i <= this.selectedStage; i++) {
                this.selectedPlan.stages[i].forEach(t => targets.add(t));
            }
            return Array.from(targets);
        }
        
        return [];
    }
    
    /**
     * Update the run info text to show what will run.
     */
    updateRunInfo() {
        let infoContainer = document.getElementById('runInfoText');
        
        // Create the info container if it doesn't exist
        if (!infoContainer) {
            const stagesPreview = document.getElementById('planStagesPreview');
            if (stagesPreview && stagesPreview.parentElement) {
                const infoDiv = document.createElement('div');
                infoDiv.id = 'runInfoText';
                infoDiv.className = 'run-info-text';
                stagesPreview.parentElement.insertBefore(infoDiv, stagesPreview);
                infoContainer = infoDiv;
            }
        }
        
        if (!infoContainer) return;
        
        const targetsToRun = this.getTargetsToRun();
        
        if (this.runMode === 'all') {
            infoContainer.innerHTML = `<span class="run-info-label">Will run:</span> All ${targetsToRun.length} targets`;
        } else if (this.runMode === 'target') {
            if (this.selectedTarget) {
                const deps = this.getTargetDependencies(this.selectedTarget);
                if (deps.length > 0) {
                    infoContainer.innerHTML = `<span class="run-info-label">Will run:</span> <strong>${this.selectedTarget}</strong> + ${deps.length} ${deps.length === 1 ? 'dependency' : 'dependencies'}`;
                } else {
                    infoContainer.innerHTML = `<span class="run-info-label">Will run:</span> <strong>${this.selectedTarget}</strong> (no dependencies)`;
                }
            } else {
                infoContainer.innerHTML = `<span class="run-info-hint">Select a target to see what will run</span>`;
            }
        } else if (this.runMode === 'stage') {
            if (this.selectedStage !== null) {
                const stageNum = this.selectedStage + 1;
                infoContainer.innerHTML = `<span class="run-info-label">Will run:</span> Stages 1-${stageNum} (${targetsToRun.length} targets)`;
            } else {
                infoContainer.innerHTML = `<span class="run-info-hint">Select a stage to see what will run</span>`;
            }
        }
    }

    renderTargetSelector(container) {
        if (!this.selectedPlan?.stages) return;

        const allTargets = new Set();
        this.selectedPlan.stages.forEach(stage => {
            stage.forEach(target => allTargets.add(target));
        });

        container.innerHTML = Array.from(allTargets).map(target => `
            <div class="selector-option ${this.selectedTarget === target ? 'selected' : ''}" 
                 onclick="window.planRunner.selectTarget('${target}')">
                ${target}
            </div>
        `).join('');
    }

    renderStageSelector(container) {
        if (!this.selectedPlan?.stages) return;

        container.innerHTML = this.selectedPlan.stages.map((stage, idx) => `
            <div class="selector-option ${this.selectedStage === idx ? 'selected' : ''}" 
                 onclick="window.planRunner.selectStage(${idx})">
                Stage ${idx + 1}: ${stage.join(', ')}
            </div>
        `).join('');
    }

    selectTarget(target) {
        this.selectedTarget = target;
        this.renderTargetSelector(document.getElementById('selectorOptions'));
        this.updateRunInfo();
        this.renderStagesPreview();
    }

    selectStage(stageIdx) {
        this.selectedStage = stageIdx;
        this.renderStageSelector(document.getElementById('selectorOptions'));
        this.updateRunInfo();
        this.renderStagesPreview();
    }

    renderFailTargets() {
        const container = document.getElementById('planFailSelector');
        if (!container || !this.selectedPlan?.stages) return;

        const allTargets = new Set();
        this.selectedPlan.stages.forEach(stage => {
            stage.forEach(target => allTargets.add(target));
        });

        if (allTargets.size === 0) {
            container.innerHTML = '<div class="empty-hint">No targets available</div>';
            return;
        }

        container.innerHTML = Array.from(allTargets).map(target => `
            <div class="fail-option ${this.failTargets.has(target) ? 'selected' : ''}"
                 onclick="window.planRunner.toggleFailTarget('${target}')">
                ${target}
            </div>
        `).join('');
    }

    toggleFailTarget(target) {
        if (this.failTargets.has(target)) {
            this.failTargets.delete(target);
        } else {
            this.failTargets.add(target);
        }
        this.renderFailTargets();
    }

    renderStagesPreview() {
        const container = document.getElementById('planStagesPreview');
        if (!container || !this.selectedPlan?.stages) return;

        // Get all targets that will run
        const targetsToRun = new Set(this.getTargetsToRun());

        container.innerHTML = this.selectedPlan.stages.map((stage, idx) => {
            // Determine if this stage is highlighted based on mode
            let stageHighlighted = false;
            if (this.runMode === 'stage' && this.selectedStage !== null) {
                stageHighlighted = idx <= this.selectedStage;
            } else if (this.runMode === 'all') {
                stageHighlighted = true;
            } else if (this.runMode === 'target' && this.selectedTarget) {
                // Stage is highlighted if any of its targets will run
                stageHighlighted = stage.some(t => targetsToRun.has(t));
            }
            
            return `
                <div class="preview-stage ${stageHighlighted ? 'highlighted' : 'dimmed'}">
                    <span class="preview-stage-number">${idx + 1}</span>
                    <div class="preview-stage-targets">
                        ${stage.map(target => {
                            const willRun = targetsToRun.has(target);
                            const isSelectedTarget = this.runMode === 'target' && target === this.selectedTarget;
                            let classes = 'preview-target';
                            if (willRun) classes += ' will-run';
                            if (isSelectedTarget) classes += ' selected-target';
                            if (!willRun && this.runMode !== 'all') classes += ' dimmed';
                            
                            return `<span class="${classes}">${target}</span>`;
                        }).join('')}
                    </div>
                </div>
            `;
        }).join('');
    }

    closePlanDetails() {
        const panel = document.getElementById('planDetailsPanel');
        if (panel) {
            panel.style.display = 'none';
        }
        this.selectedPlan = null;
        this.renderPlans();
    }

    async deletePlan(planId) {
        if (!confirm('Are you sure you want to delete this plan?')) return;

        try {
            const data = await api.deletePlan(planId);
            
            if (data.status === 'success') {
                if (this.selectedPlan?.id === planId) {
                    this.closePlanDetails();
                }
                this.loadPlans();
                if (window.showToast) {
                    window.showToast('Plan deleted successfully', 'success');
                }
            } else {
                if (window.showToast) {
                    window.showToast(data.message || 'Failed to delete plan', 'error');
                }
            }
        } catch (err) {
            console.error('Failed to delete plan:', err);
            if (window.showToast) {
                window.showToast('Failed to delete plan', 'error');
            }
        }
    }

    async runSelectedPlan(dashboard) {
        if (!this.selectedPlan) return;

        const request = {
            planId: this.selectedPlan.id,
            mode: this.runMode,
            failTargets: Array.from(this.failTargets)
        };

        if (this.runMode === 'target' && this.selectedTarget) {
            request.targetName = this.selectedTarget;
        } else if (this.runMode === 'stage' && this.selectedStage !== null) {
            request.stageIndex = this.selectedStage;
        }

        try {
            // Clear previous run
            if (dashboard) {
                dashboard.clear();
            }

            const data = await api.startPlanRun(request);
            
            if (data.status !== 'success') {
                if (window.showToast) {
                    window.showToast(data.message || 'Failed to start plan', 'error');
                }
            }
        } catch (err) {
            console.error('Failed to run plan:', err);
            if (window.showToast) {
                window.showToast('Failed to run plan: ' + err.message, 'error');
            }
        }
    }

    showError(message) {
        const container = document.getElementById('savedPlansList');
        if (!container) return;

        container.innerHTML = `
            <div class="error-message">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                    <path d="M12 2v20m10-10H2"/>
                </svg>
                <div class="error-text">${message}</div>
            </div>
        `;
    }
}
