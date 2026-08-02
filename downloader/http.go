package downloader

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ProgressCallback is called repeatedly to report progress
type ProgressCallback func(bytesDownloaded int64, totalBytes int64, speedBytesPerSec int64)

// High-speed HTTP client with 8MB read/write buffers, connection pooling
var FastClient = &http.Client{
	Timeout: 0, // No timeout for large file downloads
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 120 * time.Second,
		}).DialContext,
		MaxIdleConns:          2000,
		MaxIdleConnsPerHost:   1000,
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		ReadBufferSize:        32 * 1024 * 1024, // 32MB Socket Read Buffer for max throughput
		WriteBufferSize:       32 * 1024 * 1024, // 32MB Socket Write Buffer for max throughput
	},
}

// DownloadHTTP downloads a file from a URL to a specified directory at maximum speed
func DownloadHTTP(ctx context.Context, url string, destDir string, filename string, callback ProgressCallback) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := FastClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
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

	// High-speed 8MB read buffer
	buf := make([]byte, 8*1024*1024)
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

			// Compute speed and trigger callback every 3 seconds or on EOF
			if (elapsed >= 3*time.Second || err == io.EOF) && callback != nil {
				speed := int64(0)
				if elapsed.Seconds() > 0 {
					speed = int64(float64(downloaded-lastReportedDownloaded) / elapsed.Seconds())
				}
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
