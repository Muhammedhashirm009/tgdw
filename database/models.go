package database

import "time"

type User struct {
	ID           int       `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"password_hash"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
}

type Task struct {
	ID               int       `json:"id"`
	UserID           int       `json:"user_id"`
	FileName         string    `json:"file_name"`
	FileSize         int64     `json:"file_size"`
	InputType        string    `json:"input_type"`
	DownloadProgress int       `json:"download_progress"`
	UploadProgress   int       `json:"upload_progress"`
	DownloadSpeed    int64     `json:"download_speed"`
	UploadSpeed      int64     `json:"upload_speed"`
	Status           string    `json:"status"`
	DriveLink        string    `json:"drive_link"`
	CreatedAt        time.Time `json:"created_at"`
}

type Torrent struct {
	ID            int    `json:"id"`
	TaskID        int    `json:"task_id"`
	MagnetLink    string `json:"magnet_link"`
	Seeders       int    `json:"seeders"`
	Peers         int    `json:"peers"`
	DownloadSpeed int64  `json:"download_speed"`
	Progress      int    `json:"progress"`
}

type Settings struct {
	ID                 int       `json:"id"`
	BotToken           string    `json:"bot_token"`
	GoogleClientID     string    `json:"google_client_id"`
	GoogleClientSecret string    `json:"google_client_secret"`
	DownloadDirectory  string    `json:"download_directory"`
	MaxFileSize        int64     `json:"max_file_size"`
	ConcurrentTasks    int       `json:"concurrent_tasks"`
	TelegramAPIEndpoint string   `json:"telegram_api_endpoint"`
	TelegramAPIID      string    `json:"telegram_api_id"`
	TelegramAPIHash    string    `json:"telegram_api_hash"`
	AccessToken        string    `json:"access_token"`
	RefreshToken       string    `json:"refresh_token"`
	TokenExpiry        time.Time `json:"token_expiry"`
}

type Log struct {
	ID        int       `json:"id"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}
