package downloader

import (
	"archive/zip"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
)

type TorrentProgressCallback func(fileName string, bytesCompleted int64, totalBytes int64, speed int64, peers int)

type TorrentDownloader struct {
	client  *torrent.Client
	dataDir string
}

func NewTorrentDownloader(dataDir string) (*TorrentDownloader, error) {
	err := os.MkdirAll(dataDir, 0755)
	if err != nil {
		return nil, err
	}

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dataDir
	
	client, err := torrent.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &TorrentDownloader{client: client, dataDir: dataDir}, nil
}

func (td *TorrentDownloader) Close() {
	if td.client != nil {
		td.client.Close()
	}
}

func (td *TorrentDownloader) monitorProgress(ctx context.Context, t *torrent.Torrent, callback TorrentProgressCallback) error {
	var lastReport time.Time
	var lastBytes int64
	
	for {
		select {
		case <-ctx.Done():
			t.Drop()
			return ctx.Err()
		case <-time.After(time.Second):
			bytesCompleted := t.BytesCompleted()
			totalBytes := t.Info().TotalLength()
			
			speed := int64(0)
			if !lastReport.IsZero() {
				elapsed := time.Since(lastReport).Seconds()
				if elapsed > 0 {
					speed = int64(float64(bytesCompleted-lastBytes) / elapsed)
				}
			}
			
			if callback != nil {
				callback(t.Name(), bytesCompleted, totalBytes, speed, len(t.PeerConns()))
			}
			
			lastReport = time.Now()
			lastBytes = bytesCompleted

			if bytesCompleted == totalBytes {
				log.Printf("Torrent %s finished downloading", t.Name())
				return nil
			}
		}
	}
}

func (td *TorrentDownloader) DownloadMagnet(ctx context.Context, magnetLink string, callback TorrentProgressCallback) (string, error) {
	t, err := td.client.AddMagnet(magnetLink)
	if err != nil {
		return "", err
	}
	defer t.Drop() // Will keep files but remove from client
	
	select {
	case <-t.GotInfo():
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(60 * time.Second): // Timeout getting metadata
		return "", context.DeadlineExceeded
	}
	
	t.DownloadAll()
	
	err = td.monitorProgress(ctx, t, callback)
	if err != nil {
		return "", err
	}
	
	return filepath.Join(td.dataDir, t.Name()), nil
}

func (td *TorrentDownloader) DownloadFile(ctx context.Context, torrentFilePath string, callback TorrentProgressCallback) (string, error) {
	t, err := td.client.AddTorrentFromFile(torrentFilePath)
	if err != nil {
		return "", err
	}
	defer t.Drop()
	
	select {
	case <-t.GotInfo():
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(60 * time.Second):
		return "", context.DeadlineExceeded
	}
	
	t.DownloadAll()
	
	err = td.monitorProgress(ctx, t, callback)
	if err != nil {
		return "", err
	}

	return filepath.Join(td.dataDir, t.Name()), nil
}

// ZipDirectory recursively compresses a directory into a single zip file.
func ZipDirectory(source, target string) error {
	zipfile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer zipfile.Close()

	archive := zip.NewWriter(zipfile)
	defer archive.Close()

	filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}

		header.Name = strings.TrimPrefix(path, filepath.Dir(source)+"/")

		if info.IsDir() {
			header.Name += "/"
		} else {
			header.Method = zip.Deflate
		}

		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(writer, file)
		return err
	})

	return nil
}
