package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/downloader/telegram-cloud-transfer/downloader"
	"github.com/downloader/telegram-cloud-transfer/uploader"
	"golang.org/x/oauth2"
)

var daemon *uploader.WorkerDaemon
var workerConfig *uploader.WorkerConfig

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

	go func() {
		log.Printf("Listening for health probes on 0.0.0.0:%s...", port)
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

	// 3. Fetch config from Control Plane
	var err error
	workerConfig, err = daemon.FetchConfig()
	if err != nil {
		log.Printf("⚠️ Could not fetch config: %v (will retry on each job)", err)
	} else {
		log.Printf("📋 Config loaded: %d GDrive accounts, max %d concurrent jobs",
			len(workerConfig.GDriveAccounts), workerConfig.MaxConcurrentJobs)
	}

	// 4. Job Polling Ticker Loop
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		for range ticker.C {
			job, err := daemon.PollNextJob()
			if err != nil || job == nil {
				continue
			}

			log.Printf("📥 Job Received: ID=%s Type=%s", job.ID, job.JobType)
			daemon.ActiveJobs++

			go processJob(job)
		}
	}()

	// Block forever
	select {}
}

func processJob(job *uploader.PolledJob) {
	defer func() {
		daemon.ActiveJobs--
		if r := recover(); r != nil {
			log.Printf("❌ PANIC in job %s: %v", job.ID, r)
			daemon.SendJobFail(job.ID, "Internal panic")
		}
	}()

	ctx := context.Background()
	tmpDir := filepath.Join(os.TempDir(), "aurora-worker", job.ID)
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir) // Cleanup after job

	var downloadedPath string
	var dlErr error

	switch job.JobType {
	case "http_url":
		// Download via HTTP
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

		daemon.SendJobProgress(job.ID, "downloading", 5.0, 0, 0, 0, 0)

		downloadedPath, dlErr = downloader.DownloadHTTP(ctx, job.SourceInput, tmpDir, fileName,
			func(downloaded, total, speed int64) {
				pct := 0.0
				if total > 0 {
					pct = float64(downloaded) / float64(total) * 50.0 // Download is 0-50%
				}
				daemon.SendJobProgress(job.ID, "downloading", pct, downloaded, total, speed, 0)
			})

	case "torrent_magnet":
		daemon.SendJobProgress(job.ID, "downloading", 2.0, 0, 0, 0, 0)

		td, tdErr := downloader.NewTorrentDownloader(tmpDir)
		if tdErr != nil {
			daemon.SendJobFail(job.ID, "Failed to init torrent client: "+tdErr.Error())
			return
		}
		defer td.Close()

		downloadedPath, dlErr = td.DownloadMagnet(ctx, job.SourceInput,
			func(name string, completed, total, speed int64, peers int) {
				pct := 0.0
				if total > 0 {
					pct = float64(completed) / float64(total) * 50.0
				}
				daemon.SendJobProgress(job.ID, "downloading", pct, completed, total, speed, 0)
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
		// Download via Telegram Bot API (local server at port 8081 supports any file size)
		botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
		if botToken == "" {
			daemon.SendJobFail(job.ID, "Missing TELEGRAM_BOT_TOKEN env var")
			return
		}

		tgDl := downloader.NewTelegramFileDownloader(botToken)

		daemon.SendJobProgress(job.ID, "downloading", 5.0, 0, 0, 0, 0)

		fileName := job.FileName
		if fileName == "" {
			fileName = "telegram_file_" + job.ID
		}

		downloadedPath, dlErr = tgDl.DownloadByFileID(ctx, job.SourceInput, tmpDir, fileName,
			func(downloaded, total, speed int64) {
				pct := 0.0
				var eta int64 = 0
				if total > 0 {
					pct = float64(downloaded) / float64(total) * 50.0
					if speed > 0 && total > downloaded {
						eta = (total - downloaded) / speed
					}
				} else if downloaded > 0 {
					pct = 25.0
				}
				daemon.SendJobProgress(job.ID, "downloading", pct, downloaded, total, speed, int(eta))
			})

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
	daemon.SendJobProgress(job.ID, "uploading", 50.0, 0, 0, 0, 0)

	// Upload to Google Drive
	// Try to get GDrive credentials from config or env
	var driveUploader *uploader.DriveUploader

	if workerConfig != nil && len(workerConfig.GDriveAccounts) > 0 {
		acct := workerConfig.GDriveAccounts[0]
		token := &oauth2.Token{
			AccessToken:  acct.AccessToken,
			RefreshToken: acct.RefreshToken,
			TokenType:    "Bearer",
		}
		var uErr error
		driveUploader, uErr = uploader.NewDriveUploader(ctx, token, acct.ClientID, acct.ClientSecret)
		if uErr != nil {
			log.Printf("⚠️ GDrive auth from config failed: %v, trying env vars", uErr)
		}
	}

	// Fallback to env vars
	if driveUploader == nil {
		accessToken := os.Getenv("GDRIVE_ACCESS_TOKEN")
		refreshToken := os.Getenv("GDRIVE_REFRESH_TOKEN")
		clientID := os.Getenv("GOOGLE_CLIENT_ID")
		clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")

		if refreshToken == "" {
			daemon.SendJobFail(job.ID, "No Google Drive credentials available")
			return
		}

		token := &oauth2.Token{
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			TokenType:    "Bearer",
		}

		var uErr error
		driveUploader, uErr = uploader.NewDriveUploader(ctx, token, clientID, clientSecret)
		if uErr != nil {
			daemon.SendJobFail(job.ID, "GDrive auth failed: "+uErr.Error())
			return
		}
	}

	// Get file info
	fileInfo, _ := os.Stat(downloadedPath)
	fileSize := int64(0)
	if fileInfo != nil {
		fileSize = fileInfo.Size()
	}
	fileName := filepath.Base(downloadedPath)

	webLink, fileId, uploadErr := driveUploader.UploadFile(ctx, downloadedPath, fileName,
		func(uploaded, total, speed int64) {
			pct := 50.0
			var eta int64 = 0
			if total > 0 {
				pct = 50.0 + (float64(uploaded) / float64(total) * 50.0) // Upload is 50-100%
				if speed > 0 && total > uploaded {
					eta = (total - uploaded) / speed
				}
			}
			daemon.SendJobProgress(job.ID, "uploading", pct, uploaded, total, speed, int(eta))
		})

	if uploadErr != nil {
		log.Printf("❌ Upload failed for job %s: %v", job.ID, uploadErr)
		daemon.SendJobFail(job.ID, "Upload failed: "+uploadErr.Error())
		return
	}

	log.Printf("✅ Job %s Complete: fileId=%s link=%s", job.ID, fileId, webLink)
	daemon.SendJobComplete(job.ID, fileId, fileSize, fileName)
}
