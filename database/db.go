package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
)

var DB *sql.DB
var taskCancels sync.Map
var memoryTasks sync.Map
var memoryTaskCounter int = 1
var memoryTaskMu sync.Mutex
var premiumUsers sync.Map

// Global Context Register for Tasks
func RegisterCancelFunc(taskID int, cancel context.CancelFunc) {
	taskCancels.Store(taskID, cancel)
}

func CancelTask(taskID int) bool {
	if cancel, ok := taskCancels.LoadAndDelete(taskID); ok {
		cancel.(context.CancelFunc)()
	}

	if DB != nil {
		res, err := DB.Exec("UPDATE tasks SET status = 'Cancelled' WHERE id = ? AND status IN ('Downloading', 'Uploading', 'Pending')", taskID)
		if err == nil {
			rows, _ := res.RowsAffected()
			return rows > 0
		}
	}
	return true
}

func InitDB() error {
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		log.Println("Notice: MYSQL_HOST not configured. Running bot in standalone mode with Worker API.")
		return nil
	}
	user := os.Getenv("MYSQL_USER")
	pass := os.Getenv("MYSQL_PASSWORD")
	dbname := os.Getenv("MYSQL_DATABASE")

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true", user, pass, host, dbname)
	
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Printf("Warning: MySQL connection failed: %v", err)
		return nil
	}

	db.SetConnMaxLifetime(45 * time.Second)
	db.SetConnMaxIdleTime(45 * time.Second)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)

	if err := db.Ping(); err != nil {
		log.Printf("Warning: MySQL ping failed: %v. Running in standalone mode.", err)
		return nil
	}

	DB = db
	log.Println("MySQL Database connected successfully.")
	return nil
}

func VerifyUser(username, password string) bool {
	if DB == nil {
		return username == "admin" && password == "99901234"
	}
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
	s.ID = 1
	s.BotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	s.GoogleClientID = os.Getenv("GOOGLE_CLIENT_ID")
	s.GoogleClientSecret = os.Getenv("GOOGLE_CLIENT_SECRET")
	s.DownloadDirectory = "./downloads"
	s.ConcurrentTasks = 3
	s.TelegramAPIEndpoint = "https://api.telegram.org"
	s.TelegramAPIID = os.Getenv("TELEGRAM_API_ID")
	s.TelegramAPIHash = os.Getenv("TELEGRAM_API_HASH")
	s.MaxFileSizeNormal = 4294967296 // 4GB default
	s.RetentionHours = 48
	s.AdminTelegramIDs = os.Getenv("ADMIN_TELEGRAM_ID")

	if DB == nil {
		return s, nil
	}

	var expiry sql.NullTime
	var access, refresh, adminIDs sql.NullString
	var maxNormal sql.NullInt64

	err := DB.QueryRow("SELECT id, IFNULL(bot_token, ''), IFNULL(google_client_id, ''), IFNULL(google_client_secret, ''), download_directory, max_file_size, concurrent_tasks, IFNULL(telegram_api_endpoint, 'http://telegram-bot-api:8081'), IFNULL(telegram_api_id, ''), IFNULL(telegram_api_hash, ''), access_token, refresh_token, token_expiry, IFNULL(retention_hours, 48), admin_telegram_ids, max_file_size_normal FROM settings ORDER BY id ASC LIMIT 1").Scan(
		&s.ID, &s.BotToken, &s.GoogleClientID, &s.GoogleClientSecret, &s.DownloadDirectory, &s.MaxFileSize, &s.ConcurrentTasks, &s.TelegramAPIEndpoint, &s.TelegramAPIID, &s.TelegramAPIHash, &access, &refresh, &expiry, &s.RetentionHours, &adminIDs, &maxNormal,
	)
	
	if access.Valid { s.AccessToken = access.String }
	if refresh.Valid { s.RefreshToken = refresh.String }
	if expiry.Valid { s.TokenExpiry = expiry.Time }
	if adminIDs.Valid { s.AdminTelegramIDs = adminIDs.String }
	if maxNormal.Valid { s.MaxFileSizeNormal = maxNormal.Int64 }

	return s, err
}

func UpdateSettings(s Settings) error {
	if DB == nil { return nil }
	_, err := DB.Exec(`
		UPDATE settings 
		SET bot_token = ?, google_client_id = ?, google_client_secret = ?, download_directory = ?, max_file_size = ?, concurrent_tasks = ?, telegram_api_endpoint = ?, telegram_api_id = ?, telegram_api_hash = ?, retention_hours = ?, admin_telegram_ids = ?, max_file_size_normal = ?
		WHERE id = ?`,
		s.BotToken, s.GoogleClientID, s.GoogleClientSecret, s.DownloadDirectory, s.MaxFileSize, s.ConcurrentTasks, s.TelegramAPIEndpoint, s.TelegramAPIID, s.TelegramAPIHash, s.RetentionHours, s.AdminTelegramIDs, s.MaxFileSizeNormal, s.ID,
	)
	return err
}

func UpdateOAuthTokens(id int, accessToken, refreshToken string, expiry time.Time) error {
	if DB == nil { return nil }
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
	if DB == nil {
		memoryTaskMu.Lock()
		taskID := memoryTaskCounter
		memoryTaskCounter++
		memoryTaskMu.Unlock()

		t := Task{
			ID:             taskID,
			UserID:         userID,
			TelegramUserID: telegramUserID,
			FileName:       fileName,
			FileSize:       fileSize,
			InputType:      inputType,
			Status:         "Pending",
			CreatedAt:      time.Now(),
		}
		memoryTasks.Store(taskID, t)
		return taskID, nil
	}

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

func GetDailyTaskCount(telegramUserID int64) (int, error) {
	if DB == nil {
		count := 0
		now := time.Now()
		memoryTasks.Range(func(key, value interface{}) bool {
			if t, ok := value.(Task); ok {
				if t.TelegramUserID == telegramUserID && t.CreatedAt.Year() == now.Year() && t.CreatedAt.YearDay() == now.YearDay() {
					count++
				}
			}
			return true
		})
		return count, nil
	}
	var count int
	err := DB.QueryRow("SELECT COUNT(*) FROM tasks WHERE telegram_user_id = ? AND DATE(created_at) = CURDATE()", telegramUserID).Scan(&count)
	return count, err
}

func IsAdminTelegram(telegramUserID int64) bool {
	envAdminID := os.Getenv("ADMIN_TELEGRAM_ID")
	if envAdminID != "" {
		userIDStr := strconv.FormatInt(telegramUserID, 10)
		for _, idStr := range strings.Split(envAdminID, ",") {
			if strings.TrimSpace(idStr) == userIDStr {
				return true
			}
		}
	}

	settings, err := GetSettings()
	if err == nil && settings.AdminTelegramIDs != "" {
		userIDStr := strconv.FormatInt(telegramUserID, 10)
		for _, idStr := range strings.Split(settings.AdminTelegramIDs, ",") {
			if strings.TrimSpace(idStr) == userIDStr {
				return true
			}
		}
	}
	return false
}

// IsPremiumTelegram checks if user is Admin, in PREMIUM_TELEGRAM_IDS env, or in memory premium list
func IsPremiumTelegram(telegramUserID int64) bool {
	if IsAdminTelegram(telegramUserID) {
		return true
	}

	envPremID := os.Getenv("PREMIUM_TELEGRAM_IDS")
	if envPremID != "" {
		userIDStr := strconv.FormatInt(telegramUserID, 10)
		for _, idStr := range strings.Split(envPremID, ",") {
			if strings.TrimSpace(idStr) == userIDStr {
				return true
			}
		}
	}

	if _, ok := premiumUsers.Load(telegramUserID); ok {
		return true
	}

	return false
}

// GrantPremium Access to a Telegram User ID
func GrantPremium(telegramUserID int64) {
	premiumUsers.Store(telegramUserID, true)
}

func GetTasksByTelegramUser(telegramUserID int64, limit int) ([]Task, error) {
	if DB == nil {
		var tasks []Task
		memoryTasks.Range(func(key, value interface{}) bool {
			if t, ok := value.(Task); ok {
				if t.TelegramUserID == telegramUserID {
					tasks = append(tasks, t)
				}
			}
			return true
		})
		if len(tasks) > limit {
			tasks = tasks[len(tasks)-limit:]
		}
		return tasks, nil
	}

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
	if DB == nil {
		if val, ok := memoryTasks.Load(taskID); ok {
			if t, ok := val.(Task); ok {
				t.Status = status
				t.DriveLink = driveLink
				t.DriveFileID = driveFileID
				t.ElapsedTime = elapsedTime
				memoryTasks.Store(taskID, t)
			}
		}
		return nil
	}
	_, err := DB.Exec("UPDATE tasks SET status = ?, drive_link = ?, drive_file_id = ?, elapsed_time = ? WHERE id = ?", status, driveLink, driveFileID, elapsedTime, taskID)
	return err
}

func UpdateTaskDownloadProgress(taskID int, progress int, speed int64) error {
	if DB == nil { return nil }
	_, err := DB.Exec("UPDATE tasks SET download_progress = ?, download_speed = ? WHERE id = ?", progress, speed, taskID)
	return err
}

func UpdateTaskUploadProgress(taskID int, progress int, speed int64) error {
	if DB == nil { return nil }
	_, err := DB.Exec("UPDATE tasks SET upload_progress = ?, upload_speed = ? WHERE id = ?", progress, speed, taskID)
	return err
}

func GetAllTasks() ([]Task, error) {
	if DB == nil {
		var tasks []Task
		memoryTasks.Range(func(key, value interface{}) bool {
			if t, ok := value.(Task); ok {
				tasks = append(tasks, t)
			}
			return true
		})
		return tasks, nil
	}
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
	if DB == nil {
		return 0, 0, nil
	}
	var downloads, uploads int
	err := DB.QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'Downloading'").Scan(&downloads)
	if err != nil {
		return 0, 0, err
	}
	err = DB.QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'Uploading'").Scan(&uploads)
	return downloads, uploads, err
}

func GetExpiredTasks(hours int) ([]Task, error) {
	if DB == nil {
		return nil, nil
	}
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
