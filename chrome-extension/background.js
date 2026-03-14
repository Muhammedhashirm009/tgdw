// GDriveBridge — Background Service Worker
// Intercepts downloads and forwards them to the dashboard API

const INTERCEPT_EXTENSIONS = [
    '.zip', '.rar', '.iso', '.exe', '.7z', '.tar', '.gz', '.tar.gz',
    '.dmg', '.msi', '.deb', '.rpm', '.apk', '.xz', '.bz2',
    '.bin', '.img', '.torrent'
];

// Listen for new downloads
chrome.downloads.onCreated.addListener(async (downloadItem) => {
    const settings = await chrome.storage.sync.get(['dashboardUrl', 'apiToken', 'enabled', 'interceptMode']);

    // Check if extension is enabled
    if (settings.enabled === false) return;

    const url = downloadItem.url || '';
    const filename = downloadItem.filename || extractFilename(url);

    // Check if we should intercept this download
    if (!shouldIntercept(url, filename, settings.interceptMode)) return;

    // Send to dashboard
    try {
        const result = await sendToDashboard(settings.dashboardUrl, settings.apiToken, {
            url: url,
            source_site: extractDomain(downloadItem.referrer || url),
            filename: filename,
            file_size: downloadItem.totalBytes > 0 ? formatBytes(downloadItem.totalBytes) : 'Unknown'
        });

        if (result.success) {
            // Cancel the browser download since bot will handle it
            chrome.downloads.cancel(downloadItem.id);
            chrome.downloads.erase({ id: downloadItem.id });

            showNotification(
                '✅ Link Sent to Bot',
                `${filename}\nTask #${result.task_id} created`
            );

            // Store in recent history
            addToHistory({
                url, filename,
                source: extractDomain(downloadItem.referrer || url),
                taskId: result.task_id,
                time: new Date().toISOString()
            });
        } else {
            showNotification('❌ Failed to Send', result.error || 'Unknown error');
        }
    } catch (err) {
        console.error('GDriveBridge error:', err);
        showNotification('❌ Connection Error', 'Could not reach your dashboard. Check Extension Options.');
    }
});

function shouldIntercept(url, filename, mode) {
    if (!url || url.startsWith('blob:') || url.startsWith('data:')) return false;

    // In 'all' mode, intercept everything
    if (mode === 'all') return true;

    // Default: filter by file extension
    const lowerUrl = (url + filename).toLowerCase();
    return INTERCEPT_EXTENSIONS.some(ext => lowerUrl.includes(ext));
}

function extractFilename(url) {
    try {
        const pathname = new URL(url).pathname;
        const parts = pathname.split('/');
        return decodeURIComponent(parts[parts.length - 1]) || 'unknown_file';
    } catch {
        return 'unknown_file';
    }
}

function extractDomain(url) {
    try {
        return new URL(url).hostname;
    } catch {
        return '';
    }
}

function formatBytes(bytes) {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

async function sendToDashboard(dashboardUrl, token, data) {
    if (!dashboardUrl || !token) {
        throw new Error('Dashboard URL and token not configured');
    }

    // Ensure URL doesn't have trailing slash
    const baseUrl = dashboardUrl.replace(/\/+$/, '');

    const response = await fetch(`${baseUrl}/api/bridge/send-link`, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify(data)
    });

    return await response.json();
}

function showNotification(title, message) {
    chrome.notifications.create({
        type: 'basic',
        iconUrl: 'icons/icon128.png',
        title: title,
        message: message,
        priority: 2
    });
}

async function addToHistory(entry) {
    const { history = [] } = await chrome.storage.local.get('history');
    history.unshift(entry);
    // Keep only last 20 entries
    await chrome.storage.local.set({ history: history.slice(0, 20) });
}

// Handle messages from popup
chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
    if (msg.type === 'getStatus') {
        chrome.storage.sync.get(['dashboardUrl', 'apiToken', 'enabled'], (settings) => {
            sendResponse({
                configured: !!(settings.dashboardUrl && settings.apiToken),
                enabled: settings.enabled !== false
            });
        });
        return true; // async response
    }

    if (msg.type === 'toggleEnabled') {
        chrome.storage.sync.get(['enabled'], (settings) => {
            const newState = !(settings.enabled !== false);
            chrome.storage.sync.set({ enabled: newState });
            sendResponse({ enabled: newState });
        });
        return true;
    }

    if (msg.type === 'getHistory') {
        chrome.storage.local.get('history', (data) => {
            sendResponse({ history: data.history || [] });
        });
        return true;
    }
});
