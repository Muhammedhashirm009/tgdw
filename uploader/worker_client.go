package uploader

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type WorkerUserProvisionResponse struct {
	IsNew        bool   `json:"is_new"`
	UserID       string `json:"user_id"`
	Username     string `json:"username"`
	TempPassword string `json:"temp_password"`
	DisplayName  string `json:"display_name"`
	Error        string `json:"error"`
}

type GDriveAccountCredential struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"display_name"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	StorageUsed  int64  `json:"storage_used"`
	StorageTotal int64  `json:"storage_total"`
}

type CatalogSyncPayload struct {
	Title                 string `json:"title"`
	Type                  string `json:"type"` // "movie" or "series"
	Overview              string `json:"overview,omitempty"`
	PosterPath            string `json:"poster_path,omitempty"`
	DriveFileID           string `json:"drive_file_id"`
	DriveLink             string `json:"drive_link,omitempty"`
	FileSize              int64  `json:"file_size"`
	Quality               string `json:"quality,omitempty"`
	UploadedByName        string `json:"uploaded_by_name,omitempty"`
	UploadedByUsername    string `json:"uploaded_by_username,omitempty"`
	UploadedByTelegramID  string `json:"uploaded_by_telegram_id,omitempty"`
	UploadedByUserID      string `json:"uploaded_by_user_id,omitempty"`
}

type CatalogSyncResponse struct {
	Success bool   `json:"success"`
	ID      int    `json:"id"`
	Error   string `json:"error"`
}

// ProvisionUser provisions or fetches a D1 user account for a Telegram user
func ProvisionUser(workerURL, adminKey string, tgID int64, tgUsername, tgName string) (*WorkerUserProvisionResponse, error) {
	endpoint := fmt.Sprintf("%s/api/admin/bot-user-provision", workerURL)

	payload := map[string]interface{}{
		"telegram_id":       fmt.Sprintf("%d", tgID),
		"telegram_username": tgUsername,
		"telegram_name":     tgName,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	if adminKey != "" {
		req.Header.Set("x-admin-key", adminKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res WorkerUserProvisionResponse
	if err := json.Unmarshal(respBytes, &res); err != nil {
		return nil, err
	}

	if res.Error != "" {
		return nil, fmt.Errorf("worker error: %s", res.Error)
	}

	return &res, nil
}

// GetActiveGDriveAccounts fetches active load-balanced GDrive accounts from Worker API according to Selection Protocol
func GetActiveGDriveAccounts(workerURL, adminKey string) ([]GDriveAccountCredential, error) {
	endpoint := fmt.Sprintf("%s/api/admin/gdrive-accounts/active", workerURL)

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	if adminKey != "" {
		req.Header.Set("x-admin-key", adminKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var accounts []GDriveAccountCredential
	if err := json.Unmarshal(respBytes, &accounts); err != nil {
		return nil, err
	}

	return accounts, nil
}

// SyncCatalogToWorker registers the uploaded media file into Cloudflare D1 via Worker API
func SyncCatalogToWorker(workerURL, adminKey string, payload CatalogSyncPayload) (*CatalogSyncResponse, error) {
	endpoint := fmt.Sprintf("%s/api/admin/catalog", workerURL)

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	if adminKey != "" {
		req.Header.Set("x-admin-key", adminKey)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res CatalogSyncResponse
	if err := json.Unmarshal(respBytes, &res); err != nil {
		return nil, err
	}

	if res.Error != "" {
		return nil, fmt.Errorf("worker sync error: %s", res.Error)
	}

	return &res, nil
}
