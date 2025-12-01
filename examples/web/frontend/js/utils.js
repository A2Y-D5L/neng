// CI Pipeline Dashboard - Utility Functions

/**
 * Escapes HTML special characters to prevent XSS attacks.
 * @param {string} text - The text to escape.
 * @returns {string} The escaped HTML string.
 */
export function escapeHtml(text) {
    if (!text) return '';
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

/**
 * Formats a duration in milliseconds to a human-readable string.
 * @param {number} ms - Duration in milliseconds.
 * @returns {string} Formatted duration string.
 */
export function formatDuration(ms) {
    if (!ms || ms <= 0) return '0ms';
    if (ms < 1000) return `${ms}ms`;
    if (ms < 60000) return `${(ms / 1000).toFixed(2)}s`;
    const minutes = Math.floor(ms / 60000);
    const seconds = ((ms % 60000) / 1000).toFixed(1);
    return `${minutes}m ${seconds}s`;
}

/**
 * Gets status icon SVG for a given status.
 * @param {string} status - The status (pending, running, success, failed, skipped).
 * @returns {string} SVG HTML string.
 */
export function getStatusIcon(status) {
    switch (status) {
        case 'running':
            return `<svg class="target-status-icon running" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"/>
            </svg>`;
        case 'success':
            return `<svg class="target-status-icon" viewBox="0 0 24 24" fill="none" stroke="#3fb950" stroke-width="2">
                <path d="M20 6L9 17l-5-5"/>
            </svg>`;
        case 'failed':
            return `<svg class="target-status-icon" viewBox="0 0 24 24" fill="none" stroke="#f85149" stroke-width="2">
                <circle cx="12" cy="12" r="10"/>
                <path d="M15 9l-6 6M9 9l6 6"/>
            </svg>`;
        case 'skipped':
            return `<svg class="target-status-icon" viewBox="0 0 24 24" fill="none" stroke="#8b949e" stroke-width="2">
                <path d="M5 4h4l10 8-10 8H5l10-8z"/>
            </svg>`;
        default:
            return `<svg class="target-status-icon" viewBox="0 0 24 24" fill="none" stroke="#6e7681" stroke-width="2">
                <circle cx="12" cy="12" r="10"/>
            </svg>`;
    }
}
