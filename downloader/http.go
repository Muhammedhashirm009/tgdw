package downloader

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ProgressCallback is called repeatedly to report progress
type ProgressCallback func(bytesDownloaded int64, totalBytes int64, speedBytesPerSec int64)

// DownloadHTTP downloads a file from a URL to a specified directory
func DownloadHTTP(url string, destDir string, filename string, callback ProgressCallback) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bad HTTP status: %s", resp.Status)
	}

	totalSize := resp.ContentLength
	
	// Create destination directory if it doesn't exist
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}

	destPath := filepath.Join(destDir, filename)
	
	out, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	buf := make([]byte, 32*1024)
	var downloaded int64
	var lastReportedDownloaded int64
	lastReportTime := time.Now()

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_, writeErr := out.Write(buf[:n])
			if writeErr != nil {
				return "", writeErr
			}
			downloaded += int64(n)
			
			now := time.Now()
			elapsed := now.Sub(lastReportTime)
			
			// Compute speed and trigger callback every 1 second
			if elapsed >= time.Second && callback != nil {
				speed := int64(float64(downloaded-lastReportedDownloaded) / elapsed.Seconds())
				callback(downloaded, totalSize, speed)
				lastReportTime = now
				lastReportedDownloaded = downloaded
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return destPath, nil
}
