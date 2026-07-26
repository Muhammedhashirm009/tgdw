package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/downloader/telegram-cloud-transfer/bot"
)

func main() {
	log.Println("===========================================")
	log.Println("🚀 Starting Aurora Files Telegram Bot")
	log.Println("===========================================")

	// 1. Start Lightweight Health Server on Port 9990 for Koyeb / Docker TCP & HTTP Health Probes
	port := os.Getenv("PORT")
	if port == "" {
		port = "9990"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"app":     "Aurora Files Telegram Bot",
			"version": "2.0.0",
		})
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	go func() {
		log.Printf("Listening for health probes on 0.0.0.0:%s...", port)
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			log.Printf("Warning: Health server stopped: %v", err)
		}
	}()

	// 2. Load Configuration from Environment Variables
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		botToken = os.Getenv("BOT_TOKEN")
	}

	workerURL := os.Getenv("WORKER_API_URL")
	if workerURL == "" {
		workerURL = "https://aurora-worker.muhammedhashirm4.workers.dev"
	}

	adminKey := os.Getenv("ADMIN_API_KEY")
	tgAPIURL := os.Getenv("TELEGRAM_API_URL")
	if tgAPIURL == "" && (os.Getenv("TELEGRAM_API_ID") != "" || os.Getenv("TELEGRAM_API_HASH") != "") {
		tgAPIURL = "http://127.0.0.1:8081"
		log.Println("⚡ Configured Local Telegram Bot API Server on http://127.0.0.1:8081 (supports 2GB uploads)")
	}
	dlDir := os.Getenv("DOWNLOAD_DIR")
	if dlDir == "" {
		dlDir = "./downloads"
	}

	if botToken == "" {
		log.Println("⚠️ TELEGRAM_BOT_TOKEN not configured in environment!")
		log.Println("The health check on port 9990 is active. Set TELEGRAM_BOT_TOKEN to start bot polling.")
		select {} // Keep health check alive
	}

	// 3. Launch Bot Engine
	b, err := bot.NewBot(botToken, tgAPIURL, dlDir, workerURL, adminKey)
	if err != nil {
		log.Fatalf("❌ Failed to initialize Aurora Files bot: %v", err)
	}

	b.Start()

	// Block forever
	select {}
}
