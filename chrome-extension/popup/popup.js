document.addEventListener('DOMContentLoaded', () => {
    // BUG FIX: guard against undefined response (service worker not yet active / reloading)
    chrome.runtime.sendMessage({ type: 'getStatus' }, (response) => {
        if (chrome.runtime.lastError || !response) {
            // Extension service worker is initializing — show a safe default state
            document.getElementById('statusDot').classList.add('disconnected');
            document.getElementById('statusText').textContent = 'Loading... try again in a moment';
            document.getElementById('enableToggle').checked = false;
            return;
        }

        const dot = document.getElementById('statusDot');
        const text = document.getElementById('statusText');
        const toggle = document.getElementById('enableToggle');

        if (response.configured) {
            dot.classList.add(response.enabled ? 'connected' : 'paused');
            text.textContent = response.enabled ? 'Connected & Active' : 'Paused';
        } else {
            dot.classList.add('disconnected');
            text.textContent = 'Not configured — open Settings';
        }

        // BUG FIX: reflect the ACTUAL stored state, not assume checked
        toggle.checked = response.enabled;
    });

    // Toggle enable/disable
    document.getElementById('enableToggle').addEventListener('change', () => {
        chrome.runtime.sendMessage({ type: 'toggleEnabled' }, (response) => {
            if (chrome.runtime.lastError || !response) return;

            const dot = document.getElementById('statusDot');
            const text = document.getElementById('statusText');
            // BUG FIX: clear all state classes before adding new one
            dot.className = 'status-indicator ' + (response.enabled ? 'connected' : 'paused');
            text.textContent = response.enabled ? 'Connected & Active' : 'Paused';
        });
    });

    // Load history
    chrome.runtime.sendMessage({ type: 'getHistory' }, (response) => {
        if (chrome.runtime.lastError || !response) return;

        const list = document.getElementById('historyList');
        if (!response.history || response.history.length === 0) {
            list.innerHTML = '<p class="empty">No links sent yet</p>';
            return;
        }

        let html = '';
        response.history.slice(0, 5).forEach(entry => {
            const ago = timeAgo(new Date(entry.time));
            // BUG FIX: escape filename/source to prevent XSS via stored history entries
            const safeName = escapeHtml(entry.filename || 'Unknown');
            const safeSource = escapeHtml(entry.source || '');
            html += `
                <div class="history-item">
                    <div class="history-name">${safeName}</div>
                    <div class="history-meta">${safeSource} · ${ago} · #${entry.taskId}</div>
                </div>
            `;
        });
        list.innerHTML = html;
    });

    // Open options page
    document.getElementById('openOptions').addEventListener('click', (e) => {
        e.preventDefault();
        chrome.runtime.openOptionsPage();
    });
});

function timeAgo(date) {
    const seconds = Math.floor((new Date() - date) / 1000);
    if (seconds < 60) return 'just now';
    if (seconds < 3600) return Math.floor(seconds / 60) + 'm ago';
    if (seconds < 86400) return Math.floor(seconds / 3600) + 'h ago';
    return Math.floor(seconds / 86400) + 'd ago';
}

// BUG FIX: prevent XSS from stored filenames/domain names in history
function escapeHtml(str) {
    const div = document.createElement('div');
    div.appendChild(document.createTextNode(str));
    return div.innerHTML;
}
