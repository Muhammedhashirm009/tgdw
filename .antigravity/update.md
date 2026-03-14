# UPDATE PLAN: Chrome Extension — Download Interceptor Bridge

> **Feature Codename:** `GDriveBridge Extension`
> **Status:** Planning Phase
> **Target Integration:** Existing Telegram Bot (Golang) + Web Dashboard

---

## 📌 Overview

Build a Chrome Extension that intercepts file downloads on sites like **GetIntoPC**, **FitGirl Repacks**, **OceanOfGames**, etc. — where direct download links are obfuscated behind multiple redirects or buttons — and forwards the resolved direct link to the user's Telegram bot automatically via a secure **token-based bridge**.

---

## 🏗️ Architecture

```
[User visits site e.g. getintopc.com]
        |
        v
[Chrome Extension detects download trigger]
        |
        v
[Extension intercepts the direct/final URL]
        |
        v
[Extension sends URL → Dashboard API (with Token)]
        |
        v
[Dashboard API validates token → passes to Bot]
        |
        v
[Telegram Bot receives link → starts download to GDrive]
        |
        v
[User gets Telegram notification: "Download started!"]
```

---

## 🔐 Token System

### How It Works

1. User logs into the **Web Dashboard**
2. Dashboard generates a unique **API Token** per user (UUID v4 or JWT)
3. User copies the token + dashboard URL into the **Extension Settings**
4. Every request from the extension is authenticated via this token
5. Token can be **regenerated** or **revoked** from the dashboard

### Token Structure

```
TOKEN FORMAT: gdbridge_<userId>_<randomHex32>
EXAMPLE:      gdbridge_42_a3f9c1d8e2b047f6a9c3d5e7f1b2a4c8
```

### Token Storage

| Location | What is stored |
|---|---|
| Dashboard DB | Hashed token + userId + createdAt + lastUsed |
| Chrome Extension | Raw token + dashboard URL (in `chrome.storage.sync`) |
| Bot (Golang) | No storage needed — dashboard validates and relays |

---

## 🧩 Components to Build

### 1. Chrome Extension

**Files:**
```
extension/
├── manifest.json          # Extension config (Manifest V3)
├── background.js          # Service worker — intercepts downloads
├── content.js             # (Optional) DOM scraping for tricky sites
├── popup/
│   ├── popup.html         # Extension popup UI
│   ├── popup.js           # Popup logic
│   └── popup.css          # Styling
├── options/
│   ├── options.html       # Settings page
│   └── options.js         # Save token + dashboard URL
└── icons/
    ├── icon16.png
    ├── icon48.png
    └── icon128.png
```

**Core Functionality:**

- **Intercept downloads** using `chrome.webRequest.onBeforeRequest` or `chrome.downloads.onCreated`
- **Filter URLs** — only intercept on whitelisted sites or specific file extensions (`.zip`, `.rar`, `.iso`, `.exe`, `.7z`, etc.)
- **Send to dashboard** via `fetch()` POST with token in `Authorization` header
- **Show notification** via `chrome.notifications` confirming the link was sent to the bot

**manifest.json (V3):**
```json
{
  "manifest_version": 3,
  "name": "GDriveBridge",
  "version": "1.0.0",
  "description": "Send download links directly to your Telegram bot",
  "permissions": [
    "downloads",
    "webRequest",
    "notifications",
    "storage"
  ],
  "host_permissions": [
    "<all_urls>"
  ],
  "background": {
    "service_worker": "background.js"
  },
  "action": {
    "default_popup": "popup/popup.html",
    "default_icon": "icons/icon48.png"
  },
  "options_page": "options/options.html"
}
```

---

### 2. Dashboard API (New Endpoints)

Add these endpoints to your existing dashboard backend:

| Method | Endpoint | Description |
|---|---|---|
| `POST` | `/api/token/generate` | Generate a new token for logged-in user |
| `DELETE` | `/api/token/revoke` | Revoke current token |
| `GET` | `/api/token/status` | Check token validity |
| `POST` | `/api/bridge/send-link` | **Receive link from extension** |

**`POST /api/bridge/send-link` — Request:**
```json
{
  "url": "https://example.com/file.zip",
  "source_site": "getintopc.com",
  "filename": "Windows11.zip",
  "file_size": "5.2 GB"
}
```
**Headers:**
```
Authorization: Bearer gdbridge_42_a3f9c1d8e2b047f6a9c3d5e7f1b2a4c8
Content-Type: application/json
```

**`POST /api/bridge/send-link` — Response:**
```json
{
  "success": true,
  "message": "Link sent to Telegram bot",
  "job_id": "job_abc123",
  "telegram_chat_id": "123456789"
}
```

---

### 3. Dashboard UI (New Section)

Add a **"Extension Setup"** page in your web dashboard:

**Sections:**
- Token display (masked, with copy button)
- "Regenerate Token" button (with confirmation dialog)
- Extension setup instructions (step-by-step with screenshots)
- "Download Extension" link or link to Chrome Web Store
- Activity log: recent links sent via extension

---

### 4. Golang Bot (Minor Update)

Add a new handler for when the dashboard relays a link from the extension:

```go
// New function in bot
func handleBridgeLink(chatID int64, url string, sourceSite string) {
    // Same as existing direct link download handler
    // Send confirmation message to user
    // Start download job
}
```

The dashboard calls the bot's internal API (or directly invokes the download function) when it receives a valid bridge request.

---

## 🌐 Supported Sites (Phase 1)

The extension will work on **all sites** but will be optimized/tested for:

| Site | Intercept Method |
|---|---|
| getintopc.com | `webRequest` on `.zip`/`.iso` final redirect |
| fitgirl-repacks.site | `downloads.onCreated` listener |
| oceanofgames.com | `webRequest` download URL pattern |
| filecr.com | `webRequest` + redirect chain following |
| direct links (any) | `downloads.onCreated` — catches all |

---

## 🔄 User Flow (Step by Step)

```
1. User installs Chrome Extension
2. User opens Extension Options page
3. User opens their Dashboard → navigates to "Extension Setup"
4. User copies their Token and Dashboard URL
5. User pastes both into Extension Options → clicks Save
6. Extension shows green "Connected ✓" status

-- During browsing --

7. User visits getintopc.com
8. User clicks the download button on the site
9. Extension detects the final download URL
10. Extension POSTs the URL to Dashboard with token
11. Dashboard validates token → forwards to Golang bot
12. Bot sends Telegram message: 
    "🔗 Link captured from getintopc.com
     📁 File: Windows11.iso (5.4 GB)
     ▶️ Starting download to GDrive..."
13. Extension shows Chrome notification:
    "✅ Link sent to your bot!"
```

---

## 🛡️ Security Considerations

| Concern | Mitigation |
|---|---|
| Token theft | Store token in `chrome.storage.sync` (encrypted by Chrome), never in localStorage |
| MITM attacks | Dashboard API must use HTTPS only |
| Token brute force | Rate limit `/api/bridge/send-link` to 30 req/min per token |
| Malicious URLs | Validate URL format on dashboard before forwarding to bot |
| Token exposure in logs | Store only hashed tokens in DB; never log raw tokens |
| CORS | Dashboard API must whitelist extension's origin or use `chrome.runtime.id` header |

---

## 📋 Implementation Phases

### Phase 1 — Core Bridge (MVP)
- [ ] Token generation + storage in Dashboard DB
- [ ] `POST /api/bridge/send-link` endpoint
- [ ] Dashboard "Extension Setup" UI page
- [ ] Chrome Extension: options page (token + URL input)
- [ ] Chrome Extension: `downloads.onCreated` listener → POST to dashboard
- [ ] Chrome Extension: Chrome notification on success/failure
- [ ] Golang bot: handle incoming bridge links

### Phase 2 — Smart Interception
- [ ] `webRequest` listener for intercepting before download starts (cancel browser download, send to bot instead)
- [ ] File extension filter settings in extension options
- [ ] Site whitelist/blacklist in extension options
- [ ] Filename + file size detection before sending

### Phase 3 — Polish
- [ ] Extension popup: show recent sent links
- [ ] Dashboard activity log for bridge requests
- [ ] Token expiry + auto-renewal
- [ ] Token revocation from Telegram bot command (`/revoketoken`)
- [ ] Multi-account support (one extension, multiple bots)
- [ ] Firefox extension port (Manifest V2 compatible)

---

## 📁 Suggested File Structure in Your Repo

```
your-repo/
├── bot/                    # Existing Golang bot
│   └── handlers/
│       └── bridge.go       # NEW: handle bridge link relay
├── dashboard/              # Existing web dashboard
│   ├── api/
│   │   ├── token.go        # NEW: token generate/revoke/status
│   │   └── bridge.go       # NEW: receive link from extension
│   └── views/
│       └── extension.html  # NEW: Extension setup page
└── chrome-extension/       # NEW: entire extension folder
    ├── manifest.json
    ├── background.js
    ├── content.js
    ├── popup/
    ├── options/
    └── icons/
```

---

## ✅ Definition of Done

- [ ] Extension connects to dashboard with token (green status)
- [ ] Clicking a download on getintopc.com triggers a Telegram bot message
- [ ] Browser does NOT start its own download (intercepted cleanly)
- [ ] Bot successfully downloads the file to GDrive
- [ ] Token can be regenerated from dashboard without breaking existing session
- [ ] All API endpoints return proper error messages for invalid/expired tokens

---

*Plan authored for GDriveBridge feature — ready for implementation.*
