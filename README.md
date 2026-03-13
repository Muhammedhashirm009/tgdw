# Telegram Cloud Transfer Bot

A Telegram bot that receives files and automatically uploads them to Google Drive, with a web dashboard for management.

## Features

- 🔗 **Direct URL Download** — Send HTTP/HTTPS links to pull files straight from the web
- 📥 **File Transfer** — Send files to the bot, auto-uploaded to a `telecloud` folder on Google Drive
- 📊 **Real-time Progress** — Visual progress bars with speed, ETA, and elapsed time
- 👑 **User Roles** — Admin users get unlimited access; normal users have daily limits
- 🌐 **Web Dashboard** — Dark-mode admin panel for settings, tasks, and Google OAuth
- 🗑️ **Auto-Cleanup** — Garbage collector deletes Drive files after configurable hours
- 📱 **Mobile Responsive** — Dashboard works on phones with hamburger menu

## Bot Commands

| Command | Description |
|---|---|
| `/start` | Welcome message with interactive menu |
| `/help` | List all commands |
| `/tasks` | View your recent tasks with status |
| `/status` | Active downloads/uploads count |
| `/me` | Your profile, role, and daily usage |
| `/cancel <id>` | Cancel an active task |

## User Roles

| | Admin | Normal User |
|---|---|---|
| **File Size** | Unlimited | 4 GB (configurable) |
| **Daily Downloads** | Unlimited | 5 per day |
| **Setup** | Set Telegram ID in Dashboard | Default |

## Quick Start

### Docker (Recommended)

```bash
# Set your Telegram API credentials
export TELEGRAM_API_ID=your_api_id
export TELEGRAM_API_HASH=your_api_hash

# Build and run
docker-compose up -d
```

### Manual

```bash
go mod tidy
go build -o bot-app ./main.go
./bot-app
```

## Configuration

Open the dashboard at `http://localhost:9990` and configure:

1. **Telegram Bot Token** — from [@BotFather](https://t.me/BotFather)
2. **Google Client ID & Secret** — from [Google Cloud Console](https://console.cloud.google.com/)
3. **Connect Google Drive** — click the OAuth button
4. **Admin Telegram IDs** — comma-separated user IDs for admin access
5. **Retention Hours** — auto-delete uploaded files after N hours (default: 48)

Default dashboard login: `admin` / `99901234`

## Architecture

```
main.go              → Entry point (dashboard + bot + garbage collector)
bot/bot.go           → Telegram bot handlers, commands, progress UI
bot/orchestrator.go  → Hot-reload bot on settings change
database/db.go       → MySQL queries, migrations, role checks
database/models.go   → Data models (User, Task, Settings, etc.)
downloader/http.go   → HTTP file downloader with progress
downloader/torrent.go→ Torrent downloader (BitTorrent support)
uploader/drive.go    → Google Drive upload + folder management
dashboard/server.go  → Web API server with auth
dashboard/static/    → Frontend (HTML, CSS, JS)
```

## Environment Variables

| Variable | Description |
|---|---|
| `TELEGRAM_API_ID` | Telegram API ID (for local bot API server) |
| `TELEGRAM_API_HASH` | Telegram API Hash |
| `PORT` | Dashboard port (default: 9990) |

## License

MIT
