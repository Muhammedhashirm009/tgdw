package main

import (
	"log"
	"os"
	"time"

	"github.com/downloader/telegram-cloud-transfer/bot"
	"github.com/downloader/telegram-cloud-transfer/dashboard"
	"github.com/downloader/telegram-cloud-transfer/database"
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
	
	// Block forever
	select {}
}
