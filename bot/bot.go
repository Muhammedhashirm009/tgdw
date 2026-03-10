package bot

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/downloader/telegram-cloud-transfer/database"
	"github.com/downloader/telegram-cloud-transfer/downloader"
	"github.com/downloader/telegram-cloud-transfer/uploader"
	"golang.org/x/oauth2"
	tele "gopkg.in/telebot.v3"
)

type BotHandler struct {
	bot *tele.Bot
}

// NewBot initializes the telegram bot with telebot.v3
func NewBot(token string, apiURL string) (*BotHandler, error) {
	pref := tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
		Client: &http.Client{
			// Give the local Telegram API proxy up to 30 minutes to download a massive file
			// to its local hard drive before returning the file path to us.
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

// Start begins polling for updates
func (bh *BotHandler) Start() {
	log.Println("Starting Telegram Bot...")
	bh.bot.Start()
}

func (bh *BotHandler) setupRoutes() {
	// Register slash commands as per skills.md
	bh.bot.Handle("/start", bh.handleStart)
	bh.bot.Handle("/tasks", bh.handleTasks)
	bh.bot.Handle("/cancel", bh.handleCancel)
	bh.bot.Handle("/status", bh.handleStatus)
	
	// Register content handlers
	bh.bot.Handle(tele.OnText, bh.handleText)
	bh.bot.Handle(tele.OnDocument, bh.handleDocument)
}

func (bh *BotHandler) handleStart(c tele.Context) error {
	return c.Send("Welcome to Telegram Cloud Transfer Bot!\nSend me a direct link, magnet link, or a file to get started.")
}

func (bh *BotHandler) handleTasks(c tele.Context) error {
	// TODO: Fetch tasks from database and format them
	return c.Send("Active tasks:\nNo active tasks at the moment.")
}

func (bh *BotHandler) handleCancel(c tele.Context) error {
	args := c.Args()
	if len(args) == 0 {
		return c.Send("Usage: /cancel <task_id>")
	}
	var taskID int
	fmt.Sscanf(args[0], "%d", &taskID)
	
	if database.CancelTask(taskID) {
		database.UpdateTaskStatus(taskID, "Cancelled", "", "", "")
		return c.Send(fmt.Sprintf("Task %d successfully cancelled.", taskID))
	}
	return c.Send(fmt.Sprintf("Error: Task %d not found or already completed.", taskID))
}

func (bh *BotHandler) handleStatus(c tele.Context) error {
	// TODO: Fetch system status
	return c.Send("System Status:\nCPU: 5%\nDisk: 20GB used")
}

func (bh *BotHandler) handleText(c tele.Context) error {
	text := c.Text()
	
	if text == "" {
		return nil
	}
	
	// Create a dynamic progress message
	msg, err := bh.bot.Send(c.Chat(), "Task: Parsing input...\n\n" + text)
	if err != nil {
		return err
	}
	
	// TODO: Detect if it's HTTP, HTTPS, or Magnet link and start download
	bh.bot.Edit(msg, "Added to queue.")
	return nil
}

func (bh *BotHandler) handleDocument(c tele.Context) error {
	doc := c.Message().Document
	
	if doc == nil {
		return nil
	}

	// Fetch Settings
	settings, err := database.GetSettings()
	if err != nil {
		return c.Send("Internal error: Could not load settings.")
	}

	if settings.AccessToken == "" {
		return c.Send("Google Drive is not connected. Please connect via Dashboard.")
	}
	
	msg, err := bh.bot.Send(c.Chat(), "Task: Initializing...\nFile: " + doc.FileName)
	if err != nil {
		return err
	}

	// Create Task in DB
	// We'll use user ID 1 for now since we haven't implemented multi-user fully
	taskID, err := database.CreateTask(1, doc.FileName, doc.FileSize, "Telegram Document")
	if err != nil {
		bh.bot.Edit(msg, "Error inserting task into database.")
		return err
	}

	bh.bot.Edit(msg, "<b>Task queued. Starting download...</b>", &tele.SendOptions{ParseMode: tele.ModeHTML})
	database.UpdateTaskStatus(taskID, "Downloading", "", "", "")

	// Create a cancelable context for this entire task
	ctx, cancel := context.WithCancel(context.Background())
	database.RegisterCancelFunc(taskID, cancel)

	// Run in background
	go func() {
		defer cancel() // ensure memory is freed
		defer func() {
			if r := recover(); r != nil {
				database.UpdateTaskStatus(taskID, "Failed", "", "", "")
				bh.bot.Edit(msg, fmt.Sprintf("❌ <b>Task %d failed unexpectedly.</b>", taskID), &tele.SendOptions{ParseMode: tele.ModeHTML})
			}
		}()

		var downloadPath string
		startTime := time.Now()

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
				case <-time.After(2 * time.Second):
					// find most recently modified file in /var/lib/telegram-bot-api
					var maxSize int64
					filepath.Walk("/var/lib/telegram-bot-api", func(path string, info os.FileInfo, err error) error {
						if err != nil || info.IsDir() {
							return nil
						}
						// If modified in the last 10 seconds
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

						var eta string
						if speed > 0 {
							secondsLeft := (doc.FileSize - maxSize) / speed
							eta = (time.Duration(secondsLeft) * time.Second).String()
						} else {
							eta = "calculating..."
						}

						elapsed := time.Since(startTime).Round(time.Second).String()
						humanSpeed := humanize.Bytes(uint64(speed)) + "/s"

						text := fmt.Sprintf("📥 <b>Downloading File [%d]</b>\n\n"+
							"📄 <b>Name:</b> <code>%s</code>\n"+
							"📊 <b>Progress:</b> %d%%\n"+
							"🚀 <b>Speed:</b> %s\n"+
							"⏳ <b>ETA:</b> %s\n"+
							"⏱️ <b>Elapsed:</b> %s\n\n"+
							"<i>Use /cancel %d to abort</i>",
							taskID, doc.FileName, progress, humanSpeed, eta, elapsed, taskID)

						bh.bot.Edit(msg, text, &tele.SendOptions{ParseMode: tele.ModeHTML})

						lastSize = maxSize
						lastReport = time.Now()
					}
				}
			}
		}()

		// Get Telegram File Path (This blocks until the local proxy finishes downloading)
		file, err := bh.bot.FileByID(doc.FileID)
		trackCancel() // Stop tracking once FileByID completes
		
		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.bot.Edit(msg, "❌ <b>Error getting file from Telegram:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
			return
		}
		
		// If the file exists locally (from the local Telegram proxy), we don't need to HTTP download it
		if stat, err := os.Stat(file.FilePath); err == nil && !stat.IsDir() {
			downloadPath = file.FilePath
			database.UpdateTaskDownloadProgress(taskID, 100, 0)
			bh.bot.Edit(msg, fmt.Sprintf("Task %d: File accessed locally.\nFile: %s\nStatus: Skipping HTTP download.", taskID, doc.FileName))
		} else {
			// Construct download URL using custom API endpoint
			apiBase := settings.TelegramAPIEndpoint
			if apiBase == "" {
				apiBase = "https://api.telegram.org"
			}
			fileURL := fmt.Sprintf("%s/file/bot%s/%s", apiBase, settings.BotToken, file.FilePath)

			lastUpdate := time.Now()
			
			// 1. Download
			downloadPath, err = downloader.DownloadHTTP(ctx, fileURL, settings.DownloadDirectory, doc.FileName, func(downloaded, total, speed int64) {
				if time.Since(lastUpdate) > 2*time.Second {
					progress := int((float64(downloaded) / float64(total)) * 100)
					database.UpdateTaskDownloadProgress(taskID, progress, speed)
					
					var eta string
					if speed > 0 {
						secondsLeft := (total - downloaded) / speed
						eta = (time.Duration(secondsLeft) * time.Second).String()
					} else {
						eta = "calculating..."
					}
					
					elapsed := time.Since(startTime).Round(time.Second).String()
					humanSpeed := humanize.Bytes(uint64(speed)) + "/s"
					
					text := fmt.Sprintf("📥 <b>Downloading File [%d]</b>\n\n"+
						"📄 <b>Name:</b> <code>%s</code>\n"+
						"📊 <b>Progress:</b> %d%%\n"+
						"🚀 <b>Speed:</b> %s\n"+
						"⏳ <b>ETA:</b> %s\n"+
						"⏱️ <b>Elapsed:</b> %s\n\n"+
						"<i>Use /cancel %d to abort</i>",
						taskID, doc.FileName, progress, humanSpeed, eta, elapsed, taskID)

					bh.bot.Edit(msg, text, &tele.SendOptions{ParseMode: tele.ModeHTML})
					lastUpdate = time.Now()
				}
			})

			if err != nil {
				// Prevent overwriting a manual "Cancelled" state by the context error
				if ctx.Err() == context.Canceled {
					return
				}
				database.UpdateTaskStatus(taskID, "Failed", "", "", "")
				bh.bot.Edit(msg, "❌ <b>Download Failed:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
				return
			}
		}

		database.UpdateTaskDownloadProgress(taskID, 100, 0)
		database.UpdateTaskStatus(taskID, "Uploading", "", "", "")
		bh.bot.Edit(msg, fmt.Sprintf("☁️ <b>Task %d: Starting Upload to Google Drive...</b>\n📄 <b>File:</b> <code>%s</code>", taskID, doc.FileName), &tele.SendOptions{ParseMode: tele.ModeHTML})

		// 2. Upload to Google Drive
		token := &oauth2.Token{
			AccessToken:  settings.AccessToken,
			RefreshToken: settings.RefreshToken,
			Expiry:       settings.TokenExpiry,
			TokenType:    "Bearer",
		}

		uploaderInstance, err := uploader.NewDriveUploader(context.Background(), token, settings.GoogleClientID, settings.GoogleClientSecret)
		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.bot.Edit(msg, "❌ <b>Upload Failed Setup:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
			return
		}

		lastUpdate := time.Now()
		driveLink, driveFileID, err := uploaderInstance.UploadFile(ctx, downloadPath, doc.FileName, func(uploaded, total, speed int64) {
			if time.Since(lastUpdate) > 2*time.Second {
				progress := int((float64(uploaded) / float64(total)) * 100)
				database.UpdateTaskUploadProgress(taskID, progress, speed)
				
				var eta string
				if speed > 0 {
					secondsLeft := (total - uploaded) / speed
					eta = (time.Duration(secondsLeft) * time.Second).String()
				} else {
					eta = "calculating..."
				}
				
				elapsed := time.Since(startTime).Round(time.Second).String()
				humanSpeed := humanize.Bytes(uint64(speed)) + "/s"
				
				text := fmt.Sprintf("☁️ <b>Uploading File [%d]</b>\n\n"+
					"📄 <b>Name:</b> <code>%s</code>\n"+
					"📊 <b>Progress:</b> %d%%\n"+
					"🚀 <b>Speed:</b> %s\n"+
					"⏳ <b>ETA:</b> %s\n"+
					"⏱️ <b>Elapsed:</b> %s\n\n"+
					"<i>Use /cancel %d to abort</i>",
					taskID, doc.FileName, progress, humanSpeed, eta, elapsed, taskID)

				bh.bot.Edit(msg, text, &tele.SendOptions{ParseMode: tele.ModeHTML})
				lastUpdate = time.Now()
			}
		})

		if err != nil {
			if ctx.Err() == context.Canceled {
				return
			}
			database.UpdateTaskStatus(taskID, "Failed", "", "", "")
			bh.bot.Edit(msg, "❌ <b>Upload Failed:</b> "+err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
			return
		}

		finalElapsed := time.Since(startTime).Round(time.Second).String()
		database.UpdateTaskUploadProgress(taskID, 100, 0)
		database.UpdateTaskStatus(taskID, "Completed", driveLink, driveFileID, finalElapsed)

		finalText := fmt.Sprintf("✅ <b>Task %d Completed!</b>\n\n"+
			"📄 <b>File:</b> <code>%s</code>\n"+
			"⏱️ <b>Total Time:</b> %s\n\n"+
			"🔗 <a href=\"%s\">Open in Google Drive</a>",
			taskID, doc.FileName, finalElapsed, driveLink)

		bh.bot.Edit(msg, finalText, &tele.SendOptions{ParseMode: tele.ModeHTML})
	}()

	return nil
}
