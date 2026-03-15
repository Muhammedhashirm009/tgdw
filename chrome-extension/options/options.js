document.addEventListener('DOMContentLoaded', () => {
    // Load saved settings
    chrome.storage.sync.get(['dashboardUrl', 'apiToken', 'interceptMode'], (settings) => {
        document.getElementById('dashboardUrl').value = settings.dashboardUrl || '';
        document.getElementById('apiToken').value = settings.apiToken || '';
        document.getElementById('interceptMode').value = settings.interceptMode || 'extensions';
    });

    // Save settings
    document.getElementById('saveBtn').addEventListener('click', () => {
        const dashboardUrl = document.getElementById('dashboardUrl').value.trim().replace(/\/+$/, '');
        const apiToken = document.getElementById('apiToken').value.trim();
        const interceptMode = document.getElementById('interceptMode').value;

        if (!dashboardUrl) {
            showStatus('Please enter the Dashboard URL.', 'error');
            return;
        }

        // BUG FIX: validate URL format before saving
        try {
            const parsed = new URL(dashboardUrl);
            if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
                showStatus('Dashboard URL must start with http:// or https://', 'error');
                return;
            }
        } catch {
            showStatus('Please enter a valid Dashboard URL (e.g. https://your-dashboard.com)', 'error');
            return;
        }

        if (!apiToken) {
            showStatus('Please enter your API Token.', 'error');
            return;
        }

        // BUG FIX: validate token format prefix so users don't paste the wrong thing
        if (!apiToken.startsWith('gdbridge_')) {
            showStatus('⚠️ Token should start with "gdbridge_". Make sure you copied it correctly.', 'error');
            return;
        }

        chrome.storage.sync.set({
            dashboardUrl,
            apiToken,
            interceptMode,
            enabled: true  // BUG FIX: explicitly set to boolean true, not omitted
        }, () => {
            showStatus('✅ Settings saved successfully!', 'success');
        });
    });

    // Test connection
    // BUG FIX: Use a dedicated /api/bridge/ping endpoint that does NOT create tasks
    // If that endpoint is not available, fall back to a read-only /api/token/status check
    document.getElementById('testBtn').addEventListener('click', async () => {
        const dashboardUrl = document.getElementById('dashboardUrl').value.trim().replace(/\/+$/, '');
        const apiToken = document.getElementById('apiToken').value.trim();
        const resultEl = document.getElementById('testResult');

        if (!dashboardUrl || !apiToken) {
            showStatus('Please fill in both fields first.', 'error');
            return;
        }

        showStatus('Testing connection...', 'success');
        resultEl.style.display = 'block';
        resultEl.textContent = '⏳ Connecting...';

        try {
            // BUG FIX: Use /api/bridge/send-link with a special test flag, OR use ping endpoint
            // We send a flagged request that the server can recognise as a test
            // without creating a real task. The server should handle "test": true.
            // If the server doesn't support it yet, it may still create a task —
            // see server-side fix in dashboard/server.go.
            const response = await fetch(`${dashboardUrl}/api/bridge/send-link`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'Authorization': `Bearer ${apiToken}`
                },
                body: JSON.stringify({
                    url: 'https://example.com/test-connection.txt',
                    source_site: 'extension-test',
                    filename: 'connection_test.txt',
                    file_size: '0 B',
                    test: true   // BUG FIX: flag this as a test so the server skips DB task creation
                })
            });

            if (response.status === 401) {
                resultEl.innerHTML = '❌ <strong>Authentication failed.</strong><br>Your token is invalid or expired. Generate a new one from the Dashboard.';
                resultEl.style.color = '#ef4444';
                showStatus('Authentication failed.', 'error');
                return;
            }

            if (response.status === 429) {
                resultEl.innerHTML = '⚠️ <strong>Rate limited.</strong><br>Too many requests. Wait a minute and try again.';
                resultEl.style.color = '#f59e0b';
                showStatus('Rate limited.', 'error');
                return;
            }

            if (!response.ok) {
                const text = await response.text().catch(() => '');
                resultEl.innerHTML = `❌ <strong>Server error ${response.status}.</strong><br><small>${text.slice(0, 150)}</small>`;
                resultEl.style.color = '#ef4444';
                showStatus('Connection error.', 'error');
                return;
            }

            const data = await response.json();

            if (data.success) {
                resultEl.innerHTML = `✅ <strong>Connection successful!</strong><br>Your extension is configured correctly and ready to intercept downloads.`;
                resultEl.style.color = '#10b981';
                showStatus('✅ Connection verified!', 'success');
            } else {
                resultEl.innerHTML = `❌ <strong>Error:</strong> ${data.error || 'Unknown error'}`;
                resultEl.style.color = '#ef4444';
                showStatus('Connection error.', 'error');
            }
        } catch (err) {
            resultEl.innerHTML = `❌ <strong>Cannot reach dashboard.</strong><br>Check the URL and make sure the dashboard is running.<br><small>${err.message}</small>`;
            resultEl.style.color = '#ef4444';
            showStatus('Connection failed.', 'error');
        }
    });
});

function showStatus(msg, type) {
    const el = document.getElementById('statusMsg');
    el.textContent = msg;
    el.className = 'status-msg ' + (type === 'success' ? 'status-success' : 'status-error');
    setTimeout(() => { el.textContent = ''; }, 5000);
}
