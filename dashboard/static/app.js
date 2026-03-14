document.addEventListener('DOMContentLoaded', () => {
    // Mobile sidebar toggle
    const hamburger = document.getElementById('hamburgerBtn');
    const sidebar = document.getElementById('sidebar');
    const overlay = document.getElementById('sidebarOverlay');

    function toggleSidebar() {
        sidebar.classList.toggle('open');
        overlay.classList.toggle('active');
        hamburger.classList.toggle('active');
    }

    function closeSidebar() {
        sidebar.classList.remove('open');
        overlay.classList.remove('active');
        hamburger.classList.remove('active');
    }

    hamburger.addEventListener('click', toggleSidebar);
    overlay.addEventListener('click', closeSidebar);

    // Navigation routing
    const links = document.querySelectorAll('.menu a');
    const views = document.querySelectorAll('.view');

    links.forEach(link => {
        link.addEventListener('click', (e) => {
            e.preventDefault();

            // Remove active class
            links.forEach(l => l.classList.remove('active'));
            views.forEach(v => v.classList.remove('active'));

            // Add active class to clicked link
            link.classList.add('active');

            // Show target view
            const targetViewId = link.getAttribute('data-view') + '-view';
            const targetView = document.getElementById(targetViewId);
            if (targetView) targetView.classList.add('active');

            // Close sidebar on mobile after navigation
            closeSidebar();
        });
    });

    // Logout handling
    const logoutBtn = document.getElementById('logoutBtn');
    if (logoutBtn) {
        logoutBtn.addEventListener('click', () => {
            fetch('/api/logout', { method: 'POST' })
                .then(() => {
                    window.location.href = 'login.html';
                })
                .catch(err => console.error("Logout failed", err));
        });
    }

    // Mock fetching data
    function fetchStatus() {
        console.log("Fetching status from /api/status ...");
        fetch('/api/status')
            .then(res => {
                if (res.status === 401) window.location.href = 'login.html';
                return res.json();
            })
            .then(data => {
                document.getElementById('active-downloads').textContent = data.active_downloads || 0;
                document.getElementById('active-uploads').textContent = data.active_uploads || 0;

                const statusEl = document.getElementById('system-status');
                statusEl.textContent = data.status || "Unknown";
                statusEl.className = data.status === 'ok' ? 'status-ok' : 'status-danger';
            })
            .catch(err => {
                console.error("Error fetching status:", err)
                const statusEl = document.getElementById('system-status');
                statusEl.textContent = "Offline";
                statusEl.className = 'status-danger';
            });
    }

    function renderTaskStatusBadge(status) {
        let className = "status-badge ";
        switch (status) {
            case "Completed": className += "status-completed"; break;
            case "Failed": className += "status-failed"; break;
            case "Pending": className += "status-pending"; break;
            case "Downloading": className += "status-downloading"; break;
            case "Uploading": className += "status-uploading"; break;
            default: className += "status-default"; break;
        }
        return `<span class="${className}">${status}</span>`;
    }

    function formatBytes(bytes, decimals = 2) {
        if (!+bytes) return '0 Bytes';
        const k = 1024, dm = decimals < 0 ? 0 : decimals;
        const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return `${parseFloat((bytes / Math.pow(k, i)).toFixed(dm))} ${sizes[i]}`;
    }

    function fetchTasks() {
        console.log("Fetching tasks from /api/tasks ...");
        fetch('/api/tasks')
            .then(res => {
                if (res.status === 401) window.location.href = 'login.html';
                return res.json();
            })
            .then(data => {
                const overviewBody = document.getElementById('tasks-table-body');
                const allTasksBody = document.querySelector('#tasks-view tbody');

                if (!data || data.length === 0) {
                    overviewBody.innerHTML = `<tr><td colspan="4" class="empty-state">No current tasks.</td></tr>`;
                    allTasksBody.innerHTML = `<tr><td colspan="5" class="empty-state">No tasks to display.</td></tr>`;
                    return;
                }

                let overviewHTML = '';
                let allTasksHTML = '';

                data.forEach((t, i) => {
                    // Combine progress into one bar based on status
                    let progress = 0;
                    let speed = 0;
                    if (t.status === 'Downloading') {
                        progress = t.download_progress;
                        speed = t.download_speed;
                    }
                    if (t.status === 'Uploading') {
                        progress = t.upload_progress;
                        speed = t.upload_speed;
                    }
                    if (t.status === 'Completed') progress = 100;

                    let speedText = speed > 0 ? formatBytes(speed) + '/s' : '-';
                    let etaText = '-';

                    if (speed > 0 && progress < 100) {
                        let bytesRemaining = (t.file_size * (100 - progress)) / 100;
                        let secondsRemaining = Math.round(bytesRemaining / speed);

                        if (secondsRemaining < 60) {
                            etaText = secondsRemaining + 's';
                        } else if (secondsRemaining < 3600) {
                            etaText = Math.floor(secondsRemaining / 60) + 'm ' + (secondsRemaining % 60) + 's';
                        } else {
                            etaText = Math.floor(secondsRemaining / 3600) + 'h ' + Math.floor((secondsRemaining % 3600) / 60) + 'm';
                        }
                    }

                    const pBar = `
                        <div class="progress-bar">
                            <div class="progress-fill" style="width: ${progress}%"></div>
                            <span class="progress-text">${progress}%</span>
                        </div>
                        <div style="font-size: 0.70rem; color: var(--text-secondary); margin-top: 4px; display: flex; justify-content: space-between;">
                            <span>Speed: ${speedText}</span>
                            <span>ETA: ${etaText}</span>
                        </div>
                    `;

                    // Only show first 5 on overview
                    if (i < 5) {
                        overviewHTML += `
                        <tr>
                            <td>
                                <div style="display: flex; align-items: center; gap: 12px;">
                                    <span style="font-size: 1.5rem;">📄</span>
                                    <div>
                                        <div style="font-weight: 500;">${t.file_name}</div>
                                        <div style="font-size: 0.75rem; color: var(--text-secondary);">${formatBytes(t.file_size)}</div>
                                    </div>
                                </div>
                            </td>
                            <td>${t.input_type}</td>
                            <td>${pBar}</td>
                            <td>${renderTaskStatusBadge(t.status)}</td>
                        </tr>
                        `;
                    }

                    // Show all in the all-tasks view
                    let linkHtml = t.drive_link ? `<a href="${t.drive_link}" target="_blank" class="btn btn-primary" style="padding: 6px 12px; font-size: 0.75rem;">Drive Link</a>` : '-';
                    
                    let actionsHtml = "";
                    if (t.status === 'Downloading' || t.status === 'Uploading') {
                        actionsHtml += `<button class="btn btn-danger" style="padding: 4px 8px; font-size: 0.75rem;" onclick="cancelTask(${t.id})">Cancel</button>`;
                    } else if (t.status === 'Completed' || t.status === 'Cancelled') {
                        actionsHtml += `<span style="font-size: 0.75rem; color: var(--text-secondary);">${t.elapsed_time || ''}</span>`;
                    }

                    allTasksHTML += `
                        <tr>
                            <td>#${t.id}</td>
                            <td>
                                <div>${t.file_name}</div>
                                <div style="font-size: 0.75rem; color: var(--text-secondary);">${formatBytes(t.file_size)}</div>
                            </td>
                            <td>${t.input_type}</td>
                            <td>${pBar}</td>
                            <td>
                                <div style="display: flex; flex-direction: column; gap: 4px; align-items: flex-start;">
                                    ${renderTaskStatusBadge(t.status)}
                                    ${actionsHtml}
                                </div>
                            </td>
                            <td>${linkHtml}</td>
                        </tr>
                    `;
                });

                overviewBody.innerHTML = overviewHTML;
                allTasksBody.innerHTML = allTasksHTML;
            })
            .catch(console.error);
    }

    // Expose functionality to inline button onclicks
    window.cancelTask = function(taskId) {
        if (!confirm("Are you sure you want to cancel Task #" + taskId + "?")) return;
        
        fetch('/api/cancel', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: taskId })
        })
        .then(res => res.json())
        .then(data => {
            if (data.success) {
                alert("Task " + taskId + " cancelled successfully.");
                fetchTasks(); // refresh table immediately
                fetchStatus();
            } else {
                alert("Failed to cancel: " + (data.error || "Unknown error"));
            }
        })
        .catch(err => alert("Network error trying to cancel task."));
    }

    // Settings Logic
    const settingsBtn = document.getElementById('save-settings-btn');
    const settingsMsg = document.getElementById('settings-msg');

    function loadSettings() {
        fetch('/api/settings')
            .then(res => {
                if (res.status === 401) window.location.href = 'login.html';
                return res.json();
            })
            .then(data => {
                if (data.error) return;
                document.getElementById('botToken').value = data.bot_token || "";
                document.getElementById('googleClientId').value = data.google_client_id || "";
                document.getElementById('googleClientSecret').value = data.google_client_secret || "";
                document.getElementById('downloadDir').value = data.download_directory || "/data/downloads";
                document.getElementById('maxFileSize').value = data.max_file_size || 0;
                document.getElementById('concurrentTasks').value = data.concurrent_tasks || 3;
                document.getElementById('telegramApiEndpoint').value = data.telegram_api_endpoint || "http://localhost:8081";
                document.getElementById('telegramApiId').value = data.telegram_api_id || "";
                document.getElementById('telegramApiHash').value = data.telegram_api_hash || "";
                document.getElementById('retentionHours').value = data.retention_hours || 48;
                document.getElementById('adminTelegramIds').value = data.admin_telegram_ids || "";
                document.getElementById('maxFileSizeNormal').value = data.max_file_size_normal || 4294967296;

                if (data.bot_token) {
                    document.getElementById('botToken').placeholder = "Token is set (hidden)";
                }

                // Setup Google Drive Auth UI
                const authStatus = document.getElementById('googleAuthStatus');
                const authBtn = document.getElementById('googleLoginBtn');

                if (data.is_google_connected) {
                    authStatus.textContent = "Google Drive is Connected ✓";
                    authStatus.style.color = "var(--success-color)";
                    authBtn.textContent = "Reconnect Google Drive";
                } else if (!data.google_client_id) {
                    authStatus.textContent = "Please save Client ID & Secret first.";
                    authBtn.style.opacity = 0.5;
                    authBtn.style.pointerEvents = "none";
                } else {
                    authStatus.textContent = "Not connected. Ready to authorize.";
                    authBtn.style.opacity = 1;
                    authBtn.style.pointerEvents = "auto";
                }
            })
            .catch(console.error);
    }

    settingsBtn.addEventListener('click', () => {
        settingsBtn.disabled = true;
        settingsBtn.textContent = "Saving...";

        const payload = {
            id: 1, // hardcoded for single settings row
            bot_token: document.getElementById('botToken').value,
            google_client_id: document.getElementById('googleClientId').value,
            google_client_secret: document.getElementById('googleClientSecret').value,
            download_directory: document.getElementById('downloadDir').value,
            max_file_size: parseInt(document.getElementById('maxFileSize').value) || 0,
            concurrent_tasks: parseInt(document.getElementById('concurrentTasks').value) || 3,
            telegram_api_endpoint: document.getElementById('telegramApiEndpoint').value,
            telegram_api_id: document.getElementById('telegramApiId').value,
            telegram_api_hash: document.getElementById('telegramApiHash').value,
            retention_hours: parseInt(document.getElementById('retentionHours').value) || 48,
            admin_telegram_ids: document.getElementById('adminTelegramIds').value,
            max_file_size_normal: parseInt(document.getElementById('maxFileSizeNormal').value) || 4294967296
        };

        fetch('/api/settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
            .then(res => res.json())
            .then(data => {
                if (data.success) {
                    settingsMsg.style.color = "var(--success-color)";
                    settingsMsg.textContent = "Settings saved successfully! Changes will apply automatically.";
                } else {
                    settingsMsg.style.color = "var(--danger-color)";
                    settingsMsg.textContent = data.error || "Failed to save.";
                }
            })
            .catch(err => {
                settingsMsg.style.color = "var(--danger-color)";
                settingsMsg.textContent = "Network error.";
            })
            .finally(() => {
                settingsBtn.disabled = false;
                settingsBtn.textContent = "Save Settings";
                setTimeout(() => settingsMsg.textContent = "", 5000);
            });
    });

    // ===== Extension Tab Logic =====
    let currentRawToken = null; // only set right after generation

    function loadExtensionData() {
        fetch('/api/token/status')
            .then(res => {
                if (res.status === 401) window.location.href = 'login.html';
                return res.json();
            })
            .then(data => {
                const statusEl = document.getElementById('ext-token-status');
                const tokenDisplay = document.getElementById('ext-token-display');
                const tokenValue = document.getElementById('ext-token-value');
                const generateBtn = document.getElementById('ext-generate-btn');
                const revokeBtn = document.getElementById('ext-revoke-btn');
                const metaEl = document.getElementById('ext-token-meta');

                if (data.has_token) {
                    statusEl.textContent = '✅ Token is active.';
                    statusEl.style.color = 'var(--success-color)';
                    tokenDisplay.style.display = 'flex';
                    
                    // Show raw token if we just generated it, otherwise show prefix
                    if (currentRawToken) {
                        tokenValue.textContent = currentRawToken;
                    } else {
                        tokenValue.textContent = data.token_prefix;
                    }
                    
                    generateBtn.textContent = 'Regenerate Token';
                    revokeBtn.style.display = 'inline-block';

                    // Show meta info
                    metaEl.style.display = 'block';
                    document.getElementById('ext-created').textContent = 'Created: ' + new Date(data.created_at).toLocaleDateString();
                    document.getElementById('ext-last-used').textContent = data.last_used ? 'Last used: ' + new Date(data.last_used).toLocaleString() : 'Never used';
                } else {
                    statusEl.textContent = '⚠️ No token generated yet.';
                    statusEl.style.color = 'var(--text-secondary)';
                    tokenDisplay.style.display = 'none';
                    generateBtn.textContent = 'Generate Token';
                    revokeBtn.style.display = 'none';
                    metaEl.style.display = 'none';
                    currentRawToken = null;
                }

                // Render bridge logs
                const logsBody = document.getElementById('ext-logs-body');
                if (data.logs && data.logs.length > 0) {
                    let html = '';
                    data.logs.forEach(log => {
                        const time = new Date(log.created_at).toLocaleString();
                        const statusBadge = log.status === 'sent' 
                            ? '<span class="status-badge status-completed">Sent</span>'
                            : '<span class="status-badge status-failed">Failed</span>';
                        html += `
                            <tr>
                                <td style="font-size: 0.8rem;">${time}</td>
                                <td><code style="font-size: 0.8rem;">${log.filename || 'Unknown'}</code></td>
                                <td style="font-size: 0.8rem;">${log.source_site || '-'}</td>
                                <td>${statusBadge}</td>
                            </tr>
                        `;
                    });
                    logsBody.innerHTML = html;
                } else {
                    logsBody.innerHTML = '<tr><td colspan="4" class="empty-state">No bridge activity yet.</td></tr>';
                }
            })
            .catch(console.error);
    }

    // Generate token
    document.getElementById('ext-generate-btn').addEventListener('click', () => {
        const btn = document.getElementById('ext-generate-btn');
        const isRegenerate = btn.textContent.includes('Regenerate');
        
        if (isRegenerate && !confirm('This will revoke your current token. Any connected extensions will need the new token. Continue?')) {
            return;
        }

        btn.disabled = true;
        btn.textContent = 'Generating...';

        fetch('/api/token/generate', { method: 'POST' })
            .then(res => res.json())
            .then(data => {
                if (data.success) {
                    currentRawToken = data.token;
                    loadExtensionData();
                    alert('⚠️ Copy your token now! It will not be shown again.\n\n' + data.token);
                } else {
                    alert('Error: ' + (data.error || 'Failed to generate token'));
                }
            })
            .catch(err => alert('Network error generating token.'))
            .finally(() => {
                btn.disabled = false;
            });
    });

    // Revoke token
    document.getElementById('ext-revoke-btn').addEventListener('click', () => {
        if (!confirm('Revoke your token? This will disconnect any extensions using it.')) return;

        fetch('/api/token/revoke', { method: 'DELETE' })
            .then(res => res.json())
            .then(data => {
                if (data.success) {
                    currentRawToken = null;
                    loadExtensionData();
                } else {
                    alert('Error: ' + (data.error || 'Failed to revoke token'));
                }
            })
            .catch(err => alert('Network error revoking token.'));
    });

    // Copy token
    document.getElementById('ext-copy-token').addEventListener('click', () => {
        const tokenText = document.getElementById('ext-token-value').textContent;
        navigator.clipboard.writeText(tokenText).then(() => {
            const btn = document.getElementById('ext-copy-token');
            btn.textContent = '✅ Copied!';
            setTimeout(() => { btn.textContent = '📋 Copy'; }, 2000);
        }).catch(() => {
            // Fallback for non-HTTPS contexts
            const textarea = document.createElement('textarea');
            textarea.value = tokenText;
            document.body.appendChild(textarea);
            textarea.select();
            document.execCommand('copy');
            document.body.removeChild(textarea);
            const btn = document.getElementById('ext-copy-token');
            btn.textContent = '✅ Copied!';
            setTimeout(() => { btn.textContent = '📋 Copy'; }, 2000);
        });
    });

    // Refresh data every 5 seconds
    setInterval(() => {
        fetchStatus();
        fetchTasks();
    }, 5000);

    // Refresh extension data every 10 seconds
    setInterval(loadExtensionData, 10000);

    fetchStatus();
    fetchTasks();
    loadSettings();
    loadExtensionData();
});
