package bot

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/downloader/telegram-cloud-transfer/database"
	"github.com/downloader/telegram-cloud-transfer/downloader"
	"github.com/downloader/telegram-cloud-transfer/uploader"
	"github.com/dustin/go-humanize"
	"golang.org/x/oauth2"
	tele "gopkg.in/telebot.v3"
)

const maxDailyTasksNormal = 5

// defaultConcurrentTasks is used when the configured value is invalid.
const defaultConcurrentTasks = 3

type BotHandler struct {
	bot       *tele.Bot
	torrentDL *downloader.TorrentDownloader

	// sem is a counting semaphore that bounds the number of tasks actively
	// downloading/uploading at once (acts as a simple work queue).
	sem chan struct{}

	// edits stores per-message throttling state (see message.go).
	edits sync.Map
}

// NewBot initializes the telegram bot with telebot.v3
func NewBot(token string, apiURL string, dlDir string) (*BotHandler, error) {
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

	td, err := downloader.NewTorrentDownloader(dlDir)
	if err != nil {
		log.Printf("Warning: Failed to init torrent downloader: %v", err)
	}

	// Size the work queue from settings (fallback to a sensible default).
	concurrency := defaultConcurrentTasks
	if settings, err := database.GetSettings(); err == nil && settings.ConcurrentTasks > 0 {
		concurrency = settings.ConcurrentTasks
	}

	bh := &BotHandler{
		bot:       b,
		torrentDL: td,
		sem:       make(chan struct{}, concurrency),
	}
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
	bh.bot.Handle("\fcancel_task", bh.handleCancelCallback)

	bh.bot.Handle(tele.OnText, bh.handleText)

	// Every media kind funnels into the same pipeline so videos, audio, photos,
	// voice notes and GIFs are all supported, not just generic documents.
	bh.bot.Handle(tele.OnDocument, bh.handleDocument)
	bh.bot.Handle(tele.OnVideo, bh.handleMedia)
	bh.bot.Handle(tele.OnAudio, bh.handleMedia)
	bh.bot.Handle(tele.OnVoice, bh.handleMedia)
	bh.bot.Handle(tele.OnVideoNote, bh.handleMedia)
	bh.bot.Handle(tele.OnAnimation, bh.handleMedia)
	bh.bot.Handle(tele.OnPhoto, bh.handleMedia)
}

// ===== Concurrency / queue =====

// acquireSlot blocks until a work slot is free or the context is cancelled.
func (bh *BotHandler) acquireSlot(ctx context.Context) bool {
	select {
	case bh.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (bh *BotHandler) releaseSlot() {
	select {
	case <-bh.sem:
	default:
	}
}

// slotBusy reports whether all work slots are currently taken (best-effort, used
// only to decide whether to show a "queued" notice to the user).
func (bh *BotHandler) slotBusy() bool {
	return len(bh.sem) >= cap(bh.sem)
}

// ===== Visual Helpers =====

func progressBar(percent int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := percent / 5 // 20-char bar
	empty := 20 - filled
	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

func formatSize(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	return humanize.Bytes(uint64(bytes))
}

func roleLabel(telegramUserID int64) string {
	if database.IsAdminTelegram(telegramUserID) {
		return "👑 Admin"
	}
	return "👤 User"
}

func percentOf(part, total int64) int {
	if total <= 0 {
		return 0
	}
	p := int((float64(part) / float64(total)) * 100)
	if p > 100 {
		p = 100
	}
	if p < 0 {
		p = 0
	}
	return p
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
		"Send me any of these and I'll upload it to Google Drive:\n"+
		"• 📄 A file, 🎬 video, 🎵 audio or 🖼 photo\n"+
		"• 🔗 A direct download link\n"+
		"• 🧲 A magnet link or <code>.torrent</code> file\n\n"+
		"Your role: %s\n\n"+
		"Use the buttons below or type /help for commands.", role)

	return c.Send(text, htmlOpts(), mainMenuKeyboard())
}

func (bh *BotHandler) handleHelp(c tele.Context) error {
	text := "📖 <b>Available Commands</b>\n\n" +
		"/start — Main menu\n" +
		"/help — Show this help\n" +
		"/tasks — View your recent tasks\n" +
		"/status — System status\n" +
		"/me — Your profile &amp; limits\n" +
		"/cancel &lt;id&gt; — Cancel an active task\n\n" +
		"<b>What you can send:</b>\n" +
		"• 📄 Files, 🎬 videos, 🎵 audio, 🖼 photos, 🎙 voice notes\n" +
		"• 🔗 Direct download links (http/https)\n" +
		"• 🧲 Magnet links &amp; <code>.torrent</code> files\n\n" +
		"<i>You can cancel any running task with the Cancel button or /cancel &lt;id&gt;.</i>"

	return c.Send(text, htmlOpts(), mainMenuKeyboard())
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
			"✨ <i>Unlimited file size &amp; downloads</i>",
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

	return c.Send(text, htmlOpts())
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

		line := fmt.Sprintf("%s <b>#%d</b> <code>%s</code>\n   └ %s", icon, t.ID, esc(t.FileName), t.Status)
		if t.DriveLink != "" {
			line += fmt.Sprintf(" • <a href=\"%s\">Drive</a>", t.DriveLink)
		}
		if t.ElapsedTime != "" {
			line += fmt.Sprintf(" • %s", t.ElapsedTime)
		}
		text += line + "\n\n"
	}

	return c.Send(text, htmlOpts())
}

func (bh *BotHandler) handleCancel(c tele.Context) error {
	args := c.Args()
	if len(args) == 0 {
		return c.Send("⚠️ Usage: <code>/cancel &lt;task_id&gt;</code>", htmlOpts())
	}
	taskID, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil {
		return c.Send("⚠️ Please provide a valid numeric task ID.")
	}

	if database.CancelTask(taskID) {
		database.UpdateTaskStatus(taskID, "Cancelled", "", "", "")
		return c.Send(fmt.Sprintf("🚫 Task <b>#%d</b> cancelled successfully.", taskID), htmlOpts())
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

	return c.Send(text, htmlOpts())
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

func (bh *BotHandler) handleCancelCallback(c tele.Context) error {
	taskID, err := strconv.Atoi(strings.TrimSpace(c.Data()))
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid task."})
	}

	if database.CancelTask(taskID) {
		database.UpdateTaskStatus(taskID, "Cancelled", "", "", "")
		return c.Respond(&tele.CallbackResponse{Text: fmt.Sprintf("Task #%d cancelled.", taskID)})
	}
	return c.Respond(&tele.CallbackResponse{Text: "Task is not active anymore."})
}

// ===== Text Handler =====

func (bh *BotHandler) handleText(c tele.Context) error {
	text := strings.TrimSpace(c.Text())
	if text == "" {
		return nil
	}

	if strings.HasPrefix(text, "magnet:?") {
		return bh.handleMagnet(c, text)
	}

	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
		return bh.handleDirectLink(c, text)
	}

	return c.Send("🤖 Send me a <b>file, video, audio, photo</b>, a <b>direct link</b>, "+
		"a <b>magnet link</b> or a <code>.torrent</code> file and I'll upload it to Google Drive.", htmlOpts())
}

// ===== Direct Link Handler =====

func (bh *BotHandler) handleDirectLink(c tele.Context, downloadURL string) error {
	telegramUserID := c.Sender().ID
	isAdmin := database.IsAdminTelegram(telegramUserID)

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

	fileSize, fileName := probeRemoteFile(downloadURL)

	// --- Role-based limits ---
	if !isAdmin {
		dailyCount, _ := database.GetDailyTaskCount(telegramUserID)
		if dailyCount >= maxDailyTasksNormal {
			bh.editFinal(msg, fmt.Sprintf("🚫 <b>Daily limit reached!</b>\n\n"+
				"You've used <b>%d/%d</b> downloads today.\n"+
				"Try again tomorrow or contact an admin.",
				dailyCount, maxDailyTasksNormal))
			return nil
		}

		maxSize := settings.MaxFileSizeNormal
		if maxSize <= 0 {
			maxSize = 4294967296 // 4GB default
		}
		if fileSize > maxSize {
			bh.editFinal(msg, fmt.Sprintf("🚫 <b>File too large!</b>\n\n"+
				"📦 <b>File size:</b> %s\n"+
				"📏 <b>Max allowed:</b> %s\n\n"+
				"Contact an admin for larger files.",
				formatSize(fileSize), formatSize(maxSize)))
			return nil
		}
	}

	bh.editFinal(msg, fmt.Sprintf("🔗 <b>Direct Link Received</b>\n\n"+
		"📄 <b>Name:</b> <code>%s</code>\n"+
		"📦 <b>Size:</b> %s\n"+
		"⏳ <b>Status:</b> Queued...",
		esc(fileName), formatSize(fileSize)))

	taskID, err := database.CreateTaskWithTelegram(1, telegramUserID, fileName, fileSize, "Direct Link")
	if err != nil {
		bh.editFinal(msg, "❌ Error creating task in database.")
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	database.RegisterCancelFunc(taskID, cancel)

	go func() {
		defer cancel()
		defer bh.cleanupEditState(msg)
		defer func() {
			if r := recover(); r != nil {
				database.UpdateTaskStatus(taskID, "Failed", "", "", "")
				bh.editFinal(msg, fmt.Sprintf("❌ <b>Task #%d failed unexpectedly.</b>", taskID))
			}
		}()

		// Wait for a free work slot (queue).
		if bh.slotBusy() {
			bh.editFinal(msg, fmt.Sprintf("⏳ <b>Queued</b> [#%d]\n\n📄 <code>%s</code>\n\n"+
				"<i>Waiting for a free slot…</i>", taskID, fileName), cancelButton(taskID))
		}
		if !bh.acquireSlot(ctx) {
			database.UpdateTaskStatus(taskID, "Cancelled", "", "", "")
			return
		}
		defer bh.releaseSlot()

		// === STREAMING UPLOAD PHASE ===
		database.UpdateTaskStatus(taskID, "Uploading", "", "", "")

		bh.editFinal(msg, fmt.Sprintf("☁️ <b>Streaming to Google Drive</b> [#%d]\n\n"+
			"📄 <code>%s</code>\n"+
			"<code>[%s] 0%%</code>\n\n"+
			"⏳ Connecting...",
			taskID, fileName, progressBar(0)), cancelButton(taskID))

		uploaderInstance, err := bh.newUploader(settings)
		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.editFinal(msg, "❌ <b>Upload Setup Failed:</b> "+err.Error())
			return
		}

		req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.editFinal(msg, "❌ <b>Download Failed:</b> "+err.Error())
			return
		}
		req.Header.Set("User-Agent", browserUserAgent)

		client := &http.Client{Timeout: 0} // no timeout for large streams
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode >= 400 {
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.editFinal(msg, "❌ <b>Download Failed:</b> HTTP Error or Unreachable")
			if resp != nil {
				resp.Body.Close()
			}
			return
		}
		defer resp.Body.Close()

		startTime := time.Now()
		lastUpdate := time.Now()

		driveLink, driveFileID, err := uploaderInstance.UploadStream(ctx, resp.Body, fileName, fileSize, func(uploaded, total, speed int64) {
			if time.Since(lastUpdate) < minEditInterval {
				return
			}
			lastUpdate = time.Now()

			progress := percentOf(uploaded, total)
			database.UpdateTaskUploadProgress(taskID, progress, speed)

			eta := calcETA(total-uploaded, speed)
			if total <= 0 {
				eta = "unknown"
			}
			bh.editMsg(msg, renderProgress("☁️ Streaming to Drive", taskID, fileName, progress, speed, eta, startTime), cancelButton(taskID))
		})

		if err != nil {
			if ctx.Err() == context.Canceled {
				database.UpdateTaskStatus(taskID, "Cancelled", "", "", "")
				bh.editFinal(msg, fmt.Sprintf("🚫 <b>Task #%d cancelled.</b>", taskID))
				return
			}
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.editFinal(msg, "❌ <b>Upload Failed:</b> "+err.Error())
			return
		}

		bh.finishTask(msg, taskID, fileName, fileSize, startTime, driveLink, driveFileID)
	}()

	return nil
}

// probeRemoteFile attempts to discover the size and filename of a remote URL
// using a HEAD request (falling back to a GET that is aborted after headers).
func probeRemoteFile(downloadURL string) (int64, string) {
	client := &http.Client{Timeout: 15 * time.Second}

	doProbe := func(method string) (*http.Response, error) {
		req, err := http.NewRequest(method, downloadURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", browserUserAgent)
		if method == "GET" {
			// Abort body download right after headers arrive.
			ctx, cancel := context.WithCancel(context.Background())
			req = req.WithContext(ctx)
			resp, err := client.Do(req)
			cancel()
			return resp, err
		}
		return client.Do(req)
	}

	resp, err := doProbe("HEAD")
	if err != nil || resp == nil || resp.StatusCode >= 400 {
		if resp != nil {
			resp.Body.Close()
		}
		resp, err = doProbe("GET")
	}
	if err != nil || resp == nil {
		if resp != nil {
			resp.Body.Close()
		}
		return 0, fmt.Sprintf("download_%d", time.Now().Unix())
	}
	defer resp.Body.Close()

	fileSize := resp.ContentLength
	if fileSize < 0 {
		fileSize = 0
	}

	fileName := filenameFromContentDisposition(resp.Header.Get("Content-Disposition"))
	if fileName == "" {
		fileName = path.Base(resp.Request.URL.Path)
		if fileName == "/" || fileName == "." || fileName == "" {
			fileName = fmt.Sprintf("download_%d", time.Now().Unix())
		}
	}
	return fileSize, fileName
}

func filenameFromContentDisposition(cd string) string {
	if cd == "" {
		return ""
	}
	idx := strings.Index(cd, "filename=")
	if idx == -1 {
		return ""
	}
	name := cd[idx+len("filename="):]
	name = strings.Trim(name, `"' `)
	if semi := strings.Index(name, ";"); semi != -1 {
		name = name[:semi]
	}
	return strings.Trim(name, `"' `)
}

const browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"

// ===== Document / Media Handlers =====

func (bh *BotHandler) handleDocument(c tele.Context) error {
	doc := c.Message().Document
	if doc == nil {
		return nil
	}

	if strings.HasSuffix(strings.ToLower(doc.FileName), ".torrent") {
		return bh.handleTorrentFile(c, doc)
	}

	return bh.processFile(c, doc.FileID, doc.FileName, doc.FileSize, "Telegram Document")
}

// handleMedia handles videos, audio, photos, voice notes, video notes and GIFs
// by extracting the underlying file and reusing the shared pipeline.
func (bh *BotHandler) handleMedia(c tele.Context) error {
	fileID, fileName, size, inputType, ok := extractMedia(c.Message())
	if !ok {
		return nil
	}
	return bh.processFile(c, fileID, fileName, size, inputType)
}

// extractMedia pulls the file id, a reasonable filename, the size and a label
// from whatever media type the message carries.
func extractMedia(m *tele.Message) (fileID, fileName string, size int64, inputType string, ok bool) {
	stamp := time.Now().Unix()
	switch {
	case m.Video != nil:
		name := m.Video.FileName
		if name == "" {
			name = fmt.Sprintf("video_%d.mp4", stamp)
		}
		return m.Video.FileID, name, m.Video.FileSize, "Video", true
	case m.Audio != nil:
		name := m.Audio.FileName
		if name == "" {
			if m.Audio.Title != "" {
				name = m.Audio.Title + ".mp3"
			} else {
				name = fmt.Sprintf("audio_%d.mp3", stamp)
			}
		}
		return m.Audio.FileID, name, m.Audio.FileSize, "Audio", true
	case m.Animation != nil:
		name := m.Animation.FileName
		if name == "" {
			name = fmt.Sprintf("animation_%d.gif", stamp)
		}
		return m.Animation.FileID, name, m.Animation.FileSize, "Animation", true
	case m.Voice != nil:
		return m.Voice.FileID, fmt.Sprintf("voice_%d.ogg", stamp), m.Voice.FileSize, "Voice", true
	case m.VideoNote != nil:
		return m.VideoNote.FileID, fmt.Sprintf("videonote_%d.mp4", stamp), m.VideoNote.FileSize, "Video Note", true
	case m.Photo != nil:
		return m.Photo.FileID, fmt.Sprintf("photo_%d.jpg", stamp), m.Photo.FileSize, "Photo", true
	}
	return "", "", 0, "", false
}

// processFile is the shared download → upload pipeline for any Telegram media.
func (bh *BotHandler) processFile(c tele.Context, fileID, fileName string, fileSize int64, inputType string) error {
	telegramUserID := c.Sender().ID
	isAdmin := database.IsAdminTelegram(telegramUserID)

	settings, err := database.GetSettings()
	if err != nil {
		return c.Send("❌ Internal error: Could not load settings.")
	}

	if settings.AccessToken == "" {
		return c.Send("⚠️ Google Drive is not connected.\nPlease connect via the Dashboard.")
	}

	if strings.TrimSpace(fileName) == "" {
		fileName = fmt.Sprintf("file_%d", time.Now().Unix())
	}

	// --- Role-based limits ---
	if !isAdmin {
		dailyCount, _ := database.GetDailyTaskCount(telegramUserID)
		if dailyCount >= maxDailyTasksNormal {
			return c.Send(fmt.Sprintf("🚫 <b>Daily limit reached!</b>\n\n"+
				"You've used <b>%d/%d</b> downloads today.\n"+
				"Try again tomorrow or contact an admin.",
				dailyCount, maxDailyTasksNormal), htmlOpts())
		}

		maxSize := settings.MaxFileSizeNormal
		if maxSize <= 0 {
			maxSize = 4294967296 // 4GB default
		}
		if fileSize > maxSize {
			return c.Send(fmt.Sprintf("🚫 <b>File too large!</b>\n\n"+
				"📦 <b>Your file:</b> %s\n"+
				"📏 <b>Max allowed:</b> %s\n\n"+
				"Contact an admin for larger files.",
				formatSize(fileSize), formatSize(maxSize)), htmlOpts())
		}
	}

	msg, err := bh.bot.Send(c.Chat(), fmt.Sprintf("📎 <b>%s Received</b>\n\n"+
		"📄 <b>Name:</b> <code>%s</code>\n"+
		"📦 <b>Size:</b> %s\n"+
		"⏳ <b>Status:</b> Queued...",
		inputType, esc(fileName), formatSize(fileSize)), htmlOpts())
	if err != nil {
		return err
	}

	taskID, err := database.CreateTaskWithTelegram(1, telegramUserID, fileName, fileSize, inputType)
	if err != nil {
		bh.editFinal(msg, "❌ Error creating task in database.")
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	database.RegisterCancelFunc(taskID, cancel)

	go func() {
		defer cancel()
		defer bh.cleanupEditState(msg)
		defer func() {
			if r := recover(); r != nil {
				database.UpdateTaskStatus(taskID, "Failed", "", "", "")
				bh.editFinal(msg, fmt.Sprintf("❌ <b>Task #%d failed unexpectedly.</b>", taskID))
			}
		}()

		// Wait for a free work slot (queue).
		if bh.slotBusy() {
			bh.editFinal(msg, fmt.Sprintf("⏳ <b>Queued</b> [#%d]\n\n📄 <code>%s</code>\n\n"+
				"<i>Waiting for a free slot…</i>", taskID, fileName), cancelButton(taskID))
		}
		if !bh.acquireSlot(ctx) {
			database.UpdateTaskStatus(taskID, "Cancelled", "", "", "")
			return
		}
		defer bh.releaseSlot()

		database.UpdateTaskStatus(taskID, "Downloading", "", "", "")
		startTime := time.Now()

		// Background tracker for the local proxy download phase (Local Bot API
		// writes the file to disk; we watch its size to show progress).
		trackCtx, trackCancel := context.WithCancel(context.Background())
		trackDone := make(chan struct{})
		go func() {
			defer close(trackDone)
			var lastSize int64
			var lastReport time.Time
			for {
				select {
				case <-trackCtx.Done():
					return
				case <-ctx.Done():
					return
				case <-time.After(minEditInterval):
					var maxSize int64
					filepath.Walk("/var/lib/telegram-bot-api", func(p string, info os.FileInfo, err error) error {
						if err != nil || info.IsDir() {
							return nil
						}
						if time.Since(info.ModTime()) < 10*time.Second && info.Size() > maxSize {
							maxSize = info.Size()
						}
						return nil
					})

					if maxSize > 0 {
						speed := int64(0)
						if lastSize > 0 && maxSize > lastSize && !lastReport.IsZero() {
							speed = int64(float64(maxSize-lastSize) / time.Since(lastReport).Seconds())
						}

						progress := percentOf(maxSize, fileSize)
						database.UpdateTaskDownloadProgress(taskID, progress, speed)
						eta := calcETA(fileSize-maxSize, speed)
						bh.editMsg(msg, renderProgress("📥 Downloading", taskID, fileName, progress, speed, eta, startTime), cancelButton(taskID))

						lastSize = maxSize
						lastReport = time.Now()
					}
				}
			}
		}()

		file, err := bh.bot.FileByID(fileID)
		trackCancel()
		<-trackDone

		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.editFinal(msg, "❌ <b>Error getting file from Telegram:</b> "+err.Error())
			return
		}

		uploaderInstance, err := bh.newUploader(settings)
		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.editFinal(msg, "❌ <b>Upload Setup Failed:</b> "+err.Error())
			return
		}

		var driveLink, driveFileID string

		if stat, statErr := os.Stat(file.FilePath); statErr == nil && !stat.IsDir() {
			// File is available locally (Local Bot API Server) — upload from disk.
			database.UpdateTaskDownloadProgress(taskID, 100, 0)
			database.UpdateTaskStatus(taskID, "Uploading", "", "", "")
			bh.editFinal(msg, fmt.Sprintf("☁️ <b>Uploading to Google Drive</b> [#%d]\n\n"+
				"📄 <code>%s</code>\n"+
				"<code>[%s] 0%%</code>\n\n"+
				"⏳ Starting upload...",
				taskID, fileName, progressBar(0)), cancelButton(taskID))

			lastUpdate := time.Now()
			driveLink, driveFileID, err = uploaderInstance.UploadFile(ctx, file.FilePath, fileName, func(uploaded, total, speed int64) {
				if time.Since(lastUpdate) < minEditInterval {
					return
				}
				lastUpdate = time.Now()
				progress := percentOf(uploaded, total)
				database.UpdateTaskUploadProgress(taskID, progress, speed)
				eta := calcETA(total-uploaded, speed)
				bh.editMsg(msg, renderProgress("☁️ Uploading", taskID, fileName, progress, speed, eta, startTime), cancelButton(taskID))
			})
		} else {
			// Remote standard API — stream directly to Google Drive.
			apiBase := settings.TelegramAPIEndpoint
			if apiBase == "" {
				apiBase = "https://api.telegram.org"
			}
			fileURL := fmt.Sprintf("%s/file/bot%s/%s", apiBase, settings.BotToken, file.FilePath)

			database.UpdateTaskStatus(taskID, "Uploading", "", "", "")
			bh.editFinal(msg, fmt.Sprintf("☁️ <b>Streaming to Google Drive</b> [#%d]\n\n"+
				"📄 <code>%s</code>\n"+
				"<code>[%s] 0%%</code>\n\n"+
				"⏳ Connecting...",
				taskID, fileName, progressBar(0)), cancelButton(taskID))

			req, reqErr := http.NewRequestWithContext(ctx, "GET", fileURL, nil)
			if reqErr != nil {
				database.UpdateTaskStatus(taskID, "Failed", "", "", "")
				bh.editFinal(msg, "❌ <b>Download Failed:</b> "+reqErr.Error())
				return
			}

			client := &http.Client{Timeout: 0}
			resp, doErr := client.Do(req)
			if doErr != nil || resp.StatusCode >= 400 {
				database.UpdateTaskStatus(taskID, "Failed", "", "", "")
				bh.editFinal(msg, "❌ <b>Download Failed:</b> HTTP Error")
				if resp != nil {
					resp.Body.Close()
				}
				return
			}
			defer resp.Body.Close()

			lastUpdate := time.Now()
			driveLink, driveFileID, err = uploaderInstance.UploadStream(ctx, resp.Body, fileName, fileSize, func(uploaded, total, speed int64) {
				if time.Since(lastUpdate) < minEditInterval {
					return
				}
				lastUpdate = time.Now()
				progress := percentOf(uploaded, total)
				database.UpdateTaskUploadProgress(taskID, progress, speed)
				eta := calcETA(total-uploaded, speed)
				bh.editMsg(msg, renderProgress("☁️ Streaming to Drive", taskID, fileName, progress, speed, eta, startTime), cancelButton(taskID))
			})
		}

		if err != nil {
			if ctx.Err() == context.Canceled {
				database.UpdateTaskStatus(taskID, "Cancelled", "", "", "")
				bh.editFinal(msg, fmt.Sprintf("🚫 <b>Task #%d cancelled.</b>", taskID))
				return
			}
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.editFinal(msg, "❌ <b>Upload Failed:</b> "+err.Error())
			return
		}

		bh.finishTask(msg, taskID, fileName, fileSize, startTime, driveLink, driveFileID)
	}()

	return nil
}

// ===== Torrent Handlers =====

func (bh *BotHandler) handleMagnet(c tele.Context, magnetLink string) error {
	if bh.torrentDL == nil {
		return c.Send("❌ Torrent downloading is currently unavailable.")
	}

	telegramUserID := c.Sender().ID
	msg, err := bh.bot.Send(c.Chat(), "🧲 Initializing torrent from magnet...")
	if err != nil {
		return err
	}

	go bh.startTorrentTask(c, msg, telegramUserID, magnetLink, "", "Magnet Link")
	return nil
}

func (bh *BotHandler) handleTorrentFile(c tele.Context, doc *tele.Document) error {
	if bh.torrentDL == nil {
		return c.Send("❌ Torrent downloading is currently unavailable.")
	}

	settings, err := database.GetSettings()
	if err != nil {
		return c.Send("❌ Internal error: Could not load settings.")
	}

	telegramUserID := c.Sender().ID
	msg, err := bh.bot.Send(c.Chat(), "📥 Downloading .torrent file...")
	if err != nil {
		return err
	}

	file, err := bh.bot.FileByID(doc.FileID)
	if err != nil {
		bh.editFinal(msg, "❌ Failed to fetch .torrent file from Telegram.")
		return err
	}

	apiBase := settings.TelegramAPIEndpoint
	if apiBase == "" {
		apiBase = "https://api.telegram.org"
	}
	fileURL := fmt.Sprintf("%s/file/bot%s/%s", apiBase, settings.BotToken, file.FilePath)

	torrentPath, err := downloader.DownloadHTTP(context.Background(), fileURL, settings.DownloadDirectory, doc.FileName, nil)
	if err != nil {
		bh.editFinal(msg, "❌ Failed to download .torrent file.")
		return err
	}

	go bh.startTorrentTask(c, msg, telegramUserID, "", torrentPath, ".torrent File")
	return nil
}

func (bh *BotHandler) startTorrentTask(c tele.Context, msg *tele.Message, telegramUserID int64, magnetLink, torrentFilePath, inputType string) {
	defer bh.cleanupEditState(msg)

	isAdmin := database.IsAdminTelegram(telegramUserID)
	settings, _ := database.GetSettings()

	if settings.AccessToken == "" {
		bh.editFinal(msg, "⚠️ Google Drive is not connected.\nPlease connect via the Dashboard.")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	taskID, _ := database.CreateTaskWithTelegram(1, telegramUserID, "Resolving metadata...", 0, inputType)
	database.RegisterCancelFunc(taskID, cancel)

	defer func() {
		if r := recover(); r != nil {
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.editFinal(msg, fmt.Sprintf("❌ <b>Task #%d failed unexpectedly.</b>", taskID))
		}
	}()

	// Acquire a work slot before doing any heavy lifting (queue behaviour).
	if bh.slotBusy() {
		bh.editFinal(msg, fmt.Sprintf("⏳ <b>Queued</b> [#%d]\n\n<i>Waiting for a free slot…</i>", taskID), cancelButton(taskID))
	}
	if !bh.acquireSlot(ctx) {
		database.UpdateTaskStatus(taskID, "Cancelled", "", "", "")
		return
	}
	defer bh.releaseSlot()

	database.UpdateTaskStatus(taskID, "Downloading", "", "", "")
	bh.editFinal(msg, fmt.Sprintf("🧲 <b>Fetching torrent metadata</b> [#%d]...\n"+
		"<i>This can take a moment depending on seeders.</i>",
		taskID), cancelButton(taskID))

	startTime := time.Now()
	var finalSize int64
	var finalName string
	var limitRejected bool
	var lastUpdate time.Time

	callback := func(fileName string, completed, total, speed int64, peers int) {
		if finalSize == 0 && total > 0 {
			finalSize = total
			finalName = fileName

			if !isAdmin {
				dailyCount, _ := database.GetDailyTaskCount(telegramUserID)
				// -1 because we already created the task row above.
				if dailyCount-1 >= maxDailyTasksNormal {
					limitRejected = true
					database.UpdateTaskStatus(taskID, "Failed", "", "", "")
					cancel()
					bh.editFinal(msg, "🚫 <b>Daily limit reached!</b>\n\nContact an admin.")
					return
				}

				maxSize := settings.MaxFileSizeNormal
				if maxSize <= 0 {
					maxSize = 4294967296
				}
				if total > maxSize {
					limitRejected = true
					database.UpdateTaskStatus(taskID, "Failed", "", "", "")
					cancel()
					bh.editFinal(msg, fmt.Sprintf("🚫 <b>Torrent too large!</b>\n\n📦 <b>Size:</b> %s\n📏 <b>Max:</b> %s",
						formatSize(total), formatSize(maxSize)))
					return
				}
			}

			database.DB.Exec("UPDATE tasks SET file_name = ?, file_size = ? WHERE id = ?", fileName, total, taskID)
		}

		if time.Since(lastUpdate) < minEditInterval {
			return
		}
		lastUpdate = time.Now()

		progress := percentOf(completed, total)
		database.UpdateTaskDownloadProgress(taskID, progress, speed)
		eta := calcETA(total-completed, speed)
		elapsed := time.Since(startTime).Round(time.Second).String()

		text := fmt.Sprintf("🧲 <b>Downloading Torrent</b> [#%d]\n\n"+
			"📄 <code>%s</code>\n"+
			"<code>[%s] %d%%</code>\n\n"+
			"⚡ %s/s  •  👥 %d peers\n"+
			"⏳ %s  •  ⏱ %s",
			taskID, esc(fileName),
			progressBar(progress), progress,
			formatSize(speed), peers, eta, elapsed)

		bh.editMsg(msg, text, cancelButton(taskID))
	}

	var resultPath string
	var err error
	if magnetLink != "" {
		resultPath, err = bh.torrentDL.DownloadMagnet(ctx, magnetLink, callback)
	} else {
		resultPath, err = bh.torrentDL.DownloadFile(ctx, torrentFilePath, callback)
		os.Remove(torrentFilePath)
	}

	if limitRejected {
		return
	}

	if err != nil {
		if ctx.Err() == context.Canceled {
			database.UpdateTaskStatus(taskID, "Cancelled", "", "", "")
			bh.editFinal(msg, fmt.Sprintf("🚫 <b>Task #%d cancelled.</b>", taskID))
			return
		}
		database.UpdateTaskStatus(taskID, "Failed", "", "", "")
		bh.editFinal(msg, "❌ <b>Torrent Failed:</b> "+err.Error())
		return
	}

	if finalName == "" {
		finalName = filepath.Base(resultPath)
	}

	// === ZIP PHASE (folders are zipped before upload) ===
	uploadPath := resultPath
	uploadName := finalName
	if stat, statErr := os.Stat(resultPath); statErr == nil && stat.IsDir() {
		bh.editFinal(msg, fmt.Sprintf("📦 <b>Zipping Folder</b> [#%d]...\n\n📄 <code>%s.zip</code>", taskID, finalName))
		zipPath := resultPath + ".zip"
		if zipErr := downloader.ZipDirectory(resultPath, zipPath); zipErr == nil {
			uploadPath = zipPath
			uploadName = finalName + ".zip"
			os.RemoveAll(resultPath)
		}
	}

	// === UPLOAD PHASE ===
	database.UpdateTaskDownloadProgress(taskID, 100, 0)
	database.UpdateTaskStatus(taskID, "Uploading", "", "", "")

	uploaderInstance, err := bh.newUploader(settings)
	if err != nil {
		bh.editFinal(msg, "❌ <b>Upload Setup Failed:</b> "+err.Error())
		return
	}

	lastUpload := time.Now()
	driveLink, driveFileID, err := uploaderInstance.UploadFile(ctx, uploadPath, uploadName, func(uploaded, total, speed int64) {
		if time.Since(lastUpload) < minEditInterval {
			return
		}
		lastUpload = time.Now()
		progress := percentOf(uploaded, total)
		database.UpdateTaskUploadProgress(taskID, progress, speed)
		eta := calcETA(total-uploaded, speed)
		bh.editMsg(msg, renderProgress("☁️ Uploading to Drive", taskID, uploadName, progress, speed, eta, startTime), cancelButton(taskID))
	})

	os.Remove(uploadPath)

	if err != nil {
		if ctx.Err() == context.Canceled {
			database.UpdateTaskStatus(taskID, "Cancelled", "", "", "")
			bh.editFinal(msg, fmt.Sprintf("🚫 <b>Task #%d cancelled.</b>", taskID))
			return
		}
		database.UpdateTaskStatus(taskID, "Failed", "", "", "")
		bh.editFinal(msg, "❌ <b>Upload Failed:</b> "+err.Error())
		return
	}

	bh.finishTask(msg, taskID, uploadName, finalSize, startTime, driveLink, driveFileID)
}

// ===== Shared helpers =====

// newUploader builds a Google Drive uploader from the current settings/token.
func (bh *BotHandler) newUploader(settings database.Settings) (*uploader.DriveUploader, error) {
	token := &oauth2.Token{
		AccessToken:  settings.AccessToken,
		RefreshToken: settings.RefreshToken,
		Expiry:       settings.TokenExpiry,
		TokenType:    "Bearer",
	}
	return uploader.NewDriveUploader(context.Background(), token, settings.GoogleClientID, settings.GoogleClientSecret)
}

// renderProgress builds a consistent progress message used across all phases.
func renderProgress(title string, taskID int, fileName string, progress int, speed int64, eta string, startTime time.Time) string {
	elapsed := time.Since(startTime).Round(time.Second).String()
	return fmt.Sprintf("%s [#%d]\n\n"+
		"📄 <code>%s</code>\n"+
		"<code>[%s] %d%%</code>\n\n"+
		"⚡ %s/s  •  ⏳ %s  •  ⏱ %s",
		title, taskID, esc(fileName),
		progressBar(progress), progress,
		formatSize(speed), eta, elapsed)
}

// finishTask marks a task complete and renders the final success message.
func (bh *BotHandler) finishTask(msg *tele.Message, taskID int, fileName string, fileSize int64, startTime time.Time, driveLink, driveFileID string) {
	finalElapsed := time.Since(startTime).Round(time.Second).String()
	database.UpdateTaskUploadProgress(taskID, 100, 0)
	database.UpdateTaskStatus(taskID, "Completed", driveLink, driveFileID, finalElapsed)

	completeText := fmt.Sprintf("✅ <b>Task #%d Complete!</b>\n\n"+
		"📄 <b>File:</b> <code>%s</code>\n"+
		"📦 <b>Size:</b> %s\n"+
		"⏱ <b>Time:</b> %s\n\n"+
		"<code>[████████████████████] 100%%</code>",
		taskID, esc(fileName), formatSize(fileSize), finalElapsed)

	if driveLink != "" {
		bh.editFinal(msg, completeText, driveButton(driveLink))
	} else {
		bh.editFinal(msg, completeText)
	}
}

// calcETA computes estimated time remaining
func calcETA(bytesRemaining, speed int64) string {
	if speed <= 0 {
		return "calculating..."
	}
	seconds := bytesRemaining / speed
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh %dm", seconds/3600, (seconds%3600)/60)
}
