// CI Pipeline Dashboard - API Communication Layer

/**
 * API endpoints for the CI Dashboard
 */
const API = {
    EVENTS: '/api/events',
    RUN: '/api/run',
    RUN_CUSTOM: '/api/run/custom',
    RUN_PLAN: '/api/run/plan',
    CANCEL: '/api/cancel',
    STATUS: '/api/status',
    TARGETS: '/api/targets',
    PREVIEW: '/api/preview',
    PLANS: '/api/plans',
    PLANS_SAVE: '/api/plans/save',
    PLANS_DELETE: '/api/plans/delete',
    PLANS_DETAILS: '/api/plans/details',
};

/**
 * Check if a pipeline is currently running.
 * @returns {Promise<{running: boolean}>}
 */
export async function checkStatus() {
    const response = await fetch(API.STATUS);
    return response.json();
}

/**
 * Start a demo pipeline run.
 * @param {boolean} withFailure - Whether to simulate a failure.
 * @returns {Promise<{status: string, message: string}>}
 */
export async function startDemoRun(withFailure = false) {
    const url = withFailure ? `${API.RUN}?fail=true` : API.RUN;
    const response = await fetch(url, { method: 'POST' });
    return response.json();
}

/**
 * Start a custom pipeline run.
 * @param {Object} params - Run parameters.
 * @param {string[]} params.targets - Target names to include.
 * @param {string[]} params.rootTargets - Root targets to execute.
 * @param {string[]} params.failTargets - Targets that should fail.
 * @returns {Promise<{status: string, message: string, roots?: string[]}>}
 */
export async function startCustomRun({ targets, rootTargets, failTargets }) {
    const response = await fetch(API.RUN_CUSTOM, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ targets, rootTargets, failTargets }),
    });
    return response.json();
}

/**
 * Start a pipeline run from a saved plan.
 * @param {Object} params - Run parameters.
 * @param {string} params.planId - ID of the saved plan.
 * @param {string} params.mode - Run mode ('all', 'target', 'stage').
 * @param {string[]} params.failTargets - Targets that should fail.
 * @param {string} [params.targetName] - Target name (for 'target' mode).
 * @param {number} [params.stageIndex] - Stage index (for 'stage' mode).
 * @returns {Promise<{status: string, message: string, planName?: string, roots?: string[]}>}
 */
export async function startPlanRun(params) {
    const response = await fetch(API.RUN_PLAN, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(params),
    });
    return response.json();
}

/**
 * Cancel the current pipeline run.
 * @returns {Promise<{status: string, message: string}>}
 */
export async function cancelRun() {
    const response = await fetch(API.CANCEL, { method: 'POST' });
    return response.json();
}

/**
 * Fetch available targets for plan composition.
 * @returns {Promise<{targets: Array}>}
 */
export async function fetchAvailableTargets() {
    const response = await fetch(API.TARGETS);
    return response.json();
}

/**
 * Preview a plan without executing it.
 * @param {Object} params - Preview parameters.
 * @param {string[]} params.targets - Target names to include.
 * @param {string[]} params.rootTargets - Root targets.
 * @returns {Promise<{valid: boolean, error?: string, targets?: Array, stages?: Array}>}
 */
export async function previewPlan({ targets, rootTargets }) {
    const response = await fetch(API.PREVIEW, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ targets, rootTargets }),
    });
    return response.json();
}

/**
 * Fetch all saved plans.
 * @returns {Promise<{plans: Array}>}
 */
export async function fetchSavedPlans() {
    const response = await fetch(API.PLANS);
    return response.json();
}

/**
 * Save a new plan.
 * @param {Object} params - Plan parameters.
 * @param {string} params.name - Plan name.
 * @param {string} [params.description] - Plan description.
 * @param {string[]} params.targets - Target names.
 * @returns {Promise<{status: string, plan?: Object, message?: string}>}
 */
export async function savePlan({ name, description, targets }) {
    const response = await fetch(API.PLANS_SAVE, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, description, targets }),
    });
    return response.json();
}

/**
 * Delete a saved plan.
 * @param {string} planId - ID of the plan to delete.
 * @returns {Promise<{status: string, message?: string}>}
 */
export async function deletePlan(planId) {
    const response = await fetch(`${API.PLANS_DELETE}?id=${planId}`, {
        method: 'DELETE',
    });
    return response.json();
}

/**
 * Fetch details of a specific plan.
 * @param {string} planId - ID of the plan.
 * @returns {Promise<{status: string, plan?: Object, targets?: Array, stages?: Array, message?: string}>}
 */
export async function fetchPlanDetails(planId) {
    const response = await fetch(`${API.PLANS_DETAILS}?id=${planId}`);
    return response.json();
}

/**
 * Get the SSE events endpoint URL.
 * @returns {string}
 */
export function getEventsEndpoint() {
    return API.EVENTS;
}
