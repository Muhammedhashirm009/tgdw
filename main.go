package main

import (
	"context"
	"encoding/json"
	"io"
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

		maxAllowed := 2
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

	// 3. Fetch config from Control Plane
	var err error
	workerConfig, err = daemon.FetchConfig()
	if err != nil {
		log.Printf("⚠️ Could not fetch config: %v (will retry on each job)", err)
	} else {
		log.Printf("📋 Config loaded: %d GDrive accounts, max %d concurrent jobs",
			len(workerConfig.GDriveAccounts), workerConfig.MaxConcurrentJobs)
	}

	// 4. Job Polling Ticker Loop with Strict Local Concurrency Enforcement
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond) // Fast polling for instant first response (old bot was in-process)
		for range ticker.C {
			maxAllowed := 2
			if workerConfig != nil && workerConfig.MaxConcurrentJobs > 0 {
				maxAllowed = workerConfig.MaxConcurrentJobs
			}
			if daemon.ActiveJobs >= maxAllowed {
				continue // Worker is at capacity; do not poll until a slot opens up
			}

			job, err := daemon.PollNextJob()
			if err != nil || job == nil {
				continue
			}

			log.Printf("📥 Job Received: ID=%s Type=%s (Active: %d/%d)", job.ID, job.JobType, daemon.ActiveJobs+1, maxAllowed)
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

	// Instant first response: send "starting" progress immediately when job is picked up
	daemon.SendJobProgress(job.ID, "downloading", 0.0, 0, job.FileSize, 0, 0)

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
				daemon.SendJobProgress(job.ID, "downloading", pct, downloaded, total, speed, int(eta))
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
		// Download via Telegram Bot API (local server at port 8081 supports any file size)
		botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
		if botToken == "" {
			daemon.SendJobFail(job.ID, "Missing TELEGRAM_BOT_TOKEN env var")
			return
		}

		tgDl := downloader.NewTelegramFileDownloader(botToken)

		daemon.SendJobProgress(job.ID, "downloading", 0.0, 0, 0, 0, 0)

		fileName := job.FileName
		if fileName == "" {
			fileName = "telegram_file_" + job.ID
		}

		downloadedPath, dlErr = tgDl.DownloadByFileID(ctx, job.SourceInput, tmpDir, fileName, job.FileSize,
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
				daemon.SendJobProgress(job.ID, "downloading", pct, downloaded, total, speed, int(eta))
			})

	case "torrent_file":
		// BUG 10 fix: Download .torrent file from Telegram first, then use torrent downloader
		botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
		if botToken == "" {
			daemon.SendJobFail(job.ID, "Missing TELEGRAM_BOT_TOKEN env var")
			return
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

	// Upload to Google Drive — Job-Assigned Round-Robin Account Protocol
	var activeAccounts []*uploader.DriveUploader

	// Primary: Use job-assigned account from Control Plane's Round-Robin load balancer
	if job.GDriveAccount != nil && job.GDriveAccount.RefreshToken != "" {
		token := &oauth2.Token{
			AccessToken:  job.GDriveAccount.AccessToken,
			RefreshToken: job.GDriveAccount.RefreshToken,
			TokenType:    "Bearer",
			Expiry:       time.Now().Add(-time.Hour), // Force token refresh
		}
		uploaderInst, uErr := uploader.NewDriveUploader(ctx, token, job.GDriveAccount.ClientID, job.GDriveAccount.ClientSecret)
		if uErr == nil {
			activeAccounts = append(activeAccounts, uploaderInst)
			log.Printf("🎯 Assigned GDrive account: %s (%s)", job.GDriveAccount.Email, job.GDriveAccount.ID)
		} else {
			log.Printf("⚠️ GDrive assigned account '%s' auth error: %v", job.GDriveAccount.Email, uErr)
		}
	}

	// Secondary: Include all other active accounts for failover
	if workerConfig != nil && len(workerConfig.GDriveAccounts) > 0 {
		for _, acct := range workerConfig.GDriveAccounts {
			if job.GDriveAccount != nil && acct.ID == job.GDriveAccount.ID {
				continue // already added
			}
			token := &oauth2.Token{
				AccessToken:  acct.AccessToken,
				RefreshToken: acct.RefreshToken,
				TokenType:    "Bearer",
				Expiry:       time.Now().Add(-time.Hour), // Force token refresh
			}
			uploaderInst, uErr := uploader.NewDriveUploader(ctx, token, acct.ClientID, acct.ClientSecret)
			if uErr == nil {
				activeAccounts = append(activeAccounts, uploaderInst)
			}
		}
	}

	if len(activeAccounts) == 0 {
		daemon.SendJobFail(job.ID, "No active Google Drive storage account available")
		return
	}

	var webLink, fileId string
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
				daemon.SendJobProgress(job.ID, "uploading", pct, uploaded, total, speed, int(eta))
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
