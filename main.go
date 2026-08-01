package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/downloader/telegram-cloud-transfer/uploader"
)

func main() {
	log.Println("===========================================")
	log.Println("🚀 Starting Aurora Go Upload Worker (V2 Engine)")
	log.Println("===========================================")

	// 1. Health Probe Server on Port 9990
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
	daemon := uploader.NewWorkerDaemon("worker_credentials.json")
	if err := daemon.LoadOrRegister(); err != nil {
		log.Printf("⚠️ Worker Registration Warning: %v (operating in standby)", err)
	} else {
		daemon.StartHeartbeatLoop()
		log.Println("💓 5s Worker Telemetry Heartbeat Active")
	}

	// 3. Job Polling Ticker Loop
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		for range ticker.C {
			job, err := daemon.PollNextJob()
			if err != nil || job == nil {
				continue
			}

			log.Printf("📥 Assigned Job Received: JobID=%s Type=%s Source=%s", job.ID, job.JobType, job.SourceInput)
			daemon.ActiveJobs++
			daemon.SendJobProgress(job.ID, "downloading", 10.0, 0, 100, 5242880, 20)

			// Simulate processing pipeline execution
			time.Sleep(2 * time.Second)
			daemon.SendJobProgress(job.ID, "uploading", 65.0, 65, 100, 10485760, 5)
			time.Sleep(2 * time.Second)

			daemon.SendJobComplete(job.ID, "drive_file_simulated", 104857600, job.SourceInput)
			daemon.ActiveJobs--
			log.Printf("✅ Job Completed: JobID=%s", job.ID)
		}
	}()

	// Block forever
	select {}
}
