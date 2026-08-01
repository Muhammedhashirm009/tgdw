package uploader

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"
)

type WorkerCredentials struct {
	WorkerID        string `json:"worker_id"`
	APIKey          string `json:"api_key"`
	APISecret       string `json:"api_secret"`
	ControlPlaneURL string `json:"control_plane_url"`
}

type RegistrationRequest struct {
	JoinToken string `json:"joinToken"`
	Hostname  string `json:"hostname"`
	Version   string `json:"version"`
	Platform  string `json:"platform"`
	CPU       int    `json:"cpu"`
	Memory    int    `json:"memory"`
	Region    string `json:"region"`
}

type RegistrationResponse struct {
	WorkerID          string `json:"workerId"`
	APIKey            string `json:"apiKey"`
	APISecret         string `json:"apiSecret"`
	HeartbeatInterval int    `json:"heartbeatInterval"`
	Error             string `json:"error"`
}

type HeartbeatPayload struct {
	CPU           float64 `json:"cpu"`
	RAM           float64 `json:"ram"`
	Jobs          int     `json:"jobs"`
	Queue         int     `json:"queue"`
	Version       string  `json:"version"`
	UploadSpeed   int64   `json:"upload_speed"`
	DownloadSpeed int64   `json:"download_speed"`
}

type PolledJob struct {
	ID                 string `json:"id"`
	JobType            string `json:"jobType"`
	SourceInput        string `json:"sourceInput"`
	DestinationDriveID string `json:"destinationDriveId"`
	FileName           string `json:"fileName"`
	CancelRequested    int    `json:"cancelRequested"`
}

type PollJobResponse struct {
	Job   *PolledJob `json:"job"`
	Error string     `json:"error"`
}

type WorkerDaemon struct {
	Creds      *WorkerCredentials
	HTTPClient *http.Client
	CredsFile  string
	ActiveJobs int
}

func NewWorkerDaemon(credsFile string) *WorkerDaemon {
	return &WorkerDaemon{
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
		CredsFile:  credsFile,
	}
}

// LoadOrRegister initializes worker credentials using JOIN_TOKEN if needed
func (d *WorkerDaemon) LoadOrRegister() error {
	// 1. Check if local credentials file exists
	if _, err := os.Stat(d.CredsFile); err == nil {
		data, err := os.ReadFile(d.CredsFile)
		if err == nil {
			var creds WorkerCredentials
			if err := json.Unmarshal(data, &creds); err == nil && creds.WorkerID != "" {
				d.Creds = &creds
				log.Printf("✅ Loaded Worker Credentials: WorkerID=%s", creds.WorkerID)
				return nil
			}
		}
	}

	// 2. Perform Register Flow using JOIN_TOKEN
	joinToken := os.Getenv("JOIN_TOKEN")
	controlPlane := os.Getenv("CONTROL_PLANE")
	if controlPlane == "" {
		controlPlane = os.Getenv("WORKER_API_URL")
	}
	if controlPlane == "" {
		controlPlane = "https://aurora-worker.muhammedhashirm4.workers.dev"
	}

	if joinToken == "" {
		log.Println("⚠️ JOIN_TOKEN not provided in environment. Worker operating in auto-registration mode.")
		joinToken = "auto_register"
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "go-upload-worker"
	}

	regReq := RegistrationRequest{
		JoinToken: joinToken,
		Hostname:  hostname,
		Version:   "2.0.0",
		Platform:  runtime.GOOS,
		CPU:       runtime.NumCPU(),
		Memory:    8192,
		Region:    os.Getenv("WORKER_REGION"),
	}

	reqBytes, _ := json.Marshal(regReq)
	resp, err := d.HTTPClient.Post(controlPlane+"/api/workers/register", "application/json", bytes.NewBuffer(reqBytes))
	if err != nil {
		return fmt.Errorf("failed to register with control plane: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	var regResp RegistrationResponse
	if err := json.Unmarshal(respBytes, &regResp); err != nil {
		return fmt.Errorf("invalid registration response: %w", err)
	}

	if regResp.Error != "" {
		return fmt.Errorf("registration rejected: %s", regResp.Error)
	}

	d.Creds = &WorkerCredentials{
		WorkerID:        regResp.WorkerID,
		APIKey:          regResp.APIKey,
		APISecret:       regResp.APISecret,
		ControlPlaneURL: controlPlane,
	}

	// Save credentials locally
	saveBytes, _ := json.MarshalIndent(d.Creds, "", "  ")
	_ = os.WriteFile(d.CredsFile, saveBytes, 0600)
	log.Printf("🎉 Successfully Registered Worker! WorkerID=%s (Credentials saved to %s)", d.Creds.WorkerID, d.CredsFile)

	return nil
}

// StartHeartbeatLoop runs the 5-second authenticated heartbeat ticker
func (d *WorkerDaemon) StartHeartbeatLoop() {
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for range ticker.C {
			if d.Creds == nil {
				continue
			}

			payload := HeartbeatPayload{
				CPU:     15.5,
				RAM:     32.0,
				Jobs:    d.ActiveJobs,
				Queue:   0,
				Version: "2.0.0",
			}

			bodyBytes, _ := json.Marshal(payload)
			req, err := http.NewRequest("POST", d.Creds.ControlPlaneURL+"/api/workers/heartbeat", bytes.NewBuffer(bodyBytes))
			if err != nil {
				continue
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Worker-ID", d.Creds.WorkerID)
			req.Header.Set("X-API-Key", d.Creds.APIKey)
			req.Header.Set("X-API-Secret", d.Creds.APISecret)

			resp, err := d.HTTPClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}
	}()
}

// PollNextJob fetches an assigned upload job from Control Plane
func (d *WorkerDaemon) PollNextJob() (*PolledJob, error) {
	if d.Creds == nil {
		return nil, fmt.Errorf("worker not authenticated")
	}

	req, err := http.NewRequest("GET", d.Creds.ControlPlaneURL+"/api/workers/jobs/poll", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Worker-ID", d.Creds.WorkerID)
	req.Header.Set("X-API-Key", d.Creds.APIKey)
	req.Header.Set("X-API-Secret", d.Creds.APISecret)

	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	var pollResp PollJobResponse
	if err := json.Unmarshal(respBytes, &pollResp); err != nil {
		return nil, err
	}

	return pollResp.Job, nil
}

// SendJobProgress sends progress update to Control Plane
func (d *WorkerDaemon) SendJobProgress(jobID, status string, pct float64, transferred, total, speed int64, eta int) {
	if d.Creds == nil {
		return
	}

	payload := map[string]interface{}{
		"jobId":            jobID,
		"status":           status,
		"progressPercent":  pct,
		"transferredBytes": transferred,
		"fileSize":         total,
		"speedBytes":       speed,
		"etaSeconds":       eta,
	}

	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", d.Creds.ControlPlaneURL+"/api/workers/jobs/progress", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Worker-ID", d.Creds.WorkerID)
	req.Header.Set("X-API-Key", d.Creds.APIKey)
	req.Header.Set("X-API-Secret", d.Creds.APISecret)

	resp, err := d.HTTPClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// SendJobComplete notifies Control Plane of job completion
func (d *WorkerDaemon) SendJobComplete(jobID, driveFileID string, fileSize int64, fileName string) {
	if d.Creds == nil {
		return
	}

	payload := map[string]interface{}{
		"jobId":       jobID,
		"driveFileId": driveFileID,
		"fileSize":    fileSize,
		"fileName":    fileName,
	}

	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", d.Creds.ControlPlaneURL+"/api/workers/jobs/complete", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Worker-ID", d.Creds.WorkerID)
	req.Header.Set("X-API-Key", d.Creds.APIKey)
	req.Header.Set("X-API-Secret", d.Creds.APISecret)

	resp, err := d.HTTPClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// SendJobFail notifies Control Plane of job failure
func (d *WorkerDaemon) SendJobFail(jobID, errorMessage string) {
	if d.Creds == nil {
		return
	}

	payload := map[string]interface{}{
		"jobId":        jobID,
		"errorMessage": errorMessage,
	}

	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", d.Creds.ControlPlaneURL+"/api/workers/jobs/fail", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Worker-ID", d.Creds.WorkerID)
	req.Header.Set("X-API-Key", d.Creds.APIKey)
	req.Header.Set("X-API-Secret", d.Creds.APISecret)

	resp, err := d.HTTPClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

type WorkerConfig struct {
	WorkerID          string              `json:"workerId"`
	HeartbeatInterval int                 `json:"heartbeatInterval"`
	MaxConcurrentJobs int                 `json:"maxConcurrentJobs"`
	GDriveAccounts    []GDriveAccountCred `json:"gdriveAccounts"`
	UploadChunkSize   int64               `json:"uploadChunkSize"`
}

type GDriveAccountCred struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// FetchConfig fetches worker configuration from Control Plane
func (d *WorkerDaemon) FetchConfig() (*WorkerConfig, error) {
	if d.Creds == nil {
		return nil, fmt.Errorf("worker not authenticated")
	}

	req, err := http.NewRequest("GET", d.Creds.ControlPlaneURL+"/api/workers/config", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Worker-ID", d.Creds.WorkerID)
	req.Header.Set("X-API-Key", d.Creds.APIKey)
	req.Header.Set("X-API-Secret", d.Creds.APISecret)

	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	var config WorkerConfig
	if err := json.Unmarshal(respBytes, &config); err != nil {
		return nil, err
	}

	return &config, nil
}
