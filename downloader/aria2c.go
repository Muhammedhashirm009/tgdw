package downloader

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// aria2c progress line format:
// [#gid DOWNLOADED/TOTAL(N%) CN:N DL:SPEED ETA:T]
// Example: [#b15d6a 5.4MiB/100MiB(5%) CN:16 DL:3.2MiB ETA:28s]
// The (N%) token is optional and appears when total is known.
var aria2ProgressRe = regexp.MustCompile(`\[#\w+\s+([\d.]+\w*)\/([\d.]+\w*)\(?[\d%]*\)?\s+CN:\d+\s+DL:([\d.]+\w*)`)

// DownloadAria2c downloads a file using aria2c (multi-connection for maximum speed).
// Falls back to DownloadHTTP if aria2c is not installed on the system.
// totalSize can be 0 if unknown — a HEAD request will be attempted to retrieve it.
func DownloadAria2c(ctx context.Context, url string, destDir string, filename string, totalSize int64, callback ProgressCallback) (string, error) {
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
	os.Remove(destPath)
	os.Remove(destPath + ".aria2")

	// --file-allocation=none: CRITICAL — prevents sparse pre-allocation which
	// would make os.Stat() return the full file size before any bytes download.
	// --show-console-readout + --summary-interval=1: emit progress lines to stdout
	// so we can parse real downloaded bytes instead of polling the file.
	args := []string{
		"--max-connection-per-server=16",
		"--split=16",
		"--min-split-size=1M",
		"--max-tries=3",
		"--retry-wait=2",
		"--connect-timeout=10",
		"--timeout=60",
		"--file-allocation=none",
		"--show-console-readout=true",
		"--summary-interval=1",
		"--dir", destDir,
		"--out", filename,
		"--allow-overwrite=true",
		"--auto-file-renaming=false",
		"--user-agent=Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		url,
	}

	cmd := exec.CommandContext(ctx, aria2cPath, args...)

	// Capture stdout for progress parsing (aria2c writes progress to stdout)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return DownloadHTTP(ctx, url, destDir, filename, callback)
	}

	if err := cmd.Start(); err != nil {
		log.Printf("aria2c failed to start, falling back to HTTP: %v", err)
		return DownloadHTTP(ctx, url, destDir, filename, callback)
	}

	type progressUpdate struct {
		downloaded int64
		total      int64
		speed      int64
	}
	progressCh := make(chan progressUpdate, 32)

	// Parse aria2c's progress output line by line
	go func() {
		defer close(progressCh)
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.Contains(line, "DL:") {
				continue
			}
			m := aria2ProgressRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			downloaded := parseAria2Size(m[1])
			parsedTotal := parseAria2Size(m[2])
			speed := parseAria2Size(m[3])

			// Use the known total if aria2c reports 0 (server didn't send Content-Length)
			if parsedTotal <= 0 && totalSize > 0 {
				parsedTotal = totalSize
			}

			if downloaded > 0 || speed > 0 {
				select {
				case progressCh <- progressUpdate{downloaded, parsedTotal, speed}:
				default:
				}
			}
		}
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var latestProgress progressUpdate
	var hasNewProgress bool

	for {
		select {
		case <-ctx.Done():
			cmd.Process.Kill()
			os.Remove(destPath)
			os.Remove(destPath + ".aria2")
			return "", ctx.Err()

		case p, ok := <-progressCh:
			if ok {
				latestProgress = p
				hasNewProgress = true
			}

		case <-ticker.C:
			if hasNewProgress && callback != nil {
				callback(latestProgress.downloaded, latestProgress.total, latestProgress.speed)
				hasNewProgress = false
			}

		case downloadErr := <-done:
			// Drain remaining progress
			for p := range progressCh {
				latestProgress = p
			}

			if downloadErr != nil {
				log.Printf("aria2c error: %v — falling back to HTTP", downloadErr)
				os.Remove(destPath)
				os.Remove(destPath + ".aria2")
				return DownloadHTTP(ctx, url, destDir, filename, callback)
			}

			// Final callback with accurate disk size
			if stat, serr := os.Stat(destPath); serr == nil && callback != nil {
				finalSize := stat.Size()
				if totalSize <= 0 {
					totalSize = finalSize
				}
				callback(finalSize, totalSize, 0)
			}

			os.Remove(destPath + ".aria2")
			return destPath, nil
		}
	}
}

// parseAria2Size converts aria2c human-readable size strings to bytes.
// Handles: 0B, 100KiB, 45MiB, 1.2GiB, 134MiB, etc.
func parseAria2Size(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || s == "0B" {
		return 0
	}
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	if i == 0 {
		return 0
	}
	numStr := s[:i]
	unit := strings.ToUpper(strings.TrimSpace(s[i:]))
	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0
	}
	switch unit {
	case "B", "":
		return int64(val)
	case "K", "KB", "KIB":
		return int64(val * 1024)
	case "M", "MB", "MIB":
		return int64(val * 1024 * 1024)
	case "G", "GB", "GIB":
		return int64(val * 1024 * 1024 * 1024)
	case "T", "TB", "TIB":
		return int64(val * 1024 * 1024 * 1024 * 1024)
	}
	return int64(val)
}

// fetchContentLength does a HEAD request to get the Content-Length of a URL.
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
func InstallAria2c() {
	if Aria2cAvailable() {
		return
	}
	log.Println("aria2c not found — attempting to install via apt-get...")
	cmd := exec.Command("apt-get", "install", "-y", "aria2")
	if err := cmd.Run(); err != nil {
		log.Printf("Warning: Could not install aria2c: %v. Downloads will use single-connection HTTP.", err)
	} else {
		log.Println("aria2c installed successfully.")
	}
}

// BestEffortSizeStr formats downloaded vs total as a readable string.
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
