package downloader

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// TelegramFileDownloader downloads files from Telegram using local Bot API server (no size limit)
type TelegramFileDownloader struct {
	BotToken   string
	APIBaseURL string // http://127.0.0.1:8081 for local, https://api.telegram.org for standard
}

// NewTelegramFileDownloader creates a downloader.
// If local Bot API server is available at port 8081, uses it (no 20MB limit).
// Otherwise falls back to standard Bot API (20MB limit).
func NewTelegramFileDownloader(botToken string) *TelegramFileDownloader {
	baseURL := "https://api.telegram.org"

	// Check if local Bot API server is running on port 8081
	conn, err := net.DialTimeout("tcp", "127.0.0.1:8081", 2*time.Second)
	if err == nil {
		conn.Close()
		baseURL = "http://127.0.0.1:8081"
		log.Println("📲 Using local Telegram Bot API server (no file size limit)")
	} else {
		log.Println("⚠️ Local Bot API server not available, using standard API (20MB limit)")
	}

	return &TelegramFileDownloader{
		BotToken:   botToken,
		APIBaseURL: baseURL,
	}
}

// getFileResponse is the response from Bot API's getFile
type getFileResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		FileID   string `json:"file_id"`
		FileSize int64  `json:"file_size"`
		FilePath string `json:"file_path"`
	} `json:"result"`
}

// DownloadByFileID downloads a Telegram file using Bot API getFile + download
func (tfd *TelegramFileDownloader) DownloadByFileID(ctx context.Context, fileID, destDir, fileName string, callback ProgressCallback) (string, error) {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	destPath := filepath.Join(destDir, fileName)

	// Step 1: Call getFile to get the file_path
	getFileURL := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", tfd.APIBaseURL, tfd.BotToken, fileID)
	log.Printf("📲 getFile: %s", getFileURL)

	resp, err := http.Get(getFileURL)
	if err != nil {
		return "", fmt.Errorf("getFile request failed: %w", err)
	}
	defer resp.Body.Close()

	// Parse JSON response manually to avoid importing encoding/json just for this
	body, _ := io.ReadAll(resp.Body)
	filePath := extractJSONString(body, "file_path")
	if filePath == "" {
		return "", fmt.Errorf("getFile failed or file_path empty. Response: %s", string(body[:min(len(body), 200)]))
	}

	// Step 2: Download the file
	var downloadURL string
	if tfd.APIBaseURL == "http://127.0.0.1:8081" {
		// Local Bot API: files are stored locally, path is absolute
		// Check if filePath is an absolute path (local server stores files on disk)
		if filepath.IsAbs(filePath) {
			// Direct file copy from local Bot API server storage
			log.Printf("📂 Local file copy: %s -> %s", filePath, destPath)
			return copyLocalFile(ctx, filePath, destPath, callback)
		}
		downloadURL = fmt.Sprintf("%s/file/bot%s/%s", tfd.APIBaseURL, tfd.BotToken, filePath)
	} else {
		downloadURL = fmt.Sprintf("%s/file/bot%s/%s", tfd.APIBaseURL, tfd.BotToken, filePath)
	}

	log.Printf("📥 Downloading: %s", downloadURL)

	return DownloadHTTP(ctx, downloadURL, destDir, fileName, callback)
}

// copyLocalFile copies a file from local Bot API server storage to destination
func copyLocalFile(ctx context.Context, src, dst string, callback ProgressCallback) (string, error) {
	srcFile, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("open source: %w", err)
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return "", err
	}
	totalSize := info.Size()

	dstFile, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer dstFile.Close()

	buf := make([]byte, 4*1024*1024) // 4MB buffer
	var copied int64
	lastReport := time.Now()
	var lastReportBytes int64

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		n, readErr := srcFile.Read(buf)
		if n > 0 {
			_, writeErr := dstFile.Write(buf[:n])
			if writeErr != nil {
				return "", writeErr
			}
			copied += int64(n)

			now := time.Now()
			if callback != nil && now.Sub(lastReport) >= time.Second {
				speed := int64(float64(copied-lastReportBytes) / now.Sub(lastReport).Seconds())
				callback(copied, totalSize, speed)
				lastReport = now
				lastReportBytes = copied
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}

	return dst, nil
}

// extractJSONString extracts a string value for a key from JSON bytes (simple parser)
func extractJSONString(data []byte, key string) string {
	s := string(data)
	searchKey := fmt.Sprintf(`"%s":"`, key)
	idx := indexOf(s, searchKey)
	if idx < 0 {
		// Try with space after colon
		searchKey = fmt.Sprintf(`"%s": "`, key)
		idx = indexOf(s, searchKey)
	}
	if idx < 0 {
		return ""
	}

	start := idx + len(searchKey)
	end := start
	for end < len(s) && s[end] != '"' {
		if s[end] == '\\' {
			end++ // skip escaped char
		}
		end++
	}
	if end > start {
		return s[start:end]
	}
	return ""
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
