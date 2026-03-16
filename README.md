# Telegram Cloud Transfer Bot

A Telegram bot that receives files and automatically uploads them to Google Drive, with a multi-user web dashboard and Chrome extension for download interception.

## Features

- 🔗 **Direct URL Download** — Send HTTP/HTTPS links to pull files straight from the web
- 📥 **File Transfer** — Send files to the bot, auto-uploaded to a `telecloud` folder on Google Drive
- 📊 **Real-time Progress** — Visual progress bars with speed, ETA, and elapsed time
- 👥 **Multi-User System** — Each bot user registers via `/register`, gets their own dashboard login and bridge token
- 👑 **User Roles** — Admin users get unlimited access; normal users have daily limits
- 🌐 **Web Dashboard** — Dark-mode panel for tasks, extension setup, and settings (admin-only)
- 🔌 **Chrome Extension** — Intercepts downloads and sends them to your bot via bridge token
- 🗑️ **Auto-Cleanup** — Garbage collector deletes Drive files after configurable hours
- 📱 **Mobile Responsive** — Dashboard works on phones with hamburger menu

## Bot Commands

| Command | Description |
|---|---|
| `/start` | Welcome message with interactive menu |
| `/help` | List all commands |
| `/register <password>` | Create your dashboard account (linked to your Telegram) |
| `/myaccount` | View your dashboard username and login URL |
| `/tasks` | View your recent tasks with status |
| `/status` | Active downloads/uploads count |
| `/me` | Your profile, role, and daily usage |
| `/cancel <id>` | Cancel an active task |

## Multi-User Flow

1. User sends `/register MyPassword123` to the bot
2. Bot creates account and replies with username (`tg_<telegram_id>`) + dashboard URL
3. User logs into the dashboard with those credentials
4. Each user sees **only their own tasks** and can generate their **own bridge token**
5. Bridge downloads send progress/completion messages to **the user's Telegram chat**
6. Admin user sees all tasks and has access to the Settings tab

## User Roles

| | Admin | Normal User |
|---|---|---|
| **File Size** | Unlimited | 4 GB (configurable) |
| **Daily Downloads** | Unlimited | 5 per day |
| **Dashboard Settings** | Full access | Hidden |
| **Task Visibility** | All users' tasks | Own tasks only |

## Quick Start

### Deploy on Koyeb (Recommended)

1. Push this repo to GitHub
2. Create a new Koyeb service → select **Dockerfile** builder
3. Set environment variables (see below)
4. Deploy — Koyeb builds from the Dockerfile and runs `start.sh`

### Docker Compose (Local)

```bash
# Edit docker-compose.yml with your credentials, then:
docker-compose up -d
```

### Manual

```bash
# Set required environment variables first (see below)
go mod tidy
go build -o bot-app ./main.go
./bot-app
```

## Environment Variables

All env vars are set in **Koyeb Service → Settings → Environment Variables** (or in `docker-compose.yml` for local).

### Required

| Variable | Description | Example |
|---|---|---|
| `MYSQL_HOST` | MySQL host and port | `your-db-host:3306` |
| `MYSQL_USER` | MySQL username | `db_user` |
| `MYSQL_PASSWORD` | MySQL password (use **Secret** type) | `MyDBPass` |
| `MYSQL_DATABASE` | MySQL database name | `downloader` |
| `TELEGRAM_API_ID` | From [my.telegram.org](https://my.telegram.org) | `12345678` |
| `TELEGRAM_API_HASH` | From my.telegram.org (use **Secret** type) | `abcdef1234...` |

### Optional

| Variable | Description | Default |
|---|---|---|
| `DASHBOARD_URL` | Your app's public URL (shown to users on `/register`) | `your-dashboard-url` |
| `PORT` | Dashboard port | `9990` |

## Configuration

Open the dashboard at your deployed URL (or `http://localhost:9990`) and log in.

**Default admin login:** `admin` / `99901234`

From the dashboard, configure:

1. **Telegram Bot Token** — from [@BotFather](https://t.me/BotFather)
2. **Google Client ID & Secret** — from [Google Cloud Console](https://console.cloud.google.com/)
3. **Connect Google Drive** — click the OAuth button
4. **Admin Telegram IDs** — comma-separated user IDs for admin access
5. **Retention Hours** — auto-delete uploaded files after N hours (default: 48)

## Architecture

```
main.go              → Entry point (dashboard + bot + garbage collector)
bot/bot.go           → Telegram bot handlers, user registration, progress UI
bot/orchestrator.go  → Hot-reload bot on settings change
database/db.go       → MySQL queries, migrations, multi-user functions
database/models.go   → Data models (User, Task, Settings, BridgeToken, etc.)
downloader/          → HTTP, aria2c, and stream downloaders
uploader/drive.go    → Google Drive upload + folder management
dashboard/server.go  → Web API server with per-user auth
dashboard/static/    → Frontend (HTML, CSS, JS)
chrome-extension/    → Browser extension for download interception
```

## License

MIT
