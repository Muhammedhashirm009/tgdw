package bot

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/downloader/telegram-cloud-transfer/database"
	"github.com/downloader/telegram-cloud-transfer/downloader"
	"github.com/downloader/telegram-cloud-transfer/uploader"
	"golang.org/x/oauth2"
	tele "gopkg.in/telebot.v3"
)

const maxDailyTasksNormal = 5

type BotHandler struct {
	bot *tele.Bot
}

// NewBot initializes the telegram bot with telebot.v3
func NewBot(token string, apiURL string) (*BotHandler, error) {
	pref := tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
		Client: &http.Client{
			Timeout: 30 * time.Minute,
		},
	}

	if apiURL != "" && apiURL != "https://api.telegram.org" {
		pref.URL = apiURL
	}

	b, err := tele.NewBot(pref)
	if err != nil {
		return nil, err
	}

	bh := &BotHandler{bot: b}
	bh.setupRoutes()

	return bh, nil
}

func (bh *BotHandler) Start() {
	log.Println("Starting Telegram Bot...")
	bh.bot.Start()
}

func (bh *BotHandler) setupRoutes() {
	bh.bot.Handle("/start", bh.handleStart)
	bh.bot.Handle("/help", bh.handleHelp)
	bh.bot.Handle("/tasks", bh.handleTasks)
	bh.bot.Handle("/cancel", bh.handleCancel)
	bh.bot.Handle("/status", bh.handleStatus)
	bh.bot.Handle("/me", bh.handleMe)

	// Inline button callbacks
	bh.bot.Handle("\ftasks", bh.handleTasksCallback)
	bh.bot.Handle("\fstatus", bh.handleStatusCallback)
	bh.bot.Handle("\fhelp", bh.handleHelpCallback)
	bh.bot.Handle("\fme", bh.handleMeCallback)

	bh.bot.Handle(tele.OnText, bh.handleText)
	bh.bot.Handle(tele.OnDocument, bh.handleDocument)
}

// ===== Visual Helpers =====

func progressBar(percent int) string {
	filled := percent / 5 // 20-char bar
	empty := 20 - filled
	if filled < 0 {
		filled = 0
	}
	if empty < 0 {
		empty = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

func formatSize(bytes int64) string {
	return humanize.Bytes(uint64(bytes))
}

func roleLabel(telegramUserID int64) string {
	if database.IsAdminTelegram(telegramUserID) {
		return "👑 Admin"
	}
	return "👤 User"
}

// ===== Main Menu =====

func mainMenuKeyboard() *tele.ReplyMarkup {
	menu := &tele.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("📋 My Tasks", "tasks"),
			menu.Data("📊 Status", "status"),
		),
		menu.Row(
			menu.Data("👤 My Info", "me"),
			menu.Data("❓ Help", "help"),
		),
	)
	return menu
}

func driveButton(link string) *tele.ReplyMarkup {
	menu := &tele.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.URL("📂 Open in Google Drive", link)),
	)
	return menu
}

// ===== Command Handlers =====

func (bh *BotHandler) handleStart(c tele.Context) error {
	role := roleLabel(c.Sender().ID)

	text := fmt.Sprintf("🚀 <b>Welcome to Cloud Transfer Bot!</b>\n\n"+
		"Send me a <b>file</b> and I'll upload it to Google Drive.\n\n"+
		"Your role: %s\n\n"+
		"Use the buttons below or type /help for commands.", role)

	return c.Send(text, &tele.SendOptions{ParseMode: tele.ModeHTML}, mainMenuKeyboard())
}

func (bh *BotHandler) handleHelp(c tele.Context) error {
	text := "📖 <b>Available Commands</b>\n\n" +
		"/start — Main menu\n" +
		"/help — Show this help\n" +
		"/tasks — View your recent tasks\n" +
		"/status — System status\n" +
		"/me — Your profile & limits\n" +
		"/cancel &lt;id&gt; — Cancel an active task\n\n" +
		"<b>How to use:</b>\n" +
		"Simply send me a file and I'll download it, then upload it to Google Drive automatically."

	return c.Send(text, &tele.SendOptions{ParseMode: tele.ModeHTML}, mainMenuKeyboard())
}

func (bh *BotHandler) handleMe(c tele.Context) error {
	userID := c.Sender().ID
	isAdmin := database.IsAdminTelegram(userID)
	dailyCount, _ := database.GetDailyTaskCount(userID)

	settings, _ := database.GetSettings()
	maxSize := formatSize(settings.MaxFileSizeNormal)

	var text string
	if isAdmin {
		text = fmt.Sprintf("👤 <b>Your Profile</b>\n\n"+
			"🆔 <b>Telegram ID:</b> <code>%d</code>\n"+
			"👑 <b>Role:</b> Admin\n"+
			"📊 <b>Tasks Today:</b> %d\n\n"+
			"✨ <i>Unlimited file size & downloads</i>",
			userID, dailyCount)
	} else {
		remaining := maxDailyTasksNormal - dailyCount
		if remaining < 0 {
			remaining = 0
		}
		text = fmt.Sprintf("👤 <b>Your Profile</b>\n\n"+
			"🆔 <b>Telegram ID:</b> <code>%d</code>\n"+
			"👤 <b>Role:</b> User\n"+
			"📊 <b>Tasks Today:</b> %d / %d\n"+
			"📦 <b>Max File Size:</b> %s\n"+
			"🔄 <b>Remaining Today:</b> %d",
			userID, dailyCount, maxDailyTasksNormal, maxSize, remaining)
	}

	return c.Send(text, &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (bh *BotHandler) handleTasks(c tele.Context) error {
	tasks, err := database.GetTasksByTelegramUser(c.Sender().ID, 10)
	if err != nil {
		return c.Send("❌ Error fetching tasks: " + err.Error())
	}
	if len(tasks) == 0 {
		return c.Send("📋 You have no tasks yet.\n\nSend me a file to get started!")
	}

	text := "📋 <b>Your Recent Tasks</b>\n\n"
	for _, t := range tasks {
		icon := "⏳"
		switch t.Status {
		case "Completed":
			icon = "✅"
		case "Failed":
			icon = "❌"
		case "Downloading":
			icon = "📥"
		case "Uploading":
			icon = "☁️"
		case "Cancelled":
			icon = "🚫"
		}

		line := fmt.Sprintf("%s <b>#%d</b> <code>%s</code>\n   └ %s", icon, t.ID, t.FileName, t.Status)
		if t.DriveLink != "" {
			line += fmt.Sprintf(" • <a href=\"%s\">Drive</a>", t.DriveLink)
		}
		if t.ElapsedTime != "" {
			line += fmt.Sprintf(" • %s", t.ElapsedTime)
		}
		text += line + "\n\n"
	}

	return c.Send(text, &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (bh *BotHandler) handleCancel(c tele.Context) error {
	args := c.Args()
	if len(args) == 0 {
		return c.Send("⚠️ Usage: <code>/cancel &lt;task_id&gt;</code>", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}
	var taskID int
	fmt.Sscanf(args[0], "%d", &taskID)

	if database.CancelTask(taskID) {
		database.UpdateTaskStatus(taskID, "Cancelled", "", "", "")
		return c.Send(fmt.Sprintf("🚫 Task <b>#%d</b> cancelled successfully.", taskID), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}
	return c.Send(fmt.Sprintf("❌ Task #%d not found or already completed.", taskID))
}

func (bh *BotHandler) handleStatus(c tele.Context) error {
	downloads, uploads, err := database.GetStatusSummary()
	if err != nil {
		return c.Send("❌ Error fetching status: " + err.Error())
	}

	text := fmt.Sprintf("📊 <b>System Status</b>\n\n"+
		"📥 <b>Active Downloads:</b> %d\n"+
		"☁️ <b>Active Uploads:</b> %d\n"+
		"🟢 <b>Bot:</b> Online",
		downloads, uploads)

	return c.Send(text, &tele.SendOptions{ParseMode: tele.ModeHTML})
}

// ===== Inline Button Callbacks =====

func (bh *BotHandler) handleTasksCallback(c tele.Context) error {
	c.Respond()
	return bh.handleTasks(c)
}

func (bh *BotHandler) handleStatusCallback(c tele.Context) error {
	c.Respond()
	return bh.handleStatus(c)
}

func (bh *BotHandler) handleHelpCallback(c tele.Context) error {
	c.Respond()
	return bh.handleHelp(c)
}

func (bh *BotHandler) handleMeCallback(c tele.Context) error {
	c.Respond()
	return bh.handleMe(c)
}

// ===== Text Handler =====

func (bh *BotHandler) handleText(c tele.Context) error {
	text := strings.TrimSpace(c.Text())
	if text == "" {
		return nil
	}

	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
		return bh.handleDirectLink(c, text)
	}

	return c.Send("Send me a document or a direct HTTP/HTTPS link to download and upload it to Google Drive.", &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (bh *BotHandler) handleDirectLink(c tele.Context, downloadURL string) error {
	telegramUserID := c.Sender().ID
	isAdmin := database.IsAdminTelegram(telegramUserID)

	// Fetch Settings
	settings, err := database.GetSettings()
	if err != nil {
		return c.Send("❌ Internal error: Could not load settings.")
	}

	if settings.AccessToken == "" {
		return c.Send("⚠️ Google Drive is not connected.\nPlease connect via the Dashboard.")
	}

	msg, err := bh.bot.Send(c.Chat(), "⏳ Fetching link info...")
	if err != nil {
		return err
	}

	// Fetch Headers to get size and name
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("HEAD", downloadURL, nil)
	if err != nil {
		bh.bot.Edit(msg, "❌ Invalid URL configuration.")
		return err
	}

	// Disguise as a standard browser to avoid some basic blocks
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	resp, err := client.Do(req)
	
	if err != nil || resp.StatusCode >= 400 {
		// Fallback to GET if HEAD fails or is rejected
		req, _ = http.NewRequest("GET", downloadURL, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
		
		// Create a context to cancel the GET request immediately after getting headers
		cancelCtx, cancelFunc := context.WithCancel(context.Background())
		req = req.WithContext(cancelCtx)
		
		resp, err = client.Do(req)
		cancelFunc() // abort body download
		
		if err != nil || resp.StatusCode >= 400 {
			bh.bot.Edit(msg, "❌ Could not reach the file. Ensure the link points directly to a downloadable file.")
			return err
		}
	}

	fileSize := resp.ContentLength
	if fileSize < 0 {
		fileSize = 0 // Unknown size
	}

	// Try extracting filename from Content-Disposition
	fileName := ""
	cd := resp.Header.Get("Content-Disposition")
	if cd != "" {
		// Basic parsing for filename="..."
		if idx := strings.Index(cd, "filename="); idx != -1 {
			fileName = cd[idx+len("filename="):]
			fileName = strings.Trim(fileName, `"' `)
			// Remove any trailing parameters separated by semicolon
			if semi := strings.Index(fileName, ";"); semi != -1 {
				fileName = fileName[:semi]
			}
		}
	}
	
	// Fallback to URL path base
	if fileName == "" {
		fileName = path.Base(req.URL.Path)
		if fileName == "/" || fileName == "." || fileName == "" {
			fileName = fmt.Sprintf("download_%d", time.Now().Unix())
		}
	}

	// --- Role-based limits ---
	if !isAdmin {
		// Check daily limit
		dailyCount, _ := database.GetDailyTaskCount(telegramUserID)
		if dailyCount >= maxDailyTasksNormal {
			bh.bot.Edit(msg, fmt.Sprintf("🚫 <b>Daily limit reached!</b>\n\n"+
				"You've used <b>%d/%d</b> downloads today.\n"+
				"Try again tomorrow or contact an admin.",
				dailyCount, maxDailyTasksNormal), &tele.SendOptions{ParseMode: tele.ModeHTML})
			return nil
		}

		// Check file size limit
		maxSize := settings.MaxFileSizeNormal
		if maxSize <= 0 {
			maxSize = 4294967296 // 4GB default
		}
		if fileSize > maxSize {
			bh.bot.Edit(msg, fmt.Sprintf("🚫 <b>File too large!</b>\n\n"+
				"📦 <b>File size:</b> %s\n"+
				"📏 <b>Max allowed:</b> %s\n\n"+
				"Contact an admin for larger files.",
				formatSize(fileSize), formatSize(maxSize)), &tele.SendOptions{ParseMode: tele.ModeHTML})
			return nil
		}
	}

	bh.bot.Edit(msg, fmt.Sprintf("🔗 <b>Direct Link Received</b>\n\n"+
		"📄 <b>Name:</b> <code>%s</code>\n"+
		"📦 <b>Size:</b> %s\n"+
		"⏳ <b>Status:</b> Queued...",
		fileName, formatSize(fileSize)), &tele.SendOptions{ParseMode: tele.ModeHTML})

	taskID, err := database.CreateTaskWithTelegram(1, telegramUserID, fileName, fileSize, "Direct Link")
	if err != nil {
		bh.bot.Edit(msg, "❌ Error creating task in database.")
		return err
	}

	database.UpdateTaskStatus(taskID, "Downloading", "", "", "")

	ctx, cancel := context.WithCancel(context.Background())
	database.RegisterCancelFunc(taskID, cancel)

	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				database.UpdateTaskStatus(taskID, "Failed", "", "", "")
				bh.bot.Edit(msg, fmt.Sprintf("❌ <b>Task #%d failed unexpectedly.</b>", taskID), &tele.SendOptions{ParseMode: tele.ModeHTML})
			}
		}()

		var downloadPath string
		startTime := time.Now()

		// === DOWNLOAD PHASE ===
		lastUpdate := time.Now()
		downloadPath, err = downloader.DownloadHTTP(ctx, downloadURL, settings.DownloadDirectory, fileName, func(downloaded, total, speed int64) {
			if fileSize <= 0 && total > 0 {
				fileSize = total
				database.UpdateTaskFileSize(taskID, fileSize)
			}

			if time.Since(lastUpdate) > 3*time.Second {
				progress := 0
				if total > 0 {
					progress = int((float64(downloaded) / float64(total)) * 100)
				}
				
				database.UpdateTaskDownloadProgress(taskID, progress, speed)

				eta := calcETA(total-downloaded, speed)
				if total <= 0 {
					eta = "unknown"
				}
				
				elapsed := time.Since(startTime).Round(time.Second).String()

				text := fmt.Sprintf("📥 <b>Downloading Web Link</b> [#%d]\n\n"+
					"📄 <code>%s</code>\n"+
					"<code>[%s] %d%%</code>\n\n"+
					"⚡ %s/s  •  ⏳ %s  •  ⏱ %s\n\n"+
					"<i>/cancel %d to abort</i>",
					taskID, fileName,
					progressBar(progress), progress,
					formatSize(speed), eta, elapsed, taskID)

				bh.bot.Edit(msg, text, &tele.SendOptions{ParseMode: tele.ModeHTML})
				lastUpdate = time.Now()
			}
		})

		if err != nil {
			if ctx.Err() == context.Canceled {
				return
			}
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.bot.Edit(msg, "❌ <b>Download Failed:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
			return
		}

		// Update actual file size if it was unknown
		if fileSize <= 0 {
			if stat, statErr := os.Stat(downloadPath); statErr == nil {
				fileSize = stat.Size()
				// Unfortunately updating DB schema for file_size after creation requires another func, 
				// but progress is tracked at 100% now so it's okay.
			}
		}

		// === UPLOAD PHASE ===
		database.UpdateTaskDownloadProgress(taskID, 100, 0)
		database.UpdateTaskStatus(taskID, "Uploading", "", "", "")

		bh.bot.Edit(msg, fmt.Sprintf("☁️ <b>Uploading to Google Drive</b> [#%d]\n\n"+
			"📄 <code>%s</code>\n"+
			"<code>[%s] 0%%</code>\n\n"+
			"⏳ Starting upload...",
			taskID, fileName, progressBar(0)), &tele.SendOptions{ParseMode: tele.ModeHTML})

		token := &oauth2.Token{
			AccessToken:  settings.AccessToken,
			RefreshToken: settings.RefreshToken,
			Expiry:       settings.TokenExpiry,
			TokenType:    "Bearer",
		}

		uploaderInstance, err := uploader.NewDriveUploader(context.Background(), token, settings.GoogleClientID, settings.GoogleClientSecret)
		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.bot.Edit(msg, "❌ <b>Upload Setup Failed:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
			return
		}

		lastUpdate = time.Now()
		driveLink, driveFileID, err := uploaderInstance.UploadFile(ctx, downloadPath, fileName, func(uploaded, total, speed int64) {
			if time.Since(lastUpdate) > 3*time.Second {
				progress := 0
				if total > 0 {
					progress = int((float64(uploaded) / float64(total)) * 100)
				}
				
				database.UpdateTaskUploadProgress(taskID, progress, speed)

				eta := calcETA(total-uploaded, speed)
				elapsed := time.Since(startTime).Round(time.Second).String()

				text := fmt.Sprintf("☁️ <b>Uploading</b> [#%d]\n\n"+
					"📄 <code>%s</code>\n"+
					"<code>[%s] %d%%</code>\n\n"+
					"⚡ %s/s  •  ⏳ %s  •  ⏱ %s\n\n"+
					"<i>/cancel %d to abort</i>",
					taskID, fileName,
					progressBar(progress), progress,
					formatSize(speed), eta, elapsed, taskID)

				bh.bot.Edit(msg, text, &tele.SendOptions{ParseMode: tele.ModeHTML})
				lastUpdate = time.Now()
			}
		})

		// Clean up the local file after upload
		os.Remove(downloadPath)

		if err != nil {
			if ctx.Err() == context.Canceled {
				return
			}
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.bot.Edit(msg, "❌ <b>Upload Failed:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
			return
		}

		// === COMPLETION ===
		finalElapsed := time.Since(startTime).Round(time.Second).String()
		database.UpdateTaskUploadProgress(taskID, 100, 0)
		database.UpdateTaskStatus(taskID, "Completed", driveLink, driveFileID, finalElapsed)

		completeText := fmt.Sprintf("✅ <b>Task #%d Complete!</b>\n\n"+
			"📄 <b>File:</b> <code>%s</code>\n"+
			"📦 <b>Size:</b> %s\n"+
			"⏱ <b>Time:</b> %s\n\n"+
			"<code>[████████████████████] 100%%</code>",
			taskID, fileName, formatSize(fileSize), finalElapsed)

		if driveLink != "" {
			bh.bot.Edit(msg, completeText, &tele.SendOptions{ParseMode: tele.ModeHTML}, driveButton(driveLink))
		} else {
			bh.bot.Edit(msg, completeText, &tele.SendOptions{ParseMode: tele.ModeHTML})
		}
	}()

	return nil
}

// ===== Document Handler (Main Pipeline) =====

func (bh *BotHandler) handleDocument(c tele.Context) error {
	doc := c.Message().Document
	if doc == nil {
		return nil
	}

	telegramUserID := c.Sender().ID
	isAdmin := database.IsAdminTelegram(telegramUserID)

	// Fetch Settings
	settings, err := database.GetSettings()
	if err != nil {
		return c.Send("❌ Internal error: Could not load settings.")
	}

	if settings.AccessToken == "" {
		return c.Send("⚠️ Google Drive is not connected.\nPlease connect via the Dashboard.")
	}

	// --- Role-based limits ---
	if !isAdmin {
		// Check daily limit
		dailyCount, _ := database.GetDailyTaskCount(telegramUserID)
		if dailyCount >= maxDailyTasksNormal {
			return c.Send(fmt.Sprintf("🚫 <b>Daily limit reached!</b>\n\n"+
				"You've used <b>%d/%d</b> downloads today.\n"+
				"Try again tomorrow or contact an admin.",
				dailyCount, maxDailyTasksNormal), &tele.SendOptions{ParseMode: tele.ModeHTML})
		}

		// Check file size limit
		maxSize := settings.MaxFileSizeNormal
		if maxSize <= 0 {
			maxSize = 4294967296 // 4GB default
		}
		if doc.FileSize > maxSize {
			return c.Send(fmt.Sprintf("🚫 <b>File too large!</b>\n\n"+
				"📦 <b>Your file:</b> %s\n"+
				"📏 <b>Max allowed:</b> %s\n\n"+
				"Contact an admin for larger files.",
				formatSize(doc.FileSize), formatSize(maxSize)), &tele.SendOptions{ParseMode: tele.ModeHTML})
		}
	}

	// Initial message
	msg, err := bh.bot.Send(c.Chat(), fmt.Sprintf("📎 <b>File Received</b>\n\n"+
		"📄 <b>Name:</b> <code>%s</code>\n"+
		"📦 <b>Size:</b> %s\n"+
		"⏳ <b>Status:</b> Queued...",
		doc.FileName, formatSize(doc.FileSize)), &tele.SendOptions{ParseMode: tele.ModeHTML})
	if err != nil {
		return err
	}

	// Create Task in DB with Telegram user ID
	taskID, err := database.CreateTaskWithTelegram(1, telegramUserID, doc.FileName, doc.FileSize, "Telegram Document")
	if err != nil {
		bh.bot.Edit(msg, "❌ Error creating task in database.")
		return err
	}

	database.UpdateTaskStatus(taskID, "Downloading", "", "", "")

	// Create a cancelable context
	ctx, cancel := context.WithCancel(context.Background())
	database.RegisterCancelFunc(taskID, cancel)

	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				database.UpdateTaskStatus(taskID, "Failed", "", "", "")
				bh.bot.Edit(msg, fmt.Sprintf("❌ <b>Task #%d failed unexpectedly.</b>", taskID), &tele.SendOptions{ParseMode: tele.ModeHTML})
			}
		}()

		var downloadPath string
		startTime := time.Now()

		// === DOWNLOAD PHASE ===

		// Background tracker for the local proxy download phase
		trackCtx, trackCancel := context.WithCancel(context.Background())
		go func() {
			var lastSize int64
			var lastReport time.Time
			for {
				select {
				case <-trackCtx.Done():
					return
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
					var maxSize int64
					filepath.Walk("/var/lib/telegram-bot-api", func(path string, info os.FileInfo, err error) error {
						if err != nil || info.IsDir() {
							return nil
						}
						if time.Since(info.ModTime()) < 10*time.Second {
							if info.Size() > maxSize {
								maxSize = info.Size()
							}
						}
						return nil
					})

					if maxSize > 0 {
						speed := int64(0)
						if lastSize > 0 && maxSize > lastSize && !lastReport.IsZero() {
							speed = int64(float64(maxSize-lastSize) / time.Since(lastReport).Seconds())
						}

						progress := int((float64(maxSize) / float64(doc.FileSize)) * 100)
						if progress > 100 {
							progress = 100
						}

						database.UpdateTaskDownloadProgress(taskID, progress, speed)

						eta := calcETA(doc.FileSize-maxSize, speed)
						elapsed := time.Since(startTime).Round(time.Second).String()

						text := fmt.Sprintf("📥 <b>Downloading</b> [#%d]\n\n"+
							"📄 <code>%s</code>\n"+
							"<code>[%s] %d%%</code>\n\n"+
							"⚡ %s/s  •  ⏳ %s  •  ⏱ %s\n\n"+
							"<i>/cancel %d to abort</i>",
							taskID, doc.FileName,
							progressBar(progress), progress,
							formatSize(speed), eta, elapsed, taskID)

						bh.bot.Edit(msg, text, &tele.SendOptions{ParseMode: tele.ModeHTML})

						lastSize = maxSize
						lastReport = time.Now()
					}
				}
			}
		}()

		// Get Telegram file path
		file, err := bh.bot.FileByID(doc.FileID)
		trackCancel()

		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.bot.Edit(msg, "❌ <b>Error getting file from Telegram:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
			return
		}

		if stat, err := os.Stat(file.FilePath); err == nil && !stat.IsDir() {
			downloadPath = file.FilePath
			database.UpdateTaskDownloadProgress(taskID, 100, 0)
		} else {
			apiBase := settings.TelegramAPIEndpoint
			if apiBase == "" {
				apiBase = "https://api.telegram.org"
			}
			fileURL := fmt.Sprintf("%s/file/bot%s/%s", apiBase, settings.BotToken, file.FilePath)

			lastUpdate := time.Now()

			downloadPath, err = downloader.DownloadHTTP(ctx, fileURL, settings.DownloadDirectory, doc.FileName, func(downloaded, total, speed int64) {
				if time.Since(lastUpdate) > 3*time.Second {
					progress := int((float64(downloaded) / float64(total)) * 100)
					database.UpdateTaskDownloadProgress(taskID, progress, speed)

					eta := calcETA(total-downloaded, speed)
					elapsed := time.Since(startTime).Round(time.Second).String()

					text := fmt.Sprintf("📥 <b>Downloading</b> [#%d]\n\n"+
						"📄 <code>%s</code>\n"+
						"<code>[%s] %d%%</code>\n\n"+
						"⚡ %s/s  •  ⏳ %s  •  ⏱ %s\n\n"+
						"<i>/cancel %d to abort</i>",
						taskID, doc.FileName,
						progressBar(progress), progress,
						formatSize(speed), eta, elapsed, taskID)

					bh.bot.Edit(msg, text, &tele.SendOptions{ParseMode: tele.ModeHTML})
					lastUpdate = time.Now()
				}
			})

			if err != nil {
				if ctx.Err() == context.Canceled {
					return
				}
				database.UpdateTaskStatus(taskID, "Failed", "", "", "")
				bh.bot.Edit(msg, "❌ <b>Download Failed:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
				return
			}
		}

		// === UPLOAD PHASE ===
		database.UpdateTaskDownloadProgress(taskID, 100, 0)
		database.UpdateTaskStatus(taskID, "Uploading", "", "", "")

		bh.bot.Edit(msg, fmt.Sprintf("☁️ <b>Uploading to Google Drive</b> [#%d]\n\n"+
			"📄 <code>%s</code>\n"+
			"<code>[%s] 0%%</code>\n\n"+
			"⏳ Starting upload...",
			taskID, doc.FileName, progressBar(0)), &tele.SendOptions{ParseMode: tele.ModeHTML})

		token := &oauth2.Token{
			AccessToken:  settings.AccessToken,
			RefreshToken: settings.RefreshToken,
			Expiry:       settings.TokenExpiry,
			TokenType:    "Bearer",
		}

		uploaderInstance, err := uploader.NewDriveUploader(context.Background(), token, settings.GoogleClientID, settings.GoogleClientSecret)
		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.bot.Edit(msg, "❌ <b>Upload Setup Failed:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
			return
		}

		lastUpdate := time.Now()
		driveLink, driveFileID, err := uploaderInstance.UploadFile(ctx, downloadPath, doc.FileName, func(uploaded, total, speed int64) {
			if time.Since(lastUpdate) > 3*time.Second {
				progress := int((float64(uploaded) / float64(total)) * 100)
				database.UpdateTaskUploadProgress(taskID, progress, speed)

				eta := calcETA(total-uploaded, speed)
				elapsed := time.Since(startTime).Round(time.Second).String()

				text := fmt.Sprintf("☁️ <b>Uploading</b> [#%d]\n\n"+
					"📄 <code>%s</code>\n"+
					"<code>[%s] %d%%</code>\n\n"+
					"⚡ %s/s  •  ⏳ %s  •  ⏱ %s\n\n"+
					"<i>/cancel %d to abort</i>",
					taskID, doc.FileName,
					progressBar(progress), progress,
					formatSize(speed), eta, elapsed, taskID)

				bh.bot.Edit(msg, text, &tele.SendOptions{ParseMode: tele.ModeHTML})
				lastUpdate = time.Now()
			}
		})

		// Clean up the local file after upload
		os.Remove(downloadPath)

		if err != nil {
			if ctx.Err() == context.Canceled {
				return
			}
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.bot.Edit(msg, "❌ <b>Upload Failed:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
			return
		}

		// === COMPLETION ===
		finalElapsed := time.Since(startTime).Round(time.Second).String()
		database.UpdateTaskUploadProgress(taskID, 100, 0)
		database.UpdateTaskStatus(taskID, "Completed", driveLink, driveFileID, finalElapsed)

		completeText := fmt.Sprintf("✅ <b>Task #%d Complete!</b>\n\n"+
			"📄 <b>File:</b> <code>%s</code>\n"+
			"📦 <b>Size:</b> %s\n"+
			"⏱ <b>Time:</b> %s\n\n"+
			"<code>[████████████████████] 100%%</code>",
			taskID, doc.FileName, formatSize(doc.FileSize), finalElapsed)

		if driveLink != "" {
			bh.bot.Edit(msg, completeText, &tele.SendOptions{ParseMode: tele.ModeHTML}, driveButton(driveLink))
		} else {
			bh.bot.Edit(msg, completeText, &tele.SendOptions{ParseMode: tele.ModeHTML})
		}
	}()

	return nil
}

// ===== Common Functions =====

func calcETA(remainingBytes, speed int64) string {
	if speed <= 0 {
		return "calculating..."
	}
	seconds := remainingBytes / speed
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh %dm", seconds/3600, (seconds%3600)/60)
}

// ===== Bridge Extension Logic =====

func (bh *BotHandler) processBridgeTask(taskID int, downloadURL string, fileName string, initialSize int64, chatID int64) {
	// Let's send a starting message to Telegram if admin is configured
	var msg *tele.Message
	if chatID > 0 {
		chat := &tele.Chat{ID: chatID}
		startText := fmt.Sprintf("🔌 <b>New Extension Download</b>\n\n"+
			"📄 <b>Name:</b> <code>%s</code>\n"+
			"🔗 <b>Source:</b> %s\n"+
			"⏳ <b>Status:</b> Starting...",
			fileName, downloadURL)

		m, err := bh.bot.Send(chat, startText, &tele.SendOptions{ParseMode: tele.ModeHTML})
		if err == nil {
			msg = m
		} else {
			log.Printf("Failed to notify admin about bridge task: %v", err)
		}
	}

	settings, err := database.GetSettings()
	if err != nil || settings.AccessToken == "" {
		database.UpdateTaskStatus(taskID, "Failed", "", "", "")
		if msg != nil {
			bh.bot.Edit(msg, "❌ <b>Task Failed:</b> Google Drive not configured.", &tele.SendOptions{ParseMode: tele.ModeHTML})
		}
		return
	}

	database.UpdateTaskStatus(taskID, "Downloading", "", "", "")

	ctx, cancel := context.WithCancel(context.Background())
	database.RegisterCancelFunc(taskID, cancel)

	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			if msg != nil {
				bh.bot.Edit(msg, fmt.Sprintf("❌ <b>Bridge Task #%d failed unexpectedly.</b>", taskID), &tele.SendOptions{ParseMode: tele.ModeHTML})
			}
		}
	}()

	var downloadPath string
	fileSize := initialSize
	startTime := time.Now()

	// === DOWNLOAD PHASE ===
	lastUpdate := time.Now()
	downloadPath, err = downloader.DownloadHTTP(ctx, downloadURL, settings.DownloadDirectory, fileName, func(downloaded, total, speed int64) {
		if fileSize <= 0 && total > 0 {
			fileSize = total
			database.UpdateTaskFileSize(taskID, fileSize)
		}
		
		if time.Since(lastUpdate) > 3*time.Second {
			progress := 0
			if total > 0 {
				progress = int((float64(downloaded) / float64(total)) * 100)
			}
			database.UpdateTaskDownloadProgress(taskID, progress, speed)

			if msg != nil {
				eta := calcETA(total-downloaded, speed)
				if total <= 0 {
					eta = "unknown"
				}
				elapsed := time.Since(startTime).Round(time.Second).String()

				text := fmt.Sprintf("🔌 <b>Bridge Download</b> [#%d]\n\n"+
					"📄 <code>%s</code>\n"+
					"<code>[%s] %d%%</code>\n\n"+
					"⚡ %s/s  •  ⏳ %s  •  ⏱ %s\n\n"+
					"<i>/cancel %d to abort</i>",
					taskID, fileName,
					progressBar(progress), progress,
					formatSize(speed), eta, elapsed, taskID)

				bh.bot.Edit(msg, text, &tele.SendOptions{ParseMode: tele.ModeHTML})
			}
			lastUpdate = time.Now()
		}
	})

	if err != nil {
		if ctx.Err() == context.Canceled {
			return
		}
		database.UpdateTaskStatus(taskID, "Failed", "", "", "")
		if msg != nil {
			bh.bot.Edit(msg, "❌ <b>Bridge Download Failed:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
		}
		return
	}

	// Update size if it was unknown (e.g. from extension just clicking link)
	if fileSize <= 0 {
		if stat, statErr := os.Stat(downloadPath); statErr == nil {
			fileSize = stat.Size()
		}
	}

	// === UPLOAD PHASE ===
	database.UpdateTaskDownloadProgress(taskID, 100, 0)
	database.UpdateTaskStatus(taskID, "Uploading", "", "", "")

	if msg != nil {
		bh.bot.Edit(msg, fmt.Sprintf("☁️ <b>Bridge Uploading to Drive</b> [#%d]\n\n"+
			"📄 <code>%s</code>\n"+
			"<code>[%s] 0%%</code>\n\n"+
			"⏳ Starting upload...",
			taskID, fileName, progressBar(0)), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	token := &oauth2.Token{
		AccessToken:  settings.AccessToken,
		RefreshToken: settings.RefreshToken,
		Expiry:       settings.TokenExpiry,
		TokenType:    "Bearer",
	}

	uploaderInstance, err := uploader.NewDriveUploader(context.Background(), token, settings.GoogleClientID, settings.GoogleClientSecret)
	if err != nil {
		database.UpdateTaskStatus(taskID, "Failed", "", "", "")
		if msg != nil {
			bh.bot.Edit(msg, "❌ <b>Upload Setup Failed:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
		}
		return
	}

	lastUpdate = time.Now()
	driveLink, driveFileID, err := uploaderInstance.UploadFile(ctx, downloadPath, fileName, func(uploaded, total, speed int64) {
		if time.Since(lastUpdate) > 3*time.Second {
			progress := 0
			if total > 0 {
				progress = int((float64(uploaded) / float64(total)) * 100)
			}
			database.UpdateTaskUploadProgress(taskID, progress, speed)

			if msg != nil {
				eta := calcETA(total-uploaded, speed)
				elapsed := time.Since(startTime).Round(time.Second).String()

				text := fmt.Sprintf("☁️ <b>Bridge Uploading</b> [#%d]\n\n"+
					"📄 <code>%s</code>\n"+
					"<code>[%s] %d%%</code>\n\n"+
					"⚡ %s/s  •  ⏳ %s  •  ⏱ %s\n\n"+
					"<i>/cancel %d to abort</i>",
					taskID, fileName,
					progressBar(progress), progress,
					formatSize(speed), eta, elapsed, taskID)

				bh.bot.Edit(msg, text, &tele.SendOptions{ParseMode: tele.ModeHTML})
			}
			lastUpdate = time.Now()
		}
	})

	os.Remove(downloadPath)

	if err != nil {
		if ctx.Err() == context.Canceled {
			return
		}
		database.UpdateTaskStatus(taskID, "Failed", "", "", "")
		if msg != nil {
			bh.bot.Edit(msg, "❌ <b>Bridge Upload Failed:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
		}
		return
	}

	// === COMPLETION ===
	finalElapsed := time.Since(startTime).Round(time.Second).String()
	database.UpdateTaskUploadProgress(taskID, 100, 0)
	database.UpdateTaskStatus(taskID, "Completed", driveLink, driveFileID, finalElapsed)

	if msg != nil {
		completeText := fmt.Sprintf("✅ <b>Bridge Task #%d Complete!</b>\n\n"+
			"📄 <b>File:</b> <code>%s</code>\n"+
			"📦 <b>Size:</b> %s\n"+
			"⏱ <b>Time:</b> %s\n\n"+
			"<code>[████████████████████] 100%%</code>",
			taskID, fileName, formatSize(fileSize), finalElapsed)

		if driveLink != "" {
			bh.bot.Edit(msg, completeText, &tele.SendOptions{ParseMode: tele.ModeHTML}, driveButton(driveLink))
		} else {
			bh.bot.Edit(msg, completeText, &tele.SendOptions{ParseMode: tele.ModeHTML})
		}
	}
}
