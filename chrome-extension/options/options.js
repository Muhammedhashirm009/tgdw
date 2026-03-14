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

        if (!dashboardUrl || !apiToken) {
            showStatus('Please fill in both Dashboard URL and API Token.', 'error');
            return;
        }

        chrome.storage.sync.set({
            dashboardUrl,
            apiToken,
            interceptMode,
            enabled: true
        }, () => {
            showStatus('✅ Settings saved successfully!', 'success');
        });
    });

    // Test connection
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
                    file_size: '0 B'
                })
            });

            const data = await response.json();

            if (response.ok && data.success) {
                resultEl.innerHTML = `✅ <strong>Connection successful!</strong><br>Task #${data.task_id} was created. Your extension is ready.`;
                resultEl.style.color = '#10b981';
                showStatus('✅ Connection verified!', 'success');
            } else if (response.status === 401) {
                resultEl.innerHTML = '❌ <strong>Authentication failed.</strong> Your token is invalid or expired. Generate a new one from the Dashboard.';
                resultEl.style.color = '#ef4444';
                showStatus('Authentication failed.', 'error');
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
