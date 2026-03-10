package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/downloader/telegram-cloud-transfer/bot"
	"github.com/downloader/telegram-cloud-transfer/dashboard"
	"github.com/downloader/telegram-cloud-transfer/database"
	"github.com/downloader/telegram-cloud-transfer/uploader"
	"golang.org/x/oauth2"
)

func main() {
	// Initialize Database
	err := database.InitDB()
	if err != nil {
		log.Printf("Warning: Database initialization failed: %v", err)
		log.Printf("Ensure MYSQL_HOST, MYSQL_USER, MYSQL_PASSWORD, MYSQL_DATABASE are set.")
	}

	// Start Web Dashboard
	go func() {
		// Use port 9990 by default
		port := os.Getenv("PORT")
		if port == "" {
			port = "9990"
		}
		
		server := dashboard.NewServer(":" + port)
		if err := server.Start(); err != nil {
			log.Fatalf("Dashboard server failed: %v", err)
		}
	}()

	// Handle Bot Orchestration dynamically for token changes
	orchestrator := bot.NewBotOrchestrator()
	
	// Poll settings and reload bot if token changes
	go func() {
		for {
			settings, err := database.GetSettings()
			if err == nil {
				if settings.BotToken == "" {
					log.Println("bot_token not configured in database. Telegram bot will not start. Please configure via Dashboard.")
				} else {
					orchestrator.Reload(settings.BotToken, settings.TelegramAPIEndpoint)
				}
			} else {
				log.Printf("Warning: Failed to load settings from DB: %v", err)
			}
			time.Sleep(10 * time.Second)
		}
	}()
	
	// Garbage Collector for Auto-Deleting files older than 48 hours
	go func() {
		for {
			time.Sleep(1 * time.Hour) // Run every hour
			
			settings, err := database.GetSettings()
			if err != nil || settings.AccessToken == "" {
				continue
			}

			expiredTasks, err := database.GetExpiredTasks(48)
			if err != nil || len(expiredTasks) == 0 {
				continue
			}

			log.Printf("Garbage Collector found %d tasks older than 48 hours", len(expiredTasks))

			token := &oauth2.Token{
				AccessToken:  settings.AccessToken,
				RefreshToken: settings.RefreshToken,
				Expiry:       settings.TokenExpiry,
				TokenType:    "Bearer",
			}

			uploaderInstance, err := uploader.NewDriveUploader(context.Background(), token, settings.GoogleClientID, settings.GoogleClientSecret)
			if err != nil {
				continue
			}

			for _, task := range expiredTasks {
				log.Printf("Auto-deleting Google Drive file for Task %d (DriveFileID: %s)", task.ID, task.DriveFileID)
				err := uploaderInstance.DeleteFile(task.DriveFileID)
				if err != nil {
					log.Printf("Failed to delete file from Drive for Task %d: %v", task.ID, err)
				} else {
					database.DB.Exec("DELETE FROM tasks WHERE id = ?", task.ID)
					log.Printf("Successfully deleted and purged Task %d", task.ID)
				}
			}
		}
	}()
	
	// Block forever
	select {}
}
