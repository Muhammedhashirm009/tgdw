package downloader

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// DownloadAria2c downloads a file using aria2c (multi-connection for maximum speed).
// Falls back to DownloadHTTP if aria2c is not installed on the system.
// totalSize can be 0 if unknown — a HEAD request will be attempted to retrieve it.
func DownloadAria2c(ctx context.Context, url string, destDir string, filename string, totalSize int64, callback ProgressCallback) (string, error) {
	// Check if aria2c is available on this system
	aria2cPath, err := exec.LookPath("aria2c")
	if err != nil {
		log.Printf("aria2c not found, falling back to HTTP download: %v", err)
		return DownloadHTTP(ctx, url, destDir, filename, callback)
	}

	// If we don't know the total size yet, try a quick HEAD request
	if totalSize <= 0 {
		totalSize = fetchContentLength(url)
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}

	destPath := filepath.Join(destDir, filename)

	// Remove any existing partial file so aria2c doesn't get confused
	os.Remove(destPath)
	os.Remove(destPath + ".aria2") // aria2c control file

	// Build aria2c command — 16 connections max for maximum throughput
	args := []string{
		"--max-connection-per-server=16",
		"--split=16",
		"--min-split-size=1M",
		"--max-tries=3",
		"--retry-wait=2",
		"--connect-timeout=10",
		"--timeout=60",
		"--dir", destDir,
		"--out", filename,
		"--allow-overwrite=true",
		"--auto-file-renaming=false",
		"--user-agent=Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		"--quiet=true",
		url,
	}

	cmd := exec.CommandContext(ctx, aria2cPath, args...)

	// Capture stderr for error diagnosis
	stderrPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		log.Printf("aria2c failed to start, falling back to HTTP: %v", err)
		return DownloadHTTP(ctx, url, destDir, filename, callback)
	}

	// Monitor download progress by polling the output file size
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	ticker := time.NewTicker(500 * time.Millisecond) // poll every 500ms for snappy UI
	defer ticker.Stop()

	startTime := time.Now()
	var lastSize int64
	var lastPollTime = startTime

	for {
		select {
		case <-ctx.Done():
			cmd.Process.Kill()
			os.Remove(destPath)
			os.Remove(destPath + ".aria2")
			return "", ctx.Err()

		case downloadErr := <-done:
			// aria2c finished — read any stderr
			if stderrPipe != nil {
				stderrBytes := make([]byte, 512)
				n, _ := stderrPipe.Read(stderrBytes)
				if n > 0 {
					log.Printf("aria2c stderr: %s", stderrBytes[:n])
				}
			}

			if downloadErr != nil {
				log.Printf("aria2c exited with error: %v — falling back to HTTP", downloadErr)
				// Clean up and fall back
				os.Remove(destPath)
				os.Remove(destPath + ".aria2")
				return DownloadHTTP(ctx, url, destDir, filename, callback)
			}

			// Final progress callback at 100%
			if stat, serr := os.Stat(destPath); serr == nil && callback != nil {
				finalSize := stat.Size()
				elapsed := time.Since(lastPollTime).Seconds()
				var finalSpeed int64
				if elapsed > 0 && finalSize > lastSize {
					finalSpeed = int64(float64(finalSize-lastSize) / elapsed)
				}
				if totalSize <= 0 {
					totalSize = finalSize
				}
				callback(finalSize, totalSize, finalSpeed)
			}

			// Clean up the aria2c control file if it still exists
			os.Remove(destPath + ".aria2")
			return destPath, nil

		case <-ticker.C:
			stat, serr := os.Stat(destPath)
			if serr != nil {
				continue // file not created yet — aria2c is still connecting
			}

			currentSize := stat.Size()
			now := time.Now()
			elapsed := now.Sub(lastPollTime).Seconds()

			var speed int64
			if elapsed > 0 && currentSize > lastSize {
				speed = int64(float64(currentSize-lastSize) / elapsed)
			}

			if callback != nil && currentSize > 0 {
				callback(currentSize, totalSize, speed)
			}

			lastSize = currentSize
			lastPollTime = now
		}
	}
}

// fetchContentLength does a HEAD request to get the Content-Length of a URL.
// Returns 0 if it cannot be determined.
func fetchContentLength(url string) int64 {
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		return 0
	}
	defer resp.Body.Close()
	if resp.ContentLength > 0 {
		return resp.ContentLength
	}
	return 0
}

// Aria2cAvailable returns true if aria2c is installed on the system.
func Aria2cAvailable() bool {
	_, err := exec.LookPath("aria2c")
	return err == nil
}

// FormatDownloadMethod returns a human-readable label for the download method being used.
func FormatDownloadMethod() string {
	if Aria2cAvailable() {
		return "⚡ aria2c (16x)"
	}
	return "📥 HTTP"
}

// InstallAria2c attempts to install aria2c via apt-get (for Debian/Ubuntu systems).
// This is a best-effort operation and should only be called once at startup.
func InstallAria2c() {
	if Aria2cAvailable() {
		return
	}
	log.Println("aria2c not found — attempting to install via apt-get...")
	cmd := exec.Command("apt-get", "install", "-y", "aria2")
	if err := cmd.Run(); err != nil {
		log.Printf("Warning: Could not install aria2c automatically: %v. Downloads will use single-connection HTTP.", err)
	} else {
		log.Println("aria2c installed successfully.")
	}
}

// bestEffortSize formats a size string showing downloaded vs total
func BestEffortSizeStr(downloaded, total int64) string {
	if total > 0 {
		return fmt.Sprintf("%s / %s", formatHuman(downloaded), formatHuman(total))
	}
	return formatHuman(downloaded)
}

func formatHuman(b int64) string {
	const k = 1024
	switch {
	case b >= k*k*k:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(k*k*k))
	case b >= k*k:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(k*k))
	case b >= k:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(k))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
