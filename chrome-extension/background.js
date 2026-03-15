// GDriveBridge — Background Service Worker
// Intercepts downloads, pauses them, and asks the user: Bridge or Download Normally?

const INTERCEPT_EXTENSIONS = [
    '.zip', '.rar', '.iso', '.exe', '.7z', '.tar', '.gz', '.tar.gz',
    '.dmg', '.msi', '.deb', '.rpm', '.apk', '.xz', '.bz2',
    '.bin', '.img', '.torrent', '.mp4', '.mkv', '.avi', '.mov',
    '.pdf', '.epub', '.mp3', '.flac'
];

// In-memory store for pending download decisions: notificationId → downloadItem
const pendingDownloads = new Map();

// ===== Download Interception =====

chrome.downloads.onCreated.addListener(async (downloadItem) => {
    const settings = await chrome.storage.sync.get(['dashboardUrl', 'apiToken', 'enabled', 'interceptMode']);

    // Check if extension is enabled (BUG FIX: explicit boolean check)
    if (!settings.enabled) return;

    // Don't intercept if not configured — no point pausing downloads we can't send
    if (!settings.dashboardUrl || !settings.apiToken) return;

    const url = downloadItem.url || '';
    // BUG FIX: extractFilename strips query params
    const filename = downloadItem.filename || extractFilename(url);

    if (!shouldIntercept(url, filename, settings.interceptMode)) return;

    // ── PAUSE the browser download first before asking the user ──────────────
    try {
        await chrome.downloads.pause(downloadItem.id);
    } catch (e) {
        // Download may have already completed in the split second — just skip it
        console.warn('GDriveBridge: could not pause download, skipping intercept:', e);
        return;
    }

    // Show a notification with two action buttons
    const notifId = `gdbridge_${downloadItem.id}_${Date.now()}`;
    const fileLabel = filename.length > 40 ? filename.slice(0, 38) + '…' : filename;
    const sizeLabel = downloadItem.totalBytes > 0 ? ` (${formatBytes(downloadItem.totalBytes)})` : '';

    chrome.notifications.create(notifId, {
        type: 'basic',
        iconUrl: 'icons/icon128.png',
        title: '🔌 GDriveBridge — How to handle this download?',
        message: `${fileLabel}${sizeLabel}\n\nChoose how to send this to your bot, or dismiss to let Chrome download it normally.`,
        priority: 2,
        requireInteraction: true,
        buttons: [
            { title: '⚡ aria2c  (Fast, 16x connections)' },
            { title: '🌊 Stream  (Direct to Drive, no disk)' }
        ]
    });

    // Store the full context so the button handler can act on it
    pendingDownloads.set(notifId, {
        downloadItem,
        url,
        filename,
        settings
    });
});

// ===== Notification Button Clicks =====

chrome.notifications.onButtonClicked.addListener(async (notifId, buttonIndex) => {
    if (!pendingDownloads.has(notifId)) return;

    const { downloadItem, url, filename, settings } = pendingDownloads.get(notifId);
    pendingDownloads.delete(notifId);
    chrome.notifications.clear(notifId);

    if (buttonIndex === 0 || buttonIndex === 1) {
        // ── "⚡ aria2c" (0) or "🌊 Stream" (1) ─────────────────────────────
        const bridgeMode = buttonIndex === 0 ? 'aria2c' : 'stream';
        const modeLabel  = buttonIndex === 0 ? '⚡ aria2c' : '🌊 Streaming';

        // Cancel the browser download and forward to the dashboard
        try {
            await chrome.downloads.cancel(downloadItem.id);
            await chrome.downloads.erase({ id: downloadItem.id });
        } catch (e) {
            console.warn('GDriveBridge: cancel after bridge choice failed:', e);
        }

        try {
            const result = await sendToDashboard(settings.dashboardUrl, settings.apiToken, {
                url: url,
                source_site: extractDomain(downloadItem.referrer || url),
                filename: filename,
                file_size: downloadItem.totalBytes > 0 ? formatBytes(downloadItem.totalBytes) : 'Unknown',
                file_size_bytes: downloadItem.totalBytes > 0 ? downloadItem.totalBytes : 0,
                bridge_mode: bridgeMode
            });

            if (result.success) {
                showNotification(
                    `✅ Bridged — ${modeLabel}`,
                    `${filename}\nTask #${result.task_id} queued.`
                );
                addToHistory({
                    url, filename,
                    source: extractDomain(downloadItem.referrer || url),
                    taskId: result.task_id,
                    time: new Date().toISOString()
                });
            } else {
                // Bridge failed — resume the browser download as fallback
                showNotification('❌ Bridge Failed', (result.error || 'Unknown error') + '\nResuming normal download…');
                chrome.downloads.resume(downloadItem.id).catch(() => {
                    chrome.downloads.download({ url });
                });
            }
        } catch (err) {
            console.error('GDriveBridge sendToDashboard error:', err);
            showNotification('❌ Connection Error', 'Could not reach your dashboard.\nResuming normal download…');
            chrome.downloads.resume(downloadItem.id).catch(() => {
                chrome.downloads.download({ url });
            });
        }

    }
});

// ===== Notification Dismissed Without Button Click =====
// If the user closes the notification without choosing, resume the download

chrome.notifications.onClosed.addListener((notifId, byUser) => {
    if (!pendingDownloads.has(notifId)) return;

    const { downloadItem, url } = pendingDownloads.get(notifId);
    pendingDownloads.delete(notifId);

    // Resume the paused download since no choice was made
    chrome.downloads.resume(downloadItem.id).catch(() => {
        chrome.downloads.download({ url });
    });
});

// ===== Detection Logic =====

// BUG FIX: check URL path and filename SEPARATELY using endsWith
// Old: (url + filename) caused false positives like "refzip" matching ".zip"
function shouldIntercept(url, filename, mode) {
    if (!url || url.startsWith('blob:') || url.startsWith('data:') || url.startsWith('chrome-extension:')) return false;

    if (mode === 'all') return true;

    const lowerFilename = filename.toLowerCase();
    let lowerPath = '';
    try {
        lowerPath = new URL(url).pathname.toLowerCase();
    } catch {
        lowerPath = url.toLowerCase();
    }

    return INTERCEPT_EXTENSIONS.some(ext =>
        lowerFilename.endsWith(ext) || lowerPath.endsWith(ext)
    );
}

// BUG FIX: strips query strings and fragments — old version returned "file.zip?token=abc"
function extractFilename(url) {
    try {
        const pathname = new URL(url).pathname;
        const parts = pathname.split('/');
        return decodeURIComponent(parts[parts.length - 1]) || 'unknown_file';
    } catch {
        const parts = url.split('/');
        const segment = parts[parts.length - 1].split('?')[0].split('#')[0];
        return segment || 'unknown_file';
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

// BUG FIX: checks HTTP status before parsing JSON — old version treated 401/500 as success
async function sendToDashboard(dashboardUrl, token, data) {
    if (!dashboardUrl || !token) {
        throw new Error('Dashboard URL and token not configured');
    }

    const baseUrl = dashboardUrl.replace(/\/+$/, '');

    const response = await fetch(`${baseUrl}/api/bridge/send-link`, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify(data)
    });

    if (response.status === 401) {
        return { success: false, error: 'Invalid API token. Please regenerate from Dashboard → Extension Settings.' };
    }
    if (response.status === 429) {
        return { success: false, error: 'Rate limit exceeded. Try again in a moment.' };
    }
    if (!response.ok) {
        const text = await response.text().catch(() => '');
        return { success: false, error: `Server error ${response.status}: ${text.slice(0, 100)}` };
    }

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
    await chrome.storage.local.set({ history: history.slice(0, 20) });
}

// ===== Popup Message Handlers =====

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
    if (msg.type === 'getStatus') {
        chrome.storage.sync.get(['dashboardUrl', 'apiToken', 'enabled'], (settings) => {
            sendResponse({
                configured: !!(settings.dashboardUrl && settings.apiToken),
                enabled: settings.enabled === true,
                pendingCount: pendingDownloads.size
            });
        });
        return true;
    }

    if (msg.type === 'toggleEnabled') {
        chrome.storage.sync.get(['enabled'], (settings) => {
            const newState = !(settings.enabled === true);
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
