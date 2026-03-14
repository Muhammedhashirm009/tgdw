package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// StreamTransfer makes a single GET request, dynamically extracting the total size if available,
// and streams the data concurrently to the provided writer via an io.Pipe to the Google Drive uploader.
// The callback receives (downloadedBytes, totalBytes, speedBytesPerSec).
func StreamTransfer(ctx context.Context, url string, writer io.Writer, callback ProgressCallback) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	// Spoof user-agent to avoid basic blocks on free-file hosts
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	req.Header.Set("Accept", "*/*")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("bad status code during stream: %d (%s)", resp.StatusCode, resp.Status)
	}

	totalSize := resp.ContentLength
	// ContentLength can be -1 if the server doesn't provide it. That's fine! 
	// We'll just stream dynamically until EOF.

	buf := make([]byte, 256*1024) // 256 KB buffer for high speed throughput
	var transferred int64
	var lastReported int64
	lastReportTime := time.Now()

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_, writeErr := writer.Write(buf[:n])
			if writeErr != nil {
				return transferred, fmt.Errorf("error writing to upload stream pipe: %v", writeErr)
			}
			transferred += int64(n)

			now := time.Now()
			elapsed := now.Sub(lastReportTime)

			// Callback every 1 second
			if elapsed >= time.Second && callback != nil {
				speed := int64(float64(transferred-lastReported) / elapsed.Seconds())
				callback(transferred, totalSize, speed)
				lastReportTime = now
				lastReported = transferred
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return transferred, err
		}
	}

	// Final callback hit to force 100% completion state in UI
	if callback != nil && transferred > 0 {
		callback(transferred, totalSize, 0)
	}

	return transferred, nil
}
