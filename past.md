# Project History and Updates (past.md)

This file records all the major updates and progress made on the Telegram Cloud Transfer project, as per the rules.

## [2026-03-10] Project Initialization
- Read the requirements from `.antigravity/skills.md`.
- Created task tracker and implementation plan.
- Initialized the project structure (`bot`, `dashboard`, `downloader`, `database`, `config`).
- Created `go.mod` manually since Go wasn't found in system PATH.

## [2026-03-10] Core Development
- **Database Setup**: Defined the MySQL schema matching `skills.md` requirements and added the connection logic (`database/db.go`, `database/models.go`).
- **Downloader Engine**: Implemented HTTP downloading with chunked progress reporting (`downloader/http.go`). Included basic boilerplate for Torrent downloads via `anacrolix/torrent` (`downloader/torrent.go`).
- **Drive Uploader**: Wrote Google Drive API Service for file uploading and generating shareable links with a callback for progress tracking (`uploader/drive.go`).
- **Telegram Bot**: Wrote `gopkg.in/telebot.v3` handlers structure (`bot/bot.go`) with (`/start`, `/status`, `/tasks`, `/cancel`) and text/document callbacks.

## [2026-03-10] Web Dashboard
- Wrote API routes logic for dashboard (`dashboard/server.go`).
- Created a sleek modern HTML/CSS/Javascript dark-mode interface (`dashboard/static/index.html`, `dashboard/static/style.css`, `dashboard/static/app.js`).

## [2026-03-10] Project Wrapping Up & Docker
- Created the glue logic with `main.go`.
- Designed `Dockerfile` and `docker-compose.yml` for simplified deployment.
- Updated `docker-compose.yml` to use the remote MySQL database provided in `databasecred.md` instead of a local container.

## [2026-03-10] WSL Compatibility Testing
- Verified compatibility by resolving module conflicts in Linux (`cloud.google.com/go/compute/metadata`).
- Successfully ran `go mod tidy` and `go build -o bot-app ./main.go` inside an **Ubuntu WSL** environment.

## [2026-03-10] Bug Fixes and Reliability Updates
- **Database Connection**: Added `SetConnMaxLifetime`, `SetMaxOpenConns`, and `SetMaxIdleConns` to the MySQL `sql.DB` instance to prevent "connection reset by peer" and "broken pipe" errors from the remote database host.
- **Bot Orchestrator**: Re-wrote `main.go` and `bot/bot.go` to use a `BotOrchestrator` struct. It polls the database every 10 seconds for configuration updates (like the Telegram token) and dynamically reboots the `telebot` listener without needing to restart the container or the Go application entirely.

## [2026-03-10] Dashboard Google Authentication Integration
- **OAuth Connect Button**: Modified the Dashboard Settings page to include a "Connect Google Drive" button.
- **Go OAuth logic**: Added `oauth2` endpoint handlers `/api/auth/google/login` and `/api/auth/google/callback` to process the grant. Tokens (`access_token`, `refresh_token`, `token_expiry`) are now reliably stored directly in the `settings` database table instead of locally on disk via a `.pickle` file.

**Where we stopped**:
The core connection logic is solid, the dashboard settings form handles bot token reloads, and the Google Account connection flow is complete.
To continue the development:
1. Hook up the `TODO` stubs in `bot/bot.go` to the Database and Downloader packages to achieve end-to-end functionality.
2. Execute `docker-compose up -d` to spin up the local database and build the container, or run `bot-app` directly.
