## Skill Name

**telegram-cloud-transfer**

---

# Overview

**telegram-cloud-transfer** is a system that allows users to download files from multiple sources via a Telegram bot and automatically upload them to **Google Drive**.

Users can send:

* Files
* Torrent files
* Magnet links
* Direct download URLs

The bot will automatically download the content, upload it to Google Drive, and return the generated share link.

The system includes:

* A **Telegram bot written in Go**
* A **web-based admin dashboard**
* A **remote database**
* **Real-time status updates**
* **Docker containerization**
* **Cloud deployment support**

The goal is to provide a **personal cloud downloader and uploader controlled entirely through Telegram**.

---

# Core Functionality

## Universal Download System

The bot accepts multiple input formats.

Supported inputs:

### Telegram File Upload

Users can directly send files to the bot.

Example:

```
(send file to bot)
```

The bot will download the file from Telegram servers.

---

### Direct Download URL

Example:

```
https://example.com/file.zip
```

The system downloads the file using HTTP/HTTPS.

Features:

* resume downloads
* large file support
* parallel connections

---

### Torrent Files

Example:

```
movie.torrent
```

---

### Magnet Links

Example:

```
magnet:?xt=urn:btih:xxxxxxxx
```

Torrent downloads are handled by a torrent engine integrated into the system.

---

# Interactive Telegram Interface

The bot provides **live status updates** using **Telegram Bot API**.

Instead of sending multiple messages, the bot edits one message continuously.

Example progress display:

```
Task: Downloading

File: ubuntu.iso
Size: 2.3 GB

Progress
████████░░░░░░░ 48%

Speed: 7 MB/s
ETA: 3m 20s
```

After download finishes:

```
Uploading to Google Drive

██████████░░░░ 72%

Speed: 11 MB/s
```

Final message:

```
Upload Completed

File: ubuntu.iso

Google Drive Link
https://drive.google.com/file/xxxxx
```

---

# System Architecture

```
User
 │
 ▼
Telegram Bot (Go)
 │
 ├── Input Parser
 │
 ├── Task Manager
 │
 ├── Download Engine
 │       ├ HTTP Downloader
 │       ├ Torrent Downloader
 │
 ├── Upload Engine
 │       └ Google Drive API
 │
 ▼
Remote Database
 │
 ▼
Web API
 │
 ▼
Admin Dashboard
```

---

# Components

## Telegram Bot

The Telegram bot acts as the main user interface.

Responsibilities:

* receive messages
* detect file/link type
* create tasks
* monitor progress
* send updates
* return Drive links

Built using the **Telegram Bot API**.

---

## Task Manager

The task manager controls all downloads and uploads.

Responsibilities:

* queue management
* task tracking
* error handling
* progress monitoring

Each task has states such as:

* queued
* downloading
* uploading
* completed
* failed

---

## Download Engine

Responsible for downloading files from external sources.

### HTTP Downloader

Handles:

* direct download links
* parallel downloads
* resume support

---

### Torrent Downloader

Handles torrent-based downloads.

Supported inputs:

* `.torrent`
* magnet links

Recommended library:

```
github.com/anacrolix/torrent
```

Features:

* peer discovery
* piece download
* progress reporting

---

# Google Drive Upload System

Completed files are uploaded to **Google Drive**.

Uploads use the **Google Drive API**.

Features:

* resumable uploads
* large file support
* shareable link generation
* upload progress monitoring

Authentication is handled through **OAuth 2.0**.

---

# Database

The system uses a remote **MySQL** database.

The database stores:

* users
* tasks
* torrents
* configuration settings
* logs

---

# Database Schema

## users

```
id
username
email
password_hash
role
created_at
```

---

## tasks

```
id
user_id
file_name
file_size
input_type
download_progress
upload_progress
status
drive_link
created_at
```

---

## torrents

```
id
task_id
magnet_link
seeders
peers
download_speed
progress
```

---

## settings

```
id
bot_token
google_client_id
google_client_secret
download_directory
max_file_size
concurrent_tasks
```

---

## logs

```
id
level
message
created_at
```

---

# Web Dashboard

The system includes a **web-based admin dashboard**.

Built with:

* HTML
* CSS
* JavaScript
* Go backend

The dashboard allows administrators to manage the bot and monitor activity.

---

# Authentication System

The dashboard requires login.

Login fields:

```
Email
Password
```

Security features:

* password hashing with bcrypt
* session authentication
* login protection

---

# Dashboard Features

## Overview Page

Displays:

* active downloads
* active uploads
* completed tasks
* failed tasks
* disk usage
* CPU usage

---

## Task Manager

Shows all download/upload tasks.

Example table:

```
ID | File | Type | Status | Progress | Speed
```

---

## Torrent Monitor

Displays torrent statistics:

```
Name
Peers
Seeders
Download speed
Progress
```

---

## Bot Settings

Allows administrators to configure:

```
Telegram bot token
Google Drive credentials
Download directory
Upload folder
Max file size
Concurrent downloads
```

---

## Logs Page

Shows system activity logs including:

* download events
* upload results
* errors
* authentication logs

---

# File Storage

Temporary files are stored locally.

Directory structure:

```
/data/downloads
/data/torrents
/data/uploads
```

Files are deleted after successful upload.

---

# Telegram Commands

### Start

```
/start
```

Initialize the bot.

---

### Tasks

```
/tasks
```

List active tasks.

---

### Cancel

```
/cancel <task_id>
```

Stop a running task.

---

### Status

```
/status
```

Display system statistics.

---

# Real-Time Progress Updates

The bot sends a message when a task starts and continuously edits it.

Logic example:

```
send_message("Starting download...")

loop every 2 seconds:
  update progress
  edit message
```

This allows the user to monitor the task without message spam.

---

# Docker Deployment

The entire system runs inside **Docker**.

Docker ensures consistent deployment across servers and cloud platforms.

---

# Docker Structure

```
project/
 ├ bot/
 ├ dashboard/
 ├ downloader/
 ├ database/
 ├ Dockerfile
 ├ docker-compose.yml
```

---

# Environment Variables

```
BOT_TOKEN
MYSQL_HOST
MYSQL_USER
MYSQL_PASSWORD
MYSQL_DATABASE
```

---

# Cloud Deployment

The system is designed for easy deployment on cloud platforms such as **Koyeb**.

Deployment steps:

1. Build Docker image
2. Push to container registry
3. Deploy container
4. Configure environment variables
5. Connect remote MySQL database

---

# Security

Security measures include:

* authentication for dashboard
* encrypted passwords
* secure API tokens
* file size limits
* rate limiting
* sandboxed torrent downloads

---

# Performance Features

To ensure efficient operation:

* concurrent downloads
* resumable transfers
* download queue management
* parallel upload streams

---

# Future Enhancements

Possible improvements:

* multi-user support
* user storage quotas
* multiple cloud storage providers
* file compression before upload
* advanced monitoring graphs
* Telegram inline controls
* notification system
* automatic file organization in Drive


# Google Drive Authentication

The system supports two methods for authenticating with **Google Drive** using the **Google Drive API**.

Administrators can choose the authentication method based on deployment environment.

Authentication is implemented using **OAuth 2.0**.

---

# Method 1 — token.pickle Authentication

This method uses a stored OAuth token file (`token.pickle`) that contains the access and refresh tokens for the Google account.

### Workflow

1. Administrator performs OAuth authentication once.
2. The system generates a `token.pickle` file.
3. The file is stored on the server.
4. The bot uses the token to upload files to Google Drive.

### Advantages

* Very simple setup
* No login required after configuration
* Works well for **single-user systems**

### File Location

```text
/config/token.pickle
```

### Usage

During startup the system loads the token:

```text
Load token.pickle
Validate token
Refresh token if expired
Connect to Google Drive API
```

### Notes

If the token expires or becomes invalid, the admin must reauthenticate and regenerate the token.

---

# Method 2 — Web OAuth Login

The system also supports authentication through the web dashboard.

The administrator logs in to Google through the dashboard interface.

### Workflow

1. Admin opens the dashboard
2. Clicks **Connect Google Drive**
3. Redirects to Google login
4. Admin approves permissions
5. Access token stored in database

This allows dynamic management of Google accounts.

---

### OAuth Flow

```text
Admin Dashboard
      │
      ▼
Click "Connect Google Drive"
      │
      ▼
Redirect to Google OAuth
      │
      ▼
User Login
      │
      ▼
Authorization Approval
      │
      ▼
OAuth Callback
      │
      ▼
Token Stored in Database
```

---

### Stored Data

Tokens are stored in the database:

```
access_token
refresh_token
token_expiry
google_account_email
```

---

# Recommended Approach

For most deployments:

### Small Personal Bot

Use:

```
token.pickle
```

Advantages:

* simpler
* faster setup
* fewer dependencies

---

### Multi-user or Production System

Use:

```
Web OAuth login
```

Advantages:

* scalable
* supports multiple accounts
* better token management

---

# Fallback Logic

The system should follow this authentication order:

```text
1. Check if OAuth token exists in database
2. If not → check for token.pickle
3. If neither exists → require authentication
```

---

# Security Recommendations

* Store tokens securely
* Encrypt refresh tokens in database
* Restrict dashboard access
* Use HTTPS for OAuth callbacks
