package downloader

import (
	"log"
	"path/filepath"
	"time"

	"github.com/anacrolix/torrent"
)

type TorrentProgressCallback func(fileName string, bytesCompleted int64, totalBytes int64, peers int)

type TorrentDownloader struct {
	client  *torrent.Client
	dataDir string
}

func NewTorrentDownloader(dataDir string) (*TorrentDownloader, error) {
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

func (td *TorrentDownloader) DownloadMagnet(magnetLink string, callback TorrentProgressCallback) (string, error) {
	t, err := td.client.AddMagnet(magnetLink)
	if err != nil {
		return "", err
	}
	
	<-t.GotInfo() // Wait for metadata
	
	t.DownloadAll()
	
	// Monitor progress
	for {
		bytesCompleted := t.BytesCompleted()
		totalBytes := t.Info().TotalLength()
		
		if callback != nil {
			callback(t.Name(), bytesCompleted, totalBytes, len(t.PeerConns()))
		}
		
		if bytesCompleted == totalBytes {
			break
		}
		time.Sleep(2 * time.Second)
	}
	
	log.Printf("Torrent %s finished downloading", t.Name())
	return filepath.Join(td.dataDir, t.Name()), nil
}

func (td *TorrentDownloader) DownloadFile(torrentFilePath string, callback TorrentProgressCallback) (string, error) {
	t, err := td.client.AddTorrentFromFile(torrentFilePath)
	if err != nil {
		return "", err
	}
	
	<-t.GotInfo() // Wait for metadata
	
	t.DownloadAll()
	
	for {
		bytesCompleted := t.BytesCompleted()
		totalBytes := t.Info().TotalLength()
		
		if callback != nil {
			callback(t.Name(), bytesCompleted, totalBytes, len(t.PeerConns()))
		}
		
		if bytesCompleted == totalBytes {
			break
		}
		time.Sleep(2 * time.Second)
	}
	return filepath.Join(td.dataDir, t.Name()), nil
}
