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
	workerURL string
	adminKey  string

	// sem is a counting semaphore that bounds the number of tasks actively
	// downloading/uploading at once (acts as a simple work queue).
	sem chan struct{}

	// edits stores per-message throttling state (see message.go).
	edits sync.Map
}

// NewBot initializes the telegram bot with telebot.v3
func NewBot(token string, apiURL string, dlDir string, workerURL string, adminKey string) (*BotHandler, error) {
	pref := tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
		Client: &http.Client{
			Timeout: 30 * time.Minute,
		},
	}

	if apiURL != "" {
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
		workerURL: workerURL,
		adminKey:  adminKey,
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
	bh.bot.Handle("/grant", bh.handleGrant)
	bh.bot.Handle("/premium", bh.handleGrant)

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
	if database.IsPremiumTelegram(telegramUserID) {
		return "⭐ Premium User"
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
	sender := c.Sender()
	senderName := strings.TrimSpace(sender.FirstName + " " + sender.LastName)

	var userAccInfo string = ""
	if bh.workerURL != "" {
		provRes, err := uploader.ProvisionUser(bh.workerURL, bh.adminKey, sender.ID, sender.Username, senderName)
		if err == nil && provRes != nil {
			if provRes.IsNew {
				userAccInfo = fmt.Sprintf("\n🎉 <b>Your Aurora Play Account Has Been Created!</b>\n"+
					"👤 <b>Username:</b> <code>%s</code>\n"+
					"🔑 <b>Temporary Password:</b> <code>%s</code>\n\n"+
					"🌐 <b>Log in at:</b> https://aurora-play.pages.dev/#/login\n"+
					"⚠️ <i>Please log in and update your password in Profile!</i>\n\n",
					provRes.Username, provRes.TempPassword)
			} else {
				userAccInfo = fmt.Sprintf("\n👤 <b>Aurora Account:</b> <code>%s</code>\n\n", provRes.Username)
			}
		}
	}

	text := fmt.Sprintf("✨ <b>Welcome to Aurora Files!</b>\n"+
		"<i>Powered by Aurora Play Ecosystem</i>\n"+
		"%s"+
		"Send me any of these and I'll upload it to Google Drive & auto-sync to Aurora Play:\n"+
		"• 📄 A file, 🎬 video, 🎵 audio or 🖼 photo\n"+
		"• 🔗 A direct download link\n"+
		"• 🧲 A magnet link or <code>.torrent</code> file\n\n"+
		"🍿 <b>Stream your library anytime at:</b> https://aurora-play.pages.dev", userAccInfo)

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
	isPremium := database.IsPremiumTelegram(userID)
	dailyCount, _ := database.GetDailyTaskCount(userID)

	settings, _ := database.GetSettings()
	maxSize := formatSize(settings.MaxFileSizeNormal)

	var text string
	if isAdmin || isPremium {
		roleTitle := "⭐ Premium User"
		if isAdmin {
			roleTitle = "👑 Admin"
		}
		text = fmt.Sprintf("👤 <b>Your Profile</b>\n\n"+
			"🆔 <b>Telegram ID:</b> <code>%d</code>\n"+
			"✨ <b>Role:</b> %s\n"+
			"📊 <b>Tasks Today:</b> %d\n\n"+
			"✨ <i>Unlimited file size &amp; downloads</i>",
			userID, roleTitle, dailyCount)
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

func (bh *BotHandler) handleGrant(c tele.Context) error {
	if !database.IsAdminTelegram(c.Sender().ID) {
		return c.Send("❌ Only bot admins can grant premium access.")
	}
	args := c.Args()
	if len(args) == 0 {
		return c.Send("⚠️ Usage: <code>/grant &lt;telegram_user_id&gt;</code>", htmlOpts())
	}
	userID, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
	if err != nil {
		return c.Send("⚠️ Please provide a valid numeric Telegram User ID.")
	}
	database.GrantPremium(userID)
	return c.Send(fmt.Sprintf("⭐ Granted <b>Premium Access</b> to Telegram User ID <code>%d</code>!", userID), htmlOpts())
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

const normalUserMaxFileSize int64 = 5368709120 // 5 GB

func checkLimits(telegramUserID int64, fileSize int64) string {
	if database.IsAdminTelegram(telegramUserID) || database.IsPremiumTelegram(telegramUserID) {
		return "" // Unlimited for Admin and Premium users
	}

	dailyCount, _ := database.GetDailyTaskCount(telegramUserID)
	if dailyCount >= maxDailyTasksNormal {
		return fmt.Sprintf("🚫 <b>Daily limit reached!</b>\n\n"+
			"Normal users are limited to <b>%d downloads/day</b>.\n"+
			"Contact an admin to get ⭐ <b>Premium Unlimited Access</b>!", maxDailyTasksNormal)
	}

	if fileSize > 0 && fileSize > normalUserMaxFileSize {
		return fmt.Sprintf("🚫 <b>File exceeds 5 GB limit!</b>\n\n"+
			"📦 <b>File size:</b> %s\n"+
			"📏 <b>Max allowed for normal users:</b> 5.0 GB\n\n"+
			"Contact an admin to get ⭐ <b>Premium Unlimited Access</b>!", formatSize(fileSize))
	}

	return ""
}

// ===== Direct Link Handler =====

func (bh *BotHandler) handleDirectLink(c tele.Context, downloadURL string) error {
	telegramUserID := c.Sender().ID

	msg, err := bh.bot.Send(c.Chat(), "⏳ Fetching link info...")
	if err != nil {
		return err
	}

	fileSize, fileName := probeRemoteFile(downloadURL)

	if limitErr := checkLimits(telegramUserID, fileSize); limitErr != "" {
		bh.editFinal(msg, limitErr)
		return nil
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

		settings, _ := database.GetSettings()
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

			go uploader.SyncTaskProgressToWorker(bh.workerURL, bh.adminKey, uploader.LiveTaskProgressPayload{
				TaskID: taskID, FileName: fileName, FileSize: fileSize,
				Status: "Uploading", Progress: progress, Speed: speed,
				TelegramID: fmt.Sprintf("%d", msg.Sender.ID), TelegramUser: msg.Sender.Username,
			})

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
	if strings.TrimSpace(fileName) == "" {
		fileName = fmt.Sprintf("file_%d", time.Now().Unix())
	}

	if limitErr := checkLimits(telegramUserID, fileSize); limitErr != "" {
		return c.Send(limitErr, htmlOpts())
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

						go uploader.SyncTaskProgressToWorker(bh.workerURL, bh.adminKey, uploader.LiveTaskProgressPayload{
							TaskID: taskID, FileName: fileName, FileSize: fileSize,
							Status: "Downloading", Progress: progress, Speed: speed,
							TelegramID: fmt.Sprintf("%d", c.Sender().ID), TelegramUser: c.Sender().Username,
						})

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

		settings, _ := database.GetSettings()
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

				go uploader.SyncTaskProgressToWorker(bh.workerURL, bh.adminKey, uploader.LiveTaskProgressPayload{
					TaskID: taskID, FileName: fileName, FileSize: fileSize,
					Status: "Uploading", Progress: progress, Speed: speed,
					TelegramID: fmt.Sprintf("%d", c.Sender().ID), TelegramUser: c.Sender().Username,
				})

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

				go uploader.SyncTaskProgressToWorker(bh.workerURL, bh.adminKey, uploader.LiveTaskProgressPayload{
					TaskID: taskID, FileName: fileName, FileSize: fileSize,
					Status: "Uploading", Progress: progress, Speed: speed,
					TelegramID: fmt.Sprintf("%d", c.Sender().ID), TelegramUser: c.Sender().Username,
				})

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
	var lastUpdate time.Time

	callback := func(fileName string, completed, total, speed int64, peers int) {
		if finalSize == 0 && total > 0 {
			finalSize = total
			finalName = fileName

			if limitErr := checkLimits(telegramUserID, total); limitErr != "" {
				database.UpdateTaskStatus(taskID, "Failed", "", "", "")
				cancel()
				bh.editFinal(msg, limitErr)
				return
			}

			if database.DB != nil {
				database.DB.Exec("UPDATE tasks SET file_name = ?, file_size = ? WHERE id = ?", fileName, total, taskID)
			}
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

	settings, _ := database.GetSettings()
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

		go uploader.SyncTaskProgressToWorker(bh.workerURL, bh.adminKey, uploader.LiveTaskProgressPayload{
			TaskID: taskID, FileName: uploadName, FileSize: total,
			Status: "Uploading", Progress: progress, Speed: speed,
			TelegramID: fmt.Sprintf("%d", msg.Sender.ID), TelegramUser: msg.Sender.Username,
		})

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

// newUploader builds a Google Drive uploader from Worker API active accounts (Selection Protocol) or settings fallback.
func (bh *BotHandler) newUploader(settings database.Settings) (*uploader.DriveUploader, error) {
	if bh.workerURL != "" {
		accounts, err := uploader.GetActiveGDriveAccounts(bh.workerURL, bh.adminKey)
		if err == nil && len(accounts) > 0 {
			acc := accounts[0]
			if strings.TrimSpace(acc.AccessToken) != "" || strings.TrimSpace(acc.RefreshToken) != "" {
				token := &oauth2.Token{
					AccessToken:  acc.AccessToken,
					RefreshToken: acc.RefreshToken,
					TokenType:    "Bearer",
				}
				uploaderInst, err := uploader.NewDriveUploader(context.Background(), token, acc.ClientID, acc.ClientSecret)
				if err == nil {
					return uploaderInst, nil
				}
			}
		}
	}

	if strings.TrimSpace(settings.AccessToken) == "" && strings.TrimSpace(settings.RefreshToken) == "" {
		return nil, fmt.Errorf("no connected Google Drive account found!\n\nPlease ask a Super Admin to connect Google Drive in the Admin Panel:\n👉 https://aurora-play.pages.dev/#/superadmin/gdrive")
	}

	token := &oauth2.Token{
		AccessToken:  settings.AccessToken,
		RefreshToken: settings.RefreshToken,
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

// finishTask marks a task complete, auto-syncs to Cloudflare Worker D1, and renders dual Telegram action buttons.
func (bh *BotHandler) finishTask(msg *tele.Message, taskID int, fileName string, fileSize int64, startTime time.Time, driveLink, driveFileID string) {
	finalElapsed := time.Since(startTime).Round(time.Second).String()
	database.UpdateTaskUploadProgress(taskID, 100, 0)
	database.UpdateTaskStatus(taskID, "Completed", driveLink, driveFileID, finalElapsed)

	// Notify worker that task is complete (remove from live tasks)
	go uploader.SyncTaskProgressToWorker(bh.workerURL, bh.adminKey, uploader.LiveTaskProgressPayload{
		TaskID: taskID, FileName: fileName, FileSize: fileSize,
		Status: "Completed", Progress: 100, Speed: 0,
		TelegramID: fmt.Sprintf("%d", msg.Chat.ID), TelegramUser: msg.Chat.Username,
	})

	var catalogID int = 0
	if bh.workerURL != "" && driveFileID != "" {
		// Use msg.Sender if available (the actual user), fallback to msg.Chat
		var senderName, senderUsername string
		var senderID int64
		if msg.Sender != nil {
			senderName = strings.TrimSpace(msg.Sender.FirstName + " " + msg.Sender.LastName)
			senderUsername = msg.Sender.Username
			senderID = msg.Sender.ID
		} else {
			senderName = strings.TrimSpace(msg.Chat.FirstName + " " + msg.Chat.LastName)
			senderUsername = msg.Chat.Username
			senderID = msg.Chat.ID
		}
		if senderName == "" {
			senderName = senderUsername
		}

		log.Printf("[Catalog Sync] Syncing '%s' to Worker (user: %s / tg:%d)", fileName, senderUsername, senderID)
		syncRes, syncErr := uploader.SyncCatalogToWorker(bh.workerURL, bh.adminKey, uploader.CatalogSyncPayload{
			Title:                fileName,
			Type:                 "movie",
			DriveFileID:          driveFileID,
			DriveLink:            driveLink,
			FileSize:             fileSize,
			UploadedByName:       senderName,
			UploadedByUsername:   senderUsername,
			UploadedByTelegramID: fmt.Sprintf("%d", senderID),
		})
		if syncErr != nil {
			log.Printf("[Catalog Sync] ❌ FAILED to sync '%s' to Worker: %v", fileName, syncErr)
		} else if syncRes != nil && syncRes.ID > 0 {
			catalogID = syncRes.ID
			log.Printf("[Catalog Sync] ✅ Synced '%s' → catalog ID %d", fileName, catalogID)
		}
	}

	completeText := fmt.Sprintf("✅ <b>Upload Complete!</b>\n\n"+
		"📄 <b>File:</b> <code>%s</code>\n"+
		"📦 <b>Size:</b> %s\n"+
		"⏱ <b>Time:</b> %s\n\n"+
		"<code>[████████████████████] 100%%</code>",
		esc(fileName), formatSize(fileSize), finalElapsed)

	menu := &tele.ReplyMarkup{}
	var rows []tele.Row

	if driveLink != "" {
		rows = append(rows, menu.Row(menu.URL("📂 Open in Google Drive", driveLink)))
	}

	if catalogID > 0 {
		auroraLink := fmt.Sprintf("https://aurora-play.pages.dev/#/download/%d", catalogID)
		rows = append(rows, menu.Row(menu.URL("🚀 Stream & Download on Aurora Play", auroraLink)))
	} else {
		rows = append(rows, menu.Row(menu.URL("🚀 Open Aurora Play", "https://aurora-play.pages.dev")))
	}

	menu.Inline(rows...)
	bh.editFinal(msg, completeText, menu)
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
