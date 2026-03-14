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

	// Handle Bot Orchestration dynamically for token changes
	orchestrator := bot.NewBotOrchestrator()

	// Start Web Dashboard
	go func() {
		// Use port 9990 by default
		port := os.Getenv("PORT")
		if port == "" {
			port = "9990"
		}
		
		server := dashboard.NewServer(":" + port)
		server.OnBridgeTask = orchestrator.HandleBridgeTask // Wire bridge API to Bot Orchestrator

		if err := server.Start(); err != nil {
			log.Fatalf("Dashboard server failed: %v", err)
		}
	}()
	
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
	
	// Garbage Collector for Auto-Deleting files older than the configured retention period
	go func() {
		for {
			settings, err := database.GetSettings()
			if err == nil && settings.AccessToken != "" {
				retentionHours := settings.RetentionHours
				if retentionHours <= 0 {
					retentionHours = 48 // fallback default
				}

				expiredTasks, err := database.GetExpiredTasks(retentionHours)
				if err == nil && len(expiredTasks) > 0 {
					log.Printf("Garbage Collector found %d tasks older than %d hours", len(expiredTasks), retentionHours)

					token := &oauth2.Token{
						AccessToken:  settings.AccessToken,
						RefreshToken: settings.RefreshToken,
						Expiry:       settings.TokenExpiry,
						TokenType:    "Bearer",
					}

					uploaderInstance, uploaderErr := uploader.NewDriveUploader(context.Background(), token, settings.GoogleClientID, settings.GoogleClientSecret)
					if uploaderErr == nil {
						for _, task := range expiredTasks {
							log.Printf("Auto-deleting Google Drive file for Task %d (DriveFileID: %s)", task.ID, task.DriveFileID)
							err := uploaderInstance.DeleteFile(task.DriveFileID)
							if err != nil {
								log.Printf("Failed to delete file from Drive for Task %d: %v. Keeping DB record for retry.", task.ID, err)
								continue // Keep the DB row so GC retries next cycle
							}
							log.Printf("Successfully deleted file from Drive for Task %d", task.ID)
							_, dbErr := database.DB.Exec("DELETE FROM tasks WHERE id = ?", task.ID)
							if dbErr != nil {
								log.Printf("Failed to delete task %d from database: %v", task.ID, dbErr)
							}
						}
					} else {
						log.Printf("Garbage Collector: failed to create Drive uploader: %v", uploaderErr)
					}
				}
			}
			time.Sleep(1 * time.Hour) // Run every hour
		}
	}()

	
	// Block forever
	select {}
}
