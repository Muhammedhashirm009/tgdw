package database

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
)

var DB *sql.DB
var taskCancels sync.Map

// Global Context Register for Tasks
func RegisterCancelFunc(taskID int, cancel context.CancelFunc) {
	taskCancels.Store(taskID, cancel)
}

func CancelTask(taskID int) bool {
	// If it's running in memory, cancel it.
	if cancel, ok := taskCancels.LoadAndDelete(taskID); ok {
		cancel.(context.CancelFunc)()
	}

	// Always forcefully mark it as cancelled in the database 
	// to clean up stuck/zombie tasks that aren't actively running.
	res, err := DB.Exec("UPDATE tasks SET status = 'Cancelled' WHERE id = ? AND status IN ('Downloading', 'Uploading', 'Pending')", taskID)
	if err == nil {
		rows, _ := res.RowsAffected()
		return rows > 0
	}
	return false
}

func InitDB() error {
	host := "82.25.121.49:3306"
	user := "u914498476_downloaderu"
	pass := "Ashir9990*"
	dbname := "u914498476_downloader"

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true", user, pass, host, dbname)
	
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}

	// Connection Pool Settings to prevent "connection reset by peer"
	db.SetConnMaxLifetime(45 * time.Second)
	db.SetConnMaxIdleTime(45 * time.Second)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)

	if err := db.Ping(); err != nil {
		return err
	}

	DB = db
	log.Println("Database connection established")

	return createTables()
}

func createTables() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INT AUTO_INCREMENT PRIMARY KEY,
			username VARCHAR(255) NOT NULL,
			email VARCHAR(255) NOT NULL UNIQUE,
			password_hash VARCHAR(255) NOT NULL,
			role VARCHAR(50) DEFAULT 'user',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id INT AUTO_INCREMENT PRIMARY KEY,
			user_id INT NOT NULL,
			file_name VARCHAR(255) NOT NULL,
			file_size BIGINT DEFAULT 0,
			input_type VARCHAR(50) NOT NULL,
			download_progress INT DEFAULT 0,
			upload_progress INT DEFAULT 0,
			download_speed BIGINT DEFAULT 0,
			upload_speed BIGINT DEFAULT 0,
			status VARCHAR(50) NOT NULL,
			drive_link TEXT,
			drive_file_id VARCHAR(255),
			elapsed_time VARCHAR(50) DEFAULT '',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS torrents (
			id INT AUTO_INCREMENT PRIMARY KEY,
			task_id INT NOT NULL,
			magnet_link TEXT NOT NULL,
			seeders INT DEFAULT 0,
			peers INT DEFAULT 0,
			download_speed BIGINT DEFAULT 0,
			progress INT DEFAULT 0,
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			id INT AUTO_INCREMENT PRIMARY KEY,
			bot_token VARCHAR(255),
			google_client_id VARCHAR(255),
			google_client_secret VARCHAR(255),
			download_directory VARCHAR(255) DEFAULT '/data/downloads',
			max_file_size BIGINT DEFAULT 0,
			concurrent_tasks INT DEFAULT 3,
			telegram_api_endpoint VARCHAR(255) DEFAULT 'http://telegram-bot-api:8081',
			telegram_api_id VARCHAR(255) DEFAULT '',
			telegram_api_hash VARCHAR(255) DEFAULT '',
			access_token TEXT,
			refresh_token TEXT,
			token_expiry TIMESTAMP NULL,
			retention_hours INT DEFAULT 48,
			admin_telegram_ids TEXT,
			max_file_size_normal BIGINT DEFAULT 4294967296
		)`,
		`CREATE TABLE IF NOT EXISTS logs (
			id INT AUTO_INCREMENT PRIMARY KEY,
			level VARCHAR(50) NOT NULL,
			message TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS bridge_tokens (
			id INT AUTO_INCREMENT PRIMARY KEY,
			user_id INT NOT NULL,
			token_hash VARCHAR(64) NOT NULL,
			token_prefix VARCHAR(50) NOT NULL,
			last_used TIMESTAMP NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS bridge_logs (
			id INT AUTO_INCREMENT PRIMARY KEY,
			user_id INT NOT NULL,
			url TEXT NOT NULL,
			source_site VARCHAR(255) DEFAULT '',
			filename VARCHAR(255) DEFAULT '',
			file_size VARCHAR(50) DEFAULT '',
			status VARCHAR(50) DEFAULT 'sent',
			task_id INT DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
	}

	for _, query := range queries {
		_, err := DB.Exec(query)
		if err != nil {
			return fmt.Errorf("error creating table: %v for query: %s", err, query)
		}
	}

	// Schema Updates / Migrations (ignoring errors if columns already exist)
	DB.Exec("ALTER TABLE settings ADD COLUMN access_token TEXT")
	DB.Exec("ALTER TABLE settings ADD COLUMN refresh_token TEXT")
	DB.Exec("ALTER TABLE settings ADD COLUMN token_expiry TIMESTAMP NULL")
	DB.Exec("ALTER TABLE settings ADD COLUMN telegram_api_endpoint VARCHAR(255) DEFAULT 'http://telegram-bot-api:8081'")
	DB.Exec("ALTER TABLE settings ADD COLUMN telegram_api_id VARCHAR(255) DEFAULT ''")
	DB.Exec("ALTER TABLE settings ADD COLUMN telegram_api_hash VARCHAR(255) DEFAULT ''")
	DB.Exec("ALTER TABLE settings ADD COLUMN retention_hours INT DEFAULT 48")
	DB.Exec("ALTER TABLE settings ADD COLUMN admin_telegram_ids TEXT")
	DB.Exec("ALTER TABLE settings ADD COLUMN max_file_size_normal BIGINT DEFAULT 4294967296")

	DB.Exec("ALTER TABLE tasks ADD COLUMN download_speed BIGINT DEFAULT 0")
	DB.Exec("ALTER TABLE tasks ADD COLUMN upload_speed BIGINT DEFAULT 0")
	DB.Exec("ALTER TABLE tasks ADD COLUMN drive_file_id VARCHAR(255) DEFAULT ''")
	DB.Exec("ALTER TABLE tasks ADD COLUMN elapsed_time VARCHAR(50) DEFAULT ''")
	DB.Exec("ALTER TABLE tasks ADD COLUMN telegram_user_id BIGINT DEFAULT 0")

	log.Println("Database tables verified/created successfully")
	return EnsureAdminUser()
}

// EnsureAdminUser checks if the admin user exists, and if not, creates it with default credentials
func EnsureAdminUser() error {
	var id int
	err := DB.QueryRow("SELECT id FROM users WHERE username = 'admin' LIMIT 1").Scan(&id)
	if err == sql.ErrNoRows {
		// Create admin user
		hash, err := bcrypt.GenerateFromPassword([]byte("99901234"), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		_, err = DB.Exec("INSERT INTO users (username, email, password_hash, role) VALUES (?, ?, ?, ?)", "admin", "admin@localhost", string(hash), "admin")
		if err != nil {
			return err
		}
		log.Println("Admin user created successfully")
		return nil
	}
	return err
}

// VerifyUser checks the given username and password against the database
func VerifyUser(username, password string) bool {
	var hash string
	err := DB.QueryRow("SELECT password_hash FROM users WHERE username = ?", username).Scan(&hash)
	if err != nil {
		return false
	}
	err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

func GetSettings() (Settings, error) {
	var s Settings
	var expiry sql.NullTime
	var access, refresh, adminIDs sql.NullString
	var maxNormal sql.NullInt64

	// Fetch the first settings row
	err := DB.QueryRow("SELECT id, IFNULL(bot_token, ''), IFNULL(google_client_id, ''), IFNULL(google_client_secret, ''), download_directory, max_file_size, concurrent_tasks, IFNULL(telegram_api_endpoint, 'http://telegram-bot-api:8081'), IFNULL(telegram_api_id, ''), IFNULL(telegram_api_hash, ''), access_token, refresh_token, token_expiry, IFNULL(retention_hours, 48), admin_telegram_ids, max_file_size_normal FROM settings ORDER BY id ASC LIMIT 1").Scan(
		&s.ID, &s.BotToken, &s.GoogleClientID, &s.GoogleClientSecret, &s.DownloadDirectory, &s.MaxFileSize, &s.ConcurrentTasks, &s.TelegramAPIEndpoint, &s.TelegramAPIID, &s.TelegramAPIHash, &access, &refresh, &expiry, &s.RetentionHours, &adminIDs, &maxNormal,
	)
	
	if access.Valid {
		s.AccessToken = access.String
	}
	if refresh.Valid {
		s.RefreshToken = refresh.String
	}
	if expiry.Valid {
		s.TokenExpiry = expiry.Time
	}
	if adminIDs.Valid {
		s.AdminTelegramIDs = adminIDs.String
	}
	if maxNormal.Valid {
		s.MaxFileSizeNormal = maxNormal.Int64
	} else {
		s.MaxFileSizeNormal = 4294967296 // 4GB default
	}

	if err == sql.ErrNoRows {
		// Insert default row
		_, err = DB.Exec("INSERT INTO settings (bot_token, google_client_id, google_client_secret, download_directory, max_file_size, concurrent_tasks, telegram_api_endpoint, telegram_api_id, telegram_api_hash, retention_hours, admin_telegram_ids, max_file_size_normal) VALUES ('', '', '', '/data/downloads', 0, 3, 'http://telegram-bot-api:8081', '', '', 48, '', 4294967296)")
		if err != nil {
			return s, err
		}
		// Fetch again
		return GetSettings()
	}
	
	return s, err
}

func UpdateSettings(s Settings) error {
	_, err := DB.Exec(`
		UPDATE settings 
		SET bot_token = ?, google_client_id = ?, google_client_secret = ?, download_directory = ?, max_file_size = ?, concurrent_tasks = ?, telegram_api_endpoint = ?, telegram_api_id = ?, telegram_api_hash = ?, retention_hours = ?, admin_telegram_ids = ?, max_file_size_normal = ?
		WHERE id = ?`,
		s.BotToken, s.GoogleClientID, s.GoogleClientSecret, s.DownloadDirectory, s.MaxFileSize, s.ConcurrentTasks, s.TelegramAPIEndpoint, s.TelegramAPIID, s.TelegramAPIHash, s.RetentionHours, s.AdminTelegramIDs, s.MaxFileSizeNormal, s.ID,
	)
	return err
}

func UpdateOAuthTokens(id int, accessToken, refreshToken string, expiry time.Time) error {
	_, err := DB.Exec(`
		UPDATE settings 
		SET access_token = ?, refresh_token = ?, token_expiry = ?
		WHERE id = ?`,
		accessToken, refreshToken, expiry, id,
	)
	return err
}

func CreateTask(userID int, fileName string, fileSize int64, inputType string) (int, error) {
	return CreateTaskWithTelegram(userID, 0, fileName, fileSize, inputType)
}

func CreateTaskWithTelegram(userID int, telegramUserID int64, fileName string, fileSize int64, inputType string) (int, error) {
	res, err := DB.Exec(`
		INSERT INTO tasks (user_id, file_name, file_size, input_type, status, telegram_user_id) 
		VALUES (?, ?, ?, ?, 'Pending', ?)`,
		userID, fileName, fileSize, inputType, telegramUserID,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return int(id), err
}

// GetDailyTaskCount returns how many tasks a Telegram user has created today
func GetDailyTaskCount(telegramUserID int64) (int, error) {
	var count int
	err := DB.QueryRow("SELECT COUNT(*) FROM tasks WHERE telegram_user_id = ? AND DATE(created_at) = CURDATE()", telegramUserID).Scan(&count)
	return count, err
}

// IsAdminTelegram checks if a Telegram user ID is in the admin list
func IsAdminTelegram(telegramUserID int64) bool {
	settings, err := GetSettings()
	if err != nil || settings.AdminTelegramIDs == "" {
		return false
	}
	userIDStr := strconv.FormatInt(telegramUserID, 10)
	for _, idStr := range strings.Split(settings.AdminTelegramIDs, ",") {
		if strings.TrimSpace(idStr) == userIDStr {
			return true
		}
	}
	return false
}

// GetTasksByTelegramUser returns recent tasks for a specific Telegram user
func GetTasksByTelegramUser(telegramUserID int64, limit int) ([]Task, error) {
	rows, err := DB.Query("SELECT id, user_id, IFNULL(telegram_user_id, 0), file_name, IFNULL(file_size, 0), input_type, IFNULL(download_progress, 0), IFNULL(upload_progress, 0), IFNULL(download_speed, 0), IFNULL(upload_speed, 0), status, IFNULL(drive_link, ''), IFNULL(drive_file_id, ''), IFNULL(elapsed_time, ''), created_at FROM tasks WHERE telegram_user_id = ? ORDER BY id DESC LIMIT ?", telegramUserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var t Task
		err := rows.Scan(&t.ID, &t.UserID, &t.TelegramUserID, &t.FileName, &t.FileSize, &t.InputType, &t.DownloadProgress, &t.UploadProgress, &t.DownloadSpeed, &t.UploadSpeed, &t.Status, &t.DriveLink, &t.DriveFileID, &t.ElapsedTime, &t.CreatedAt)
		if err != nil {
			log.Printf("Error scanning task: %v", err)
			continue
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func UpdateTaskStatus(taskID int, status string, driveLink string, driveFileID string, elapsedTime string) error {
	_, err := DB.Exec("UPDATE tasks SET status = ?, drive_link = ?, drive_file_id = ?, elapsed_time = ? WHERE id = ?", status, driveLink, driveFileID, elapsedTime, taskID)
	return err
}

func UpdateTaskDownloadProgress(taskID int, progress int, speed int64) error {
	_, err := DB.Exec("UPDATE tasks SET download_progress = ?, download_speed = ? WHERE id = ?", progress, speed, taskID)
	return err
}

func UpdateTaskUploadProgress(taskID int, progress int, speed int64) error {
	_, err := DB.Exec("UPDATE tasks SET upload_progress = ?, upload_speed = ? WHERE id = ?", progress, speed, taskID)
	return err
}

func GetAllTasks() ([]Task, error) {
	rows, err := DB.Query("SELECT id, user_id, file_name, IFNULL(file_size, 0), input_type, IFNULL(download_progress, 0), IFNULL(upload_progress, 0), IFNULL(download_speed, 0), IFNULL(upload_speed, 0), status, IFNULL(drive_link, ''), IFNULL(drive_file_id, ''), IFNULL(elapsed_time, ''), created_at FROM tasks ORDER BY id DESC LIMIT 50")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var t Task
		err := rows.Scan(&t.ID, &t.UserID, &t.FileName, &t.FileSize, &t.InputType, &t.DownloadProgress, &t.UploadProgress, &t.DownloadSpeed, &t.UploadSpeed, &t.Status, &t.DriveLink, &t.DriveFileID, &t.ElapsedTime, &t.CreatedAt)
		if err != nil {
			log.Printf("Error scanning task in GetAllTasks: %v", err)
			continue
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func GetStatusSummary() (int, int, error) {
	var downloads, uploads int
	err := DB.QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'Downloading'").Scan(&downloads)
	if err != nil {
		return 0, 0, err
	}
	err = DB.QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'Uploading'").Scan(&uploads)
	return downloads, uploads, err
}

func GetExpiredTasks(hours int) ([]Task, error) {
	rows, err := DB.Query("SELECT id, user_id, file_name, IFNULL(file_size, 0), input_type, IFNULL(download_progress, 0), IFNULL(upload_progress, 0), IFNULL(download_speed, 0), IFNULL(upload_speed, 0), status, IFNULL(drive_link, ''), IFNULL(drive_file_id, ''), IFNULL(elapsed_time, ''), created_at FROM tasks WHERE status = 'Completed' AND IFNULL(drive_file_id, '') != '' AND created_at < DATE_SUB(NOW(), INTERVAL ? HOUR)", hours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var t Task
		err := rows.Scan(&t.ID, &t.UserID, &t.FileName, &t.FileSize, &t.InputType, &t.DownloadProgress, &t.UploadProgress, &t.DownloadSpeed, &t.UploadSpeed, &t.Status, &t.DriveLink, &t.DriveFileID, &t.ElapsedTime, &t.CreatedAt)
		if err != nil {
			log.Printf("Error scanning task in GetExpiredTasks: %v", err)
			continue
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// ===== Bridge Token Functions =====

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// GenerateBridgeToken creates a new bridge token for a user, revoking any existing one.
// Returns the raw token (shown once) and errors.
func GenerateBridgeToken(userID int) (string, error) {
	// Delete any existing tokens for this user
	DB.Exec("DELETE FROM bridge_tokens WHERE user_id = ?", userID)

	// Generate random bytes for the token
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	rawToken := fmt.Sprintf("gdbridge_%d_%s", userID, hex.EncodeToString(b))
	tokenHash := hashToken(rawToken)
	tokenPrefix := rawToken[:20] + "..."

	_, err := DB.Exec("INSERT INTO bridge_tokens (user_id, token_hash, token_prefix) VALUES (?, ?, ?)",
		userID, tokenHash, tokenPrefix)
	if err != nil {
		return "", err
	}

	return rawToken, nil
}

// ValidateBridgeToken validates a raw token and returns the user ID if valid.
func ValidateBridgeToken(rawToken string) (int, error) {
	tokenHash := hashToken(rawToken)

	var userID int
	err := DB.QueryRow("SELECT user_id FROM bridge_tokens WHERE token_hash = ?", tokenHash).Scan(&userID)
	if err != nil {
		return 0, fmt.Errorf("invalid token")
	}

	// Update last_used timestamp
	DB.Exec("UPDATE bridge_tokens SET last_used = NOW() WHERE token_hash = ?", tokenHash)

	return userID, nil
}

// RevokeBridgeToken deletes all bridge tokens for a user.
func RevokeBridgeToken(userID int) error {
	_, err := DB.Exec("DELETE FROM bridge_tokens WHERE user_id = ?", userID)
	return err
}

// GetBridgeTokenStatus returns the bridge token info for a user (without the hash).
func GetBridgeTokenStatus(userID int) (*BridgeToken, error) {
	var bt BridgeToken
	var lastUsed sql.NullTime
	err := DB.QueryRow("SELECT id, user_id, token_prefix, last_used, created_at FROM bridge_tokens WHERE user_id = ? LIMIT 1", userID).Scan(
		&bt.ID, &bt.UserID, &bt.TokenPrefix, &lastUsed, &bt.CreatedAt)
	if err != nil {
		return nil, err
	}
	if lastUsed.Valid {
		bt.LastUsed = &lastUsed.Time
	}
	return &bt, nil
}

// LogBridgeRequest logs a bridge link request.
func LogBridgeRequest(userID int, url, sourceSite, filename, fileSize, status string, taskID int) error {
	_, err := DB.Exec("INSERT INTO bridge_logs (user_id, url, source_site, filename, file_size, status, task_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
		userID, url, sourceSite, filename, fileSize, status, taskID)
	return err
}

// GetBridgeLogs returns the most recent bridge logs for a user.
func GetBridgeLogs(userID int, limit int) ([]BridgeLog, error) {
	rows, err := DB.Query("SELECT id, user_id, url, IFNULL(source_site, ''), IFNULL(filename, ''), IFNULL(file_size, ''), IFNULL(status, 'sent'), IFNULL(task_id, 0), created_at FROM bridge_logs WHERE user_id = ? ORDER BY id DESC LIMIT ?", userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []BridgeLog
	for rows.Next() {
		var bl BridgeLog
		err := rows.Scan(&bl.ID, &bl.UserID, &bl.URL, &bl.SourceSite, &bl.Filename, &bl.FileSize, &bl.Status, &bl.TaskID, &bl.CreatedAt)
		if err != nil {
			log.Printf("Error scanning bridge log: %v", err)
			continue
		}
		logs = append(logs, bl)
	}
	return logs, nil
}
