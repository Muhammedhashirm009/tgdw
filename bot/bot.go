package bot

import (
	"context"
	"fmt"
	"log"
	"time"

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
	// TODO: Cancel a task by ID
	return c.Send("Usage: /cancel <task_id>")
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

	bh.bot.Edit(msg, "Task queued. Starting download...")
	database.UpdateTaskStatus(taskID, "Downloading", "")

	// Run in background
	go func() {
		defer func() {
			if r := recover(); r != nil {
				database.UpdateTaskStatus(taskID, "Failed", "")
				bh.bot.Edit(msg, fmt.Sprintf("Task %d failed unexpectedly.", taskID))
			}
		}()

		// Get Telegram File Path
		file, err := bh.bot.FileByID(doc.FileID)
		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "")
			bh.bot.Edit(msg, "Error getting file from Telegram: "+err.Error())
			return
		}
		
		// Construct download URL using custom API endpoint
		apiBase := settings.TelegramAPIEndpoint
		if apiBase == "" {
			apiBase = "https://api.telegram.org"
		}
		fileURL := fmt.Sprintf("%s/file/bot%s/%s", apiBase, settings.BotToken, file.FilePath)

		lastUpdate := time.Now()
		
		// 1. Download
		downloadPath, err := downloader.DownloadHTTP(fileURL, settings.DownloadDirectory, doc.FileName, func(downloaded, total int64) {
			if time.Since(lastUpdate) > 2*time.Second {
				progress := int((float64(downloaded) / float64(total)) * 100)
				database.UpdateTaskDownloadProgress(taskID, progress)
				bh.bot.Edit(msg, fmt.Sprintf("Task %d: Downloading %d%%", taskID, progress))
				lastUpdate = time.Now()
			}
		})

		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "")
			bh.bot.Edit(msg, "Download Failed: "+err.Error())
			return
		}

		database.UpdateTaskDownloadProgress(taskID, 100)
		database.UpdateTaskStatus(taskID, "Uploading", "")
		bh.bot.Edit(msg, fmt.Sprintf("Task %d: Download complete. Starting Upload to Google Drive.", taskID))

		// 2. Upload to Google Drive
		token := &oauth2.Token{
			AccessToken:  settings.AccessToken,
			RefreshToken: settings.RefreshToken,
			Expiry:       settings.TokenExpiry,
			TokenType:    "Bearer",
		}

		uploaderInstance, err := uploader.NewDriveUploader(context.Background(), token, settings.GoogleClientID, settings.GoogleClientSecret)
		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "")
			bh.bot.Edit(msg, "Upload Failed Setup: "+err.Error())
			return
		}

		lastUpdate = time.Now()
		driveLink, err := uploaderInstance.UploadFile(downloadPath, func(uploaded, total int64) {
			if time.Since(lastUpdate) > 2*time.Second {
				progress := int((float64(uploaded) / float64(total)) * 100)
				database.UpdateTaskUploadProgress(taskID, progress)
				bh.bot.Edit(msg, fmt.Sprintf("Task %d: Uploading to Drive %d%%", taskID, progress))
				lastUpdate = time.Now()
			}
		})

		if err != nil {
			database.UpdateTaskStatus(taskID, "Failed", "")
			bh.bot.Edit(msg, "Upload Failed: "+err.Error())
			return
		}

		database.UpdateTaskUploadProgress(taskID, 100)
		database.UpdateTaskStatus(taskID, "Completed", driveLink)
		bh.bot.Edit(msg, fmt.Sprintf("Task %d Completed!\nGoogle Drive Link: %s", taskID, driveLink))
	}()

	return nil
}
