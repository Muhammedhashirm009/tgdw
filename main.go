package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/downloader/telegram-cloud-transfer/downloader"
	"github.com/downloader/telegram-cloud-transfer/uploader"
	"golang.org/x/oauth2"
)

var daemon *uploader.WorkerDaemon
var workerConfig *uploader.WorkerConfig
var directMsgState sync.Map

func esc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func cleanFileName(s string) string {
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", ".")
	}
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, ":", "_")
	s = strings.ReplaceAll(s, "*", "_")
	s = strings.ReplaceAll(s, "?", "_")
	s = strings.ReplaceAll(s, "\"", "_")
	s = strings.ReplaceAll(s, "<", "_")
	s = strings.ReplaceAll(s, ">", "_")
	s = strings.ReplaceAll(s, "|", "_")
	return strings.TrimSpace(s)
}

func progressBar(percent float64) string {
	p := int(percent)
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	filled := p / 5
	empty := 20 - filled
	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

func formatSize(bytes int64) string {
	if bytes <= 0 {
		return "0 B"
	}
	const k = 1024
	sizes := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	val := float64(bytes)
	for val >= k && i < len(sizes)-1 {
		val /= k
		i++
	}
	return fmt.Sprintf("%.2f %s", val, sizes[i])
}

func editTelegramDirect(chatId, msgId string, text string, replyMarkup interface{}, force bool) {
	if chatId == "" || msgId == "" || msgId == "0" || msgId == "<nil>" {
		return
	}
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		botToken = os.Getenv("BOT_TOKEN")
	}
	if botToken == "" {
		botToken = "8946065502:AAGzG1AT1KMjBfvzL8Bjon0_T6i4HyWllCc"
	}

	key := chatId + ":" + msgId
	now := time.Now()

	type msgState struct {
		t   time.Time
		txt string
	}

	if !force {
		if st, ok := directMsgState.Load(key); ok {
			last := st.(msgState)
			if last.txt == text || now.Sub(last.t) < 3*time.Second {
				return
			}
		}
	}
	directMsgState.Store(key, msgState{t: now, txt: text})

	msgIdInt, _ := strconv.Atoi(msgId)
	payload := map[string]interface{}{
		"chat_id":    chatId,
		"message_id": msgIdInt,
		"text":       text,
		"parse_mode": "HTML",
	}
	if replyMarkup != nil {
		payload["reply_markup"] = replyMarkup
	}

	bodyBytes, _ := json.Marshal(payload)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Post("https://api.telegram.org/bot"+botToken+"/editMessageText", "application/json", bytes.NewBuffer(bodyBytes))
		if err == nil {
			resp.Body.Close()
		}
	}()
}

// SyncCatalogToWorker registers the uploaded media file into Cloudflare D1 via Worker API (matching old bot)
func SyncCatalogToWorker(controlPlaneURL, apiKey string, payload map[string]interface{}) (int, error) {
	if controlPlaneURL == "" {
		controlPlaneURL = "https://aurora-worker.muhammedhashirm4.workers.dev"
	}
	endpoint := fmt.Sprintf("%s/api/admin/bot-catalog-sync", strings.TrimRight(controlPlaneURL, "/"))
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("x-admin-key", apiKey)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	var res map[string]interface{}
	json.Unmarshal(respBytes, &res)
	if idVal, ok := res["id"].(float64); ok {
		return int(idVal), nil
	}
	return 0, fmt.Errorf("no catalog id returned")
}

func main() {
	log.Println("===========================================")
	log.Println("🚀 Starting Aurora Go Upload Worker (V2 Engine)")
	log.Println("===========================================")

	// 1. Health Probe Server
	port := os.Getenv("PORT")
	if port == "" {
		port = "9990"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "online",
			"worker":  "Aurora Go Upload Engine",
			"version": "2.0.0",
		})
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	http.HandleFunc("/dispatch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		secretEnv := os.Getenv("WORKER_SECRET")
		secHeader := r.Header.Get("X-Worker-Secret")
		if secretEnv != "" && secHeader != secretEnv {
			http.Error(w, "Unauthorized worker secret", http.StatusUnauthorized)
			return
		}

		var job uploader.PolledJob
		if err := json.NewDecoder(r.Body).Decode(&job); err != nil || job.ID == "" {
			http.Error(w, "Invalid job payload", http.StatusBadRequest)
			return
		}

		maxAllowed := 3
		if workerConfig != nil && workerConfig.MaxConcurrentJobs > 0 {
			maxAllowed = workerConfig.MaxConcurrentJobs
		}
		if daemon.ActiveJobs >= maxAllowed {
			http.Error(w, "Worker at capacity", http.StatusServiceUnavailable)
			return
		}

		log.Printf("⚡ Direct Push Job Received: ID=%s Type=%s (Active: %d/%d)", job.ID, job.JobType, daemon.ActiveJobs+1, maxAllowed)
		daemon.ActiveJobs++

		go processJob(&job)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "jobId": job.ID})
	})

	go func() {
		log.Printf("Listening for direct job dispatch & health probes on 0.0.0.0:%s...", port)
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			log.Printf("Warning: Health server stopped: %v", err)
		}
	}()

	// 2. Initialize Worker Daemon & Auth V1
	daemon = uploader.NewWorkerDaemon("worker_credentials.json")
	if err := daemon.LoadOrRegister(); err != nil {
		log.Printf("⚠️ Worker Registration Warning: %v (operating in standby)", err)
	} else {
		daemon.StartHeartbeatLoop()
		log.Println("💓 5s Worker Telemetry Heartbeat Active")
	}

	// 2b. Direct King Bot Registration Loop (Zero-touch auth with KING_BOT_URL + KING_SECRET)
	kingURL := os.Getenv("KING_BOT_URL")
	kingSec := os.Getenv("KING_SECRET")
	if kingSec == "" {
		kingSec = os.Getenv("WORKER_SECRET")
	}
	if kingSec == "" {
		kingSec = "aurora_king_secret_key_2026"
	}
	workerURL := os.Getenv("WORKER_URL")

	if kingURL != "" && workerURL != "" {
		go func() {
			kingURL = strings.TrimRight(kingURL, "/")
			workerID := "worker-node"
			if daemon.Creds != nil && daemon.Creds.WorkerID != "" {
				workerID = daemon.Creds.WorkerID
			}
			workerName := os.Getenv("RENDER_SERVICE_NAME")
			if workerName == "" {
				workerName = workerID
			}

			regPayload, _ := json.Marshal(map[string]interface{}{
				"worker_id": workerID,
				"name":      workerName,
				"url":       workerURL,
				"secret":    os.Getenv("WORKER_SECRET"),
				"max_jobs":  3,
			})

			req, _ := http.NewRequest("POST", kingURL+"/api/worker/register", bytes.NewBuffer(regPayload))
			if req != nil {
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-King-Secret", kingSec)
				client := &http.Client{Timeout: 5 * time.Second}
				resp, rErr := client.Do(req)
				if rErr == nil {
					resp.Body.Close()
					log.Printf("👑 Direct King Bot Registration Successful -> %s", kingURL)
				}
			}

			// Direct Heartbeat to King Bot every 5s
			hbTicker := time.NewTicker(5 * time.Second)
			for range hbTicker.C {
				hbPayload, _ := json.Marshal(map[string]interface{}{
					"worker_id":   workerID,
					"active_jobs": daemon.ActiveJobs,
				})
				hReq, _ := http.NewRequest("POST", kingURL+"/api/worker/heartbeat", bytes.NewBuffer(hbPayload))
				if hReq != nil {
					hReq.Header.Set("Content-Type", "application/json")
					hReq.Header.Set("X-King-Secret", kingSec)
					client := &http.Client{Timeout: 5 * time.Second}
					hResp, hErr := client.Do(hReq)
					if hErr == nil {
						hResp.Body.Close()
					}
				}
			}
		}()
	}

	// 3. Fetch config from Control Plane
	var err error
	workerConfig, err = daemon.FetchConfig()
	if err != nil {
		log.Printf("⚠️ Could not fetch config: %v (will retry on each job)", err)
	} else {
		log.Printf("📋 Config loaded: %d GDrive accounts, max %d concurrent jobs",
			len(workerConfig.GDriveAccounts), workerConfig.MaxConcurrentJobs)
	}

	// 4. Job Polling Ticker Loop (Control Plane enforces concurrency dynamically via D1)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		for range ticker.C {
			job, err := daemon.PollNextJob()
			if err != nil || job == nil {
				continue
			}

			log.Printf("📥 Job Received: ID=%s Type=%s", job.ID, job.JobType)
			go processJob(job)
		}
	}()

	// Block forever
	select {}
}

func processJob(job *uploader.PolledJob) {
	workerNodeName := os.Getenv("NODE_NAME")
	if workerNodeName == "" {
		workerNodeName = os.Getenv("WORKER_NAME")
	}
	if workerNodeName == "" {
		workerNodeName = os.Getenv("RENDER_SERVICE_NAME")
	}
	if workerNodeName == "" {
		if daemon != nil && daemon.Creds != nil && daemon.Creds.WorkerID != "" {
			workerNodeName = daemon.Creds.WorkerID
		} else {
			workerNodeName = "Go Upload Worker"
		}
	}

	defer func() {
		if r := recover(); r != nil {
			log.Printf("❌ PANIC in job %s: %v", job.ID, r)
			daemon.SendJobFail(job.ID, "Internal panic")
		}
	}()

	// BUG 1 fix: Cancel-aware context that polls control plane every 10s
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Background goroutine to check for cancel_requested from control plane
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if checkCancelRequested(job.ID) {
					log.Printf("🚫 Cancel requested for job %s, aborting...", job.ID)
					cancel()
					return
				}
			}
		}
	}()

	tmpDir := filepath.Join(os.TempDir(), "aurora-worker", job.ID)
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir) // Cleanup after job

	cancelKeyboard := map[string]interface{}{
		"inline_keyboard": [][]map[string]string{
			{{"text": "❌ Cancel Task", "callback_data": "cancel_task|" + job.ID}},
		},
	}

	// Instant first response: send "starting" progress immediately when job is picked up
	go daemon.SendJobProgress(job.ID, "downloading", 0.0, 0, job.FileSize, 0, 0)
	if job.TelegramChatID != "" && fmt.Sprint(job.TelegramMessageID) != "" {
		pText := fmt.Sprintf("📥 <b>Downloading [#%s]</b>\n\n📄 <code>%s</code>\n<code>[%s] 0%%</code>\n\n⚡ 0 B/s • ⏳ calculating...\n📦 0 B / %s",
			job.ID, esc(job.FileName), progressBar(0), formatSize(job.FileSize))
		editTelegramDirect(job.TelegramChatID, fmt.Sprint(job.TelegramMessageID), pText, cancelKeyboard, true)
	}

	var downloadedPath string
	var dlErr error
	var webLink, fileId string

	// Prepare active Google Drive storage accounts for job
	var activeAccounts []*uploader.DriveUploader
	if job.GDriveAccount != nil && job.GDriveAccount.RefreshToken != "" {
		token := &oauth2.Token{
			AccessToken:  job.GDriveAccount.AccessToken,
			RefreshToken: job.GDriveAccount.RefreshToken,
			TokenType:    "Bearer",
			Expiry:       time.Now().Add(-time.Hour),
		}
		uploaderInst, uErr := uploader.NewDriveUploader(ctx, token, job.GDriveAccount.ClientID, job.GDriveAccount.ClientSecret)
		if uErr == nil {
			activeAccounts = append(activeAccounts, uploaderInst)
		}
	}
	if workerConfig != nil && len(workerConfig.GDriveAccounts) > 0 {
		for _, acct := range workerConfig.GDriveAccounts {
			if job.GDriveAccount != nil && acct.ID == job.GDriveAccount.ID {
				continue
			}
			token := &oauth2.Token{
				AccessToken:  acct.AccessToken,
				RefreshToken: acct.RefreshToken,
				TokenType:    "Bearer",
				Expiry:       time.Now().Add(-time.Hour),
			}
			uploaderInst, uErr := uploader.NewDriveUploader(ctx, token, acct.ClientID, acct.ClientSecret)
			if uErr == nil {
				activeAccounts = append(activeAccounts, uploaderInst)
			}
		}
	}

	switch job.JobType {
	case "http_url":
		fileName := job.FileName
		if fileName == "" {
			parts := strings.Split(job.SourceInput, "/")
			fileName = parts[len(parts)-1]
			if idx := strings.Index(fileName, "?"); idx > 0 {
				fileName = fileName[:idx]
			}
			if fileName == "" {
				fileName = "download_" + job.ID
			}
		}

		downloadedPath, dlErr = downloader.DownloadHTTP(ctx, job.SourceInput, tmpDir, fileName,
			func(downloaded, total, speed int64) {
				if total <= 0 && job.FileSize > 0 {
					total = job.FileSize
				}
				pct := 0.0
				var eta int64 = 0
				if total > 0 {
					pct = float64(downloaded) / float64(total) * 100.0
					if speed > 0 && total > downloaded {
						eta = (total - downloaded) / speed
					}
				}
				if daemon.SendJobProgress(job.ID, "downloading", pct, downloaded, total, speed, int(eta)) {
					cancel()
				}
				if job.TelegramChatID != "" && fmt.Sprint(job.TelegramMessageID) != "" {
					statusInfo := fmt.Sprintf("⚡ %s/s • ⏳ ~%ds", formatSize(speed), eta)
					if downloaded == 0 || speed <= 0 {
						statusInfo = "⚡ <i>Fetching Telegram stream...</i>"
					}
					pText := fmt.Sprintf("📥 <b>Downloading [#%s]</b>\n\n📄 <code>%s</code>\n<code>[%s] %d%%</code>\n\n%s\n📦 %s / %s",
						job.ID, esc(fileName), progressBar(pct), int(pct), statusInfo, formatSize(downloaded), formatSize(total))
					editTelegramDirect(job.TelegramChatID, fmt.Sprint(job.TelegramMessageID), pText, cancelKeyboard, false)
				}
			})

	case "torrent_magnet":
		daemon.SendJobProgress(job.ID, "downloading", 0.0, 0, 0, 0, 0)

		td, tdErr := downloader.NewTorrentDownloader(tmpDir)
		if tdErr != nil {
			daemon.SendJobFail(job.ID, "Failed to init torrent client: "+tdErr.Error())
			return
		}
		defer td.Close()

		downloadedPath, dlErr = td.DownloadMagnet(ctx, job.SourceInput,
			func(name string, completed, total, speed int64, peers int) {
				if total <= 0 && job.FileSize > 0 {
					total = job.FileSize
				}
				pct := 0.0
				var eta int64 = 0
				if total > 0 {
					pct = float64(completed) / float64(total) * 100.0
					if speed > 0 && total > completed {
						eta = (total - completed) / speed
					}
				}
				daemon.SendJobProgress(job.ID, "downloading", pct, completed, total, speed, int(eta))
			})

		// If torrent downloaded a directory, zip it
		if dlErr == nil {
			info, _ := os.Stat(downloadedPath)
			if info != nil && info.IsDir() {
				zipPath := downloadedPath + ".zip"
				if zErr := downloader.ZipDirectory(downloadedPath, zipPath); zErr == nil {
					downloadedPath = zipPath
				}
			}
		}

	case "telegram_file":
		botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
		if botToken == "" {
			botToken = os.Getenv("BOT_TOKEN")
		}
		if botToken == "" {
			botToken = "8946065502:AAGzG1AT1KMjBfvzL8Bjon0_T6i4HyWllCc"
		}

		tgDl := downloader.NewTelegramFileDownloader(botToken)
		go daemon.SendJobProgress(job.ID, "downloading", 0.0, 0, job.FileSize, 0, 0)

		fileName := cleanFileName(job.FileName)
		if fileName == "" {
			fileName = "telegram_file_" + job.ID
		}


		progressCb := func(downloaded, total, speed int64) {
			if total <= 0 && job.FileSize > 0 {
				total = job.FileSize
			}
			pct := 0.0
			var eta int64 = 0
			if total > 0 {
				pct = float64(downloaded) / float64(total) * 100.0
				if speed > 0 && total > downloaded {
					eta = (total - downloaded) / speed
				}
			}
			if daemon.SendJobProgress(job.ID, "downloading", pct, downloaded, total, speed, int(eta)) {
				cancel()
			}
			if job.TelegramChatID != "" && fmt.Sprint(job.TelegramMessageID) != "" {
				statusInfo := fmt.Sprintf("⚡ %s/s • ⏳ ~%ds", formatSize(speed), eta)
				if downloaded == 0 || speed <= 0 {
					statusInfo = "⚡ <i>Downloading Telegram file...</i>"
				}
				pText := fmt.Sprintf("📥 <b>Downloading [#%s]</b>\n\n📄 <code>%s</code>\n🖥️ <b>Node:</b> <code>%s</code>\n<code>[%s] %d%%</code>\n\n%s\n📦 %s / %s",
					job.ID, esc(fileName), esc(workerNodeName), progressBar(pct), int(pct), statusInfo, formatSize(downloaded), formatSize(total))
				editTelegramDirect(job.TelegramChatID, fmt.Sprint(job.TelegramMessageID), pText, cancelKeyboard, false)
			}
		}

		// 1. If local Bot API Server (port 8081) is active (0ms MTProto direct disk copy matching old bot)
		if tgDl.APIBaseURL == "http://127.0.0.1:8081" {
			log.Println("⚡ Using Local Telegram Bot API Server on 127.0.0.1:8081 (0ms MTProto Direct Copy)")
			downloadedPath, dlErr = tgDl.DownloadByFileID(ctx, job.SourceInput, tmpDir, fileName, job.FileSize, progressCb)
		} else {
			// 2. Fallback for Remote API: Stream directly from Telegram CDN into Google Drive
			fileURL, fErr := tgDl.GetFileURL(ctx, job.SourceInput)
			if fErr != nil {
				log.Printf("⚠️ getFileURL error: %v (falling back to DownloadByFileID)", fErr)
				downloadedPath, dlErr = tgDl.DownloadByFileID(ctx, job.SourceInput, tmpDir, fileName, job.FileSize, progressCb)
			} else {
				log.Printf("📥 Direct Telegram Stream URL: %s", fileURL)
				req, reqErr := http.NewRequestWithContext(ctx, "GET", fileURL, nil)
				if reqErr != nil {
					daemon.SendJobFail(job.ID, "Failed to create stream request: "+reqErr.Error())
					return
				}
				req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

				resp, doErr := downloader.FastClient.Do(req)
				if doErr != nil || resp.StatusCode >= 400 {
					log.Printf("⚠️ Direct stream error (status %v), falling back to DownloadByFileID", doErr)
					downloadedPath, dlErr = tgDl.DownloadByFileID(ctx, job.SourceInput, tmpDir, fileName, job.FileSize, progressCb)
				} else {
					defer resp.Body.Close()

					realSize := resp.ContentLength
					if realSize <= 0 {
						realSize = job.FileSize
					}

					if len(activeAccounts) > 0 {
						var streamUploadErr error
						for idx, uploaderAcc := range activeAccounts {
							webLink, fileId, streamUploadErr = uploaderAcc.UploadStream(ctx, resp.Body, fileName, realSize,
								func(uploaded, total, speed int64) {
									if total <= 0 && realSize > 0 {
										total = realSize
									}
									pct := 0.0
									var eta int64 = 0
									if total > 0 {
										pct = float64(uploaded) / float64(total) * 100.0
										if speed > 0 && total > uploaded {
											eta = (total - uploaded) / speed
										}
									}
									go daemon.SendJobProgress(job.ID, "uploading", pct, uploaded, total, speed, int(eta))
									if job.TelegramChatID != "" && fmt.Sprint(job.TelegramMessageID) != "" {
										statusInfo := fmt.Sprintf("⚡ %s/s • ⏳ ~%ds", formatSize(speed), eta)
										if uploaded == 0 || speed <= 0 {
											statusInfo = "⚡ <i>Streaming to Google Drive...</i>"
										}
										pText := fmt.Sprintf("☁️ <b>Streaming to Google Drive [#%s]</b>\n\n📄 <code>%s</code>\n🖥️ <b>Node:</b> <code>%s</code>\n<code>[%s] %d%%</code>\n\n%s\n📦 %s / %s",
											job.ID, esc(fileName), esc(workerNodeName), progressBar(pct), int(pct), statusInfo, formatSize(uploaded), formatSize(total))
										editTelegramDirect(job.TelegramChatID, fmt.Sprint(job.TelegramMessageID), pText, cancelKeyboard, false)
									}
								})
							if streamUploadErr == nil {
								break
							}
							log.Printf("⚠️ Stream upload error on account #%d: %v", idx+1, streamUploadErr)
						}

						if streamUploadErr == nil && fileId != "" {
							log.Printf("✅ Stream Job %s Complete: fileId=%s link=%s", job.ID, fileId, webLink)

							cpURL := "https://aurora-worker.muhammedhashirm4.workers.dev"
							apiKey := ""
							if daemon != nil && daemon.Creds != nil {
								cpURL = daemon.Creds.ControlPlaneURL
								apiKey = daemon.Creds.APIKey
							}
							acctID := ""
							if job.GDriveAccount != nil {
								acctID = job.GDriveAccount.ID
							}

							syncPayload := map[string]interface{}{
								"title":                  fileName,
								"drive_file_id":          fileId,
								"drive_link":             webLink,
								"gdrive_account_id":      acctID,
								"file_size":              realSize,
								"uploaded_by_telegram_id": job.TelegramChatID,
							}

							catalogID, syncErr := SyncCatalogToWorker(cpURL, apiKey, syncPayload)
							playerLink := "https://play.hxdev.in"
							addedNote := ""
							if syncErr == nil && catalogID > 0 {
								log.Printf("🎉 Catalog Synced Successfully! CatalogID=%d", catalogID)
								playerLink = fmt.Sprintf("https://play.hxdev.in/#/detail/%d", catalogID)
								addedNote = fmt.Sprintf("\n\n🎬 <b>Added to Aurora Play</b> (Catalog ID: %d)", catalogID)
							}

							// Send Telegram completion FIRST (before control plane) with throttle bypass
							if job.TelegramChatID != "" && fmt.Sprint(job.TelegramMessageID) != "" {
								driveLink := webLink
								if driveLink == "" && fileId != "" {
									driveLink = "https://drive.google.com/file/d/" + fileId + "/view"
								}
								cText := fmt.Sprintf("✅ <b>Upload Complete!</b>\n\n📄 <b>File:</b> <code>%s</code>\n📦 <b>Size:</b> %s\n🖥️ <b>Node:</b> <code>%s</code>%s\n\n<code>[████████████████████] 100%%</code>",
									esc(fileName), formatSize(realSize), esc(workerNodeName), addedNote)
								keyboard := map[string]interface{}{
									"inline_keyboard": [][]map[string]string{
										{{"text": "📂 Open in Google Drive", "url": driveLink}},
										{{"text": "🚀 Stream & Download on Aurora Play", "url": playerLink}},
									},
								}
								// Clear throttle state to guarantee completion message is sent immediately
								directMsgState.Delete(job.TelegramChatID + ":" + fmt.Sprint(job.TelegramMessageID))
								editTelegramDirect(job.TelegramChatID, fmt.Sprint(job.TelegramMessageID), cText, keyboard, true)
							}

							// THEN notify control plane synchronously (no `go`)
							daemon.SendJobComplete(job.ID, fileId, realSize, fileName)
							return
						}
					}
				}
			}
		}

	case "torrent_file":
		// BUG 10 fix: Download .torrent file from Telegram first, then use torrent downloader
		botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
		if botToken == "" {
			botToken = os.Getenv("BOT_TOKEN")
		}
		if botToken == "" {
			botToken = "8946065502:AAGzG1AT1KMjBfvzL8Bjon0_T6i4HyWllCc"
		}

		tgDl := downloader.NewTelegramFileDownloader(botToken)
		daemon.SendJobProgress(job.ID, "downloading", 0.0, 0, 0, 0, 0)

		torrentFileName := job.FileName
		if torrentFileName == "" {
			torrentFileName = "download.torrent"
		}

		torrentPath, torrentDlErr := tgDl.DownloadByFileID(ctx, job.SourceInput, tmpDir, torrentFileName, job.FileSize, nil)
		if torrentDlErr != nil {
			daemon.SendJobFail(job.ID, "Failed to download .torrent file: "+torrentDlErr.Error())
			return
		}

		td, tdErr := downloader.NewTorrentDownloader(tmpDir)
		if tdErr != nil {
			daemon.SendJobFail(job.ID, "Failed to init torrent client: "+tdErr.Error())
			return
		}
		defer td.Close()

		downloadedPath, dlErr = td.DownloadFile(ctx, torrentPath,
			func(name string, completed, total, speed int64, peers int) {
				pct := 0.0
				var eta int64 = 0
				if total > 0 {
					pct = float64(completed) / float64(total) * 100.0
					if speed > 0 && total > completed {
						eta = (total - completed) / speed
					}
				}
				daemon.SendJobProgress(job.ID, "downloading", pct, completed, total, speed, int(eta))
			})

		// If torrent downloaded a directory, zip it
		if dlErr == nil {
			info, _ := os.Stat(downloadedPath)
			if info != nil && info.IsDir() {
				zipPath := downloadedPath + ".zip"
				if zErr := downloader.ZipDirectory(downloadedPath, zipPath); zErr == nil {
					downloadedPath = zipPath
				}
			}
		}

	default:
		daemon.SendJobFail(job.ID, "Unknown job type: "+job.JobType)
		return
	}

	if dlErr != nil {
		log.Printf("❌ Download failed for job %s: %v", job.ID, dlErr)
		daemon.SendJobFail(job.ID, "Download failed: "+dlErr.Error())
		return
	}

	log.Printf("📥 Downloaded: %s", downloadedPath)

	// Get file info
	fileInfo, _ := os.Stat(downloadedPath)
	fileSize := int64(0)
	if fileInfo != nil {
		fileSize = fileInfo.Size()
	}
	fileName := filepath.Base(downloadedPath)

	daemon.SendJobProgress(job.ID, "uploading", 0.0, 0, fileSize, 0, 0)

	if len(activeAccounts) == 0 {
		daemon.SendJobFail(job.ID, "No active Google Drive storage account available")
		return
	}

	var uploadErr error

	for idx, uploaderAcc := range activeAccounts {
		webLink, fileId, uploadErr = uploaderAcc.UploadFile(ctx, downloadedPath, fileName,
			func(uploaded, total, speed int64) {
				if total <= 0 && fileSize > 0 {
					total = fileSize
				}
				pct := 0.0
				var eta int64 = 0
				if total > 0 {
					pct = float64(uploaded) / float64(total) * 100.0
					if speed > 0 && total > uploaded {
						eta = (total - uploaded) / speed
					}
				}
				go daemon.SendJobProgress(job.ID, "uploading", pct, uploaded, total, speed, int(eta))
				if job.TelegramChatID != "" && fmt.Sprint(job.TelegramMessageID) != "" {
					pText := fmt.Sprintf("☁️ <b>Uploading to Google Drive [#%s]</b>\n\n📄 <code>%s</code>\n<code>[%s] %d%%</code>\n\n⚡ %s/s • ⏳ ~%ds\n📦 %s / %s",
						job.ID, esc(fileName), progressBar(pct), int(pct), formatSize(speed), eta, formatSize(uploaded), formatSize(total))
					editTelegramDirect(job.TelegramChatID, fmt.Sprint(job.TelegramMessageID), pText, cancelKeyboard, false)
				}
			})

		if uploadErr == nil {
			break
		}
		log.Printf("⚠️ Upload failed on GDrive account #%d: %v (trying next active account)", idx+1, uploadErr)
	}

	if uploadErr != nil {
		log.Printf("❌ Upload failed for job %s across all active accounts: %v", job.ID, uploadErr)
		daemon.SendJobFail(job.ID, "Upload failed: "+uploadErr.Error())
		return
	}

	log.Printf("✅ Job %s Complete: fileId=%s link=%s", job.ID, fileId, webLink)

	// Direct Aurora Player Catalog Sync (matching old bot)
	cpURL := "https://aurora-worker.muhammedhashirm4.workers.dev"
	apiKey := ""
	if daemon != nil && daemon.Creds != nil {
		cpURL = daemon.Creds.ControlPlaneURL
		apiKey = daemon.Creds.APIKey
	}

	acctID := ""
	if job.GDriveAccount != nil {
		acctID = job.GDriveAccount.ID
	}

	syncPayload := map[string]interface{}{
		"title":                  fileName,
		"drive_file_id":          fileId,
		"drive_link":             webLink,
		"gdrive_account_id":      acctID,
		"file_size":              fileSize,
		"uploaded_by_telegram_id": job.TelegramChatID,
	}

	catalogID, syncErr := SyncCatalogToWorker(cpURL, apiKey, syncPayload)
	playerLink := "https://play.hxdev.in"
	addedNote := ""
	if syncErr == nil && catalogID > 0 {
		log.Printf("🎉 Catalog Synced Successfully! CatalogID=%d", catalogID)
		playerLink = fmt.Sprintf("https://play.hxdev.in/#/detail/%d", catalogID)
		addedNote = fmt.Sprintf("\n\n🎬 <b>Added to Aurora Play</b> (Catalog ID: %d)", catalogID)
	} else if syncErr != nil {
		log.Printf("⚠️ Catalog sync notice: %v", syncErr)
	}

	// Send Telegram completion FIRST with throttle bypass
	if job.TelegramChatID != "" && fmt.Sprint(job.TelegramMessageID) != "" {
		driveLink := webLink
		if driveLink == "" && fileId != "" {
			driveLink = "https://drive.google.com/file/d/" + fileId + "/view"
		}
		cText := fmt.Sprintf("✅ <b>Upload Complete!</b>\n\n📄 <b>File:</b> <code>%s</code>\n📦 <b>Size:</b> %s\n🖥️ <b>Node:</b> <code>%s</code>%s\n\n<code>[████████████████████] 100%%</code>",
			esc(fileName), formatSize(fileSize), esc(workerNodeName), addedNote)

		keyboard := map[string]interface{}{
			"inline_keyboard": [][]map[string]string{
				{{"text": "📂 Open in Google Drive", "url": driveLink}},
				{{"text": "🚀 Stream & Download on Aurora Play", "url": playerLink}},
			},
		}
		// Clear throttle state to guarantee completion message is sent immediately
		directMsgState.Delete(job.TelegramChatID + ":" + fmt.Sprint(job.TelegramMessageID))
		editTelegramDirect(job.TelegramChatID, fmt.Sprint(job.TelegramMessageID), cText, keyboard, true)
	}

	// THEN notify control plane synchronously
	daemon.SendJobComplete(job.ID, fileId, fileSize, fileName)
}

// checkCancelRequested polls the control plane to check if a job's cancel was requested
func checkCancelRequested(jobID string) bool {
	if daemon == nil || daemon.Creds == nil {
		return false
	}

	req, err := http.NewRequest("GET", daemon.Creds.ControlPlaneURL+"/api/workers/jobs/"+jobID, nil)
	if err != nil {
		return false
	}

	req.Header.Set("X-Worker-ID", daemon.Creds.WorkerID)
	req.Header.Set("X-API-Key", daemon.Creds.APIKey)
	req.Header.Set("X-API-Secret", daemon.Creds.APISecret)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Simple JSON check for cancel_requested or status=cancelled
	s := string(body)
	if strings.Contains(s, `"cancel_requested":1`) || strings.Contains(s, `"status":"cancelled"`) {
		return true
	}
	return false
}
