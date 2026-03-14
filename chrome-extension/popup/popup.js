document.addEventListener('DOMContentLoaded', () => {
    // Check status
    chrome.runtime.sendMessage({ type: 'getStatus' }, (response) => {
        const dot = document.getElementById('statusDot');
        const text = document.getElementById('statusText');

        if (response.configured) {
            dot.classList.add(response.enabled ? 'connected' : 'paused');
            text.textContent = response.enabled ? 'Connected & Active' : 'Paused';
        } else {
            dot.classList.add('disconnected');
            text.textContent = 'Not configured — open Settings';
        }

        document.getElementById('enableToggle').checked = response.enabled;
    });

    // Toggle enable/disable
    document.getElementById('enableToggle').addEventListener('change', () => {
        chrome.runtime.sendMessage({ type: 'toggleEnabled' }, (response) => {
            const dot = document.getElementById('statusDot');
            const text = document.getElementById('statusText');
            dot.className = 'status-indicator ' + (response.enabled ? 'connected' : 'paused');
            text.textContent = response.enabled ? 'Connected & Active' : 'Paused';
        });
    });

    // Load history
    chrome.runtime.sendMessage({ type: 'getHistory' }, (response) => {
        const list = document.getElementById('historyList');
        if (!response.history || response.history.length === 0) {
            list.innerHTML = '<p class="empty">No links sent yet</p>';
            return;
        }

        let html = '';
        response.history.slice(0, 5).forEach(entry => {
            const ago = timeAgo(new Date(entry.time));
            html += `
                <div class="history-item">
                    <div class="history-name">${entry.filename || 'Unknown'}</div>
                    <div class="history-meta">${entry.source} · ${ago} · #${entry.taskId}</div>
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
