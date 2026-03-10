package database

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

var DB *sql.DB

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
			token_expiry TIMESTAMP NULL
		)`,
		`CREATE TABLE IF NOT EXISTS logs (
			id INT AUTO_INCREMENT PRIMARY KEY,
			level VARCHAR(50) NOT NULL,
			message TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
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

	DB.Exec("ALTER TABLE tasks ADD COLUMN download_speed BIGINT DEFAULT 0")
	DB.Exec("ALTER TABLE tasks ADD COLUMN upload_speed BIGINT DEFAULT 0")
	DB.Exec("ALTER TABLE tasks ADD COLUMN drive_file_id VARCHAR(255) DEFAULT ''")

	log.Println("Database tables verified/created successfully")
	return nil
}

func GetSettings() (Settings, error) {
	var s Settings
	var expiry sql.NullTime
	var access, refresh sql.NullString

	// Fetch the first settings row
	err := DB.QueryRow("SELECT id, IFNULL(bot_token, ''), IFNULL(google_client_id, ''), IFNULL(google_client_secret, ''), download_directory, max_file_size, concurrent_tasks, IFNULL(telegram_api_endpoint, 'http://telegram-bot-api:8081'), IFNULL(telegram_api_id, ''), IFNULL(telegram_api_hash, ''), access_token, refresh_token, token_expiry FROM settings ORDER BY id ASC LIMIT 1").Scan(
		&s.ID, &s.BotToken, &s.GoogleClientID, &s.GoogleClientSecret, &s.DownloadDirectory, &s.MaxFileSize, &s.ConcurrentTasks, &s.TelegramAPIEndpoint, &s.TelegramAPIID, &s.TelegramAPIHash, &access, &refresh, &expiry,
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

	if err == sql.ErrNoRows {
		// Insert default row
		_, err = DB.Exec("INSERT INTO settings (bot_token, google_client_id, google_client_secret, download_directory, max_file_size, concurrent_tasks, telegram_api_endpoint, telegram_api_id, telegram_api_hash) VALUES ('', '', '', '/data/downloads', 0, 3, 'http://telegram-bot-api:8081', '', '')")
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
		SET bot_token = ?, google_client_id = ?, google_client_secret = ?, download_directory = ?, max_file_size = ?, concurrent_tasks = ?, telegram_api_endpoint = ?, telegram_api_id = ?, telegram_api_hash = ?
		WHERE id = ?`,
		s.BotToken, s.GoogleClientID, s.GoogleClientSecret, s.DownloadDirectory, s.MaxFileSize, s.ConcurrentTasks, s.TelegramAPIEndpoint, s.TelegramAPIID, s.TelegramAPIHash, s.ID,
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
	// Seed a dummy user if they don't exist to satisfy the FOREIGN KEY constraint
	DB.Exec("INSERT IGNORE INTO users (id, username, email, password_hash) VALUES (?, 'admin', 'admin@localhost', 'hash')", userID)

	res, err := DB.Exec(`
		INSERT INTO tasks (user_id, file_name, file_size, input_type, status) 
		VALUES (?, ?, ?, ?, 'Pending')`,
		userID, fileName, fileSize, inputType,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return int(id), err
}

func UpdateTaskStatus(taskID int, status string, driveLink string, driveFileID string) error {
	_, err := DB.Exec("UPDATE tasks SET status = ?, drive_link = ?, drive_file_id = ? WHERE id = ?", status, driveLink, driveFileID, taskID)
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
	rows, err := DB.Query("SELECT id, user_id, file_name, file_size, input_type, download_progress, upload_progress, download_speed, upload_speed, status, IFNULL(drive_link, '') as drive_link, IFNULL(drive_file_id, '') as drive_file_id, created_at FROM tasks ORDER BY id DESC LIMIT 50")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var t Task
		err := rows.Scan(&t.ID, &t.UserID, &t.FileName, &t.FileSize, &t.InputType, &t.DownloadProgress, &t.UploadProgress, &t.DownloadSpeed, &t.UploadSpeed, &t.Status, &t.DriveLink, &t.DriveFileID, &t.CreatedAt)
		if err != nil {
			return nil, err
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
	rows, err := DB.Query("SELECT id, user_id, file_name, file_size, input_type, download_progress, upload_progress, download_speed, upload_speed, status, IFNULL(drive_link, '') as drive_link, IFNULL(drive_file_id, '') as drive_file_id, created_at FROM tasks WHERE status = 'Completed' AND drive_file_id != '' AND created_at < DATE_SUB(NOW(), INTERVAL ? HOUR)", hours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var t Task
		err := rows.Scan(&t.ID, &t.UserID, &t.FileName, &t.FileSize, &t.InputType, &t.DownloadProgress, &t.UploadProgress, &t.DownloadSpeed, &t.UploadSpeed, &t.Status, &t.DriveLink, &t.DriveFileID, &t.CreatedAt)
		if err != nil {
			continue
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}
